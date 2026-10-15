package examples_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/spolly-ao/fllex/providers/momenu"
	"github.com/spolly-ao/fllex/providers/stripe"
)

// Cada gateway fala a sua língua; o evento que sai é sempre o mesmo, e é sobre
// ele que se escreve a lógica. O `SubscriptionRef` é o identificador do lado do
// gateway, para as renovações seguintes continuarem a saber a quem dizem
// respeito.
func Example_webhookDoStripe() {
	// Sem WebhookSecret a verificação de assinatura fica desligada, o que só se
	// faz num exemplo: em produção, é o segredo que separa um evento do Stripe
	// de um evento inventado por quem descobriu o endereço.
	provider := stripe.New(stripe.Config{SecretKey: "sk_test"})

	corpo := []byte(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{
		"id":"cs_1","mode":"subscription","customer":"cus_123","subscription":"sub_123",
		"client_reference_id":"sub-1"}}}`)

	ev, err := provider.ParseWebhook(corpo, "")
	if err != nil {
		fmt.Println("erro:", err)
		return
	}
	fmt.Println("tipo:        ", ev.Type)
	fmt.Println("subscrição:  ", ev.Reference, "->", ev.SubscriptionRef)
	fmt.Println("estado:      ", ev.Status)

	// Output:
	// tipo:         subscription_active
	// subscrição:   sub-1 -> sub_123
	// estado:       approved
}

// O MoMenu assina as entregas, e a assinatura está partida por dois
// cabeçalhos: a assinatura em `x-webhook-signature` e o carimbo que entra no
// cálculo em `x-webhook-timestamp`. É por isso que se usa o
// [payment.WebhookHeaderParser] e não o [payment.WebhookParser].
//
// Até 2026 não era assinada, e esta integração tratava o evento como uma
// sugestão. Hoje uma assinatura que não bate é um erro, e não um aviso.
func Example_webhookDoMoMenu() {
	provider := momenu.New(momenu.Config{
		APIKey:        "chave",
		WebhookSecret: "segredo-da-conta",
		Now:           func() time.Time { return time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC) },
	})

	corpo := []byte(`{"event":"payment.confirmed","merchantTransactionId":"tx-1",` +
		`"operationStatus":"1","externalReferenceOpcional":"encomenda-42"}`)

	// O que o MoMenu calcula: HMAC-SHA256, em hexadecimal, sobre o carimbo, um
	// ponto e o corpo em bruto.
	carimbo := "2026-10-15T10:00:00Z"
	mac := hmac.New(sha256.New, []byte("segredo-da-conta"))
	mac.Write([]byte(carimbo + "."))
	mac.Write(corpo)

	ev, err := provider.ParseWebhookWithHeaders(corpo, map[string]string{
		"x-webhook-signature": hex.EncodeToString(mac.Sum(nil)),
		"x-webhook-timestamp": carimbo,
	})
	if err != nil {
		fmt.Println("erro:", err)
		return
	}
	fmt.Println("tipo:      ", ev.Type)
	fmt.Println("estado:    ", ev.Status)
	fmt.Println("referência:", ev.Reference)

	// Output:
	// tipo:       charge_succeeded
	// estado:     approved
	// referência: encomenda-42
}

// Um corpo alterado em trânsito não passa, e é essa a diferença que conta: é o
// webhook que marca uma referência como paga.
func Example_webhookDoMoMenuAdulterado() {
	provider := momenu.New(momenu.Config{
		APIKey:        "chave",
		WebhookSecret: "segredo-da-conta",
		Now:           func() time.Time { return time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC) },
	})

	carimbo := "2026-10-15T10:00:00Z"
	mac := hmac.New(sha256.New, []byte("segredo-da-conta"))
	mac.Write([]byte(carimbo + ".{\"operationStatus\":\"4\"}"))

	// A assinatura é de um corpo que dizia "recusado"; o corpo que chega diz
	// "pago".
	_, err := provider.ParseWebhookWithHeaders([]byte(`{"operationStatus":"1"}`), map[string]string{
		"x-webhook-signature": hex.EncodeToString(mac.Sum(nil)),
		"x-webhook-timestamp": carimbo,
	})
	fmt.Println("erro:", err)

	// Output:
	// erro: fllex: assinatura de webhook inválida
}
