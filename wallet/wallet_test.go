package wallet

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// memStore é um armazenamento em memória. Ao contrário de uma implementação a
// sério, não trava o registo da carteira: os testes são de um só fio.
type memStore struct {
	wallets map[string]*Wallet
	entries map[string][]*Entry
	seq     int
}

func newMemStore() *memStore {
	return &memStore{wallets: map[string]*Wallet{}, entries: map[string][]*Entry{}}
}

func (m *memStore) ByCustomer(_ context.Context, customerID string, _ money.Currency) (*Wallet, error) {
	return m.wallets[customerID], nil
}

func (m *memStore) Ensure(_ context.Context, customerID string, currency money.Currency) (*Wallet, error) {
	if w := m.wallets[customerID]; w != nil {
		return w, nil
	}
	m.seq++
	w := &Wallet{
		ID:         fmt.Sprintf("w-%d", m.seq),
		CustomerID: customerID,
		Balance:    money.Zero(currency),
		CreatedAt:  time.Now().UTC(),
	}
	m.wallets[customerID] = w
	return w, nil
}

func (m *memStore) Apply(_ context.Context, w *Wallet, e *Entry) error {
	m.wallets[w.CustomerID] = w
	m.entries[w.ID] = append(m.entries[w.ID], e)
	return nil
}

func (m *memStore) Entries(_ context.Context, walletID string, _, _ int) ([]*Entry, int64, error) {
	list := m.entries[walletID]
	return list, int64(len(list)), nil
}

func (m *memStore) EntryByReference(_ context.Context, walletID, reference string) (*Entry, error) {
	for _, e := range m.entries[walletID] {
		if e.Reference == reference {
			return e, nil
		}
	}
	return nil, nil
}

func newService() (*Service, *memStore) {
	store := newMemStore()
	s := NewService(store, money.AOA)
	n := 0
	s.IDs = func() string { n++; return fmt.Sprintf("mov-%d", n) }
	return s, store
}

func TestCreditThenDebit(t *testing.T) {
	s, store := newService()
	ctx := context.Background()

	if _, err := s.Credit(ctx, "cliente-1", money.FromMajor(10000, money.AOA), KindTopup, "ordem-1", "Carregamento"); err != nil {
		t.Fatal(err)
	}
	w, err := s.Debit(ctx, "cliente-1", money.FromMajor(4000, money.AOA), KindCharge, "cobranca-1", "Renovação")
	if err != nil {
		t.Fatal(err)
	}
	if want := money.FromMajor(6000, money.AOA); w.Balance.Minor != want.Minor {
		t.Errorf("saldo = %s, queria %s", w.Balance, want)
	}

	// A soma do livro tem de ser o saldo: é isso que permite explicar um saldo
	// em vez de acreditar nele.
	entries, _, _ := store.Entries(ctx, w.ID, 0, 100)
	var sum int64
	for _, e := range entries {
		sum += e.Amount.Minor
	}
	if sum != w.Balance.Minor {
		t.Errorf("soma do livro = %d, saldo = %d", sum, w.Balance.Minor)
	}
	if entries[len(entries)-1].BalanceAfter.Minor != w.Balance.Minor {
		t.Error("o saldo depois do movimento não bate com o saldo da carteira")
	}
}

func TestDebitInsufficientFundsWritesNothing(t *testing.T) {
	s, store := newService()
	ctx := context.Background()
	_, _ = s.Credit(ctx, "cliente-1", money.FromMajor(1000, money.AOA), KindTopup, "ordem-1", "")

	_, err := s.Debit(ctx, "cliente-1", money.FromMajor(5000, money.AOA), KindCharge, "cobranca-1", "")
	if !errors.Is(err, payment.ErrInsufficientFunds) {
		t.Errorf("erro = %v, queria saldo insuficiente", err)
	}
	w, _ := s.Balance(ctx, "cliente-1")
	if w.Balance.Minor != money.FromMajor(1000, money.AOA).Minor {
		t.Errorf("saldo mexeu: %s", w.Balance)
	}
	entries, _, _ := store.Entries(ctx, w.ID, 0, 100)
	if len(entries) != 1 {
		t.Errorf("movimentos = %d, queria só o carregamento", len(entries))
	}
}

