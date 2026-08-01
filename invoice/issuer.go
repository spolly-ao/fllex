package invoice

import (
	"context"
	"fmt"
	"time"

	"github.com/spolly-ao/fllex/money"
)

// Request são os dados para emitir um documento.
type Request struct {
	// CustomerID e SubjectID ligam o documento a quem chama.
	CustomerID string
	SubjectID  string
	// BillTo são os dados de facturação do cliente.
	BillTo Party
	// Lines são as linhas. Sem elas, é montada uma a partir de Description e
	// Amount.
	Lines []Line
	// Description e Amount montam a linha única do caso simples.
	Description string
	Amount      money.Amount
	// DiscountPercent é o desconto sobre o subtotal.
	DiscountPercent int
	// Entity, Reference e DueDate são os dados de pagamento de uma proforma.
	Entity    string
	Reference string
	DueDate   string
	// Provider, ProviderRef e PaymentID identificam a cobrança.
	Provider    string
	ProviderRef string
	PaymentID   string
	// PeriodStart e PeriodEnd são o período coberto.
	PeriodStart *time.Time
	PeriodEnd   *time.Time
	// Notes é o texto livre do rodapé.
	Notes string
}

// Issuer emite documentos.
type Issuer struct {
	store    Store
	numberer Numberer
	// Issuer são os dados do emitente, comuns a todos os documentos.
	Issuer Party
	// IDs gera identificadores para os documentos.
	IDs func() string
	// Now devolve a hora. Substituível em testes.
	Now func() time.Time
}

// NewIssuer cria o emissor.
func NewIssuer(store Store, numberer Numberer, issuer Party) *Issuer {
	return &Issuer{
		store:    store,
		numberer: numberer,
		Issuer:   issuer,
		Now:      func() time.Time { return time.Now().UTC() },
	}
}

