package emis

import (
	"encoding/json"
	"strings"
)

// frameTokenRequest é o corpo do pedido de token de frame.
//
// Os nomes dos campos são os que a EMIS espera e não se traduzem. `mobile` e
// `card` são os dois canais que o frame pode oferecer, cada um com os valores
// "PAYMENT", "AUTHORIZATION" ou "DISABLED".
type frameTokenRequest struct {
	// Reference é a nossa referência. Volta no callback dentro de
	// `reference.id`, e é por ela que o pagamento se encontra de volta.
	Reference string `json:"reference"`
	// Amount é o valor na unidade maior, com duas casas ("1234.00").
	Amount string `json:"amount"`
	// Token é o token do comerciante, emitido pela EMIS por ponto de venda.
	Token string `json:"token"`
	// Mobile activa o Multicaixa Express dentro do frame.
	Mobile string `json:"mobile"`
	// Card activa o cartão Multicaixa dentro do mesmo frame.
	Card string `json:"card"`
	// CSSURL é a folha de estilo que a EMIS aplica ao frame. Vai sempre, mesmo
	// vazia: o gateway recusa o pedido quando o campo falta.
	CSSURL string `json:"cssUrl"`
	// CallbackURL é para onde a EMIS envia o desfecho.
	CallbackURL string `json:"callbackUrl"`
}

// frameTokenResponse é o que a EMIS devolve.
//
// Só se lê o `id`, e é de propósito. O gateway devolve mais campos consoante o
// ambiente, e um tipo que os declare todos parte a cobrança no dia em que um
// deles mudar de forma. O corpo inteiro fica no `Raw` do resultado, para quem
// precisar de diagnosticar.
type frameTokenResponse struct {
	ID string `json:"id"`
}

// Callback é o corpo que a EMIS envia para a `CallbackURL` quando o cliente
// termina, ou desiste, dentro do frame.
//
// A forma vem de produção e não de documentação, e não é sempre a mesma:
// `reference` chega como objecto (`{"id": ...}`) ou como texto, e a referência
// que seguiu no pedido pode voltar também em `merchantReferenceNumber`. Por
// isso a referência lê-se pelas duas ([ReferenciaDoAviso] e
// [Callback.Referencias]).
type Callback struct {
	// ID é o identificador da transacção do lado da EMIS.
	ID string `json:"id"`
	// Reference traz de volta a referência que seguiu no pedido.
	Reference ReferenciaDoAviso `json:"reference"`
	// Amount é o valor efectivamente cobrado.
	Amount Texto `json:"amount"`
	// Status é "ACCEPTED" quando o cliente confirmou.
	Status string `json:"status"`
	// TransactionType distingue um pagamento de uma autorização.
	TransactionType string `json:"transactionType"`
	// Currency é a moeda, que tem de ser AOA.
	Currency string `json:"currency"`
	// MerchantReferenceNumber é, em produção, a referência que seguiu no pedido,
	// ecoada de volta.
	MerchantReferenceNumber Texto `json:"merchantReferenceNumber"`
	// PointOfSale identifica o terminal. É o campo que diz se o evento é desta
	// conta ou de outra que aponte para o mesmo endereço.
	PointOfSale struct {
		ID Texto `json:"id"`
	} `json:"pointOfSale"`
	// ClearingPeriod é o período de compensação em que a operação entra.
	ClearingPeriod string `json:"clearingPeriod"`
}

// Texto é um campo que tanto chega como texto quanto como número.
//
// A EMIS não é consistente entre ambientes: o mesmo `amount` chega `1500.00` na
// qualidade e `"1500.00"` na produção, e o mesmo vale para o identificador do
// ponto de venda. Com um tipo fixo, o corpo inteiro deixa de descodificar e o
// pagamento perde-se por causa de umas aspas.
type Texto string

// UnmarshalJSON aceita texto e número, e deixa vazio tudo o resto.
func (t *Texto) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*t = Texto(s)
		return nil
	}
	// json.Number e não float64: um identificador de dezoito dígitos passado
	// por vírgula flutuante volta arredondado, e um identificador arredondado
	// não corresponde a nada.
	var n json.Number
	if json.Unmarshal(b, &n) == nil {
		*t = Texto(n.String())
		return nil
	}
	// Nulo, booleano, objecto: nada disto é um identificador nem um valor.
	// Fica vazio e quem o ler decide, em vez de deitar fora o callback inteiro
	// por causa de um campo que pode nem nos interessar.
	*t = ""
	return nil
}

// String devolve o valor como texto.
func (t Texto) String() string { return string(t) }

// ReferenciaDoAviso é o campo `reference` do aviso, que chega como objecto
// (`{"id": "..."}`) ou como texto. Os dois dão o mesmo ID; outra coisa dá vazio.
type ReferenciaDoAviso struct {
	ID Texto `json:"id"`
}

// UnmarshalJSON aceita o objecto e o texto.
func (r *ReferenciaDoAviso) UnmarshalJSON(b []byte) error {
	var t string
	if json.Unmarshal(b, &t) == nil {
		r.ID = Texto(t)
		return nil
	}
	var o struct {
		ID Texto `json:"id"`
	}
	if json.Unmarshal(b, &o) == nil {
		r.ID = o.ID
		return nil
	}
	// Um número solto ou uma lista não são uma referência: fica vazio, como no
	// [Texto], e o aviso continua a ler-se.
	r.ID = ""
	return nil
}

// Referencias são as referências que o aviso traz, sem as vazias: a de
// `reference` primeiro, depois a de `merchantReferenceNumber`.
func (cb Callback) Referencias() []string {
	var out []string
	for _, r := range []string{cb.Reference.ID.String(), cb.MerchantReferenceNumber.String()} {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}
