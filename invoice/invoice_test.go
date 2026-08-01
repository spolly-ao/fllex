package invoice

import (
	"context"
	"fmt"
	"testing"

	"github.com/spolly-ao/fllex/money"
)

type memStore struct {
	items map[string]*Invoice
	seq   int
}

func newMemStore() *memStore { return &memStore{items: map[string]*Invoice{}} }

func (m *memStore) Create(_ context.Context, inv *Invoice) error {
	m.items[inv.ID] = inv
	return nil
}
func (m *memStore) Update(_ context.Context, inv *Invoice) error {
	m.items[inv.ID] = inv
	return nil
}
func (m *memStore) ByID(_ context.Context, id string) (*Invoice, error) { return m.items[id], nil }
func (m *memStore) ExistsByProviderRef(_ context.Context, kind Kind, ref string) (bool, error) {
	for _, inv := range m.items {
		if inv.Kind == kind && inv.ProviderRef == ref {
			return true, nil
		}
	}
	return false, nil
}
func (m *memStore) PendingProformas(_ context.Context, subjectID string) ([]*Invoice, error) {
	var out []*Invoice
	for _, inv := range m.items {
		if inv.SubjectID == subjectID && inv.Kind == KindProforma && inv.Status == StatusPending {
			out = append(out, inv)
		}
	}
	return out, nil
}
func (m *memStore) ListForCustomer(context.Context, string, int, int) ([]*Invoice, int64, error) {
	return nil, 0, nil
}

func newIssuer() (*Issuer, *memStore) {
	store := newMemStore()
	counters := map[Kind]int64{}
	numberer := &PrefixNumberer{
		Sequence: func(_ context.Context, k Kind) (int64, error) {
			counters[k]++
			return counters[k], nil
		},
	}
	i := NewIssuer(store, numberer, Party{Name: "Empresa", TaxID: "5417000000"})
	n := 0
	i.IDs = func() string { n++; return fmt.Sprintf("doc-%d", n) }
	return i, store
}

func TestProformaAndInvoiceNumbering(t *testing.T) {
	i, _ := newIssuer()
	ctx := context.Background()

	pf, err := i.Proforma(ctx, Request{
		CustomerID: "c1", SubjectID: "sub-1",
		Description: "Plano Essencial", Amount: money.FromMajor(5900, money.AOA),
		Entity: "01234", Reference: "987654321",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pf.Number != "PF-000001" {
		t.Errorf("número da proforma = %q", pf.Number)
	}
	if pf.Status != StatusPending {
		t.Errorf("estado = %s, queria pendente", pf.Status)
	}
	if pf.Issuer.Name != "Empresa" {
		t.Error("os dados do emitente deviam ficar copiados no documento")
	}

	inv, err := i.Invoice(ctx, Request{
		CustomerID: "c1", SubjectID: "sub-1",
		Description: "Plano Essencial", Amount: money.FromMajor(5900, money.AOA),
		ProviderRef: "tx-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// As sequências são independentes: as facturas não herdam a numeração das
	// proformas.
	if inv.Number != "FT-000001" {
		t.Errorf("número da factura = %q", inv.Number)
	}
	if inv.PaidAt == nil {
		t.Error("uma factura nasce paga")
	}
}

func TestInvoiceDeduplicatesByProviderRef(t *testing.T) {
	// Os gateways reentregam webhooks; emitir a segunda factura com um número
	// novo é um duplicado que só se descobre no fecho do mês.
	i, store := newIssuer()
	ctx := context.Background()
	req := Request{
		CustomerID: "c1", SubjectID: "sub-1",
		Description: "Plano", Amount: money.FromMajor(5900, money.AOA),
		ProviderRef: "in_123",
	}
	first, err := i.Invoice(ctx, req)
	if err != nil || first == nil {
		t.Fatalf("primeira emissão: %v", err)
	}
	second, err := i.Invoice(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Errorf("segunda emissão devolveu %+v, queria nil", second)
	}
	if len(store.items) != 1 {
		t.Errorf("documentos = %d, queria 1", len(store.items))
	}
}

func TestDiscountShowsGrossAndTotal(t *testing.T) {
	i, _ := newIssuer()
	inv, err := i.Invoice(context.Background(), Request{
		CustomerID: "c1", Description: "Plano",
		Amount: money.FromMajor(5000, money.AOA), DiscountPercent: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	// O documento tem de mostrar o preço de tabela e a redução, que é o que um
	// cliente com desconto negociado espera ver.
	if inv.Subtotal.Minor != money.FromMajor(5000, money.AOA).Minor {
		t.Errorf("subtotal = %s, queria o preço de tabela", inv.Subtotal)
	}
	if want := money.FromMajor(4000, money.AOA); inv.Total.Minor != want.Minor {
		t.Errorf("total = %s, queria %s", inv.Total, want)
	}
}

func TestCourtesyIsFullyDiscounted(t *testing.T) {
	i, _ := newIssuer()
	inv, err := i.Courtesy(context.Background(), Request{
		CustomerID: "c1", Description: "Plano oferecido",
		Amount: money.FromMajor(5000, money.AOA),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !inv.Total.IsZero() {
		t.Errorf("total = %s, queria zero", inv.Total)
	}
	// O rasto do valor do plano fica no documento, mesmo sem dinheiro cobrado.
	if inv.Subtotal.IsZero() {
		t.Error("o subtotal devia mostrar o valor do plano")
	}
}

func TestSettleAndCancelProformas(t *testing.T) {
	i, _ := newIssuer()
	ctx := context.Background()

	_, _ = i.Proforma(ctx, Request{SubjectID: "sub-1", Description: "Ciclo 1", Amount: money.FromMajor(100, money.AOA)})
	n, err := i.Settle(ctx, "sub-1")
	if err != nil || n != 1 {
		t.Fatalf("liquidação: %d, %v", n, err)
	}
	if n, _ := i.Settle(ctx, "sub-1"); n != 0 {
		t.Errorf("segunda liquidação = %d, queria 0", n)
	}

	// Uma cobrança por ciclo: ao trocar de método, a proforma anterior deixa de
	// valer, senão ficam duas cobranças abertas pelo mesmo ciclo.
	_, _ = i.Proforma(ctx, Request{SubjectID: "sub-2", Description: "Ciclo 1", Amount: money.FromMajor(100, money.AOA)})
	n, err = i.CancelPending(ctx, "sub-2")
	if err != nil || n != 1 {
		t.Fatalf("cancelamento: %d, %v", n, err)
	}
}

func TestCreditNotePointsAtOriginal(t *testing.T) {
	i, _ := newIssuer()
	ctx := context.Background()
	original, _ := i.Invoice(ctx, Request{CustomerID: "c1", Description: "Plano", Amount: money.FromMajor(5000, money.AOA)})

	note, err := i.CreditNote(ctx, original, money.FromMajor(5000, money.AOA), "anulação por erro de facturação")
	if err != nil {
		t.Fatal(err)
	}
	if note.Kind != KindCredit {
		t.Errorf("tipo = %s", note.Kind)
	}
	// Uma factura emitida não se apaga nem se altera: corrige-se com um
	// documento novo que aponta para ela.
	if note.RelatedTo != original.ID {
		t.Errorf("documento relacionado = %q, queria %q", note.RelatedTo, original.ID)
	}
	if note.Number != "NC-000001" {
		t.Errorf("número = %q", note.Number)
	}
}

func TestNumbererRequiresSequence(t *testing.T) {
	n := &PrefixNumberer{}
	if _, err := n.Next(context.Background(), KindInvoice); err == nil {
		t.Error("sem sequência configurada devia falhar em vez de inventar números")
	}
}
