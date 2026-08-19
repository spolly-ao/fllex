package examples_test

import (
	"fmt"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
	"github.com/spolly-ao/fllex/providers/emis"
	"github.com/spolly-ao/fllex/providers/proxypay"
)

// config é a configuração de um comerciante da EMIS. O token e o ponto de venda
// são emitidos por ela, um por ponto de venda.
var config = emis.Config{
	Token:       "token-do-comerciante",
	POSID:       "433220",
	CallbackURL: "https://api.exemplo.ao/webhooks/emis",
}

// O Multicaixa Express directamente na EMIS, sem agregador pelo meio.
//
// A ordem de registo é a ordem de preferência: o Express fica à frente da
// referência, e quem pedir `mcx` cai na EMIS sem que ninguém escreva um `if`.
func Example_expressRegistarOGateway() {
	registo := payment.NewRegistry().Register(
		emis.New(config),
		proxypay.New(proxypay.Config{APIKey: "chave", Entity: "01234"}),
	)

	fmt.Println("métodos:", registo.MethodsFor(money.AOA))

	// Output:
	// métodos: [mcx reference]
}

// O que se mostra ao cliente é um frame, e o endereço dele sai da cobrança.
//
// Ao contrário do Express de um agregador, que devolve [payment.KindPaid]
// porque a chamada dele fica à espera da confirmação no telemóvel, aqui a
// chamada devolve em menos de um segundo e quem espera é o cliente à frente do
// frame. Daí [payment.KindRedirect] e o estado pendente.
func Example_expressOFrame() {
	// O que a EMIS devolveu na cobrança foi este token. A montagem do endereço
	// é a mesma, e é por isso que se pode mostrar aqui sem rede nenhuma.
	url := emis.NewClient(config).FrameURL("2f9a-0b1c")

	res := payment.ChargeResult{Kind: payment.KindRedirect, Status: payment.StatusPending, URL: url}

	switch res.Kind {
	case payment.KindRedirect:
		fmt.Println("abrir o frame em:", res.URL)
	case payment.KindPaid:
		fmt.Println("já pago")
	}
	fmt.Println("estado:          ", res.Status)

	// Output:
	// abrir o frame em: https://pagamentonline.emis.co.ao/online-payment-gateway/portal/frame?token=2f9a-0b1c
	// estado:           pending
}

// O desfecho chega por callback, e é a única confirmação que existe: o gateway
// online da EMIS não tem consulta de estado.
//
// A referência que volta é a que seguiu na cobrança, e vem de dentro do objecto
// `reference`. É por ela que o pagamento se encontra de volta.
func Example_expressOCallback() {
	provider := emis.New(config)

	corpo := []byte(`{"id":"3d4f8e51","reference":{"id":"encomenda-42"},"amount":5900.00,
		"status":"ACCEPTED","transactionType":"PAYMENT","currency":"AOA",
		"pointOfSale":{"id":"433220"}}`)

	ev, _ := provider.ParseWebhook(corpo, "")
	fmt.Println("tipo:      ", ev.Type)
	fmt.Println("referência:", ev.Reference)
	fmt.Println("valor:     ", *ev.Amount, "(comparar com o que foi cobrado)")

	// Output:
	// tipo:       charge_succeeded
	// referência: encomenda-42
	// valor:      5900.00 AOA (comparar com o que foi cobrado)
}

// O corpo do callback não vem assinado, e por isso o ponto de venda é o que
// separa um evento da EMIS de um POST que qualquer pessoa consegue fazer.
//
// O que não bate certo sai como [payment.EventNone], que quem chama trata como
// "não aconteceu nada". Não sai como erro de propósito: um erro punha o gateway
// a reenviar em ciclo, e não há reenvio que corrija um evento que não é nosso.
func Example_expressCallbackDeOutraConta() {
	provider := emis.New(config)

	corpo := []byte(`{"id":"3d4f8e51","reference":{"id":"encomenda-42"},"amount":5900.00,
		"status":"ACCEPTED","transactionType":"PAYMENT","currency":"AOA",
		"pointOfSale":{"id":"999999"}}`)

	ev, _ := provider.ParseWebhook(corpo, "")
	fmt.Println("ignorado:", ev.Ignorable())

	// Output:
	// ignorado: true
}
