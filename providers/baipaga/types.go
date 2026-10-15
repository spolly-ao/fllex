package baipaga

import "encoding/json"

// moedaBAI é como o BAI PAGA escreve o kwanza.
//
// O código ISO 4217 é AOA, e é esse que a biblioteca usa em todo o lado. O BAI
// usa AKZ, que é a abreviatura comercial. A tradução acontece só aqui e no
// [Provider], e em mais lado nenhum: um "AKZ" que escape para um money.Amount
// obriga o resto do motor a conhecer duas grafias da mesma moeda.
const moedaBAI = "AKZ"

// pedidoPagamento é o corpo de uma criação de pagamento.
//
// O montante vai como número JSON e não como texto, que é o que os exemplos da
// especificação mostram (`"totalAmount": 1545.25`). Daí o json.RawMessage: é a
// forma de escrever um decimal exacto sem passar por float64.
type pedidoPagamento struct {
	CustomerMsisdn    string          `json:"customerMsisdn"`
	TotalAmount       json.RawMessage `json:"totalAmount"`
	Currency          string          `json:"currency"`
	ShoppingCart      *carrinho       `json:"shoppingCart,omitempty"`
	ExternalReference string          `json:"externalReference"`
	Description       string          `json:"description,omitempty"`
	AcceptancePointID int64           `json:"acceptancePointId,omitempty"`
}

// carrinho é o detalhe do que está a ser pago.
//
// É opcional, e manda-se na mesma: é o que o cliente vê no telemóvel antes de
// autorizar, e a especificação diz que "permite que o cliente realize a
// confirmação do que está a pagar, oferecendo uma maior confiança no processo".
// Uma autorização sem saber do quê é uma autorização que se recusa.
type carrinho struct {
	Items []linha `json:"items"`
}

type linha struct {
	AmountPerItem json.RawMessage `json:"amountPerItem"`
	Description   string          `json:"description"`
	Count         int             `json:"count"`
	Discount      int             `json:"discount"`
	TotalAmount   json.RawMessage `json:"totalAmount"`
}

// resposta são os campos que qualquer resposta pode trazer.
//
// **O erro chega com HTTP 200.** O `code` vem no corpo e vale "OK" ou um dos
// nomes da lista de erros, e o `httpx` só trata como falha o que vier acima de
// 300. Sem ler isto, uma chave de API inválida passava por sucesso.
type resposta struct {
	Code    string `json:"code"`
	Message string `json:"message"`

	// PaymentID é o identificador do pagamento na plataforma do BAI.
	PaymentID int64 `json:"paymentId"`
	// ConfirmationURL é a página onde o cliente introduz o OTP, no fluxo com
	// confirmação fora da app do banco.
	ConfirmationURL string `json:"confirmationUrl"`
	// Valid é a resposta da validação de telemóvel.
	Valid *bool `json:"valid"`
	// ImageExtension e EncodeToString são o código QR.
	ImageExtension string `json:"imageExtension"`
	EncodeToString string `json:"encodeToString"`
	// Payment é o que a consulta de estado devolve.
	Payment *estado `json:"payment"`
}

// estado é o pagamento tal como a consulta o devolve.
type estado struct {
	ID             int64           `json:"id"`
	Amount         json.RawMessage `json:"amount"`
	Currency       string          `json:"currency"`
	CreationDate   string          `json:"creationDate"`
	LastChangeDate string          `json:"lastChangeDate"`
	Status         string          `json:"status"`
	TotalReversed  json.RawMessage `json:"totalReversed"`
	MaxReversible  json.RawMessage `json:"maxReversible"`
	Merchant       *comerciante    `json:"merchant"`
}

type comerciante struct {
	ID         int64  `json:"id"`
	ExternalID string `json:"externalId"`
	Name       string `json:"name"`
}

// Callback é o corpo XML que o BAI PAGA envia para o endereço configurado.
//
// **É XML e não JSON**, ao contrário de todos os outros gateways desta casa, e
// a assinatura vem dentro do corpo e não num cabeçalho.
//
// Os campos são lidos como texto de propósito. A assinatura é calculada sobre
// os valores tal como chegam, e converter o montante para número e outra vez
// para texto é a forma mais barata de o alterar sem dar por isso: "0.01" pode
// voltar "0.010000000000000000208", e a assinatura deixa de bater.
type Callback struct {
	XMLName           struct{}         `xml:"paymentCallback"`
	ID                string           `xml:"id"`
	Amount            string           `xml:"amount"`
	Currency          string           `xml:"currency"`
	Description       string           `xml:"description"`
	CreationDate      string           `xml:"creationDate"`
	LastChangeDate    string           `xml:"lastChangeDate"`
	Status            string           `xml:"status"`
	ExternalReference string           `xml:"externalReference"`
	Nonce             string           `xml:"nonce"`
	Signature         string           `xml:"signature"`
	Merchant          callbackMerchant `xml:"merchant"`
}

type callbackMerchant struct {
	Name        string `xml:"name"`
	Description string `xml:"description"`
	ExternalID  string `xml:"externalId"`
}
