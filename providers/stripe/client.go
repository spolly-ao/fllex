// Package stripe integra o Stripe pela sua API REST, sem SDK.
//
// A opção por HTTP directo é deliberada: mantém a biblioteca sem dependências
// externas, e o SDK do Stripe traz consigo uma versão de API fixa que obriga a
// actualizações em cadeia sempre que um dos projectos que usa esta biblioteca
// quer subir de versão. O que precisamos do Stripe (sessões de checkout,
// subscrições, portal, estornos, webhooks) são meia dúzia de endpoints
// estáveis.
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/internal/httpx"
	"github.com/spolly-ao/fllex/payment"
)

// DefaultBaseURL é a API do Stripe.
const DefaultBaseURL = "https://api.stripe.com"

// Config é a configuração do cliente.
type Config struct {
	// SecretKey é a chave secreta (sk_...).
	SecretKey string
	// WebhookSecret é o segredo de assinatura dos webhooks (whsec_...).
	//
	// Deixá-lo vazio desliga a verificação de assinatura, o que só é aceitável
	// em desenvolvimento: sem ela, qualquer pessoa que descubra o endereço do
	// webhook pode dar subscrições por pagas.
	WebhookSecret string
	// BaseURL permite apontar para um servidor de teste.
	BaseURL string
	// Timeout do cliente HTTP.
	Timeout time.Duration
	// SignatureTolerance é a diferença máxima aceite entre o carimbo temporal do
	// webhook e o relógio local, para travar reenvios de eventos antigos
	// capturados por terceiros. Zero usa cinco minutos, o valor recomendado.
	SignatureTolerance time.Duration
}

// Client fala com a API do Stripe.
type Client struct {
	cfg  Config
	http *httpx.Client
}

// NewClient cria o cliente.
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	c := httpx.New("stripe", base, timeout)
	c.MaxRetries = 0 // as escritas dizem por si se podem ser repetidas
	return &Client{cfg: cfg, http: c}
}

// Configured indica se há chave secreta.
func (c *Client) Configured() bool { return strings.TrimSpace(c.cfg.SecretKey) != "" }

func (c *Client) post(ctx context.Context, path string, form url.Values, out any) error {
	return c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: path, Form: form,
		BasicAuth: c.cfg.SecretKey, Out: out,
	})
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.http.Do(ctx, httpx.Request{
		Method: http.MethodGet, Path: path,
		BasicAuth: c.cfg.SecretKey, Out: out, Idempotent: true,
	})
}

func (c *Client) delete(ctx context.Context, path string, out any) error {
	return c.http.Do(ctx, httpx.Request{
		Method: http.MethodDelete, Path: path,
		BasicAuth: c.cfg.SecretKey, Out: out,
	})
}

// --- sessões de checkout -----------------------------------------------------

// CheckoutSession é a parte de uma sessão de checkout que nos interessa.
type CheckoutSession struct {
	ID            string `json:"id"`
	URL           string `json:"url"`
	Status        string `json:"status"`         // open | complete | expired
	PaymentStatus string `json:"payment_status"` // paid | unpaid | no_payment_required
	ExpiresAt     int64  `json:"expires_at"`
	Customer      string `json:"customer"`
	Subscription  string `json:"subscription"`
	PaymentIntent string `json:"payment_intent"`
}

// Paid indica se a sessão já foi paga.
func (s CheckoutSession) Paid() bool { return s.PaymentStatus == "paid" }

// Reusable indica se vale a pena reencaminhar o cliente para esta sessão em vez
// de criar outra: continua aberta e ainda não expirou.
//
// Reutilizar importa por uma razão prática: um cliente que carrega duas vezes
// no botão de pagar não deve gerar duas sessões, porque depois só uma delas é
// que é reconciliada e a outra fica a poluir o painel do Stripe.
func (s CheckoutSession) Reusable(now time.Time) bool {
	return s.Status == "open" && s.URL != "" && (s.ExpiresAt == 0 || time.Unix(s.ExpiresAt, 0).After(now))
}

