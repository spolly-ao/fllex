// Package emis integra o gateway de pagamentos online da EMIS, que é o
// Multicaixa Express sem agregador pelo meio.
//
// # O que distingue este provider do momenu
//
// O `providers/momenu` também cobra por Multicaixa Express, mas por cima da
// licença de outra empresa, que por sua vez fala com a EMIS. Aqui fala-se
// directamente com a EMIS, com um token de comerciante emitido por ela.
//
// A diferença não é só comercial, é de desenho: o Multicaixa Express do MoMenu
// é **síncrono** (a chamada fica à espera da confirmação no telemóvel e uma
// resposta com sucesso significa pago), e o da EMIS **não é**. Aqui pede-se um
// token, mostra-se um frame ao cliente, e o desfecho chega por callback. É o
// mesmo método de pagamento com dois desfechos diferentes, e é para isso que o
// [payment.Kind] existe.
//
// # O fluxo, em três passos
//
//  1. `POST /online-payment-gateway/portal/frameToken` com a referência, o
//     valor e o token do comerciante. A EMIS devolve um `id`.
//  2. O cliente abre `/online-payment-gateway/portal/frame?token=<id>`, num
//     frame ou numa janela, e confirma no telemóvel.
//  3. A EMIS envia o desfecho para a `CallbackURL`.
//
// # O callback não é assinado, e isso decide o resto
//
// A EMIS não assina o corpo que envia, e o gateway online não tem consulta de
// estado. Ou seja: quem receber aquele POST não tem como provar
// criptograficamente que veio da EMIS, e não há segunda fonte a quem
// perguntar. Por isso este provider **não implementa [payment.Verifier]**: uma
// consulta de estado inventada seria uma defesa que não defende nada.
//
// O que se faz em vez disso é recusar tudo o que não bata certo com a
// configuração desta conta: o ponto de venda tem de ser o nosso, a moeda tem de
// ser o kwanza e o tipo de transacção tem de ser um pagamento. Um callback que
// falhe qualquer um destes devolve [payment.EventNone], que quem chama trata
// como "não aconteceu nada".
//
// **E o valor tem de ser comparado por quem chama.** O evento traz
// `Event.Amount` preenchido de propósito: um callback com a referência certa e
// o valor errado é a forma mais barata de pagar mil kwanzas por uma compra de
// cem mil.
package emis

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/internal/httpx"
	"github.com/spolly-ao/fllex/payment"
)

// DefaultBaseURL é o gateway de pagamentos online da EMIS.
const DefaultBaseURL = "https://pagamentonline.emis.co.ao"

// Os caminhos do portal. Ficam aqui e não espalhados pelo pacote porque são
// dois e mudam ao mesmo tempo.
const (
	pathFrameToken = "/online-payment-gateway/portal/frameToken"
	pathFrame      = "/online-payment-gateway/portal/frame"
)

// defaultTimeout cobre o pedido do token, que é uma chamada curta. Ao
// contrário do Multicaixa Express do MoMenu, aqui não se fica à espera de
// ninguém: quem espera pela confirmação no telemóvel é o frame, no browser do
// cliente.
const defaultTimeout = 30 * time.Second

// Os valores que os canais do frame aceitam.
const (
	canalPagamento = "PAYMENT"
	canalDesligado = "DISABLED"
)

// Config é a configuração do cliente.
type Config struct {
	// Token é o token do comerciante, emitido pela EMIS. É por conta e por
	// ponto de venda, e é o único segredo desta integração.
	Token string

	// POSID é o identificador do ponto de venda que a EMIS associa ao token.
	//
	// Não é usado para cobrar: é usado para **recusar** callbacks que não sejam
	// desta conta. Sem ele, o único filtro que resta é a referência, e a
	// referência não é segredo nenhum. Ver [Provider.ParseWebhook].
	POSID string

	// CallbackURL é o endereço público para onde a EMIS envia o desfecho.
	CallbackURL string

	// CSSURL é a folha de estilo aplicada ao frame, para ele parecer da loja e
	// não de um sítio qualquer. Vazia usa o aspecto por omissão da EMIS.
	CSSURL string

	// Card autoriza também o cartão Multicaixa dentro do mesmo frame.
	//
	// Por omissão está desligado, que é como a integração de referência corre em
	// produção: o cartão dentro deste frame é o cartão de débito nacional, e não
	// tem nada a ver com o [payment.MethodCard] da biblioteca (esse é cartão
	// guardado, com cobrança recorrente). Ligá-lo aqui dá ao cliente uma segunda
	// forma de pagar o mesmo pedido, não um método novo.
	Card bool

	// BaseURL permite apontar para outro ambiente. Vazio usa a produção.
	BaseURL string

	// Timeout permite encurtar a espera pelo token. Deixe a zero.
	Timeout time.Duration
}

// Client fala com o gateway de pagamentos online da EMIS.
type Client struct {
	cfg  Config
	http *httpx.Client
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
	c := httpx.New("emis", base, timeout)
	// Um token de frame não cobra por si, mas abre uma forma de pagar. Repetir
	// o pedido às cegas deixa dois frames vivos para a mesma referência, e dois
	// clientes distraídos pagam os dois. O que se repete são leituras, e este
	// pacote não faz nenhuma.
	c.MaxRetries = 0
	return &Client{cfg: cfg, http: c}
}

// Configured indica se há token de comerciante.
func (c *Client) Configured() bool { return c.cfg.Token != "" }

// Config devolve a configuração, para o provider ler o ponto de venda sem a
// duplicar.
func (c *Client) Config() Config { return c.cfg }

// FrameToken pede à EMIS o token com que se abre o frame de pagamento.
func (c *Client) FrameToken(ctx context.Context, referencia, valor string) (string, map[string]any, error) {
	corpo := frameTokenRequest{
		Reference:   referencia,
		Amount:      valor,
		Token:       c.cfg.Token,
		Mobile:      canalPagamento,
		Card:        canalDesligado,
		CSSURL:      c.cfg.CSSURL,
		CallbackURL: c.cfg.CallbackURL,
	}
	if c.cfg.Card {
		corpo.Card = canalPagamento
	}

	var out frameTokenResponse
	bruto := map[string]any{}
	if err := c.http.Do(ctx, httpx.Request{
		Method: "POST",
		Path:   pathFrameToken,
		JSON:   corpo,
		Out:    &out,
	}); err != nil {
		return "", nil, err
	}
	if out.ID == "" {
		// A EMIS respondeu 200 sem token. Não há frame para mostrar, e devolver
		// sucesso aqui daria um pagamento pendente que ninguém consegue pagar.
		return "", nil, &payment.GatewayError{
			Provider: "emis",
			Message:  "a EMIS respondeu sem token de frame",
		}
	}
	bruto["id"] = out.ID
	return out.ID, bruto, nil
}

// FrameURL é o endereço que o cliente abre para pagar.
func (c *Client) FrameURL(token string) string {
	base := c.cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	return strings.TrimRight(base, "/") + pathFrame + "?token=" + url.QueryEscape(token)
}
