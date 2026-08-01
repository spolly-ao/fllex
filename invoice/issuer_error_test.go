package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/spolly-ao/fllex/money"
)

// Este ficheiro cobre os caminhos em que alguma coisa corre mal. Estão à parte
// do resto porque precisam de um armazenamento que falha a pedido, e misturá-lo
// com os testes do caminho normal tornava esses mais difíceis de ler.

type failingStore struct {
	*memStore
	onCreate, onUpdate, onExists, onPending error
}

func (f *failingStore) Create(ctx context.Context, inv *Invoice) error {
	if f.onCreate != nil {
		return f.onCreate
	}
	return f.memStore.Create(ctx, inv)
}

func (f *failingStore) Update(ctx context.Context, inv *Invoice) error {
	if f.onUpdate != nil {
		return f.onUpdate
	}
	return f.memStore.Update(ctx, inv)
}

func (f *failingStore) ExistsByProviderRef(ctx context.Context, k Kind, ref string) (bool, error) {
	if f.onExists != nil {
		return false, f.onExists
	}
	return f.memStore.ExistsByProviderRef(ctx, k, ref)
}

func (f *failingStore) PendingProformas(ctx context.Context, subjectID string) ([]*Invoice, error) {
	if f.onPending != nil {
		return nil, f.onPending
	}
	return f.memStore.PendingProformas(ctx, subjectID)
}

func issuerWith(store Store, numberer Numberer) *Issuer {
	if numberer == nil {
		counters := map[Kind]int64{}
		numberer = &PrefixNumberer{Sequence: func(_ context.Context, k Kind) (int64, error) {
			counters[k]++
			return counters[k], nil
		}}
	}
	i := NewIssuer(store, numberer, Party{Name: "Empresa"})
	// Identificadores distintos: sem eles, dois documentos aterram na mesma
	// chave do armazenamento em memória e o segundo apaga o primeiro.
	n := 0
	i.IDs = func() string { n++; return "doc-" + string(rune('0'+n)) }
	return i
}

type failingNumberer struct{ err error }

func (f failingNumberer) Next(context.Context, Kind) (string, error) { return "", f.err }

func aoaAmount(v int64) money.Amount { return money.FromMajor(v, money.AOA) }

func TestLineTotalWithoutQuantity(t *testing.T) {
	// Quantidade em falta lê-se como uma, senão uma linha antiga sem o campo
	// preenchido desaparece da factura.
	l := Line{Description: "x", UnitPrice: aoaAmount(100)}
	if got := l.Total(); got.Minor != 10000 {
		t.Errorf("total = %s, queria o preço unitário", got)
	}
	l.Quantity = -3
	if got := l.Total(); got.Minor != 10000 {
		t.Errorf("quantidade negativa = %s", got)
	}
}

func TestNumberingFailurePropagates(t *testing.T) {
	boom := errors.New("sequência em baixo")
	i := issuerWith(newMemStore(), failingNumberer{err: boom})
	ctx := context.Background()
	req := Request{Description: "x", Amount: aoaAmount(100)}

	// Sem número não se emite documento nenhum: um documento fiscal sem número
	// é pior do que um documento em falta.
	if _, err := i.Proforma(ctx, req); !errors.Is(err, boom) {
		t.Errorf("proforma = %v", err)
	}
	if _, err := i.Invoice(ctx, req); !errors.Is(err, boom) {
		t.Errorf("factura = %v", err)
	}
	if _, err := i.CreditNote(ctx, &Invoice{ID: "x"}, aoaAmount(1), "erro"); !errors.Is(err, boom) {
		t.Errorf("nota de crédito = %v", err)
	}
}

func TestCreateFailurePropagates(t *testing.T) {
	boom := errors.New("base de dados em baixo")
	i := issuerWith(&failingStore{memStore: newMemStore(), onCreate: boom}, nil)
	ctx := context.Background()
	req := Request{Description: "x", Amount: aoaAmount(100)}

	if _, err := i.Proforma(ctx, req); !errors.Is(err, boom) {
		t.Errorf("proforma = %v", err)
	}
	if _, err := i.Invoice(ctx, req); !errors.Is(err, boom) {
		t.Errorf("factura = %v", err)
	}
	if _, err := i.CreditNote(ctx, &Invoice{ID: "x"}, aoaAmount(1), "erro"); !errors.Is(err, boom) {
		t.Errorf("nota de crédito = %v", err)
	}
}

func TestDeduplicationLookupFailurePropagates(t *testing.T) {
	// Se não se consegue verificar se já existe, não se emite: emitir às cegas
	// é como aparece uma segunda factura com número novo.
	boom := errors.New("base de dados em baixo")
	i := issuerWith(&failingStore{memStore: newMemStore(), onExists: boom}, nil)
	_, err := i.Invoice(context.Background(), Request{
		Description: "x", Amount: aoaAmount(100), ProviderRef: "in_1",
	})
	if !errors.Is(err, boom) {
		t.Errorf("erro = %v", err)
	}
}

