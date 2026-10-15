package baipaga

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"
)

// mensagemAssinada monta a mensagem sobre a qual o BAI calcula o HMAC.
//
// A ordem dos oito campos é a da especificação e não se reordena:
//
//	<id>|<nonce>|<externalReference>|<amount>|<currency>|<lastChangeDate>|<merchant.externalId>|<status>
//
// **Os valores entram tal como chegam, com uma excepção.** A data tem de ser
// reformatada para `yyyyMMddHHmmss`, porque é isso que a especificação manda
// assinar, e o callback envia-a em ISO 8601 com milissegundos e fuso
// (`2018-09-03T16:02:08.504+01:00` assina-se como `20180903160208`).
//
// É a armadilha mais cara desta integração: uma assinatura que não bate por
// causa do formato da data é indistinguível de uma assinatura forjada, e
// rejeita pagamentos legítimos sem dizer porquê.
func mensagemAssinada(c Callback) string {
	return strings.Join([]string{
		c.ID,
		c.Nonce,
		c.ExternalReference,
		c.Amount,
		c.Currency,
		dataDaAssinatura(c.LastChangeDate),
		c.Merchant.ExternalID,
		c.Status,
	}, "|")
}

// dataDaAssinatura passa a data para `yyyyMMddHHmmss`.
//
// A hora fica **como veio**, sem conversão para UTC: o exemplo da especificação
// leva `+01:00` e assina `160208`, que é a hora local e não a de Greenwich.
// Converter para UTC daria `150208` e nenhuma assinatura bateria.
//
// Uma data que não se consiga ler devolve-se como veio. Assim a assinatura
// falha em vez de passar, que é o lado certo para falhar.
func dataDaAssinatura(s string) string {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z0700",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("20060102150405")
		}
	}
	return s
}

// assinaturaValida indica se a assinatura do callback foi calculada com a nossa
// chave.
//
// É **HMAC-SHA256**, e a especificação não o diz: fala em "HMAC" e mostra um
// exemplo. O algoritmo deduziu-se do exemplo, que é o único caso oficial que
// existe, e está no teste deste pacote precisamente por isso.
//
// A comparação é em tempo constante. Um `==` sobre a assinatura sai no primeiro
// byte diferente, e essa diferença de tempo mede-se de fora.
func assinaturaValida(chave string, c Callback) bool {
	se := strings.TrimSpace(c.Signature)
	if chave == "" || se == "" {
		return false
	}
	esperada, err := base64.StdEncoding.DecodeString(se)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(chave))
	mac.Write([]byte(mensagemAssinada(c)))
	return hmac.Equal(mac.Sum(nil), esperada)
}
