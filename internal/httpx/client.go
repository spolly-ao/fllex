// Package httpx é o transporte HTTP partilhado pelos providers: pedidos JSON
// ou de formulário, autenticação, leitura de erros do gateway e repetição das
// falhas passageiras.
//
// É interno de propósito. Os gateways que a biblioteca integra fazem todos a
// mesma coisa de maneiras ligeiramente diferentes, e sem um sítio comum cada um
// traz a sua cópia de tratamento de erros, com as suas próprias omissões.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/payment"
)

// Client é um cliente HTTP para uma API de gateway.
type Client struct {
	// Provider é o nome usado nos erros devolvidos.
	Provider string
	// BaseURL é a raiz da API, sem barra final.
	BaseURL string
	// Header é aplicado a todos os pedidos (autenticação, Accept).
	Header http.Header
	// HTTP é o cliente por baixo. Definir um timeout é obrigatório: sem ele, um
	// gateway que não responda prende um pedido para sempre.
	HTTP *http.Client
	// MaxRetries é quantas vezes repetir uma falha passageira (5xx, 429, erro de
	// rede). Zero não repete.
	//
	// Só se repetem pedidos idempotentes: quem chama diz-o em [Request.Idempotent].
	// Repetir uma cobrança às cegas é a forma mais rápida de cobrar duas vezes.
	MaxRetries int
	// RetryWait é a espera antes da primeira repetição; duplica a cada uma.
	RetryWait time.Duration
}

// New cria um cliente com valores sensatos.
func New(provider, baseURL string, timeout time.Duration) *Client {
	return &Client{
		Provider:   provider,
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Header:     http.Header{},
		HTTP:       &http.Client{Timeout: timeout},
		MaxRetries: 2,
		RetryWait:  500 * time.Millisecond,
	}
}

// SetHeader define um cabeçalho aplicado a todos os pedidos.
func (c *Client) SetHeader(k, v string) *Client {
	if c.Header == nil {
		c.Header = http.Header{}
	}
	c.Header.Set(k, v)
	return c
}

// Request é um pedido a fazer.
type Request struct {
	// Method é o verbo HTTP.
	Method string
	// Path é o caminho a partir da BaseURL (começa por barra).
	Path string
	// JSON é o corpo a serializar como application/json.
	JSON any
	// Form é o corpo a serializar como application/x-www-form-urlencoded (o
	// formato que o Stripe usa).
	Form url.Values
	// Body é um corpo em bruto, para quando nenhum dos anteriores serve (o
	// upload de imagem do Proxypay DDS).
	Body []byte
	// ContentType acompanha Body.
	ContentType string
	// BasicAuth define o utilizador do Basic Auth (o Stripe usa a chave secreta
	// como utilizador e palavra-passe vazia).
	BasicAuth string
	// Header são cabeçalhos só deste pedido.
	Header http.Header
	// Out, quando não é nil, recebe a resposta descodificada de JSON.
	Out any
	// RawOut, quando não é nil, recebe o corpo da resposta em bruto (PDF,
	// imagem).
	RawOut *[]byte
	// Idempotent autoriza a repetição em caso de falha passageira. Só o diga em
	// leituras e em escritas que o gateway garanta idempotentes.
	Idempotent bool
}

// Do executa o pedido, descodifica a resposta e traduz os erros do gateway para
// [payment.GatewayError].
func (c *Client) Do(ctx context.Context, r Request) error {
	attempts := 1
	if r.Idempotent && c.MaxRetries > 0 {
		attempts += c.MaxRetries
	}
	wait := c.RetryWait
	if wait <= 0 {
		wait = 500 * time.Millisecond
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			wait *= 2
		}
		err := c.do(ctx, r)
		if err == nil {
			return nil
		}
		lastErr = err
		if !payment.IsRetryable(err) {
			return err
		}
	}
	return lastErr
}

func (c *Client) do(ctx context.Context, r Request) error {
	var body io.Reader
	contentType := r.ContentType

	switch {
	case r.JSON != nil:
		b, err := json.Marshal(r.JSON)
		if err != nil {
			return fmt.Errorf("%s: serializar pedido: %w", c.Provider, err)
		}
		body = bytes.NewReader(b)
		contentType = "application/json"
	case r.Form != nil:
		body = strings.NewReader(r.Form.Encode())
		contentType = "application/x-www-form-urlencoded"
	case r.Body != nil:
		body = bytes.NewReader(r.Body)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, c.BaseURL+r.Path, body)
	if err != nil {
		return fmt.Errorf("%s: construir pedido: %w", c.Provider, err)
	}
	for k, vs := range c.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	for k, vs := range r.Header {
		req.Header.Set(k, vs[0])
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if r.BasicAuth != "" {
		req.SetBasicAuth(r.BasicAuth, "")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Falha de rede: sem estado HTTP, conta como passageira.
		return &payment.GatewayError{Provider: c.Provider, Message: err.Error()}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return &payment.GatewayError{Provider: c.Provider, StatusCode: resp.StatusCode, Message: err.Error()}
	}

	if resp.StatusCode >= 300 {
		return c.parseError(resp.StatusCode, raw)
	}

	if r.RawOut != nil {
		*r.RawOut = raw
		return nil
	}
	if r.Out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, r.Out); err != nil {
			return fmt.Errorf("%s: ler resposta: %w (corpo: %s)", c.Provider, err, truncate(string(raw), 512))
		}
	}
	return nil
}

// parseError extrai a mensagem de erro das várias formas que os gateways usam.
// Sem isto, a única coisa que chega a quem faz suporte é "erro 400".
func (c *Client) parseError(status int, raw []byte) error {
	ge := &payment.GatewayError{
		Provider:   c.Provider,
		StatusCode: status,
		Body:       truncate(string(raw), 2048),
	}
	var shapes struct {
		Message string `json:"message"`
		Error   any    `json:"error"`
		Detail  string `json:"detail"`
		Errors  []struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &shapes); err == nil {
		ge.Message = shapes.Message
		if ge.Message == "" {
			ge.Message = shapes.Detail
		}
		// {"error": {"message": "...", "code": "..."}} (Stripe)
		if m, ok := shapes.Error.(map[string]any); ok {
			if s, ok := m["message"].(string); ok && s != "" {
				ge.Message = s
			}
			if s, ok := m["code"].(string); ok {
				ge.Code = s
			}
		}
		// {"error": "..."}
		if s, ok := shapes.Error.(string); ok && s != "" && ge.Message == "" {
			ge.Message = s
		}
		if ge.Message == "" && len(shapes.Errors) > 0 {
			ge.Message = shapes.Errors[0].Message
			ge.Code = shapes.Errors[0].Code
		}
	}
	if ge.Message == "" {
		ge.Message = truncate(strings.TrimSpace(string(raw)), 256)
	}
	return ge
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// FirstNonEmpty devolve o primeiro valor não vazio depois de aparado.
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
