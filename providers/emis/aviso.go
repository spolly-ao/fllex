package emis

import (
	"crypto/hmac"
	"crypto/sha256"
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
// referência inteira, um ponto, e o HMAC-SHA256 dela com o
// [Config.CallbackSecret], em hexadecimal.
//
// # Porque é que o segredo vai no endereço
//
// A EMIS não assina o aviso e não tem consulta de estado. O que ela faz é
// enviar o aviso para o endereço que lhe demos no pedido do token, e esse
// pedido é feito de servidor para servidor: o comprador nunca o vê. Um endereço
// diferente por cobrança, com um HMAC que só quem tem o segredo calcula, é a
// única coisa que um estranho que saiba a referência e o valor não consegue
// forjar.
func (c *Client) Assinatura(referencia string) string {
	return referencia + "." + c.mac(referencia)
}

func (c *Client) mac(referencia string) string {
	m := hmac.New(sha256.New, c.cfg.CallbackSecret)
	m.Write([]byte(referencia))
	return hex.EncodeToString(m.Sum(nil))
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
		destino = strings.TrimRight(c.cfg.CallbackURL, "/") + "/" + url.PathEscape(c.Assinatura(referencia))
	}
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

// conferir lê a assinatura que chegou no endereço do aviso e devolve a
// referência que ela assina, se o HMAC bater.
func (c *Client) conferir(assinatura string) (string, bool) {
	i := strings.LastIndexByte(assinatura, '.')
	if i <= 0 {
		return "", false
	}
	referencia, recebido := assinatura[:i], assinatura[i+1:]
	return referencia, hmac.Equal([]byte(recebido), []byte(c.mac(referencia)))
}