// Proforma emite um documento de cobrança por pagar.
func (i *Issuer) Proforma(ctx context.Context, req Request) (*Invoice, error) {
	inv, err := i.build(ctx, req, KindProforma, StatusPending)
	if err != nil {
		return nil, err
	}
	if err := i.store.Create(ctx, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

// Invoice emite a factura de um pagamento recebido.
//
// Quando a referência do gateway vem preenchida, a emissão é deduplicada por
// ela: um webhook reentregue não emite uma segunda factura com um número novo,
// que é o tipo de duplicado que só se descobre no fecho do mês.
//
// Devolve (nil, nil) quando a factura já existia.
func (i *Issuer) Invoice(ctx context.Context, req Request) (*Invoice, error) {
	if req.ProviderRef != "" {
		exists, err := i.store.ExistsByProviderRef(ctx, KindInvoice, req.ProviderRef)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, nil
		}
	}
	inv, err := i.build(ctx, req, KindInvoice, StatusPaid)
	if err != nil {
		return nil, err
	}
	now := i.now()
	inv.PaidAt = &now
	if err := i.store.Create(ctx, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

// Courtesy emite a factura de uma atribuição sem cobrança: mostra o preço de
// tabela com cem por cento de desconto e total zero.
//
// Existe para que uma cortesia deixe o mesmo rasto documental que uma venda. O
// desconto fica no documento e não na subscrição, de propósito: a renovação
// seguinte cobra o preço cheio.
func (i *Issuer) Courtesy(ctx context.Context, req Request) (*Invoice, error) {
	req.DiscountPercent = 100
	return i.Invoice(ctx, req)
}

// CreditNote emite uma nota de crédito que anula, no todo ou em parte, um
// documento anterior.
func (i *Issuer) CreditNote(ctx context.Context, original *Invoice, amount money.Amount, reason string) (*Invoice, error) {
	if original == nil {
		return nil, fmt.Errorf("invoice: a nota de crédito exige o documento original")
	}
	inv, err := i.build(ctx, Request{
		CustomerID:  original.CustomerID,
		SubjectID:   original.SubjectID,
		BillTo:      original.BillTo,
		Description: reason,
		Amount:      amount,
		Notes:       reason,
	}, KindCredit, StatusPaid)
	if err != nil {
		return nil, err
	}
	inv.RelatedTo = original.ID
	now := i.now()
	inv.PaidAt = &now
	if err := i.store.Create(ctx, inv); err != nil {
		return nil, err
	}
	return inv, nil
}

// Settle marca como pagas as proformas por pagar de um assunto. Corre sempre
// que um pagamento é confirmado, a par da emissão da factura.
func (i *Issuer) Settle(ctx context.Context, subjectID string) (int, error) {
	pending, err := i.store.PendingProformas(ctx, subjectID)
	if err != nil {
		return 0, err
	}
	now := i.now()
	n := 0
	for _, inv := range pending {
		inv.Status = StatusPaid
		inv.PaidAt = &now
		if err := i.store.Update(ctx, inv); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// CancelPending cancela as proformas por pagar de um assunto.
//
// Uma cobrança por ciclo: quando o cliente troca de método a meio (arrancou em
// Multicaixa Express e passou a referência), a proforma anterior deixa de
// valer. Sem isto ficam duas cobranças abertas pelo mesmo ciclo, e o cliente
// pode pagar ambas.
func (i *Issuer) CancelPending(ctx context.Context, subjectID string) (int, error) {
	pending, err := i.store.PendingProformas(ctx, subjectID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, inv := range pending {
		inv.Status = StatusCancelled
		if err := i.store.Update(ctx, inv); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (i *Issuer) build(ctx context.Context, req Request, kind Kind, status Status) (*Invoice, error) {
	lines := req.Lines
	if len(lines) == 0 {
		desc := req.Description
		if desc == "" {
			desc = "Pagamento"
		}
		lines = []Line{{Description: desc, Quantity: 1, UnitPrice: req.Amount}}
	}

	subtotal := money.Zero(req.Amount.Currency)
	for _, l := range lines {
		t := l.Total()
		if subtotal.Currency == "" {
			subtotal = money.Zero(t.Currency)
		}
		sum, err := subtotal.Add(t)
		if err != nil {
			return nil, err
		}
		subtotal = sum
	}

	number, err := i.numberer.Next(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("invoice: atribuir número: %w", err)
	}

	return &Invoice{
		ID:              i.newID(),
		Number:          number,
		Kind:            kind,
		Status:          status,
		CustomerID:      req.CustomerID,
		SubjectID:       req.SubjectID,
		Issuer:          i.Issuer,
		BillTo:          req.BillTo,
		Lines:           lines,
		Subtotal:        subtotal,
		DiscountPercent: req.DiscountPercent,
		Total:           subtotal.PercentOff(req.DiscountPercent),
		Entity:          req.Entity,
		Reference:       req.Reference,
		DueDate:         req.DueDate,
		Provider:        req.Provider,
		ProviderRef:     req.ProviderRef,
		PaymentID:       req.PaymentID,
		PeriodStart:     req.PeriodStart,
		PeriodEnd:       req.PeriodEnd,
		Notes:           req.Notes,
		IssuedAt:        i.now(),
	}, nil
}

func (i *Issuer) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now().UTC()
}

func (i *Issuer) newID() string {
	if i.IDs != nil {
		return i.IDs()
	}
	return ""
}

// PrefixNumberer é um [Numberer] que junta um prefixo a um contador
// fornecido por quem chama.
//
// O contador tem de vir de uma sequência da base de dados
// (SELECT nextval(...), ou uma tabela de contadores com travão de linha). Não
// use um contador em memória: além de repetir números depois de um reinício,
// dois processos a correr em paralelo emitem o mesmo número.
type PrefixNumberer struct {
	// Prefixes é o prefixo de cada tipo de documento.
	Prefixes map[Kind]string
	// Width é o número de dígitos, preenchidos com zeros à esquerda.
	Width int
	// Next devolve o próximo valor da sequência do tipo indicado.
	Sequence func(ctx context.Context, kind Kind) (int64, error)
}

// DefaultPrefixes são os prefixos habituais.
var DefaultPrefixes = map[Kind]string{
	KindInvoice:  "FT",
	KindProforma: "PF",
	KindCredit:   "NC",
}

// Next devolve o próximo número do tipo indicado.
func (n *PrefixNumberer) Next(ctx context.Context, kind Kind) (string, error) {
	if n.Sequence == nil {
		return "", fmt.Errorf("invoice: numerador sem sequência configurada")
	}
	v, err := n.Sequence(ctx, kind)
	if err != nil {
		return "", err
	}
	prefixes := n.Prefixes
	if prefixes == nil {
		prefixes = DefaultPrefixes
	}
	prefix := prefixes[kind]
	if prefix == "" {
		prefix = "DOC"
	}
	width := n.Width
	if width <= 0 {
		width = 6
	}
	return fmt.Sprintf("%s-%0*d", prefix, width, v), nil
}
