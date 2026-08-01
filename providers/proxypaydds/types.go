// Package proxypaydds integra o sistema de débitos directos do Proxypay (DDS),
// usado em Angola para cobranças recorrentes em conta bancária.
//
// O DDS é outra coisa em relação às referências ATM do pacote proxypay: em vez
// de um número que o cliente vai pagar, emite-se um mandato contra a conta do
// titular e depois apresentam-se cobranças contra esse mandato. É o que mais se
// aproxima de um cartão em ficheiro num mercado onde o cartão recorrente não é
// fiável.
//
// Há dois tipos de mandato:
//
//   - CAP (pré-autorizado): exige um formulário assinado pelo titular,
//     digitalizado e processado até dar um identificador de imagem.
//   - SAP (auto-activado): o titular activa o mandato no seu próprio banco
//     (Multicaixa Express, homebanking, ATM ou balcão), introduzindo o
//     identificador do credor (IEC) e o do mandato preenchido a treze dígitos
//     (ADC). Não precisa de papel.
//
// O fluxo SAP, que é o comum:
//
//  1. NextMandateID, para obter um número sequencial.
//  2. RegisterSAPMandate.
//  3. O titular activa o mandato no banco.
//  4. Events, à espera de MandateActivated.
//  5. NextPaymentID e PresentPayment, para cada cobrança.
//  6. Events, à espera de PaymentCollected ou PaymentRejected.
//
// O passo 3 é humano e pode demorar dias. Apresentar uma cobrança contra um
// mandato ainda não activado não é um erro do sistema: é o cliente que ainda
// não fez a sua parte, e por isso não deve gastar tentativas de retentativa nem
// disparar alarmes.
package proxypaydds

// Recurrence é a periodicidade declarada no mandato.
const (
	RecurrenceAdHoc      = "ADHO" // pontual
	RecurrenceDaily      = "DAIL"
	RecurrenceWeekly     = "WEEK"
	RecurrenceMonthly    = "MNTH"
	RecurrenceQuarterly  = "QURT"
	RecurrenceBiAnnually = "MIAN"
	RecurrenceYearly     = "YEAR"
)

// Purpose é a finalidade do mandato ou da cobrança.
const (
	PurposeCash = "CASH" // prestação recorrente
	PurposeCCRD = "CCRD" // pagamento de cartão de crédito
	PurposeGovt = "GOVT" // pagamento ao Estado
	PurposeSSBE = "SSBE" // prestações sociais
	PurposeSupp = "SUPP" // serviços diversos
	PurposeTaxs = "TAXS" // impostos
	PurposeTrad = "TRAD" // serviços comerciais
)

// Motivos de cancelamento de mandato pelo credor.
const (
	CancelReasonContractChanged  = "CTAM"
	CancelReasonContractCanceled = "CTCA"
	CancelReasonContractExpired  = "CTEX"
	CancelReasonCustomerRequest  = "CUST"
	CancelReasonLastDebitCharged = "MCFC"
	CancelReasonLimitExceeded    = "MSUC"
	CancelReasonFraudDetection   = "RFEC"
)

// Motivos de reversão de uma cobrança já liquidada.
const (
	ReversalReasonAfterTermination = "ADEA"
	ReversalReasonDuplicateOp      = "AM05"
	ReversalReasonAmountDiffers    = "AM09"
	ReversalReasonDuplicate        = "DUPL"
	ReversalReasonRefusedDebtor    = "MS02"
	ReversalReasonRefusedBank      = "MS03"
)

// Motivos de recusa de um mandato.
const (
	RejectReasonAccountClosed     = "AC04"
	RejectReasonAccountBlocked    = "AC06"
	RejectReasonNotAllowed        = "AG01"
	RejectReasonAmountNotAuth     = "AM02"
	RejectReasonDuplicateOp       = "AM05"
	RejectReasonSignatureMismatch = "ANCF"
	RejectReasonMandateCanceled   = "DS02"
	RejectReasonFraudSuspicion    = "FR01"
	RejectReasonDebtorDeceased    = "MD07"
	RejectReasonDebtorInstruction = "MD16"
	RejectReasonNameMismatch      = "RR05"
)

