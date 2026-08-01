package wallet

import (
	"context"
	"errors"
	"testing"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

type failingStore struct {
	*memStore
	onEnsure, onApply, onEntries, onByRef error
}

func (f *failingStore) Ensure(ctx context.Context, customerID string, c money.Currency) (*Wallet, error) {
	if f.onEnsure != nil {
		return nil, f.onEnsure
	}
	return f.memStore.Ensure(ctx, customerID, c)
}

func (f *failingStore) Apply(ctx context.Context, w *Wallet, e *Entry) error {
	if f.onApply != nil {
		return f.onApply
	}
	return f.memStore.Apply(ctx, w, e)
}

func (f *failingStore) Entries(ctx context.Context, walletID string, offset, limit int) ([]*Entry, int64, error) {
	if f.onEntries != nil {
		return nil, 0, f.onEntries
	}
	return f.memStore.Entries(ctx, walletID, offset, limit)
}

func (f *failingStore) EntryByReference(ctx context.Context, walletID, ref string) (*Entry, error) {
	if f.onByRef != nil {
		return nil, f.onByRef
	}
	return f.memStore.EntryByReference(ctx, walletID, ref)
}

func TestStatement(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	_, _ = s.Credit(ctx, "cli-1", money.FromMajor(1000, money.AOA), KindTopup, "o1", "carregamento")
	_, _ = s.Debit(ctx, "cli-1", money.FromMajor(400, money.AOA), KindCharge, "c1", "renovação")

	entries, total, err := s.Statement(ctx, "cli-1", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(entries) != 2 {
		t.Errorf("extracto = %d movimentos, total %d", len(entries), total)
	}

	// Um cliente sem carteira vê um extracto vazio, não um erro: quem abre a
	// página do saldo pela primeira vez tem de ver "sem movimentos".
	entries, total, err = s.Statement(ctx, "cli-novo", 0, 50)
	if err != nil || total != 0 || len(entries) != 0 {
		t.Errorf("cliente novo = %v, %d, %v", entries, total, err)
	}
}

func TestStatementPropagatesErrors(t *testing.T) {
	boom := errors.New("base de dados em baixo")
	ctx := context.Background()

	s := NewService(&failingStore{memStore: newMemStore(), onEnsure: boom}, money.AOA)
	if _, _, err := s.Statement(ctx, "cli-1", 0, 10); !errors.Is(err, boom) {
		t.Errorf("extracto = %v", err)
	}
	if _, err := s.Balance(ctx, "cli-1"); !errors.Is(err, boom) {
		t.Errorf("saldo = %v", err)
	}
	if _, err := s.Credit(ctx, "cli-1", money.FromMajor(10, money.AOA), KindTopup, "", ""); !errors.Is(err, boom) {
		t.Errorf("creditar = %v", err)
	}
	if _, err := s.Debit(ctx, "cli-1", money.FromMajor(10, money.AOA), KindCharge, "", ""); !errors.Is(err, boom) {
		t.Errorf("debitar = %v", err)
	}
	if _, err := s.Adjust(ctx, "cli-1", "op-1", money.FromMajor(10, money.AOA), "acerto"); !errors.Is(err, boom) {
		t.Errorf("acertar = %v", err)
	}

	// Falha a procurar o movimento já existente.
	s = NewService(&failingStore{memStore: newMemStore(), onByRef: boom}, money.AOA)
	if _, err := s.Credit(ctx, "cli-1", money.FromMajor(10, money.AOA), KindTopup, "o1", ""); !errors.Is(err, boom) {
		t.Errorf("creditar = %v", err)
	}
	if _, err := s.Debit(ctx, "cli-1", money.FromMajor(10, money.AOA), KindCharge, "c1", ""); !errors.Is(err, boom) {
		t.Errorf("debitar = %v", err)
	}

	// Falha a gravar o movimento.
	s = NewService(&failingStore{memStore: newMemStore(), onApply: boom}, money.AOA)
	if _, err := s.Credit(ctx, "cli-1", money.FromMajor(10, money.AOA), KindTopup, "", ""); !errors.Is(err, boom) {
		t.Errorf("gravar = %v", err)
	}

	// E no extracto, a leitura dos movimentos.
	s = NewService(&failingStore{memStore: newMemStore(), onEntries: boom}, money.AOA)
	if _, _, err := s.Statement(ctx, "cli-1", 0, 10); !errors.Is(err, boom) {
		t.Errorf("movimentos = %v", err)
	}
}

func TestDebitIsIdempotentByReference(t *testing.T) {
	// Uma renovação reprocessada não pode descontar duas vezes.
	s, _ := newService()
	ctx := context.Background()
	_, _ = s.Credit(ctx, "cli-1", money.FromMajor(10000, money.AOA), KindTopup, "o1", "")

	for i := 0; i < 3; i++ {
		if _, err := s.Debit(ctx, "cli-1", money.FromMajor(1000, money.AOA), KindCharge, "renov-1", ""); err != nil {
			t.Fatal(err)
		}
	}
	w, _ := s.Balance(ctx, "cli-1")
	if want := money.FromMajor(9000, money.AOA); w.Balance.Minor != want.Minor {
		t.Errorf("saldo = %s, queria %s", w.Balance, want)
	}
}

func TestNonPositiveAmounts(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	zero := money.Zero(money.AOA)

	if _, err := s.Credit(ctx, "cli-1", zero, KindTopup, "", ""); !errors.Is(err, payment.ErrAmountNotPositive) {
		t.Errorf("creditar zero = %v", err)
	}
	if _, err := s.Debit(ctx, "cli-1", zero, KindCharge, "", ""); !errors.Is(err, payment.ErrAmountNotPositive) {
		t.Errorf("debitar zero = %v", err)
	}
	if _, err := s.Adjust(ctx, "cli-1", "op", zero, "motivo"); !errors.Is(err, payment.ErrAmountNotPositive) {
		t.Errorf("acertar zero = %v", err)
	}
}

func TestRefundCreditsBack(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	w, err := s.Refund(ctx, "cli-1", money.FromMajor(2500, money.AOA), "estorno-1", "cobrança anulada")
	if err != nil {
		t.Fatal(err)
	}
	if want := money.FromMajor(2500, money.AOA); w.Balance.Minor != want.Minor {
		t.Errorf("saldo = %s, queria %s", w.Balance, want)
	}
	entries, _, _ := s.Statement(ctx, "cli-1", 0, 10)
	if len(entries) != 1 || entries[0].Kind != KindRefund {
		t.Errorf("movimento = %+v", entries)
	}
}

func TestValidateTopupWithoutLimits(t *testing.T) {
	// Sem limites configurados, qualquer valor positivo passa.
	s, _ := newService()
	if err := s.ValidateTopup(money.FromMajor(1, money.AOA)); err != nil {
		t.Errorf("= %v", err)
	}
	if err := s.ValidateTopup(money.Zero(money.AOA)); !errors.Is(err, payment.ErrAmountNotPositive) {
		t.Errorf("zero = %v", err)
	}
}

func TestServiceWithoutIDGenerator(t *testing.T) {
	store := newMemStore()
	s := NewService(store, money.AOA)
	s.IDs = nil
	if _, err := s.Credit(context.Background(), "cli-1", money.FromMajor(10, money.AOA), KindTopup, "", ""); err != nil {
		t.Fatal(err)
	}
	w, _ := s.Balance(context.Background(), "cli-1")
	entries, _, _ := store.Entries(context.Background(), w.ID, 0, 10)
	if len(entries) != 1 || entries[0].ID != "" {
		t.Errorf("sem gerador o identificador fica vazio: %+v", entries)
	}
}

func TestApplyRejectsMixedCurrencies(t *testing.T) {
	// Um movimento noutra moeda não pode entrar numa carteira: somar kwanzas a
	// euros nunca é o que quem chama queria.
	store := newMemStore()
	s := NewService(store, money.AOA)
	ctx := context.Background()
	if _, err := s.Credit(ctx, "cli-1", money.FromMajor(10, money.EUR), KindTopup, "", ""); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("moeda diferente = %v", err)
	}
}

func TestProviderMethodsAndRejections(t *testing.T) {
	s, _ := newService()
	p := s.AsProvider()
	ctx := context.Background()

	if ms := p.Methods(); len(ms) != 1 || ms[0] != payment.MethodWallet {
		t.Errorf("métodos = %v", ms)
	}
	// Método errado.
	_, err := p.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(10, money.AOA), Method: payment.MethodCard,
		Customer: payment.Customer{ID: "cli-1"},
	})
	if !errors.Is(err, payment.ErrUnsupportedMethod) {
		t.Errorf("método errado = %v", err)
	}
	// Moeda errada.
	_, err = p.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(10, money.EUR), Method: payment.MethodWallet,
		Customer: payment.Customer{ID: "cli-1"},
	})
	if !errors.Is(err, payment.ErrUnsupportedCurrency) {
		t.Errorf("moeda errada = %v", err)
	}
	// Sem cliente não há carteira de onde tirar.
	_, err = p.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(10, money.AOA), Method: payment.MethodWallet,
	})
	if err == nil {
		t.Error("sem cliente devia falhar")
	}
	// Sem armazenamento ligado.
	unconfigured := (&Service{}).AsProvider()
	if unconfigured.Configured() {
		t.Error("sem armazenamento não está configurado")
	}
	if _, err := unconfigured.Charge(ctx, payment.ChargeRequest{}); !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("= %v", err)
	}
}

