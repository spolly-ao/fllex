// Package momenu integra o agregador angolano MoMenu: Multicaixa Express,
// eKwanza e referência bancária, todos em kwanza, com emissão de factura fiscal.
//
// Três particularidades moldam o desenho e valem por toda a documentação da
// API:
//
//   - O Multicaixa Express é síncrono e não tem webhook. O pedido HTTP fica à
//     espera enquanto o cliente confirma no telemóvel (até cerca de 180
//     segundos) e a resposta é o único sinal de que o pagamento aconteceu. Se
//     essa resposta se perder (o processo morre, um proxy corta ao fim de 60
//     segundos), o dinheiro foi cobrado e o nosso lado não sabe. É para isso
//     que existe a reconciliação por facturas, em [Client.ListInvoices].
//
//   - O webhook da referência não é assinado nem reentregue. A confirmação
//     fiável é a consulta de estado, e o webhook é apenas um acelerador.
//
//   - Os montantes são kwanzas inteiros, sem subunidade, ao contrário do resto
//     da biblioteca. A conversão é feita neste pacote e em mais lado nenhum.
package momenu

// Product é uma linha da factura fiscal. O MoMenu exige-a em todos os
// pagamentos: sem ela o documento sai sem descrição do que foi vendido.
type Product struct {
	ID              string  `json:"id,omitempty"`
	ProductName     string  `json:"productName"`
	ProductPrice    float64 `json:"productPrice"`
	ProductQuantity int     `json:"productQuantity"`
	IVA             int     `json:"iva"`
}

// Customer são os dados do pagador na factura. O MoMenu só aceita o nome
// acompanhado de um NIF real; sem NIF, a factura sai a "Consumidor Final".
type Customer struct {
	Name  string `json:"name,omitempty"`
	NIF   string `json:"nif,omitempty"`
	Phone string `json:"phone,omitempty"`
}

// PaymentInfo é o valor a cobrar e, no Multicaixa Express, o telemóvel onde
// aparece o pedido de confirmação.
type PaymentInfo struct {
	Amount      float64 `json:"amount"`
	PhoneNumber string  `json:"phoneNumber,omitempty"`
}

// MCXRequest inicia um pagamento por Multicaixa Express.
type MCXRequest struct {
	PaymentInfo PaymentInfo `json:"paymentInfo"`
	Products    []Product   `json:"products"`
	Customer    *Customer   `json:"customer,omitempty"`
	// InstantWithdraw manda liquidar o dinheiro ao comerciante de imediato. O
	// MoMenu espera-o em todos os pagamentos, por isso o cliente força-o a true
	// e nenhum sítio o pode esquecer.
	InstantWithdraw bool `json:"instantWithdraw"`
}

// MCXResponse é a resposta do Multicaixa Express. Ter sucesso significa que o
// pagamento está feito e a factura emitida.
type MCXResponse struct {
	Success       bool   `json:"success"`
	TransactionID string `json:"transactionId"`
	InvoiceURL    string `json:"invoiceUrl"`
	Message       string `json:"message"`
}

// EKwanzaRequest inicia um pagamento por eKwanza.
type EKwanzaRequest struct {
	PaymentInfo     PaymentInfo `json:"paymentInfo"`
	Products        []Product   `json:"products"`
	Customer        *Customer   `json:"customer,omitempty"`
	InstantWithdraw bool        `json:"instantWithdraw"`
}

// EKwanzaResponse devolve o código e o QR que o cliente confirma na app. O
// pagamento fica pendente até à confirmação.
type EKwanzaResponse struct {
	Success               bool   `json:"success"`
	MerchantTransactionID string `json:"merchantTransactionId"`
	Code                  string `json:"code"`
	QRCode                string `json:"qrCode"`
	ExpirationDate        string `json:"expirationDate"`
	PaymentTimeout        int    `json:"paymentTimeout"` // segundos
}

