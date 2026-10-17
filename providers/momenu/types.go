// Package momenu integra o agregador angolano MoMenu: Multicaixa Express e
// referência bancária, em kwanza, com emissão de factura fiscal.
//
// Quatro particularidades moldam o desenho e valem por toda a documentação da
// API. As três primeiras mudaram em 2026, e a integração esteve quebrada por
// causa disso: ver o cartão 21 do TASKS-PROGRESS do espaço de trabalho.
//
//   - **A entrega de webhooks é assinada.** HMAC-SHA256, em hexadecimal, sobre
//     o carimbo, um ponto e o corpo em bruto, com a assinatura em
//     `x-webhook-signature` e o carimbo em `x-webhook-timestamp`. Por estar
//     partida em dois cabeçalhos, este pacote implementa o
//     [payment.WebhookHeaderParser]; o [payment.WebhookParser] recusa, porque
//     sem o carimbo não há como verificar.
//
//   - **A consulta de estado é uma só, e é pelo transactionId.**
//     `GET /api/payment/status/{transactionId}`, com o estado à cabeça do
//     corpo. Havia caminhos por método e um estado dentro de um objecto
//     `payment`, e deixaram de existir: um corpo lido pela forma antiga dava
//     sempre pendente, e uma referência paga nunca fechava.
//
//   - **O MoMenu aceita chave de idempotência**, em `Idempotency-Key`, nas
//     cobranças e nas facturas, válida 24 horas e só para respostas de sucesso.
//     É a excepção à armadilha geral desta biblioteca: aqui repetir com a mesma
//     chave devolve a resposta original e **não cobra outra vez**. É o que
//     torna recuperável uma resposta de Multicaixa Express perdida a meio.
//
//   - **Os montantes são kwanzas inteiros**, sem subunidade, ao contrário do
//     resto da biblioteca. A conversão é feita neste pacote e em mais lado
//     nenhum.
//
// O Multicaixa Express continua a ser síncrono e sem webhook de criação: o
// pedido HTTP fica à espera enquanto o cliente confirma no telemóvel (até cerca
// de 180 segundos) e a resposta é o sinal de que o pagamento aconteceu. Com
// chave de idempotência, uma resposta perdida repete-se; sem ela, resta a
// reconciliação por facturas de [Client.ListInvoices].
//
// **O eKwanza saiu.** O MoMenu já não tem endpoint de pagamento por eKwanza, e
// o nome sobrevive na API deles só como etiqueta de método numa factura. O
// [payment.MethodEKwanza] continua a existir no vocabulário da biblioteca, para
// quem o venha a cobrar por outro gateway.
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
	// MoMenu exige-o em todos os pagamentos (sem ele responde
	// INSTANT_WITHDRAW_REQUIRED), por isso o cliente força-o a true e nenhum
	// sítio o pode esquecer.
	InstantWithdraw bool `json:"instantWithdraw"`
	// ExternalReference é a nossa referência, e o nome do campo é mesmo este.
	//
	// O MoMenu chamou-lhe `externalReferenceOpcional`, com o "Opcional" colado
	// ao nome. Não se corrige: é o que a API aceita, e um campo renomeado por
	// bom gosto é um campo que o gateway ignora.
	//
	// Volta na resposta de criação, na consulta de estado e em todos os eventos
	// do webhook, o que faz dela a forma de correlacionar pelo nosso
	// identificador em vez de dependermos só do deles. Aceita 1 a 200
	// caracteres.
	ExternalReference string `json:"externalReferenceOpcional,omitempty"`
}

// MCXResponse é a resposta do Multicaixa Express. Ter sucesso significa que o
// pagamento está feito e a factura emitida.
type MCXResponse struct {
	Success       bool   `json:"success"`
	TransactionID string `json:"transactionId"`
	InvoiceURL    string `json:"invoiceUrl"`
	Message       string `json:"message"`
	// Status vem preenchido ("paid") nas cobranças com repartição. Sem
	// repartição não vem, e o sucesso é o que diz que está pago.
	Status string `json:"status"`
	// ExternalReference é a nossa referência devolvida.
	ExternalReference string `json:"externalReferenceOpcional"`
}

