package proxypaydds

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/internal/httpx"
)

// DefaultBaseURL é a API de produção.
const DefaultBaseURL = "https://api.proxypay.co.ao"

// SandboxBaseURL é o ambiente de testes.
const SandboxBaseURL = "https://api.sandbox.proxypay.co.ao"

// Config é a configuração do cliente.
type Config struct {
	// BaseURL é a raiz da API. Vazio usa a produção.
	BaseURL string
	// APIKey é o token de autenticação (Bearer).
	APIKey string
	// EntityID é o identificador da entidade credora (IEC), no formato
	// "AOXXXXXXXXXX". É o número que o titular introduz no banco para activar o
	// mandato.
	EntityID string
	// CreditIBAN é a conta que recebe as cobranças.
	CreditIBAN string
	// Timeout do cliente HTTP.
	Timeout time.Duration
}

// Client fala com a API de débitos directos do Proxypay.
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
	c := httpx.New("proxypay-dds", base, timeout)
	c.SetHeader("Authorization", "Bearer "+cfg.APIKey)
	c.SetHeader("Accept", "application/json")
	return &Client{cfg: cfg, http: c}
}

// Configured indica se há chave de API e entidade.
func (c *Client) Configured() bool {
	return strings.TrimSpace(c.cfg.APIKey) != "" && strings.TrimSpace(c.cfg.EntityID) != ""
}

// EntityID devolve o identificador da entidade credora (IEC).
func (c *Client) EntityID() string { return c.cfg.EntityID }

// CreditIBAN devolve a conta que recebe.
func (c *Client) CreditIBAN() string { return c.cfg.CreditIBAN }

// ActivationCode devolve o código que o titular introduz no banco para activar
// o mandato: o identificador preenchido com zeros até treze dígitos (ADC).
func ActivationCode(mandateID int) string { return fmt.Sprintf("%013d", mandateID) }

func (c *Client) entityPath() string { return "/dds/v1/" + c.cfg.EntityID }

// --- mandatos ----------------------------------------------------------------

