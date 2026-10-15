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
	// WebhookSecret é o segredo com que o MoMenu assina as entregas.
	//
	// Sem ele não se aceita webhook nenhum, e isso é deliberado: a referência
	// é marcada como paga pelo webhook, e um webhook que não se verifica é uma
	// forma de qualquer pessoa marcar pagamentos como pagos.
	WebhookSecret string
	// WebhookTolerance é a janela em que um carimbo é aceite. Zero usa 15
	// minutos, que é generoso de propósito: o MoMenu não documenta o
	// espaçamento das reentregas.
	WebhookTolerance time.Duration
	// Timeout permite encurtar a espera do Multicaixa Express. Deixe a zero.
	Timeout time.Duration
	// Now devolve a hora, para os testes poderem fixar o relógio da verificação
	// do carimbo.
	Now func() time.Time
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
	// Continua sem repetição automática, e a razão mudou de sítio.
	//
	// O MoMenu passou a aceitar chave de idempotência, por isso repetir **com a
	// mesma chave** já é seguro. Só que o `httpx` repete o pedido tal como ele
	// é, e quem não mandar chave fica com uma segunda cobrança. A repetição
	// decide-se em cima, onde se sabe se há chave: ver [Client.Charge].
	c.MaxRetries = 0
	return &Client{cfg: cfg, http: c}
}

// Configured indica se há chave de API.
func (c *Client) Configured() bool { return c.cfg.APIKey != "" }

// InitMCX inicia um pagamento por Multicaixa Express.
//
// O telemóvel, o mínimo e a soma dos produtos são validados antes de qualquer
// chamada de rede: são três erros que o MoMenu devolve com nomes próprios
// (INVALID_MSISDN, BELOW_MINIMUM, AMOUNT_MISMATCH) a meio de um pedido que pode
// demorar três minutos, e descobri-los aqui custa um décimo disso.
//
// A chave de idempotência é opcional e vale a pena: com ela, uma resposta
// perdida repete-se sem cobrar outra vez, durante 24 horas.
func (c *Client) InitMCX(ctx context.Context, req MCXRequest, idempotencia string) (*MCXResponse, error) {
	normalized, err := phone.CheckAO(req.PaymentInfo.PhoneNumber)
	if err != nil {
		return nil, fmt.Errorf("momenu: multicaixa express: %w", err)
	}
	req.PaymentInfo.PhoneNumber = normalized
	if req.Customer != nil && req.Customer.Phone != "" {
		req.Customer.Phone = phone.NormalizeAO(req.Customer.Phone)
	}
	req.InstantWithdraw = true
	if err := validarValor(req.PaymentInfo.Amount, req.Products); err != nil {
		return nil, err
	}

	var out MCXResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: "/api/payment/mcx", JSON: req, Out: &out,
		Header: cabecalhoIdempotencia(idempotencia),
	}); err != nil {
		return nil, err
	}
	if !out.Success {
		return nil, recusa("multicaixa express", out.Message)
	}
	return &out, nil
}

// CreateReference cria uma referência bancária.
//
// Exige um endereço de webhook configurado na conta do MoMenu: sem ele devolve
// 400 com WEBHOOK_REQUIRED, e é o webhook que marca a referência como paga.
func (c *Client) CreateReference(ctx context.Context, req ReferenceRequest, idempotencia string) (*ReferenceResponse, error) {
	req.InstantWithdraw = true
	if req.Customer != nil && req.Customer.Phone != "" {
		req.Customer.Phone = phone.NormalizeAO(req.Customer.Phone)
	}
	if err := validarValor(req.PaymentInfo.Amount, req.Products); err != nil {
		return nil, err
	}

	var out ReferenceResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodPost, Path: "/api/payment/reference", JSON: req, Out: &out,
		Header: cabecalhoIdempotencia(idempotencia),
	}); err != nil {
		return nil, err
	}
	if !out.Success {
		// A mensagem do gateway segue até cá. Engoli-la, como se fazia antes,
		// transformava uma dúzia de códigos com nome (BANK_ACCOUNT_NOT_VERIFIED,
		// FE_CONFIG_REQUIRED, BELOW_MINIMUM) num "recusada" sem motivo, e num
		// pedido de suporte por cada falha.
		return nil, recusa("emissão de referência", out.Message)
	}
	return &out, nil
}

