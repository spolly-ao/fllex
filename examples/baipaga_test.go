package examples_test

import (
	"fmt"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
	"github.com/spolly-ao/fllex/phone"
	"github.com/spolly-ao/fllex/providers/baipaga"
	"github.com/spolly-ao/fllex/providers/proxypay"
)

// config é a de um comerciante do BAI PAGA. A chave de HMAC é tão obrigatória
// como a de API: sem ela não se aceita a confirmação, e o cliente paga sem que
// a cobrança feche.
var configBAI = baipaga.Config{
	APIKey:      "chave-do-comerciante",
	HMACKey:     "BancoBAI12345678",
	CallbackURL: "https://api.exemplo.ao/webhooks/baipaga",
}

// O BAI PAGA é o primeiro gateway desta biblioteca que fala com um banco, e não
// com uma rede nem com um agregador. O cliente é identificado pelo telemóvel e
// autoriza o débito na conta dele.
func Example_baipagaRegistarOGateway() {
	registo := payment.NewRegistry().Register(
		baipaga.New(configBAI),
		proxypay.New(proxypay.Config{APIKey: "chave", Entity: "01234"}),
	)

	fmt.Println("métodos:", registo.MethodsFor(money.AOA))

	// Output:
	// métodos: [baipaga reference]
}

// O telemóvel é a identificação do pagador, e cada rede angolana escolheu uma
// forma de o escrever. Passar a forma errada dá um erro do gateway que não diz
// que o problema é de formato.
func Example_baipagaOFormatoDoTelemovel() {
	numero := "+244 925 857 412"

	fmt.Println("BAI PAGA:          ", phone.LocalAO(numero))
	fmt.Println("Multicaixa Express:", phone.NormalizeAO(numero))
	fmt.Println("Proxypay DDS:      ", phone.FormatAODDS(numero))

	// Output:
	// BAI PAGA:           925857412
	// Multicaixa Express: 244925857412
	// Proxypay DDS:       +244-925857412
}

// O desfecho depende do fluxo, e é para isso que o Kind existe.
//
// Com confirmação na app BAI Directo não há nada para mostrar: o cliente recebe
// a notificação no telemóvel dele e o pedido fica pendente. Com confirmação
// numa página nossa, o BAI devolve uma URL para abrir num popup.
func Example_baipagaOsDoisDesfechos() {
	naApp := payment.ChargeResult{Kind: payment.KindPending, Status: payment.StatusPending}
	naPagina := payment.ChargeResult{
		Kind: payment.KindRedirect, Status: payment.StatusPending,
		URL: "https://app.inm.pt/BAI-Paga-BOX/?paymentExternalId=f268b735",
	}

	for _, res := range []payment.ChargeResult{naApp, naPagina} {
		switch res.Kind {
		case payment.KindPending:
			fmt.Println("avisar que o pagamento está à espera de aprovação no telemóvel")
		case payment.KindRedirect:
			fmt.Println("abrir o popup em", res.URL)
		}
	}

	// Output:
	// avisar que o pagamento está à espera de aprovação no telemóvel
	// abrir o popup em https://app.inm.pt/BAI-Paga-BOX/?paymentExternalId=f268b735
}

// O callback do BAI vem assinado, e uma assinatura que não bate é um erro e não
// um evento a ignorar.
//
// É a diferença em relação ao gateway online da EMIS, onde não há chave
// partilhada nem consulta de estado: ali o callback é tudo o que existe, aqui é
// só o atalho. A especificação do BAI diz, com estas palavras, que a
// notificação é "best effort" e que quem integra deve ter recurso por consulta.
func Example_baipagaOCallbackAssinado() {
	provider := baipaga.New(configBAI)

	corpo := []byte(`<paymentCallback>
	  <id>14234</id>
	  <amount>17000</amount>
	  <currency>AKZ</currency>
	  <lastChangeDate>2018-08-23T14:30:58.000+00:00</lastChangeDate>
	  <merchant><externalId>8f81cae4-4665-4db8-a4dd-a12b78515526</externalId></merchant>
	  <status>SUCCESS</status>
	  <externalReference>FACT_1023</externalReference>
	  <nonce>45287945845125</nonce>
	  <signature>7JV8XFrbdUj45H9VNm88KpOs/0idSgBaCLhxmwN6EJo=</signature>
	</paymentCallback>`)

	ev, err := provider.ParseWebhook(corpo, "")
	if err != nil {
		fmt.Println("erro:", err)
		return
	}
	fmt.Println("tipo:      ", ev.Type)
	fmt.Println("referência:", ev.Reference)
	fmt.Println("valor:     ", *ev.Amount)

	// Output:
	// tipo:       charge_succeeded
	// referência: FACT_1023
	// valor:      17000.00 AOA
}

// Um corpo alterado em trânsito não passa, e a biblioteca diz porquê.
func Example_baipagaUmCallbackAdulterado() {
	provider := baipaga.New(configBAI)

	// O mesmo corpo do exemplo anterior, com o montante mudado de 17000 para 1.
	corpo := []byte(`<paymentCallback>
	  <id>14234</id>
	  <amount>1</amount>
	  <currency>AKZ</currency>
	  <lastChangeDate>2018-08-23T14:30:58.000+00:00</lastChangeDate>
	  <merchant><externalId>8f81cae4-4665-4db8-a4dd-a12b78515526</externalId></merchant>
	  <status>SUCCESS</status>
	  <externalReference>FACT_1023</externalReference>
	  <nonce>45287945845125</nonce>
	  <signature>7JV8XFrbdUj45H9VNm88KpOs/0idSgBaCLhxmwN6EJo=</signature>
	</paymentCallback>`)

	_, err := provider.ParseWebhook(corpo, "")
	fmt.Println("erro:", err)

	// Output:
	// erro: fllex: assinatura de webhook inválida
}