// CreateCheckoutSession cria uma sessão de checkout.
func (c *Client) CreateCheckoutSession(ctx context.Context, form url.Values) (*CheckoutSession, error) {
	var out CheckoutSession
	if err := c.post(ctx, "/v1/checkout/sessions", form, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCheckoutSession lê uma sessão existente.
func (c *Client) GetCheckoutSession(ctx context.Context, id string) (*CheckoutSession, error) {
	var out CheckoutSession
	if err := c.get(ctx, "/v1/checkout/sessions/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- subscrições -------------------------------------------------------------

// CancelSubscription cancela uma subscrição. Com atPeriodEnd, marca-a para
// terminar no fim do período já pago em vez de cortar de imediato, que é o que
// dá ao cliente o serviço que ele já pagou.
func (c *Client) CancelSubscription(ctx context.Context, id string, atPeriodEnd bool) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	if atPeriodEnd {
		form := url.Values{}
		form.Set("cancel_at_period_end", "true")
		return c.post(ctx, "/v1/subscriptions/"+url.PathEscape(id), form, nil)
	}
	return c.delete(ctx, "/v1/subscriptions/"+url.PathEscape(id), nil)
}

// BillingPortalURL cria uma sessão do portal de facturação, onde o cliente gere
// o método de pagamento, vê as facturas e cancela.
func (c *Client) BillingPortalURL(ctx context.Context, customerID, returnURL string) (string, error) {
	if strings.TrimSpace(customerID) == "" {
		return "", payment.ErrUnsupported
	}
	form := url.Values{}
	form.Set("customer", customerID)
	form.Set("return_url", returnURL)
	var out struct {
		URL string `json:"url"`
	}
	if err := c.post(ctx, "/v1/billing_portal/sessions", form, &out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// --- estornos ----------------------------------------------------------------

// RefundResponse é a resposta de um estorno.
type RefundResponse struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// RefundPaymentIntent devolve dinheiro de um pagamento. amountMinor a zero
// devolve tudo.
func (c *Client) RefundPaymentIntent(ctx context.Context, paymentIntentID string, amountMinor int64, reason string) (*RefundResponse, error) {
	if strings.TrimSpace(paymentIntentID) == "" {
		return nil, fmt.Errorf("stripe: o estorno exige um payment intent")
	}
	form := url.Values{}
	form.Set("payment_intent", paymentIntentID)
	if amountMinor > 0 {
		form.Set("amount", strconv.FormatInt(amountMinor, 10))
	}
	if reason != "" {
		// O motivo vai nos metadados, e não no campo reason, porque esse só
		// aceita três valores fixos do Stripe e recusa texto livre.
		form.Set("metadata[reason]", reason)
	}
	var out RefundResponse
	if err := c.post(ctx, "/v1/refunds", form, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- leitura financeira ------------------------------------------------------

// BalanceResponse é o saldo do comerciante.
type BalanceResponse struct {
	Available []BalanceAmount `json:"available"`
	Pending   []BalanceAmount `json:"pending"`
}

// BalanceAmount é um valor numa moeda.
type BalanceAmount struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// Balance devolve o saldo disponível e pendente.
func (c *Client) Balance(ctx context.Context) (*BalanceResponse, error) {
	var out BalanceResponse
	if err := c.get(ctx, "/v1/balance", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Charge é uma cobrança no Stripe.
type Charge struct {
	ID             string `json:"id"`
	Amount         int64  `json:"amount"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	Paid           bool   `json:"paid"`
	Refunded       bool   `json:"refunded"`
	Description    string `json:"description"`
	ReceiptEmail   string `json:"receipt_email"`
	ReceiptURL     string `json:"receipt_url"`
	Created        int64  `json:"created"`
	BillingDetails struct {
		Email string `json:"email"`
	} `json:"billing_details"`
}

// Charges devolve as cobranças mais recentes.
func (c *Client) Charges(ctx context.Context, limit int) ([]Charge, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	var out struct {
		Data []Charge `json:"data"`
	}
	if err := c.get(ctx, fmt.Sprintf("/v1/charges?limit=%d", limit), &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// Invoice é uma factura do Stripe.
type Invoice struct {
	ID        string `json:"id"`
	Number    string `json:"number"`
	AmountDue int64  `json:"amount_due"`
	Currency  string `json:"currency"`
	Status    string `json:"status"`
	HostedURL string `json:"hosted_invoice_url"`
	PDF       string `json:"invoice_pdf"`
	Created   int64  `json:"created"`
}

// InvoicesForCustomer devolve as facturas de um cliente.
func (c *Client) InvoicesForCustomer(ctx context.Context, customerID string, limit int) ([]Invoice, error) {
	if strings.TrimSpace(customerID) == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	var out struct {
		Data []Invoice `json:"data"`
	}
	path := fmt.Sprintf("/v1/invoices?customer=%s&limit=%d", url.QueryEscape(customerID), limit)
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

// --- assinatura de webhooks --------------------------------------------------

// defaultSignatureTolerance é a diferença máxima aceite entre o carimbo do
// evento e o relógio local.
const defaultSignatureTolerance = 5 * time.Minute

// VerifySignature valida o cabeçalho Stripe-Signature
// (t=carimbo,v1=hmacSHA256(segredo, "carimbo.corpo")).
//
// A comparação é feita em tempo constante e o carimbo é verificado contra a
// tolerância: sem essa verificação, um evento legítimo capturado uma vez podia
// ser reenviado indefinidamente por quem o tivesse guardado.
func (c *Client) VerifySignature(payload []byte, header string) error {
	secret := c.cfg.WebhookSecret
	if secret == "" {
		return nil // verificação desligada; ver o comentário em Config
	}
	tolerance := c.cfg.SignatureTolerance
	if tolerance <= 0 {
		tolerance = defaultSignatureTolerance
	}

	var timestamp string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			timestamp = kv[1]
		case "v1":
			sigs = append(sigs, kv[1])
		}
	}
	if timestamp == "" || len(sigs) == 0 {
		return payment.ErrBadSignature
	}

	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return payment.ErrBadSignature
	}
	if drift := time.Since(time.Unix(ts, 0)); drift > tolerance || drift < -tolerance {
		return fmt.Errorf("%w: carimbo temporal fora da tolerância", payment.ErrBadSignature)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, sig := range sigs {
		if hmac.Equal([]byte(expected), []byte(sig)) {
			return nil
		}
	}
	return payment.ErrBadSignature
}