// Status consulta o estado de um pagamento pelo identificador do MoMenu.
//
// É uma consulta só, para qualquer método, e **é pelo transactionId**. Havia
// caminhos por método e a consulta da referência era pelo operationId; trocar
// os dois devolve "não encontrado", e foi o que esteve a acontecer.
func (c *Client) Status(ctx context.Context, transactionID string) (*StatusResponse, error) {
	var out StatusResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method:     http.MethodGet,
		Path:       "/api/payment/status/" + url.PathEscape(transactionID),
		Out:        &out,
		Idempotent: true,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListInvoices devolve uma página de facturas emitidas.
//
// É a base da reconciliação do Multicaixa Express: como esse método não tem
// webhook de criação, uma factura que exista
// para o telemóvel e o valor que pedimos, dentro da janela de tempo certa, é a
// prova de que o pagamento passou. O MoMenu limita a página a 50.
func (c *Client) ListInvoices(ctx context.Context, limit, offset int) (*ListInvoicesResponse, error) {
	// O MoMenu passou a aceitar até 100 por página, com 50 por omissão.
	if limit <= 0 || limit > 100 {
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
//
// **O que vai no caminho é o número da factura, não o identificador.** A
// reconciliação passava o `invoiceId` e não encontrava nada, o que lhe tirava
// justamente o telemóvel de que precisa para correlacionar.
func (c *Client) GetInvoice(ctx context.Context, invoiceNumber string) (*GetInvoiceResponse, error) {
	var out GetInvoiceResponse
	if err := c.http.Do(ctx, httpx.Request{
		Method: http.MethodGet, Path: "/api/invoices/" + url.PathEscape(invoiceNumber),
		Out: &out, Idempotent: true,
	}); err != nil {
		return nil, err
	}
	return &out, nil
}

// cabecalhoIdempotencia devolve o cabeçalho da chave, quando há chave.
//
// Vazio não manda cabeçalho nenhum, e é a diferença que conta na referência:
// com chave, repetir devolve a mesma referência; sem chave, emite outra.
func cabecalhoIdempotencia(chave string) http.Header {
	if chave == "" {
		return nil
	}
	h := http.Header{}
	h.Set("Idempotency-Key", chave)
	return h
}

// MinimoKZ é o valor mínimo de um pagamento no MoMenu, em kwanzas.
//
// São 100 kwanzas, e a razão é deles: com a taxa de 2% por cima, é o que mantém
// o líquido acima do piso de transferência da rede que liquida.
const MinimoKZ = 100

// validarValor recusa, antes da rede, os dois erros de valor que o MoMenu passou
// a impor.
//
// O AMOUNT_MISMATCH é o que morde sem avisar: se o valor e os produtos seguirem
// os dois, o MoMenu exige que somem o mesmo. Um desconto aplicado ao total e não
// às linhas passava antes, dava uma factura estranha, e hoje é uma cobrança
// recusada.
func validarValor(amount float64, products []Product) error {
	if amount > 0 && amount < MinimoKZ {
		return fmt.Errorf("momenu: %w: %.0f Kz, o mínimo é %d Kz", ErrBelowMinimum, amount, MinimoKZ)
	}
	if amount <= 0 || len(products) == 0 {
		return nil
	}
	var soma float64
	for _, p := range products {
		q := p.ProductQuantity
		if q <= 0 {
			q = 1
		}
		soma += p.ProductPrice * float64(q)
	}
	// Um cêntimo de folga: os dois lados chegam aqui por vírgula flutuante, e
	// recusar por 0,000001 era recusar por nada.
	if diff := soma - amount; diff > 0.01 || diff < -0.01 {
		return fmt.Errorf("momenu: %w: o valor é %.2f e os produtos somam %.2f", ErrAmountMismatch, amount, soma)
	}
	return nil
}

// recusa devolve o erro de uma resposta que veio com sucesso a falso.
func recusa(oque, mensagem string) error {
	return fmt.Errorf("momenu: %s recusada: %s", oque, httpx.FirstNonEmpty(mensagem, "sem motivo"))
}

// agora devolve a hora da verificação do carimbo, substituível em testes.
func (c *Client) agora() time.Time {
	if c.cfg.Now != nil {
		return c.cfg.Now()
	}
	return time.Now().UTC()
}

// Config devolve a configuração, para o provider ler o segredo do webhook sem a
// duplicar.
func (c *Client) Config() Config { return c.cfg }