// Motivos de recusa de uma cobrança.
const (
	PayRejectInsufficientFunds  = "AM04"
	PayRejectAmountDiffers      = "AM09"
	PayRejectAmountExceedsLimit = "AM14"
	// PayRejectInsufficientRetry é saldo insuficiente com reapresentação
	// automática pelo banco. Não vale a pena reapresentar por nossa conta: o
	// banco já o vai fazer, e duas apresentações podem cobrar duas vezes.
	PayRejectInsufficientRetry = "XM04"
	PayRejectSettlementFailed  = "ED05"
	PayRejectUnknownRefusal    = "MS02"
)

// Quem pediu uma alteração ou um cancelamento.
const (
	RequesterDebtorCounter  = "CUST"
	RequesterDebtorBank     = "RBED"
	RequesterCreditorBank   = "RBEC"
	RequesterCreditor       = "RCEC"
	RequesterDebtorATM      = "CSTA"
	RequesterDebtorInternet = "CSTH"
	RequesterSystem         = "SYST"
)

// Tipos de evento do fluxo de mandatos.
const (
	EventRegisterMandateCreated   = "RegisterMandateCreated"
	EventRegisterMandateSubmitted = "RegisterMandateSubmitted"
	EventRegisterMandateRejected  = "RegisterMandateRejected"
	EventMandateRegistered        = "MandateRegistered"
	EventMandateActivated         = "MandateActivated"
	EventMandateChanged           = "MandateChanged"
	EventMandateRejected          = "MandateRejected"
	EventCancelMandateCreated     = "CancelMandateCreated"
	EventCancelMandateSubmitted   = "CancelMandateSubmitted"
	EventCancelMandateRejected    = "CancelMandateRejected"
	EventMandateCanceled          = "MandateCanceled"
)

// Tipos de evento do fluxo de cobranças.
const (
	EventPresentPaymentCreated   = "PresentPaymentCreated"
	EventPresentPaymentSubmitted = "PresentPaymentSubmitted"
	EventPresentPaymentRejected  = "PresentPaymentRejected"
	EventPaymentPresented        = "PaymentPresented"
	EventPaymentCollected        = "PaymentCollected"
	EventPaymentRejected         = "PaymentRejected"
	EventPaymentRevoked          = "PaymentRevoked"
	EventCancelPaymentCreated    = "CancelPaymentCreated"
	EventCancelPaymentSubmitted  = "CancelPaymentSubmitted"
	EventCancelPaymentRejected   = "CancelPaymentRejected"
	EventPaymentCanceled         = "PaymentCanceled"
)

// Tipos de evento do fluxo de reversões.
const (
	EventReversePaymentCreated   = "ReversePaymentCreated"
	EventReversePaymentSubmitted = "ReversePaymentSubmitted"
	EventReversePaymentRejected  = "ReversePaymentRejected"
	EventPaymentReversed         = "PaymentReversed"
)

// CAPMandateRequest regista um mandato pré-autorizado, com formulário assinado.
type CAPMandateRequest struct {
	ID                  int    `json:"id"`             // até 12 dígitos; use NextMandateID
	ContractID          string `json:"contract_id"`    // referência do contrato do lado do credor (até 35)
	CreditIBAN          string `json:"credit_iban"`    // IBAN que recebe (até 25)
	DebitIBAN           string `json:"debit_iban"`     // IBAN a debitar (até 25)
	DebitorName         string `json:"debitor_name"`   // nome do titular (até 70)
	TaxID               string `json:"tax_id"`         // NIF do titular (até 14)
	SignatureDate       string `json:"signature_date"` // AAAA-MM-DD
	ImageID             string `json:"image_id"`       // vem de SubmitImageProcessing
	Recurrence          string `json:"recurrence"`
	Purpose             string `json:"purpose"`
	Email               string `json:"email,omitempty"`
	Mobile              string `json:"mobile,omitempty"`     // formato +244-XXXXXXXXX
	MaxAmount           string `json:"max_amount,omitempty"` // decimal em string
	FirstCollectionDate string `json:"first_collection_date,omitempty"`
	FinalCollectionDate string `json:"final_collection_date,omitempty"`
}

