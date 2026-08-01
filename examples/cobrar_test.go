package examples_test

import (
	"context"
	"fmt"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
	"github.com/spolly-ao/fllex/providers/momenu"
	"github.com/spolly-ao/fllex/providers/offline"
)

// Cobrar: o método e a moeda escolhem o gateway, e quem chama não precisa de
// saber qual foi.
func Example_cobrar() {
	registo := payment.NewRegistry().Register(offline.New())

	res, gateway, err := registo.Charge(context.Background(), payment.ChargeRequest{
		Reference:   "encomenda-42",
		Amount:      money.FromMajor(5900, money.AOA),
		Method:      payment.MethodExternal,
		Description: "Plano Essencial, mensal",
	})
	if err != nil {
		fmt.Println("erro:", err)
		return
	}

	fmt.Println("gateway:", gateway)
	fmt.Println("estado: ", res.Status)

	// Output:
	// gateway: offline
	// estado:  pending
}

// O Kind da resposta diz o que a interface deve mostrar a seguir. É o único
// switch que quem integra precisa de escrever.
func Example_tratarAResposta() {
	res := payment.ChargeResult{Kind: payment.KindReference, Entity: "01234", Reference: "987654321"}

	switch res.Kind {
	case payment.KindPaid:
		fmt.Println("já pago, não há mais nada a fazer")
	case payment.KindRedirect:
		fmt.Println("encaminhar para", res.URL)
	case payment.KindReference:
		fmt.Println("pagar no ATM com a entidade", res.Entity, "e a referência", res.Reference)
	case payment.KindCode:
		fmt.Println("mostrar o código", res.Code)
	case payment.KindPending:
		fmt.Println("aguardar o banco")
	}

	// Output:
	// pagar no ATM com a entidade 01234 e a referência 987654321
}

// Um gateway sem chave configurada continua registado, mas não é escolhido:
// quem está abaixo dele apanha o pedido, em vez de a compra falhar.
func Example_gatewaySemChave() {
	semChave := payment.NewRegistry().Register(momenu.New(momenu.Config{}))
	comChave := payment.NewRegistry().Register(momenu.New(momenu.Config{APIKey: "chave"}))

	fmt.Println("sem chave:", semChave.MethodsFor(money.AOA))
	fmt.Println("com chave:", comChave.MethodsFor(money.AOA))

	// Output:
	// sem chave: []
	// com chave: [mcx reference ekwanza]
}