func TestCreditNoteWithoutOriginal(t *testing.T) {
	i := issuerWith(newMemStore(), nil)
	if _, err := i.CreditNote(context.Background(), nil, aoaAmount(1), "x"); err == nil {
		t.Error("uma nota de crédito sem documento original não faz sentido")
	}
}

func TestSettleAndCancelPropagateErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("base de dados em baixo")

	// Falha a ler as proformas.
	i := issuerWith(&failingStore{memStore: newMemStore(), onPending: boom}, nil)
	if _, err := i.Settle(ctx, "sub-1"); !errors.Is(err, boom) {
		t.Errorf("liquidar = %v", err)
	}
	if _, err := i.CancelPending(ctx, "sub-1"); !errors.Is(err, boom) {
		t.Errorf("cancelar = %v", err)
	}

	// Falha a gravar a alteração, já com uma proforma na mão. Cada caso leva o
	// seu assunto: o primeiro deixa a proforma marcada como paga em memória,
	// mesmo tendo falhado a gravar, e reutilizá-la faria o segundo não
	// encontrar nada para cancelar.
	store := &failingStore{memStore: newMemStore()}
	i = issuerWith(store, nil)
	for _, subject := range []string{"sub-1", "sub-2"} {
		if _, err := i.Proforma(ctx, Request{SubjectID: subject, Description: "x", Amount: aoaAmount(100)}); err != nil {
			t.Fatal(err)
		}
	}
	store.onUpdate = boom
	if _, err := i.Settle(ctx, "sub-1"); !errors.Is(err, boom) {
		t.Errorf("liquidar = %v", err)
	}
	if _, err := i.CancelPending(ctx, "sub-2"); !errors.Is(err, boom) {
		t.Errorf("cancelar = %v", err)
	}
}

func TestBuildDefaults(t *testing.T) {
	i := issuerWith(newMemStore(), nil)
	i.Now = nil
	i.IDs = nil
	ctx := context.Background()

	// Sem descrição, a linha ganha um texto por omissão em vez de sair vazia.
	inv, err := i.Invoice(ctx, Request{Amount: aoaAmount(100)})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Lines) != 1 || inv.Lines[0].Description != "Pagamento" {
		t.Errorf("linhas = %+v", inv.Lines)
	}
	if inv.ID != "" {
		t.Errorf("sem gerador de identificadores o campo fica vazio: %q", inv.ID)
	}
	if inv.IssuedAt.IsZero() {
		t.Error("a data de emissão devia vir do relógio do sistema")
	}
}

func TestBuildWithExplicitLinesAndMixedCurrencies(t *testing.T) {
	i := issuerWith(newMemStore(), nil)
	ctx := context.Background()

	inv, err := i.Invoice(ctx, Request{
		Lines: []Line{
			{Description: "a", Quantity: 2, UnitPrice: aoaAmount(100)},
			{Description: "b", Quantity: 1, UnitPrice: aoaAmount(50)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := aoaAmount(250); inv.Subtotal.Minor != want.Minor {
		t.Errorf("subtotal = %s, queria %s", inv.Subtotal, want)
	}

	// Linhas em moedas diferentes não podem somar em silêncio.
	_, err = i.Invoice(ctx, Request{
		Lines: []Line{
			{Description: "a", Quantity: 1, UnitPrice: aoaAmount(100)},
			{Description: "b", Quantity: 1, UnitPrice: money.FromMajor(50, money.EUR)},
		},
	})
	if !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("moedas misturadas = %v", err)
	}
}

func TestPrefixNumbererFallbacks(t *testing.T) {
	// Um tipo sem prefixo configurado ganha um genérico, em vez de sair sem
	// nada antes do número.
	n := &PrefixNumberer{Sequence: func(context.Context, Kind) (int64, error) { return 7, nil }}
	got, err := n.Next(context.Background(), Kind("outro"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "DOC-000007" {
		t.Errorf("= %q", got)
	}

	// Prefixos e largura à medida.
	n = &PrefixNumberer{
		Prefixes: map[Kind]string{KindInvoice: "FAC"},
		Width:    3,
		Sequence: func(context.Context, Kind) (int64, error) { return 7, nil },
	}
	if got, _ := n.Next(context.Background(), KindInvoice); got != "FAC-007" {
		t.Errorf("= %q", got)
	}

	boom := errors.New("sequência em baixo")
	n = &PrefixNumberer{Sequence: func(context.Context, Kind) (int64, error) { return 0, boom }}
	if _, err := n.Next(context.Background(), KindInvoice); !errors.Is(err, boom) {
		t.Errorf("= %v", err)
	}
}

func TestStoreQueriesUsedByCallers(t *testing.T) {
	store := newMemStore()
	i := issuerWith(store, nil)
	ctx := context.Background()
	inv, err := i.Invoice(ctx, Request{CustomerID: "c1", Description: "x", Amount: aoaAmount(100)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.ByID(ctx, inv.ID)
	if err != nil || got == nil {
		t.Errorf("por identificador = %v, %v", got, err)
	}
	if list, _, err := store.ListForCustomer(ctx, "c1", 0, 10); err != nil || list != nil {
		t.Errorf("listagem = %v, %v", list, err)
	}
}