func TestProviderChargeWithoutExplicitMethod(t *testing.T) {
	// Um pedido sem método explícito, resolvido pelo registo, tem de funcionar.
	s, _ := newService()
	ctx := context.Background()
	_, _ = s.Credit(ctx, "cli-1", money.FromMajor(1000, money.AOA), KindTopup, "o1", "")

	res, err := s.AsProvider().Charge(ctx, payment.ChargeRequest{
		Reference: "c1", Amount: money.FromMajor(400, money.AOA),
		Customer: payment.Customer{ID: "cli-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != payment.StatusApproved {
		t.Errorf("= %+v", res)
	}
	// O saldo depois do movimento acompanha o resultado, para quem chama poder
	// mostrá-lo sem uma segunda leitura.
	if got, ok := res.Raw["balance_after"].(int64); !ok || got != money.FromMajor(600, money.AOA).Minor {
		t.Errorf("saldo no resultado = %v", res.Raw)
	}
}

func TestFindOptional(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	if got, err := s.store.ByCustomer(ctx, "cli-novo", money.AOA); err != nil || got != nil {
		t.Errorf("cliente sem carteira = %v, %v", got, err)
	}
	_, _ = s.Balance(ctx, "cli-1")
	if got, err := s.store.ByCustomer(ctx, "cli-1", money.AOA); err != nil || got == nil {
		t.Errorf("cliente com carteira = %v, %v", got, err)
	}
}
