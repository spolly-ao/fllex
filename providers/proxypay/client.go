// Package proxypay integra o Proxypay, o gateway de referências ATM usado em
// Angola.
//
// O fluxo tem três passos e uma particularidade que é fácil ignorar e cara de
// descobrir:
//
//  1. Pedir um número de referência (POST /reference_ids).
//  2. Associar-lhe o valor, a validade e os campos personalizados
//     (PUT /references/:ref).
//  3. O cliente paga no ATM, na app ou ao balcão.
//
// A particularidade é o passo 4: o Proxypay guarda os pagamentos confirmados
// numa fila até nós os confirmarmos com DELETE /payments/:id. O webhook é a via
// rápida, mas a fila é a rede de segurança, e é ela que garante que um webhook
// perdido não é dinheiro perdido. Ver [Client.ListConfirmedPayments].
package proxypay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/internal/httpx"
)

// DefaultBaseURL é a API de produção do Proxypay.
const DefaultBaseURL = "https://api.proxypay.co.ao"

// SandboxBaseURL é o ambiente de testes.
const SandboxBaseURL = "https://api.sandbox.proxypay.co.ao"

// DefaultAccept é a versão da API a pedir no cabeçalho Accept.
const DefaultAccept = "application/vnd.proxypay.v2+json"

// Config é a configuração do cliente.
type Config struct {
	// BaseURL é a raiz da API. Vazio usa a produção.
	BaseURL string
	// APIKey é o token de autenticação.
	APIKey string
	// Entity é a entidade que aparece no ecrã do ATM (cinco dígitos).
	Entity string
	// Accept é a versão da API. Vazio usa [DefaultAccept].
	Accept string
	// CallbackURL é para onde o Proxypay envia a confirmação, quando é
	// configurada por referência.
	CallbackURL string
	// Timeout do cliente HTTP.
	Timeout time.Duration
}

// Client fala com a API do Proxypay.
type Client struct {
	cfg  Config
	http *httpx.Client
}

// NewClient cria o cliente.
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	accept := cfg.Accept
	if accept == "" {
		accept = DefaultAccept
	}
	c := httpx.New("proxypay", base, timeout)
	c.SetHeader("Authorization", "Token "+cfg.APIKey)
	c.SetHeader("Accept", accept)
	return &Client{cfg: cfg, http: c}
}

// Configured indica se há chave de API.
func (c *Client) Configured() bool { return strings.TrimSpace(c.cfg.APIKey) != "" }

// Entity devolve a entidade configurada, que é o número que o cliente introduz
// no ATM antes da referência.
func (c *Client) Entity() string { return c.cfg.Entity }

// GenerateReference pede um número de referência novo.
func (c *Client) GenerateReference(ctx context.Context) (string, error) {
	var raw []byte
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: "/reference_ids", RawOut: &raw,
	}); err != nil {
		return "", err
	}
	// A resposta vem como string, com ou sem aspas conforme a versão da API.
	ref := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if ref == "" {
		return "", fmt.Errorf("proxypay: resposta vazia ao gerar referência")
	}
	return ref, nil
}

// UpdateReference associa o valor, a validade e os campos personalizados a uma
// referência. O Proxypay responde 204 sem corpo.
//
// A validade não é decorativa: é ela que decide até quando o ATM aceita o
// pagamento. Numa renovação tem de cobrir toda a janela de tolerância, e não as
// 24 horas por omissão de uma cobrança avulsa.
func (c *Client) UpdateReference(ctx context.Context, reference string, amount string, expiresAt time.Time, customFields map[string]string) error {
	payload := map[string]any{
		"amount":       amount,
		"end_datetime": expiresAt.UTC().Format(time.RFC3339),
	}
	if len(customFields) > 0 {
		payload["custom_fields"] = customFields
	}
	if c.cfg.CallbackURL != "" {
		payload["callback_url"] = c.cfg.CallbackURL
	}
	return c.http.Do(ctx, httpx.Request{
		Method: http.MethodPut,
		Path:   "/references/" + url.PathEscape(reference),
		JSON:   payload,
	})
}

// DeleteReference invalida uma referência, para que deixe de poder ser paga.
//
// É o que impede que uma subscrição já expirada continue a ter no ATM uma
// referência viva que o cliente pode pagar por engano, e que depois é preciso
// devolver à mão.
func (c *Client) DeleteReference(ctx context.Context, reference string) error {
	return c.http.Do(ctx, httpx.Request{
		Method: http.MethodDelete,
		Path:   "/references/" + url.PathEscape(reference),
	})
}

// ConfirmedPayment é um pagamento que o Proxypay confirmou e que ainda não
// confirmámos de volta.
type ConfirmedPayment struct {
	ID           int64          `json:"id"`
	ReferenceID  string         `json:"reference_id"`
	Amount       string         `json:"amount"`
	CustomFields map[string]any `json:"custom_fields"`
	Datetime     string         `json:"datetime"`
}

// PaidAt lê a data do pagamento.
func (p ConfirmedPayment) PaidAt() (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, p.Datetime); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// Field devolve um campo personalizado como string.
func (p ConfirmedPayment) Field(key string) string {
	if p.CustomFields == nil {
		return ""
	}
	switch v := p.CustomFields[key].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	case json.Number:
		return v.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// ListConfirmedPayments devolve os pagamentos que o Proxypay confirmou e que
// ainda não confirmámos de volta.
//
// É o complemento do webhook, e o que torna o sistema à prova de entregas
// perdidas: enquanto não chamarmos [Client.AcknowledgePayment], o pagamento
// continua nesta fila e o processo que a lê acaba por o apanhar.
func (c *Client) ListConfirmedPayments(ctx context.Context) ([]ConfirmedPayment, error) {
	var out []ConfirmedPayment
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodGet, Path: "/payments", Out: &out, Idempotent: true,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// AcknowledgePayment diz ao Proxypay que o pagamento foi processado do nosso
// lado, retirando-o da fila.
//
// Chame-o só depois de o efeito estar mesmo persistido: confirmar antes de
// gravar é perder o pagamento se o processo morrer no meio, porque ele deixa a
// fila e nunca mais volta.
func (c *Client) AcknowledgePayment(ctx context.Context, paymentID int64) error {
	return c.http.Do(ctx, httpx.Request{
		Method: http.MethodDelete,
		Path:   fmt.Sprintf("/payments/%d", paymentID),
	})
}