// RegisterCAPMandate regista um mandato pré-autorizado, a partir de um
// formulário assinado já processado.
func (c *Client) RegisterCAPMandate(ctx context.Context, req CAPMandateRequest) (*MandateResponse, error) {
	payload := map[string]any{
		"id":             req.ID,
		"contract_id":    req.ContractID,
		"credit_iban":    c.creditIBANOr(req.CreditIBAN),
		"debit_iban":     req.DebitIBAN,
		"debitor_name":   req.DebitorName,
		"tax_id":         req.TaxID,
		"preauth":        true,
		"signature_date": req.SignatureDate,
		"image_id":       req.ImageID,
		"recurrence":     req.Recurrence,
		"purpose":        req.Purpose,
	}
	addOptional(payload, map[string]string{
		"email": req.Email, "mobile": req.Mobile, "max_amount": req.MaxAmount,
		"first_collection_date": req.FirstCollectionDate,
		"final_collection_date": req.FinalCollectionDate,
	})
	var out MandateResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: c.entityPath() + "/mandates", JSON: payload, Out: &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// RegisterSAPMandate regista um mandato auto-activado.
//
// Depois disto o titular activa-o no seu banco introduzindo o identificador da
// entidade ([Client.EntityID]) e o código de activação
// ([ActivationCode] do id do mandato). Só então é que se lhe podem apresentar
// cobranças.
func (c *Client) RegisterSAPMandate(ctx context.Context, req SAPMandateRequest) (*MandateResponse, error) {
	payload := map[string]any{
		"id":           req.ID,
		"contract_id":  req.ContractID,
		"credit_iban":  c.creditIBANOr(req.CreditIBAN),
		"debitor_name": req.DebitorName,
		"tax_id":       req.TaxID,
		"preauth":      false,
		"recurrence":   req.Recurrence,
		"purpose":      req.Purpose,
	}
	addOptional(payload, map[string]string{
		"email": req.Email, "mobile": req.Mobile, "max_amount": req.MaxAmount,
		"first_collection_date": req.FirstCollectionDate,
		"final_collection_date": req.FinalCollectionDate,
	})
	var out MandateResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: c.entityPath() + "/mandates", JSON: payload, Out: &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelMandate cancela um mandato activo. As cobranças já apresentadas podem
// ainda assim ser liquidadas.
func (c *Client) CancelMandate(ctx context.Context, mandateID int, req CancelMandateRequest) (*CancelMandateResponse, error) {
	var out CancelMandateResponse
	path := fmt.Sprintf("%s/mandates/%d/cancelations", c.entityPath(), mandateID)
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: path, JSON: req, Out: &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- cobranças ---------------------------------------------------------------

// PresentPayment apresenta uma instrução de débito contra um mandato activo.
//
// O identificador tem de ser sequencial dentro do mandato: peça-o a
// [Client.NextPaymentID] e guarde-o antes de apresentar, para que uma repetição
// depois de uma falha de rede não crie uma segunda cobrança.
func (c *Client) PresentPayment(ctx context.Context, mandateID int, req PresentPaymentRequest) (*PaymentResponse, error) {
	var out PaymentResponse
	path := fmt.Sprintf("%s/mandates/%d/payments", c.entityPath(), mandateID)
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: path, JSON: req, Out: &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelPayment cancela uma cobrança antes da data de liquidação. Depois de
// liquidada já não se cancela: use [Client.ReversePayment].
func (c *Client) CancelPayment(ctx context.Context, mandateID, paymentID int, req CancelPaymentRequest) (*CancelPaymentResponse, error) {
	var out CancelPaymentResponse
	path := fmt.Sprintf("%s/mandates/%d/payments/%d/cancelations", c.entityPath(), mandateID, paymentID)
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: path, JSON: req, Out: &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReversePayment devolve ao titular uma cobrança já debitada.
func (c *Client) ReversePayment(ctx context.Context, mandateID, paymentID int, req ReversePaymentRequest) (*ReversePaymentResponse, error) {
	var out ReversePaymentResponse
	path := fmt.Sprintf("%s/mandates/%d/payments/%d/reversals", c.entityPath(), mandateID, paymentID)
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: path, JSON: req, Out: &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- fluxo de eventos --------------------------------------------------------

// Events devolve uma página do fluxo de eventos a partir de um deslocamento.
//
// O fluxo é um registo ordenado de tudo o que acontece aos mandatos e às
// cobranças. Guarde o deslocamento do último evento processado e peça a partir
// do seguinte: é assim que uma paragem do serviço não perde eventos nenhuns.
func (c *Client) Events(ctx context.Context, offset, count int) ([]Event, error) {
	if count <= 0 {
		count = 100
	}
	var out []Event
	path := fmt.Sprintf("%s/events?offset=%d&count=%d", c.entityPath(), offset, count)
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodGet, Path: path, Out: &out, Idempotent: true,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// --- sequências --------------------------------------------------------------

// NextMandateID devolve o próximo identificador de mandato.
func (c *Client) NextMandateID(ctx context.Context) (int, error) {
	return c.nextID(ctx, map[string]any{"type": "mandate"})
}

// NextPaymentID devolve o próximo identificador de cobrança dentro do mandato.
func (c *Client) NextPaymentID(ctx context.Context, mandateID int) (int, error) {
	return c.nextID(ctx, map[string]any{"type": "payment", "mandate_id": mandateID})
}

func (c *Client) nextID(ctx context.Context, body map[string]any) (int, error) {
	var out struct {
		ID int `json:"id"`
	}
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: c.entityPath() + "/sequences", JSON: body, Out: &out,
	}); err != nil {
		return 0, err
	}
	return out.ID, nil
}

// --- formulário de autorização (só CAP) --------------------------------------

// AuthorizationForm devolve o PDF do formulário para o titular assinar.
func (c *Client) AuthorizationForm(ctx context.Context, req AuthorizationFormRequest) ([]byte, error) {
	if req.CreditIBAN == "" {
		req.CreditIBAN = c.cfg.CreditIBAN
	}
	var pdf []byte
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: c.entityPath() + "/authorization_forms",
		JSON: req, RawOut: &pdf,
	}); err != nil {
		return nil, err
	}
	return pdf, nil
}

// SubmitImageProcessing envia o formulário assinado e digitalizado (PDF, JPEG
// ou PNG) para processamento. Consulte o estado com
// [Client.ImageProcessingStatus] até ficar concluído.
func (c *Client) SubmitImageProcessing(ctx context.Context, image []byte) (*ImageProcessingResponse, error) {
	var out ImageProcessingResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: c.entityPath() + "/image_processings",
		Body: image, ContentType: "application/octet-stream", Out: &out,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// ImageProcessingStatus consulta o processamento de um formulário.
func (c *Client) ImageProcessingStatus(ctx context.Context, jobID string) (*ImageProcessingResponse, error) {
	var out ImageProcessingResponse
	path := fmt.Sprintf("%s/image_processings/%s", c.entityPath(), url.PathEscape(jobID))
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodGet, Path: path, Out: &out, Idempotent: true,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// ProcessedImage descarrega a imagem processada do formulário.
func (c *Client) ProcessedImage(ctx context.Context, imageID string) ([]byte, error) {
	var img []byte
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodGet, Path: c.entityPath() + "/images/" + url.PathEscape(imageID),
		RawOut: &img, Idempotent: true,
	}); err != nil {
		return nil, err
	}
	return img, nil
}

// --- espera por eventos ------------------------------------------------------

// WaitForMandateEvent percorre o fluxo até encontrar um evento de um dos tipos
// pedidos para o mandato indicado. Devolve o erro do contexto se este terminar
// primeiro, o que é o normal quando o titular ainda não activou o mandato.
func (c *Client) WaitForMandateEvent(ctx context.Context, mandateID int, types []string, startOffset int, poll time.Duration) (*Event, error) {
	return c.waitFor(ctx, startOffset, poll, types, func(e *Event) bool {
		return e.Data.MandateID == mandateID
	})
}

// WaitForPaymentEvent percorre o fluxo até encontrar um evento de um dos tipos
// pedidos para a cobrança indicada.
func (c *Client) WaitForPaymentEvent(ctx context.Context, mandateID, paymentID int, types []string, startOffset int, poll time.Duration) (*Event, error) {
	return c.waitFor(ctx, startOffset, poll, types, func(e *Event) bool {
		return e.Data.MandateID == mandateID && e.Data.PaymentID == paymentID
	})
}

func (c *Client) waitFor(ctx context.Context, offset int, poll time.Duration, types []string, match func(*Event) bool) (*Event, error) {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	want := make(map[string]bool, len(types))
	for _, t := range types {
		want[t] = true
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		events, err := c.Events(ctx, offset, 100)
		if err != nil {
			return nil, err
		}
		for i := range events {
			ev := &events[i]
			offset = ev.Offset + 1
			if want[ev.Type] && match(ev) {
				return ev, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (c *Client) creditIBANOr(iban string) string {
	if strings.TrimSpace(iban) != "" {
		return iban
	}
	return c.cfg.CreditIBAN
}

func addOptional(payload map[string]any, fields map[string]string) {
	for k, v := range fields {
		if strings.TrimSpace(v) != "" {
			payload[k] = v
		}
	}
}