// SAPMandateRequest regista um mandato auto-activado. Não leva IBAN a debitar
// nem data de assinatura: é o titular que os fornece ao activar no seu banco.
type SAPMandateRequest struct {
	ID                  int    `json:"id"`
	ContractID          string `json:"contract_id"`
	CreditIBAN          string `json:"credit_iban"`
	DebitorName         string `json:"debitor_name"`
	TaxID               string `json:"tax_id"`
	Recurrence          string `json:"recurrence"`
	Purpose             string `json:"purpose"`
	Email               string `json:"email,omitempty"`
	Mobile              string `json:"mobile,omitempty"`
	MaxAmount           string `json:"max_amount,omitempty"`
	FirstCollectionDate string `json:"first_collection_date,omitempty"`
	FinalCollectionDate string `json:"final_collection_date,omitempty"`
}

// CancelMandateRequest cancela um mandato activo.
type CancelMandateRequest struct {
	Reason string `json:"reason"`
}

// PresentPaymentRequest apresenta uma cobrança contra um mandato activo.
type PresentPaymentRequest struct {
	ID             int    `json:"id"`             // sequencial dentro do mandato (1 a 9999)
	TransactionID  string `json:"transaction_id"` // correlação do nosso lado (até 35)
	Amount         string `json:"amount,omitempty"`
	CollectionDate string `json:"collection_date,omitempty"` // AAAA-MM-DD
	Purpose        string `json:"purpose,omitempty"`
}

// CancelPaymentRequest cancela uma cobrança antes da data de liquidação.
type CancelPaymentRequest struct {
	CancelationID string `json:"cancelation_id"`
	Reason        string `json:"reason,omitempty"`
}

// ReversePaymentRequest reverte uma cobrança já debitada ao titular.
type ReversePaymentRequest struct {
	Amount     string `json:"amount"`
	Reason     string `json:"reason"`
	ReversalID string `json:"reversal_id"`
}

// AuthorizationFormRequest pede o PDF do formulário de autorização CAP.
type AuthorizationFormRequest struct {
	ID            int    `json:"id"`
	ContractID    string `json:"contract_id"`
	DebitIBAN     string `json:"debit_iban"`
	CreditIBAN    string `json:"credit_iban"`
	DebitorName   string `json:"debitor_name"`
	TaxID         string `json:"tax_id"`
	Recurrence    string `json:"recurrence"`
	SignatureDate string `json:"signature_date"`
	Email         string `json:"email,omitempty"`
	Mobile        string `json:"mobile,omitempty"`
	MaxAmount     string `json:"max_amount,omitempty"`
}

// MandateResponse é um mandato tal como o Proxypay o devolve.
type MandateResponse struct {
	ID                  int    `json:"id"`
	ContractID          string `json:"contract_id"`
	CreditIBAN          string `json:"credit_iban"`
	DebitIBAN           string `json:"debit_iban,omitempty"`
	DebitorName         string `json:"debitor_name"`
	TaxID               string `json:"tax_id"`
	Preauth             bool   `json:"preauth"`
	SignatureDate       string `json:"signature_date,omitempty"`
	ImageID             string `json:"image_id,omitempty"`
	Recurrence          string `json:"recurrence"`
	Purpose             string `json:"purpose"`
	Email               string `json:"email,omitempty"`
	Mobile              string `json:"mobile,omitempty"`
	MaxAmount           string `json:"max_amount,omitempty"`
	FirstCollectionDate string `json:"first_collection_date,omitempty"`
	FinalCollectionDate string `json:"final_collection_date,omitempty"`
}

