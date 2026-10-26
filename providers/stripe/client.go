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
	// TestClock é um relógio de ensaio do Stripe (clock_...), onde nascem os
	// clientes das subscrições. É a única forma de ver uma renovação sem
	// esperar um mês: o relógio adianta-se pela API e o Stripe cobra, factura
	// e avisa como no dia verdadeiro. Só existe em contas de teste; com uma
	// chave de produção, [Provider.Charge] recusa a subscrição em vez de o
	// ignorar em silêncio.
	TestClock string
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

// Live indica se a chave é de produção (sk_live_ ou rk_live_).
func (c *Client) Live() bool {
	k := strings.TrimSpace(c.cfg.SecretKey)
	return strings.HasPrefix(k, "sk_live_") || strings.HasPrefix(k, "rk_live_")
}

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
	AmountTotal   int64  `json:"amount_total"`
	Currency      string `json:"currency"`
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

// PaymentIntent é o pagamento por trás de uma sessão, só com o estado.
type PaymentIntent struct {
	ID     string `json:"id"`
	Status string `json:"status"` // requires_action | processing | succeeded | requires_payment_method | canceled | ...
}

// GetPaymentIntent lê o estado de um pagamento.
func (c *Client) GetPaymentIntent(ctx context.Context, id string) (*PaymentIntent, error) {
	var out PaymentIntent
	if err := c.get(ctx, "/v1/payment_intents/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PaymentIntentWithCharge é o pagamento com a última cobrança expandida, que é
// onde o Radar deixa a avaliação de risco (outcome.risk_level).
type PaymentIntentWithCharge struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	LatestCharge *struct {
		ID      string `json:"id"`
		Outcome *struct {
			RiskLevel string `json:"risk_level"`
		} `json:"outcome"`
	} `json:"latest_charge"`
}

// RiskLevel é o nível do Radar na última cobrança, ou vazio sem ela.
func (pi PaymentIntentWithCharge) RiskLevel() string {
	if pi.LatestCharge == nil || pi.LatestCharge.Outcome == nil {
		return ""
	}
	return pi.LatestCharge.Outcome.RiskLevel
}

// GetPaymentIntentWithCharge lê um pagamento com a última cobrança expandida.
func (c *Client) GetPaymentIntentWithCharge(ctx context.Context, id string) (*PaymentIntentWithCharge, error) {
	var out PaymentIntentWithCharge
	if err := c.get(ctx, "/v1/payment_intents/"+url.PathEscape(id)+"?expand%5B%5D=latest_charge", &out); err != nil {
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

// Subscription é a parte de uma subscrição que nos interessa.
type Subscription struct {
	ID                string            `json:"id"`
	Customer          string            `json:"customer"`
	Status            string            `json:"status"`
	CancelAtPeriodEnd bool              `json:"cancel_at_period_end"`
	CurrentPeriodEnd  int64             `json:"current_period_end"`
	Metadata          map[string]string `json:"metadata"`
	// Items traz o fim do período nas versões da API a partir de 2025-03-31,
	// em que ele saiu da subscrição e passou para cada item.
	Items struct {
		Data []struct {
			CurrentPeriodEnd int64 `json:"current_period_end"`
		} `json:"data"`
	} `json:"items"`
}

// PeriodEnd é o fim do período pago, lido onde a versão da API o puser.
func (s Subscription) PeriodEnd() int64 {
	if s.CurrentPeriodEnd > 0 {
		return s.CurrentPeriodEnd
	}
	for _, it := range s.Items.Data {
		if it.CurrentPeriodEnd > 0 {
			return it.CurrentPeriodEnd
		}
	}
	return 0
}

// GetSubscription lê uma subscrição.
func (c *Client) GetSubscription(ctx context.Context, id string) (*Subscription, error) {
	var out Subscription
	if err := c.get(ctx, "/v1/subscriptions/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateCustomer cria um cliente. Com testClock, o cliente nasce nesse relógio
// de ensaio, e as subscrições dele passam a andar ao ritmo do relógio.
func (c *Client) CreateCustomer(ctx context.Context, email, name, testClock string) (string, error) {
	form := url.Values{}
	if email != "" {
		form.Set("email", email)
	}
	if name != "" {
		form.Set("name", name)
	}
	if testClock != "" {
		form.Set("test_clock", testClock)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.post(ctx, "/v1/customers", form, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// CreateOnceCoupon cria um cupão que desconta amountOff (em unidades menores
// da moeda) uma vez só, e se gasta numa subscrição só: é o desconto do
// primeiro ciclo de uma subscrição que depois renova pelo preço cheio. O nome
// aparece na factura do cliente.
func (c *Client) CreateOnceCoupon(ctx context.Context, amountOff int64, currency, name string) (string, error) {
	return c.CreateCoupon(ctx, amountOff, currency, name, "once", 0)
}

// CreateCoupon cria um cupão que desconta amountOff (em unidades menores da
// moeda) e se gasta numa subscrição só. duration é a do Stripe: once (a
// primeira factura), forever (todas) ou repeating (as que nascem nos months
// meses a seguir ao início). O nome aparece na factura do cliente.
func (c *Client) CreateCoupon(ctx context.Context, amountOff int64, currency, name, duration string, months int) (string, error) {
	form := url.Values{}
	form.Set("amount_off", strconv.FormatInt(amountOff, 10))
	form.Set("currency", strings.ToLower(currency))
	form.Set("duration", duration)
	if duration == "repeating" {
		form.Set("duration_in_months", strconv.Itoa(months))
	}
	form.Set("max_redemptions", "1")
	if name != "" {
		form.Set("name", name)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := c.post(ctx, "/v1/coupons", form, &out); err != nil {
		return "", err
	}
	return out.ID, nil
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
// devolve tudo. Sem chave de idempotência: ver [Client.RefundPaymentIntentOnce].
func (c *Client) RefundPaymentIntent(ctx context.Context, paymentIntentID string, amountMinor int64, reason string) (*RefundResponse, error) {
	return c.RefundPaymentIntentOnce(ctx, paymentIntentID, amountMinor, reason, "")
}

// RefundPaymentIntentOnce é o [Client.RefundPaymentIntent] com a chave de
// idempotência do Stripe (o cabeçalho Idempotency-Key). Com ela, o Stripe
// devolve o mesmo estorno a um pedido repetido, e por isso o cliente repete
// sozinho as falhas passageiras (rede, 5xx, 429): um estorno cuja resposta se
// perdeu não devolve o dinheiro duas vezes. Sem ela, nada se repete.
func (c *Client) RefundPaymentIntentOnce(ctx context.Context, paymentIntentID string, amountMinor int64, reason, idempotencyKey string) (*RefundResponse, error) {
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
	req := httpx.Request{
		Method: http.MethodPost, Path: "/v1/refunds", Form: form,
		BasicAuth: c.cfg.SecretKey,
	}
	if k := strings.TrimSpace(idempotencyKey); k != "" {
		req.Header = http.Header{"Idempotency-Key": {k}}
		req.Idempotent = true
	}
	var out RefundResponse
	req.Out = &out
	if err := c.doRetrying(ctx, req); err != nil {
		return nil, err
	}
	return &out, nil
}

// doRetrying corre um pedido com repetição das falhas passageiras quando ele se
// diz idempotente. O cliente nasce sem repetições (as escritas do Stripe não
// são idempotentes sem chave), e por isso a repetição liga-se só aqui.
func (c *Client) doRetrying(ctx context.Context, r httpx.Request) error {
	if !r.Idempotent {
		return c.http.Do(ctx, r)
	}
	h := *c.http
	h.MaxRetries = refundRetries
	return h.Do(ctx, r)
}

// refundRetries é quantas vezes se repete um estorno com chave depois de uma
// falha passageira.
const refundRetries = 2

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
	ID         string `json:"id"`
	Number     string `json:"number"`
	AmountDue  int64  `json:"amount_due"`
	AmountPaid int64  `json:"amount_paid"`
	Currency   string `json:"currency"`
	Status     string `json:"status"` // draft | open | paid | uncollectible | void
	HostedURL  string `json:"hosted_invoice_url"`
	PDF        string `json:"invoice_pdf"`
	Created    int64  `json:"created"`
	Customer   string `json:"customer"`
	// Subscription é onde a subscrição vem até à versão 2025-03-31 da API; a
	// partir dela vem em Parent. [Invoice.SubscriptionID] lê as duas.
	Subscription      string `json:"subscription"`
	BillingReason     string `json:"billing_reason"`
	StatusTransitions struct {
		PaidAt int64 `json:"paid_at"`
	} `json:"status_transitions"`
	Parent struct {
		SubscriptionDetails struct {
			Subscription string `json:"subscription"`
		} `json:"subscription_details"`
	} `json:"parent"`
}

// SubscriptionID é a subscrição da factura, na forma antiga ou na nova.
func (i Invoice) SubscriptionID() string {
	return firstNonEmpty(i.Subscription, i.Parent.SubscriptionDetails.Subscription)
}

// GetInvoice lê uma factura.
func (c *Client) GetInvoice(ctx context.Context, id string) (*Invoice, error) {
	var out Invoice
	if err := c.get(ctx, "/v1/invoices/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
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
