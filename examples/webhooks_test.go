package examples_test

import (
	"fmt"

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

// O MoMenu não assina as entregas. O evento sai sempre pendente de propósito:
// vale como aviso de que se pode ir consultar o estado, e nunca como prova de
// pagamento.
func Example_webhookDoMoMenu() {
	provider := momenu.New(momenu.Config{APIKey: "chave"})

	corpo := []byte(`{"event":"payment.confirmed","merchantTransactionId":"tx-1","operationStatus":"1"}`)

	ev, _ := provider.ParseWebhook(corpo, "")
	fmt.Println("tipo:  ", ev.Type)
	fmt.Println("estado:", ev.Status, "(ir confirmar antes de dar por pago)")

	// Output:
	// tipo:   charge_succeeded
	// estado: pending (ir confirmar antes de dar por pago)
}
