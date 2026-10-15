// Package baipaga integra o BAI PAGA, o pagamento móvel do Banco Angolano de
// Investimentos.
//
// # O que distingue este gateway dos outros
//
// É o primeiro desta biblioteca que fala com **um banco**, e não com uma rede
// (a EMIS) nem com um agregador (o MoMenu, o Proxypay). O cliente é
// identificado pelo telemóvel e autoriza o débito na conta dele, com OTP no BAI
// Directo. Não é uma carteira pré-paga nem uma referência: é uma conta à ordem
// a ser debitada com autorização.
//
// # Os três fluxos, e os três desfechos
//
//  1. **Confirmação na app do banco** ([Provider.Charge] com o pedido normal):
//     devolve só o identificador, e o cliente aprova no BAI Directo.
//     [payment.KindPending].
//  2. **Confirmação numa página nossa** ([Config.ConfirmarNaPagina]): devolve
//     uma URL para abrir num popup, onde o cliente escreve o OTP que recebeu
//     por SMS. [payment.KindRedirect].
//  3. **Código QR** ([Provider.QRCode]): devolve um JPEG em base64.
//
// # O callback é assinado, e há consulta de estado
//
// É a diferença que mais conta em relação ao `providers/emis`. Ali o callback
// não vem assinado e não há a quem perguntar, por isso não há verificador; aqui
// a notificação traz HMAC-SHA256 e existe uma consulta de estado a sério.
//
// A própria especificação escreve que a notificação é feita "numa óptica de
// best effort" e que o parceiro **deve** ter um modelo de recurso por consulta.
// Ou seja: o callback acelera, a consulta decide. É por isso que este provider
// implementa [payment.Verifier], e é por isso que o `worker.Poller` o vai
// apanhar sozinho.
//
// # O que não está aqui
//
// **O pagamento com montante cativo** (a pré-autorização). O motor não tem o
// conceito de autorizar agora e capturar depois, e o livro não sabe registar um
// valor cativo que ainda não é receita. Trazê-lo mexe no domínio, e isso é
// trabalho próprio.
//
// **Os serviços de ponto de aceitação**, autenticados com credenciais de
// backoffice. São para POS, e a fllex não é um POS.
package baipaga

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/internal/httpx"
	"github.com/spolly-ao/fllex/payment"
)

// As raízes da API REST, no contexto de gateway de pagamentos.
//
// Há um segundo par de endereços para contextos de ponto de aceitação (POS),
// que este pacote não usa.
const (
	// DefaultBaseURL é a produção.
	DefaultBaseURL = "https://api.baipaga.ao/MDW/rest/partners"
	// QABaseURL é o ambiente de qualidade, onde a integração se ensaia.
	QABaseURL = "https://ib.bancobai.ao/QUAMDW-3G/internet-banking/api/rest/partners"
)

// Os caminhos a partir da raiz.
//
// A especificação escreve-os com a raiz repetida à frente
// ("/internet-banking/api/rest/partners/payment/request"), o que é a raiz de
// qualidade mais o caminho. Aqui separam-se, que é o que permite trocar de
// ambiente numa variável.
const (
	pathPedido   = "/payment/request"
	pathIniciar  = "/payment/initiate"
	pathEstado   = "/payment"
	pathQRCode   = "/qrCode"
	pathValidar  = "/msisdn/%s/validate"
	cabecalhoKey = "X-MP-ApiKey"
)

// defaultTimeout: a criação do pedido é uma chamada curta. Quem espera pela
// autorização é o cliente, no telemóvel dele, e isso não prende uma ligação
// HTTP nossa.
const defaultTimeout = 30 * time.Second

// Config é a configuração do cliente.
type Config struct {
	// APIKey é a chave de API, atribuída pelo BAI por ambiente.
	APIKey string

	// HMACKey é a chave "K" com que se valida a assinatura dos callbacks.
	//
	// Sem ela não se aceita callback nenhum. A especificação é explícita sobre
	// quem paga a conta: "a não realização da validação da assinatura abre um
	// vector de ataque, e casos de fraude que daqui resultem serão da inteira
	// responsabilidade da entidade que realiza a integração".
	HMACKey string

	// CallbackURL é o endereço público que recebe as notificações de estado.
	CallbackURL string

	// ClientCertPEM e ClientKeyPEM são o certificado de cliente e a chave
	// privada, para a autenticação mútua. Vazios deixam a ligação sem
	// certificado, que é o que serve enquanto o ambiente de qualidade o não
	// exigir.
	ClientCertPEM []byte
	ClientKeyPEM  []byte

	// AcceptancePointID é o ponto de aceitação, quando a conta tiver um.
	AcceptancePointID int64

	// ConfirmarNaPagina escolhe o fluxo com confirmação fora da app do banco:
	// o BAI manda um OTP por SMS e devolve uma página onde o cliente o
	// introduz. Desligado, a confirmação é na app BAI Directo.
	ConfirmarNaPagina bool

	// Description é a descrição por omissão, que o cliente vê no telemóvel.
	Description string

	// BaseURL permite apontar para outro ambiente. Vazio usa a produção.
	BaseURL string

	// Timeout permite encurtar a espera. Deixe a zero.
	Timeout time.Duration
}

// Client fala com a API REST do BAI PAGA.
type Client struct {
	cfg  Config
	http *httpx.Client
	// erroCert guarda a falha de um certificado que não casa, para ela sair na
	// primeira cobrança em vez de desaparecer no construtor.
	erroCert error
}

