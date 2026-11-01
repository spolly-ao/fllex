package emis

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

// TamanhoMaximoDaReferencia é o tamanho máximo da referência que a EMIS aceita.
//
// Não está escrito em lado nenhum que se encontre: sabe-se porque uma
// referência maior é recusada com HTTP 400 e o código 108 ("size must be
// between 1 and 15"). Um identificador de pagamento com prefixo e vinte e dois
// carateres, que é o normal, não cabe.
const TamanhoMaximoDaReferencia = 15

// TamanhoMaximoDoAviso é o tamanho máximo do endereço de aviso que a EMIS
// aceita no pedido do token.
//
// Também não está escrito: mediu-se contra a EMIS de produção. Até 255
// carateres dá o token; com 256 responde HTTP 400 com o código 201 e a
// mensagem "internal error", que não aponta para o endereço. Com um
// [Config.CallbackRelay] como o publish do QStash, o encaminhador e o token
// dele ocupam quase tudo, e é por isso que a [Client.Assinatura] encolhe.
const TamanhoMaximoDoAviso = 255

// TamanhoMinimoDoMAC é o menor HMAC que a [Client.Assinatura] usa, em
// carateres de base64url: 16 são 96 bits, que nenhum ataque pela rede
// adivinha. Abaixo disto a cobrança recusa-se com [ErrCallbackTooLong].
const TamanhoMinimoDoMAC = 16

// tamanhoDoMAC é o HMAC-SHA256 inteiro em base64url, sem preenchimento.
const tamanhoDoMAC = 43

// ErrCallbackTooLong diz que o endereço de aviso não cabe nos 255 carateres da
// EMIS, nem com a assinatura mais curta. Recusa-se antes de pedir o token, em
// vez de mandar à EMIS um pedido que ela recusa com "internal error".
var ErrCallbackTooLong = errors.New("emis: o endereço de aviso passa dos 255 carateres que a EMIS aceita")

// ErrReferenceTooLong diz que a referência não cabe nos 15 carateres da EMIS e
// que não há [Config.CallbackSecret] para a encurtar.
//
// Encurtar sem segredo seria mandar à EMIS uma coisa que volta no aviso e que
// quem chama não sabe ligar ao pagamento. Com o segredo, a referência inteira
// viaja no endereço de aviso, e a curta é só o que a EMIS vê.
var ErrReferenceTooLong = errors.New("emis: a referência passa dos 15 carateres e não há segredo de aviso para a encurtar")

// ReferenciaCurta é a referência que segue para a EMIS: a própria, se couber,
// ou os primeiros 15 carateres do SHA-256 dela em hexadecimal.
//
// É determinística de propósito: quem recebe o aviso volta a calculá-la a partir
// da referência inteira, e confere-a com a que a EMIS devolve.
func ReferenciaCurta(referencia string) string {
	if len(referencia) <= TamanhoMaximoDaReferencia {
		return referencia
	}
	h := sha256.Sum256([]byte(referencia))
	return hex.EncodeToString(h[:])[:TamanhoMaximoDaReferencia]
}

// Assinatura é o que vai no fim do endereço de aviso de uma cobrança: a
// [ReferenciaCurta], um ponto, e o HMAC-SHA256 dela com o
// [Config.CallbackSecret], em base64url, encurtado ao que couber nos
// [TamanhoMaximoDoAviso] carateres do endereço inteiro (nunca abaixo de
// [TamanhoMinimoDoMAC]).
//
// # Porque é que o segredo vai no endereço
//
// A EMIS não assina o aviso e não tem consulta de estado. O que ela faz é
// enviar o aviso para o endereço que lhe demos no pedido do token, e esse
// pedido é feito de servidor para servidor: o comprador nunca o vê. Um endereço
// diferente por cobrança, com um HMAC que só quem tem o segredo calcula, é a
// única coisa que um estranho que saiba a referência e o valor não consegue
// forjar.
//
// # Porque é a curta, e não a inteira
//
// A inteira não cabe: com um encaminhador, o endereço passava dos 255
// carateres. A curta é a que a EMIS vê e devolve no aviso, e é com ela que o
// evento chega a quem chama ([Provider.ParseWebhook]).
func (c *Client) Assinatura(referencia string) string {
	curta := ReferenciaCurta(referencia)
	mac := c.mac(curta)
	livre := TamanhoMaximoDoAviso - len(c.envolver(c.baseDoAviso()+"/")) - len(url.PathEscape(curta)) - 1
	if livre < len(mac) {
		mac = mac[:max(livre, TamanhoMinimoDoMAC)]
	}
	return curta + "." + mac
}

func (c *Client) mac(curta string) string {
	m := hmac.New(sha256.New, c.cfg.CallbackSecret)
	m.Write([]byte(curta))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (c *Client) baseDoAviso() string { return strings.TrimRight(c.cfg.CallbackURL, "/") }

// envolver põe o endereço dentro do [Config.CallbackRelay], se houver.
func (c *Client) envolver(destino string) string {
	relay := strings.TrimSpace(c.cfg.CallbackRelay)
	switch {
	case relay == "":
		return destino
	case strings.Contains(relay, MarcaDoDestino):
		return strings.Replace(relay, MarcaDoDestino, destino, 1)
	default:
		return relay + destino
	}
}

// MarcaDoDestino é o sítio, no [Config.CallbackRelay], onde entra o endereço
// de aviso da cobrança.
const MarcaDoDestino = "{destino}"

// CallbackURLDe é o endereço que segue para a EMIS com a cobrança desta
// referência: o [Config.CallbackURL], com segredo a [Client.Assinatura] no fim,
// e, com [Config.CallbackRelay], tudo isso dentro do encaminhador.
func (c *Client) CallbackURLDe(referencia string) string {
	destino := c.cfg.CallbackURL
	if len(c.cfg.CallbackSecret) > 0 {
		destino = c.baseDoAviso() + "/" + url.PathEscape(c.Assinatura(referencia))
	}
	return c.envolver(destino)
}

// conferir lê a assinatura que chegou no endereço do aviso e devolve a
// referência curta que ela assina, se o HMAC bater. Aceita o HMAC encurtado
// até [TamanhoMinimoDoMAC], porque o tamanho depende do endereço de cada
// conta, e compara-o com o começo do inteiro.
func (c *Client) conferir(assinatura string) (string, bool) {
	i := strings.LastIndexByte(assinatura, '.')
	if i <= 0 {
		return "", false
	}
	curta, recebido := assinatura[:i], assinatura[i+1:]
	if len(recebido) < TamanhoMinimoDoMAC || len(recebido) > tamanhoDoMAC {
		return "", false
	}
	return curta, hmac.Equal([]byte(recebido), []byte(c.mac(curta)[:len(recebido)]))
}
