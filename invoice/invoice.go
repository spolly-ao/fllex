// Package invoice emite os documentos de cobrança: a proforma, que pede o
// pagamento, e a factura, que o confirma.
//
// Os documentos guardam por cópia tudo o que os identifica: os dados do
// emitente, os do cliente, o nome do plano e os valores. É deliberado e não é
// desnormalização por descuido. Uma factura é um documento com valor legal e
// tem de continuar a dizer o mesmo daqui a cinco anos, mesmo que o cliente
// mude de morada, o plano mude de nome ou a empresa mude de sede.
package invoice

import (
	"context"
	"time"

	"github.com/spolly-ao/fllex/money"
)

// Kind distingue os dois documentos.
type Kind string

const (
	// KindProforma é o documento de cobrança: pede o pagamento e traz os dados
	// para pagar. Não tem valor fiscal.
	KindProforma Kind = "proforma"
	// KindInvoice é a factura: confirma o pagamento recebido.
	KindInvoice Kind = "invoice"
	// KindCredit é a nota de crédito: anula, no todo ou em parte, uma factura
	// anterior. Uma factura emitida nunca se apaga nem se altera; corrige-se com
	// um documento novo que aponta para ela.
	KindCredit Kind = "credit_note"
)

// Status é o estado de um documento.
type Status string

const (
	// StatusPending: emitido, à espera de pagamento (proformas).
	StatusPending Status = "pending"
	// StatusPaid: pago.
	StatusPaid Status = "paid"
	// StatusCancelled: deixou de valer sem ter sido pago.
	StatusCancelled Status = "cancelled"
)

// Party é uma das partes do documento: quem emite ou quem é facturado.
type Party struct {
	Name    string
	TaxID   string
	Address string
	Email   string
	Phone   string
	Website string
	Country string
}

// Line é uma linha do documento.
type Line struct {
	Description string
	Quantity    int
	UnitPrice   money.Amount
	TaxRate     int
}

// Total é o valor da linha.
func (l Line) Total() money.Amount {
	q := int64(l.Quantity)
	if q <= 0 {
		q = 1
	}
	return l.UnitPrice.Mul(q)
}

// Invoice é um documento de cobrança.
type Invoice struct {
	// ID é o identificador do registo.
	ID string
	// Number é o número sequencial e único do documento. Uma vez atribuído,
	// nunca muda e nunca é reutilizado, nem quando o documento é cancelado: uma
	// sequência com buracos explica-se, uma sequência com repetições não.
	Number string

	// Kind e Status descrevem o documento.
	Kind   Kind
	Status Status

	// CustomerID e SubjectID ligam o documento a quem chama.
	CustomerID string
	SubjectID  string

	// Issuer e BillTo são as duas partes, guardadas por cópia.
	Issuer Party
	BillTo Party

	// Lines são as linhas do documento.
	Lines []Line

	// Subtotal, Discount e Total são os valores. O desconto é guardado à parte
	// do total para o documento poder mostrar o preço de tabela e a redução, que
	// é o que um cliente com desconto negociado espera ver.
	Subtotal        money.Amount
	DiscountPercent int
	Total           money.Amount

	// Entity, Reference e DueDate são os dados de pagamento de uma proforma.
	Entity    string
	Reference string
	DueDate   string

	// Provider e ProviderRef identificam a cobrança no gateway. O ProviderRef é
	// o que impede a emissão em duplicado quando um webhook é reentregue.
	Provider    string
	ProviderRef string

	// PaymentID é a cobrança que este documento representa.
	PaymentID string

	// PeriodStart e PeriodEnd são o período coberto.
	PeriodStart *time.Time
	PeriodEnd   *time.Time

	// RelatedTo aponta para o documento que este corrige (notas de crédito).
	RelatedTo string

	// Notes é o texto livre do rodapé.
	Notes string

	IssuedAt time.Time
	PaidAt   *time.Time
}

// Store é o armazenamento dos documentos.
type Store interface {
	// Create persiste um documento.
	Create(ctx context.Context, inv *Invoice) error
	// Update grava alterações de estado.
	Update(ctx context.Context, inv *Invoice) error
	// ByID devolve um documento, ou (nil, nil).
	ByID(ctx context.Context, id string) (*Invoice, error)
	// ExistsByProviderRef indica se já existe uma factura para esta referência
	// do gateway. É a defesa contra a emissão em duplicado.
	ExistsByProviderRef(ctx context.Context, kind Kind, providerRef string) (bool, error)
	// PendingProformas devolve as proformas por pagar de um assunto.
	PendingProformas(ctx context.Context, subjectID string) ([]*Invoice, error)
	// ListForCustomer devolve os documentos de um cliente, do mais recente para
	// o mais antigo.
	ListForCustomer(ctx context.Context, customerID string, offset, limit int) ([]*Invoice, int64, error)
}

// Numberer atribui números aos documentos.
//
// A implementação tem de garantir unicidade sob concorrência, e a única forma
// séria de o fazer é uma sequência da base de dados. Contar as linhas
// existentes e somar um dá números repetidos assim que dois documentos são
// emitidos ao mesmo tempo, e um número de factura repetido é um problema com a
// autoridade fiscal, não um problema de software.
type Numberer interface {
	// Next devolve o próximo número para este tipo de documento.
	Next(ctx context.Context, kind Kind) (string, error)
}