func TestCreditIsIdempotentByReference(t *testing.T) {
	// Os gateways reentregam webhooks, e um crédito repetido é dinheiro
	// oferecido.
	s, _ := newService()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := s.Credit(ctx, "cliente-1", money.FromMajor(5000, money.AOA), KindTopup, "ordem-1", ""); err != nil {
			t.Fatal(err)
		}
	}
	w, _ := s.Balance(ctx, "cliente-1")
	if want := money.FromMajor(5000, money.AOA); w.Balance.Minor != want.Minor {
		t.Errorf("saldo = %s, queria %s (o carregamento só conta uma vez)", w.Balance, want)
	}
}

func TestAdjustRequiresReason(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	if _, err := s.Adjust(ctx, "cliente-1", "operador-1", money.FromMajor(100, money.AOA), ""); !errors.Is(err, ErrReasonRequired) {
		t.Errorf("erro = %v, queria exigir motivo", err)
	}
}

func TestAdjustCanGoNegative(t *testing.T) {
	// Um operador que corrige um crédito indevido tem de o poder fazer mesmo
	// que o cliente já tenha gasto o dinheiro.
	s, _ := newService()
	ctx := context.Background()
	_, _ = s.Credit(ctx, "cliente-1", money.FromMajor(1000, money.AOA), KindTopup, "ordem-1", "")

	w, err := s.Adjust(ctx, "cliente-1", "operador-1", money.FromMajor(5000, money.AOA).Neg(), "crédito indevido")
	if err != nil {
		t.Fatal(err)
	}
	if !w.Balance.IsNegative() {
		t.Errorf("saldo = %s, queria negativo", w.Balance)
	}
}

func TestValidateTopupLimits(t *testing.T) {
	s, _ := newService()
	s.MinTopup = money.FromMajor(500, money.AOA)
	s.MaxTopup = money.FromMajor(1000000, money.AOA)

	if err := s.ValidateTopup(money.FromMajor(100, money.AOA)); err == nil {
		t.Error("abaixo do mínimo devia falhar")
	}
	if err := s.ValidateTopup(money.FromMajor(5000000, money.AOA)); err == nil {
		t.Error("acima do máximo devia falhar")
	}
	if err := s.ValidateTopup(money.FromMajor(5000, money.AOA)); err != nil {
		t.Errorf("um valor válido falhou: %v", err)
	}
}

func TestProviderChargeDebitsBalance(t *testing.T) {
	s, _ := newService()
	ctx := context.Background()
	_, _ = s.Credit(ctx, "cliente-1", money.FromMajor(10000, money.AOA), KindTopup, "ordem-1", "")

	p := s.AsProvider()
	res, err := p.Charge(ctx, payment.ChargeRequest{
		Reference: "cobranca-1",
		Amount:    money.FromMajor(4000, money.AOA),
		Method:    payment.MethodWallet,
		Customer:  payment.Customer{ID: "cliente-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// O desfecho é imediato e definitivo: não há nada a aguardar.
	if res.Kind != payment.KindPaid || res.Status != payment.StatusApproved {
		t.Errorf("resultado = %+v", res)
	}

	// Sem saldo, a cobrança recusa sem deixar rasto de dívida.
	_, err = p.Charge(ctx, payment.ChargeRequest{
		Reference: "cobranca-2",
		Amount:    money.FromMajor(100000, money.AOA),
		Method:    payment.MethodWallet,
		Customer:  payment.Customer{ID: "cliente-1"},
	})
	if !errors.Is(err, payment.ErrInsufficientFunds) {
		t.Errorf("erro = %v, queria saldo insuficiente", err)
	}
}

func TestProviderSatisfiesPaymentProvider(t *testing.T) {
	s, _ := newService()
	var p payment.Provider = s.AsProvider()
	if p.Name() != "wallet" {
		t.Errorf("nome = %q", p.Name())
	}
	if !p.SupportsCurrency(money.AOA) || p.SupportsCurrency(money.EUR) {
		t.Error("a carteira só trabalha na moeda que gere")
	}
}