// CancelMandateResponse confirma o pedido de cancelamento.
type CancelMandateResponse struct {
	MandateID int    `json:"mandate_id"`
	Reason    string `json:"reason"`
}

// PaymentResponse é uma cobrança apresentada.
type PaymentResponse struct {
	ID             int    `json:"id"`
	MandateID      int    `json:"mandate_id"`
	TransactionID  string `json:"transaction_id"`
	Amount         string `json:"amount,omitempty"`
	CollectionDate string `json:"collection_date,omitempty"`
	Purpose        string `json:"purpose,omitempty"`
}

// CancelPaymentResponse confirma o cancelamento de uma cobrança.
type CancelPaymentResponse struct {
	CancelationID string `json:"cancelation_id"`
	MandateID     int    `json:"mandate_id"`
	PaymentID     int    `json:"payment_id"`
	Reason        string `json:"reason,omitempty"`
}

// ReversePaymentResponse confirma a reversão de uma cobrança.
type ReversePaymentResponse struct {
	Amount     string `json:"amount"`
	MandateID  int    `json:"mandate_id"`
	PaymentID  int    `json:"payment_id"`
	Reason     string `json:"reason"`
	ReversalID string `json:"reversal_id"`
}

// ImageProcessingResponse é o estado do processamento do formulário assinado.
// Estados: 1 em fila, 0 concluído (com ImageID), -1 falhado.
type ImageProcessingResponse struct {
	ID        string  `json:"id"`
	ImageID   *string `json:"image_id"`
	MandateID *int    `json:"mandate_id"`
	Status    int     `json:"status"`
}

// Event é uma entrada do fluxo de eventos.
type Event struct {
	ID     string    `json:"id"`
	Offset int       `json:"offset"`
	Type   string    `json:"type"`
	Data   EventData `json:"data"`
}

// EventData junta todos os campos possíveis de todos os tipos de evento. Quais
// vêm preenchidos depende de [Event.Type].
type EventData struct {
	ContractID string `json:"contract_id,omitempty"`
	MandateID  int    `json:"mandate_id,omitempty"`
	Datetime   string `json:"datetime,omitempty"`

	CreditIBAN          string  `json:"credit_iban,omitempty"`
	DebitIBAN           string  `json:"debit_iban,omitempty"`
	DebitorName         string  `json:"debitor_name,omitempty"`
	TaxID               string  `json:"tax_id,omitempty"`
	Email               string  `json:"email,omitempty"`
	Mobile              string  `json:"mobile,omitempty"`
	Preauth             *bool   `json:"preauth,omitempty"`
	Recurrence          string  `json:"recurrence,omitempty"`
	Purpose             string  `json:"purpose,omitempty"`
	SignatureDate       string  `json:"signature_date,omitempty"`
	ImageID             string  `json:"image_id,omitempty"`
	MaxAmount           *string `json:"max_amount,omitempty"`
	FirstCollectionDate string  `json:"first_collection_date,omitempty"`
	FinalCollectionDate string  `json:"final_collection_date,omitempty"`
	Requester           string  `json:"requester,omitempty"`
	Reason              string  `json:"reason,omitempty"`

	PaymentID              int    `json:"payment_id,omitempty"`
	TransactionID          string `json:"transaction_id,omitempty"`
	Amount                 string `json:"amount,omitempty"`
	CollectionDate         string `json:"collection_date,omitempty"`
	OriginalCollectionDate string `json:"original_collection_date,omitempty"`
	SettlementDate         string `json:"settlement_date,omitempty"`
	// Retry indica que o banco vai reapresentar sozinho. Quando é verdade, não
	// reapresente por sua conta: seriam duas cobranças pela mesma coisa.
	Retry *bool `json:"retry,omitempty"`

	CancelationID string `json:"cancelation_id,omitempty"`
	ReversalID    string `json:"reversal_id,omitempty"`
	RevocationID  string `json:"revocation_id,omitempty"`
}