// NewClient cria o cliente.
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	c := httpx.New("baipaga", base, timeout)
	c.SetHeader(cabecalhoKey, cfg.APIKey)

	// Zero repetições nas escritas, e aqui há uma razão a mais do que o
	// costume: a referência externa tem de ser única, e uma segunda tentativa
	// com a mesma volta com EXISTING_EXTERNAL_REFERENCE. Uma repetição não
	// cobra duas vezes, falha. Quem apanhar um erro de rede consulta o estado,
	// que é para isso que o [Provider.VerifyCharge] existe.
	c.MaxRetries = 0

	cli := &Client{cfg: cfg, http: c}
	if len(cfg.ClientCertPEM) > 0 || len(cfg.ClientKeyPEM) > 0 {
		cli.erroCert = c.ClientCert(cfg.ClientCertPEM, cfg.ClientKeyPEM)
	}
	return cli
}

// Configured indica se há chave de API e chave de HMAC.
//
// As duas contam. Uma conta com chave de API e sem chave de HMAC consegue
// cobrar e não consegue aceitar a confirmação, o que deixa o cliente a pagar e
// a cobrança pendente para sempre.
func (c *Client) Configured() bool {
	return c.cfg.APIKey != "" && c.cfg.HMACKey != "" && c.erroCert == nil
}

// Config devolve a configuração, para o provider a ler sem a duplicar.
func (c *Client) Config() Config { return c.cfg }

// CriarPagamento cria um pedido de pagamento.
//
// Com [Config.ConfirmarNaPagina] usa o fluxo que devolve a página de OTP; sem
// ele, o que notifica a app BAI Directo.
func (c *Client) CriarPagamento(ctx context.Context, p pedidoPagamento) (*resposta, error) {
	caminho := pathPedido
	if c.cfg.ConfirmarNaPagina {
		caminho = pathIniciar
	}
	var out resposta
	if err := c.fazer(ctx, httpx.Request{Method: "POST", Path: caminho, JSON: p}, &out); err != nil {
		return nil, err
	}
	if out.PaymentID == 0 {
		return nil, &payment.GatewayError{
			Provider: "baipaga",
			Message:  "o BAI PAGA respondeu sem identificador de pagamento",
		}
	}
	return &out, nil
}

// Estado consulta um pagamento pela referência externa.
func (c *Client) Estado(ctx context.Context, referencia string) (*estado, error) {
	var out resposta
	if err := c.fazer(ctx, httpx.Request{
		Method:     "GET",
		Path:       pathEstado + "?externalReference=" + url.QueryEscape(referencia),
		Idempotent: true,
	}, &out); err != nil {
		return nil, err
	}
	if out.Payment == nil {
		return nil, payment.ErrNotFound
	}
	return out.Payment, nil
}

// ValidarMSISDN pergunta se o telemóvel pertence a um cliente do BAI PAGA.
//
// Serve para o checkout dizer "este número não tem BAI PAGA" antes de criar uma
// cobrança que nunca vai ser aceite.
func (c *Client) ValidarMSISDN(ctx context.Context, msisdn string) (bool, error) {
	var out resposta
	if err := c.fazer(ctx, httpx.Request{
		Method:     "GET",
		Path:       fmt.Sprintf(pathValidar, url.PathEscape(msisdn)),
		Idempotent: true,
	}, &out); err != nil {
		return false, err
	}
	return out.Valid != nil && *out.Valid, nil
}

// QRCode gera um código QR de pagamento. Devolve a imagem em base64 e a
// extensão que o BAI diz ter usado.
func (c *Client) QRCode(ctx context.Context, referencia string, valor json.RawMessage, largura, altura int) (*resposta, error) {
	corpo := map[string]any{
		"externalReference": referencia,
		"amount":            valor,
		"currency":          moedaBAI,
		"width":             largura,
		"height":            altura,
	}
	if c.cfg.AcceptancePointID != 0 {
		corpo["acceptancePointId"] = c.cfg.AcceptancePointID
	}
	var out resposta
	if err := c.fazer(ctx, httpx.Request{Method: "POST", Path: pathQRCode, JSON: corpo}, &out); err != nil {
		return nil, err
	}
	if out.EncodeToString == "" {
		return nil, &payment.GatewayError{Provider: "baipaga", Message: "o BAI PAGA respondeu sem imagem"}
	}
	return &out, nil
}

// fazer executa o pedido e traduz o erro que vem dentro do corpo.
//
// A resposta entra por argumento próprio e não se vai buscar ao `r.Out` com uma
// asserção de tipo: todas as chamadas deste pacote passam o mesmo tipo, e a
// asserção tinha um ramo de falha que nenhum teste consegue alcançar.
func (c *Client) fazer(ctx context.Context, r httpx.Request, out *resposta) error {
	if c.erroCert != nil {
		return c.erroCert
	}
	r.Out = out
	if err := c.http.Do(ctx, r); err != nil {
		return err
	}
	return erroDoCorpo(*out)
}

// erroDoCorpo traduz o campo `code`.
//
// O BAI responde 200 com o erro lá dentro, e o `httpx` só trata como falha o
// que vier acima de 300: sem isto, uma chave de API recusada passava por
// sucesso e a cobrança ficava pendente à espera de um cliente que nunca foi
// notificado.
func erroDoCorpo(r resposta) error {
	codigo := strings.ToUpper(strings.TrimSpace(r.Code))
	if codigo == "" || codigo == "OK" {
		return nil
	}
	mensagem := strings.TrimSpace(r.Message)
	if mensagem == "" {
		mensagem = codigo
	}
	return &payment.GatewayError{Provider: "baipaga", Code: codigo, Message: mensagem}
}