// ReferenceRequest cria uma referência bancária.
//
// Exige um endereço de webhook configurado na conta do MoMenu: sem ele a
// emissão devolve 400 com WEBHOOK_REQUIRED, e isso vale também em qualidade. É
// o webhook que marca a referência como paga.
type ReferenceRequest struct {
	PaymentInfo     PaymentInfo `json:"paymentInfo"`
	Products        []Product   `json:"products,omitempty"`
	Customer        *Customer   `json:"customer,omitempty"`
	InstantWithdraw bool        `json:"instantWithdraw"`
	// ExternalReference é a nossa referência. Ver [MCXRequest.ExternalReference].
	ExternalReference string `json:"externalReferenceOpcional,omitempty"`
}

// ReferenceResponse é a referência emitida.
//
// **Os dois identificadores já não se usam para o mesmo que se usavam.** Houve
// um tempo em que a consulta de estado era pelo OperationID, e este comentário
// avisava para não os trocar. Hoje a consulta é pelo TransactionID, e foi essa
// inversão que deixou todas as referências pendentes para sempre. O OperationID
// fica guardado porque a API continua a devolvê-lo, e não porque sirva para
// consultar.
type ReferenceResponse struct {
	Success         bool   `json:"success"`
	OperationID     string `json:"operationId"`
	TransactionID   string `json:"transactionId"`
	ReferenceNumber string `json:"referenceNumber"`
	Entity          string `json:"entity"`
	DueDate         string `json:"dueDate"`
	Message         string `json:"message"`
	// ExternalReference é a nossa referência devolvida.
	ExternalReference string `json:"externalReferenceOpcional"`
}

// StatusResponse é o estado de um pagamento, seja ele qual for.
//
// Há uma consulta só para todos os métodos, e o estado vem à cabeça do corpo.
// A forma antiga tinha-o dentro de um objecto `payment`, e ler o corpo novo por
// essa forma dava sempre pendente: o campo não existia, ficava a nil, e uma
// referência paga nunca fechava.
type StatusResponse struct {
	Success       bool   `json:"success"`
	TransactionID string `json:"transactionId"`
	// Status vale "open", "paid" ou "failed".
	Status        string  `json:"status"`
	PaymentMethod string  `json:"paymentMethod"`
	Amount        float64 `json:"amount"`
	PaidAt        string  `json:"paidAt"`
	InvoiceURL    string  `json:"invoiceUrl"`
	FailureReason string  `json:"failureReason"`
	Message       string  `json:"message"`
	// ExternalReference é a nossa referência devolvida.
	ExternalReference string `json:"externalReferenceOpcional"`
}

// WebhookEvent é o corpo que o MoMenu envia.
//
// **Vem assinado**, e a assinatura verifica-se com [assinaturaValida] antes de
// se olhar para o conteúdo.
//
// O campo `event` só vem nos eventos de sucesso. Nos outros estados o MoMenu
// envia o corpo **sem ele**, e é por isso que o desfecho se decide pelo
// `operationStatus` e nunca pelo `event`: uma condição que exigisse os dois
// deixava passar em silêncio todos os cancelamentos e todas as recusas.
type WebhookEvent struct {
	Event                 string `json:"event"` // "payment.confirmed" | "invoice.created"
	MerchantTransactionID string `json:"merchantTransactionId"`
	EkwanzaTransactionID  string `json:"ekwanzaTransactionId"`
	// OperationStatus é o que decide: "1" pago, "3" cancelado ou expirado,
	// "4" falhado ou recusado, "5" erro, no Multicaixa Express; "paid" e
	// "failed" nas referências.
	OperationStatus string `json:"operationStatus"`
	OperationData   any    `json:"operationData"`
	InvoiceURL      string `json:"invoiceUrl"`
	// ExternalReference é a nossa referência, devolvida em todos os eventos.
	ExternalReference string `json:"externalReferenceOpcional"`
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