// ReferenceRequest cria uma referência bancária.
type ReferenceRequest struct {
	PaymentInfo     PaymentInfo `json:"paymentInfo"`
	Products        []Product   `json:"products,omitempty"`
	Customer        *Customer   `json:"customer,omitempty"`
	InstantWithdraw bool        `json:"instantWithdraw"`
}

// ReferenceResponse é a referência emitida.
//
// Os dois identificadores não são intermutáveis: o webhook correlaciona pelo
// TransactionID e a consulta de estado usa o OperationID. Trocá-los devolve
// sempre "não encontrado".
type ReferenceResponse struct {
	Success         bool   `json:"success"`
	OperationID     string `json:"operationId"`
	TransactionID   string `json:"transactionId"`
	ReferenceNumber string `json:"referenceNumber"`
	Entity          string `json:"entity"`
	DueDate         string `json:"dueDate"`
}

// EKwanzaStatusResponse é o estado de um pagamento eKwanza.
type EKwanzaStatusResponse struct {
	Success       bool   `json:"success"`
	Status        string `json:"status"` // "paid" | "pending"
	OperationCode string `json:"operationCode"`
	InvoiceURL    string `json:"invoiceUrl"`
}

// ReferenceStatusResponse é o estado de uma referência.
type ReferenceStatusResponse struct {
	Success bool `json:"success"`
	Payment *struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"payment"`
	InvoiceURL string `json:"invoiceUrl"`
}

// WebhookEvent é o corpo que o MoMenu envia. Não vem assinado: trate-o como
// uma sugestão de que algo mudou e confirme sempre com a consulta de estado.
type WebhookEvent struct {
	Event                 string `json:"event"` // "payment.confirmed" | "invoice.created"
	MerchantTransactionID string `json:"merchantTransactionId"`
	EkwanzaTransactionID  string `json:"ekwanzaTransactionId"`
	OperationStatus       string `json:"operationStatus"` // "1" = sucesso
	OperationData         any    `json:"operationData"`
	InvoiceURL            string `json:"invoiceUrl"`
}

// InvoiceListItem é uma factura na listagem. Não traz o telemóvel do cliente,
// que a reconciliação precisa; para esse é preciso [Client.GetInvoice].
type InvoiceListItem struct {
	InvoiceID     string  `json:"invoiceId"`
	InvoiceNumber string  `json:"invoiceNumber"`
	InvoiceType   string  `json:"invoiceType"`
	CustomerName  string  `json:"customerName"`
	CustomerNIF   string  `json:"customerNif"`
	Total         float64 `json:"total"`
	CreatedAt     string  `json:"createdAt"`
	InvoiceURL    string  `json:"invoiceUrl"`
}

// ListInvoicesResponse é uma página de facturas.
type ListInvoicesResponse struct {
	Success  bool              `json:"success"`
	Invoices []InvoiceListItem `json:"invoices"`
	Total    int               `json:"total"`
	Limit    int               `json:"limit"`
	Offset   int               `json:"offset"`
}

// InvoiceCustomer são os dados do cliente no detalhe de uma factura.
type InvoiceCustomer struct {
	Name  string `json:"name"`
	NIF   string `json:"nif"`
	Phone string `json:"phone"`
}

// InvoiceDetail é o detalhe de uma factura, com o telemóvel que a listagem
// omite.
type InvoiceDetail struct {
	InvoiceID     string          `json:"invoiceId"`
	InvoiceNumber string          `json:"invoiceNumber"`
	InvoiceType   string          `json:"invoiceType"`
	Customer      InvoiceCustomer `json:"customer"`
	PaymentMethod string          `json:"paymentMethod"`
	Total         float64         `json:"total"`
	CreatedAt     string          `json:"createdAt"`
	InvoiceURL    string          `json:"invoiceUrl"`
}

// GetInvoiceResponse é a resposta do detalhe de uma factura.
type GetInvoiceResponse struct {
	Success bool          `json:"success"`
	Invoice InvoiceDetail `json:"invoice"`
}
