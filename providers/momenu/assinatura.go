package momenu

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Os cabeçalhos com que o MoMenu assina as entregas.
//
// São três, e a assinatura está partida por dois deles: sem o carimbo não se
// consegue calcular o HMAC. É por isso que este pacote implementa o
// [payment.WebhookHeaderParser] e não só o [payment.WebhookParser].
const (
	cabecalhoAssinatura = "x-webhook-signature"
	cabecalhoCarimbo    = "x-webhook-timestamp"
	cabecalhoSegredo    = "x-webhook-secret"
)

// toleranciaPorOmissao é a janela em que um carimbo é aceite.
//
// Cinco minutos, que é o que o próprio MoMenu sugere: "rejeitem pedidos com
// timestamp fora da vossa janela de aceitação (por exemplo, mais de 5
// minutos)". Esta constante esteve nos quinze, com um comentário a dizer que a
// documentação não indicava janela nenhuma. Indicava.
//
// A janela não é a defesa principal contra reenvios. A entrega é at-least-once
// e o próprio gateway manda deduplicar pelo `merchantTransactionId`, que é o
// que vai no [payment.Event.ID]. O carimbo serve para que um evento capturado
// hoje não valha para sempre.
const toleranciaPorOmissao = 5 * time.Minute

// segredoConfere valida o cabeçalho que traz o segredo em claro.
//
// O MoMenu envia-o quando a conta **não** tem a opção "Assinatura apenas", e a
// documentação é explícita sobre o que fazer com ele: "validem por comparação
// exacta (em tempo constante quando a linguagem o permitir) e rejeitem pedidos
// sem correspondência".
//
// Ausente é aceitável, e é o caso recomendado: com "Assinatura apenas" o
// segredo nunca viaja no pedido, e a assinatura é a única prova. Presente e
// diferente não é: uma entrega que traz o segredo de outra conta não é uma
// entrega nossa, mesmo que a assinatura por acaso verifique.
func segredoConfere(nosso, recebido string) bool {
	if recebido == "" {
		return true
	}
	return hmac.Equal([]byte(recebido), []byte(nosso))
}

// assinaturaValida indica se o corpo foi assinado com o nosso segredo.
//
// O MoMenu calcula HMAC-SHA256, em hexadecimal, sobre o carimbo, um ponto e o
// corpo em bruto: `HMAC(segredo, timestamp + "." + corpo)`. O corpo tem de ser
// o que chegou, byte a byte: reserializar o JSON muda espaços e ordem de
// chaves, e a assinatura deixa de bater.
//
// A comparação é em tempo constante. Um `==` sobre a assinatura sai no primeiro
// byte diferente, e essa diferença de tempo mede-se de fora.
func assinaturaValida(segredo string, corpo []byte, assinatura, carimbo, segredoRecebido string, tolerancia time.Duration, agora time.Time) bool {
	assinatura = strings.TrimSpace(assinatura)
	carimbo = strings.TrimSpace(carimbo)
	if segredo == "" || assinatura == "" || carimbo == "" {
		return false
	}
	if !segredoConfere(segredo, strings.TrimSpace(segredoRecebido)) {
		return false
	}
	if !carimboFresco(carimbo, tolerancia, agora) {
		return false
	}
	esperada, err := hex.DecodeString(assinatura)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(segredo))
	mac.Write([]byte(carimbo))
	mac.Write([]byte("."))
	mac.Write(corpo)
	return hmac.Equal(mac.Sum(nil), esperada)
}

// carimboFresco indica se o carimbo está dentro da janela, para os dois lados.
//
// Também recusa um carimbo do futuro: um relógio adiantado do outro lado é um
// problema real, mas aceitar qualquer data futura abria a porta a um evento
// assinado hoje para ser entregue dentro de um ano.
func carimboFresco(carimbo string, tolerancia time.Duration, agora time.Time) bool {
	t, ok := lerCarimbo(carimbo)
	if !ok {
		return false
	}
	if tolerancia <= 0 {
		tolerancia = toleranciaPorOmissao
	}
	diferenca := agora.Sub(t)
	if diferenca < 0 {
		diferenca = -diferenca
	}
	return diferenca <= tolerancia
}

// lerCarimbo lê o carimbo em ISO 8601, que é a forma que o MoMenu usa.
func lerCarimbo(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// cabecalho lê um cabeçalho sem se preocupar com a capitalização.
//
// Quem chama deve entregar as chaves em minúsculas, como o
// [payment.WebhookHeaderParser] pede, mas um mapa construído à mão a partir de
// um `http.Header` vem capitalizado, e perder a assinatura por causa de um "X"
// maiúsculo é recusar um pagamento por causa de nada.
func cabecalho(headers map[string]string, nome string) string {
	if v, ok := headers[nome]; ok {
		return v
	}
	for k, v := range headers {
		if strings.EqualFold(k, nome) {
			return v
		}
	}
	return ""
}
