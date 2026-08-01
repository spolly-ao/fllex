package momenu

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/spolly-ao/fllex/internal/httpx"
	"github.com/spolly-ao/fllex/phone"
)

// DefaultBaseURL é a API de produção do MoMenu.
const DefaultBaseURL = "https://api.momenu.online"

// defaultTimeout tem de cobrir o tempo que o Multicaixa Express fica à espera
// da confirmação no telemóvel do cliente. O limite do próprio MoMenu ronda os
// 180 segundos, por isso um timeout mais curto corta a chamada com o pagamento
// já em curso, que é exactamente o caso que obriga a reconciliar depois.
const defaultTimeout = 200 * time.Second

// Config é a configuração do cliente.
type Config struct {
	// APIKey é a chave enviada no cabeçalho x-api-key.
	APIKey string
	// BaseURL permite apontar para outro ambiente. Vazio usa a produção.
	BaseURL string
	// QA activa o cabeçalho x-env-qa, que põe o MoMenu em modo de teste.
	QA bool
	// Timeout permite encurtar a espera do Multicaixa Express. Deixe a zero.
	Timeout time.Duration
}

// Client fala com a API do MoMenu.
type Client struct {
	cfg  Config
	http *httpx.Client
}

// NewClient cria o cliente. Sem chave, os pedidos falham com o erro do
// gateway; o provider por cima trata disso antes de chegar aqui.
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	c := httpx.New("momenu", base, timeout)
	c.SetHeader("x-api-key", cfg.APIKey)
	if cfg.QA {
		c.SetHeader("x-env-qa", "true")
	}
	// Uma cobrança nunca é repetida automaticamente: o Multicaixa Express não
	// tem chave de idempotência, e repetir um pedido que já pode ter cobrado é
	// cobrar duas vezes. Só as leituras pedem repetição, uma a uma.
	c.MaxRetries = 0
	return &Client{cfg: cfg, http: c}
}

// Configured indica se há chave de API.
func (c *Client) Configured() bool { return c.cfg.APIKey != "" }

// InitMCX inicia um pagamento por Multicaixa Express.
//
// O telemóvel é normalizado e validado antes de qualquer chamada de rede: um
// número mal escrito devolve [phone.ErrInvalidAO] em vez de um erro opaco do
// gateway a meio de um pedido de três minutos.
func (c *Client) InitMCX(ctx context.Context, req MCXRequest) (*MCXResponse, error) {
	normalized, err := phone.CheckAO(req.PaymentInfo.PhoneNumber)
	if err != nil {
		return nil, fmt.Errorf("momenu: multicaixa express: %w", err)
	}
	req.PaymentInfo.PhoneNumber = normalized
	if req.Customer != nil && req.Customer.Phone != "" {
		req.Customer.Phone = phone.NormalizeAO(req.Customer.Phone)
	}
	req.InstantWithdraw = true

	var out MCXResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: "/api/payment/mcx", JSON: req, Out: &out,
	}); err != nil {
		return nil, err
	}
	if !out.Success {
		return nil, fmt.Errorf("momenu: multicaixa express recusado: %s", httpx.FirstNonEmpty(out.Message, "sem motivo"))
	}
	return &out, nil
}

// InitEKwanza inicia um pagamento por eKwanza.
//
// Ao contrário do Multicaixa Express, o telemóvel aqui é apenas para a factura:
// a confirmação é feita por QR ou código, por isso um número mal escrito é
// normalizado mas não bloqueia o pagamento.
func (c *Client) InitEKwanza(ctx context.Context, req EKwanzaRequest) (*EKwanzaResponse, error) {
	req.PaymentInfo.PhoneNumber = phone.NormalizeAO(req.PaymentInfo.PhoneNumber)
	if req.Customer != nil && req.Customer.Phone != "" {
		req.Customer.Phone = phone.NormalizeAO(req.Customer.Phone)
	}
	req.InstantWithdraw = true

	var out EKwanzaResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: "/api/payment/ekwanza", JSON: req, Out: &out,
	}); err != nil {
		return nil, err
	}
	if !out.Success {
		return nil, fmt.Errorf("momenu: ekwanza recusado")
	}
	return &out, nil
}

// CreateReference cria uma referência bancária.
func (c *Client) CreateReference(ctx context.Context, req ReferenceRequest) (*ReferenceResponse, error) {
	req.InstantWithdraw = true
	var out ReferenceResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: "/api/payment/reference", JSON: req, Out: &out,
	}); err != nil {
		return nil, err
	}
	if !out.Success {
		return nil, fmt.Errorf("momenu: emissão de referência recusada")
	}
	return &out, nil
}

// ReferenceStatus consulta o estado de uma referência.
//
// operationID é o identificador de consulta e merchantTxnID o da ordem do nosso
// lado, que o MoMenu recomenda enviar para a identificar.
func (c *Client) ReferenceStatus(ctx context.Context, operationID, merchantTxnID string) (*ReferenceStatusResponse, error) {
	path := "/api/payment/reference/status/" + url.PathEscape(operationID)
	if merchantTxnID != "" {
		path += "?merchantTransactionId=" + url.QueryEscape(merchantTxnID)
	}
	var out ReferenceStatusResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodGet, Path: path, Out: &out, Idempotent: true,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// EKwanzaStatus consulta o estado de um pagamento eKwanza pelo seu código.
func (c *Client) EKwanzaStatus(ctx context.Context, code, merchantTxnID string) (*EKwanzaStatusResponse, error) {
	path := "/api/payment/ekwanza/status/" + url.PathEscape(code)
	if merchantTxnID != "" {
		path += "?merchantTransactionId=" + url.QueryEscape(merchantTxnID)
	}
	var out EKwanzaStatusResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodGet, Path: path, Out: &out, Idempotent: true,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListInvoices devolve uma página de facturas emitidas.
//
// É a base da reconciliação do Multicaixa Express: como esse método não tem
// webhook nem aceita uma chave de idempotência nossa, uma factura que exista
// para o telemóvel e o valor que pedimos, dentro da janela de tempo certa, é a
// prova de que o pagamento passou. O MoMenu limita a página a 50.
func (c *Client) ListInvoices(ctx context.Context, limit, offset int) (*ListInvoicesResponse, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var out ListInvoicesResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodGet,
		Path:   fmt.Sprintf("/api/invoices?limit=%d&offset=%d", limit, offset),
		Out:    &out, Idempotent: true,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetInvoice devolve o detalhe de uma factura, incluindo o telemóvel do cliente
// que a listagem omite.
func (c *Client) GetInvoice(ctx context.Context, invoiceID string) (*GetInvoiceResponse, error) {
	var out GetInvoiceResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodGet, Path: "/api/invoices/" + url.PathEscape(invoiceID),
		Out: &out, Idempotent: true,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}
