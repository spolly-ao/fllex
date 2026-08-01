package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

type failingPayments struct {
	*memPayments
	onList, onUpdate error
}

func (f *failingPayments) PendingVerifiable(ctx context.Context, provider string, limit int) ([]*payment.Payment, error) {
	if f.onList != nil {
		return nil, f.onList
	}
	return f.memPayments.PendingVerifiable(ctx, provider, limit)
}

func (f *failingPayments) ExpiredPending(ctx context.Context, at time.Time, limit int) ([]*payment.Payment, error) {
	if f.onList != nil {
		return nil, f.onList
	}
	return f.memPayments.ExpiredPending(ctx, at, limit)
}

func (f *failingPayments) Update(ctx context.Context, p *payment.Payment) error {
	if f.onUpdate != nil {
		return f.onUpdate
	}
	return f.memPayments.Update(ctx, p)
}

// verifyFails devolve sempre erro na consulta de estado.
type verifyFails struct{ stubProvider }

func (v *verifyFails) VerifyCharge(context.Context, string, string) (payment.ChargeStatus, error) {
	return payment.ChargeStatus{}, errors.New("gateway em baixo")
}

// cancelFails recusa a revogação.
type cancelFails struct{ stubProvider }

func (c *cancelFails) CancelCharge(context.Context, payment.ChargeRequest, payment.ChargeResult) error {
	return errors.New("gateway em baixo")
}

// noCapabilities não sabe consultar estado nem revogar.
type noCapabilities struct{}

func (noCapabilities) Name() string                         { return "gateway" }
func (noCapabilities) Methods() []payment.Method            { return []payment.Method{payment.MethodReference} }
func (noCapabilities) Configured() bool                     { return true }
func (noCapabilities) SupportsCurrency(money.Currency) bool { return true }
func (noCapabilities) Charge(context.Context, payment.ChargeRequest) (payment.ChargeResult, error) {
	return payment.ChargeResult{}, nil
}

func TestRunnerAdd(t *testing.T) {
	var runs atomic.Int32
	done := make(chan struct{}, 2)
	r := NewRunner().Add(
		Job{Name: "a", Interval: time.Hour, Run: func(context.Context) { runs.Add(1); done <- struct{}{} }},
		Job{Name: "b", Interval: time.Hour, Run: func(context.Context) { runs.Add(1); done <- struct{}{} }},
	)
	r.Log = quietLogger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("só %d tarefas correram", runs.Load())
		}
	}
}

func TestRunnerDefaultsIntervalAndLogger(t *testing.T) {
	// Sem intervalo configurado usa um minuto, e sem registador usa o do
	// sistema: nenhum dos dois pode impedir a tarefa de correr.
	done := make(chan struct{})
	var once atomic.Bool
	r := NewRunner(Job{Name: "sem intervalo", Run: func(context.Context) {
		if once.CompareAndSwap(false, true) {
			close(done)
		}
	}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a tarefa não correu")
	}
	if r.log() == nil {
		t.Error("o registador por omissão não pode ser nil")
	}
}

func TestPollerListErrorStopsThePass(t *testing.T) {
	store := &failingPayments{memPayments: newMemPayments(), onList: errors.New("base de dados em baixo")}
	applied := 0
	p := &Poller{
		Store:    store,
		Registry: payment.NewRegistry().Register(&stubProvider{paid: true}),
		Log:      quietLogger(),
		OnPaid:   func(context.Context, *payment.Payment, payment.ChargeStatus) error { applied++; return nil },
	}
	p.Run(context.Background())
	if applied != 0 {
		t.Error("sem lista não há nada a aplicar")
	}
}

func TestPollerSkipsUnknownProvider(t *testing.T) {
	// Uma cobrança de um gateway que já não está registado não pode derrubar a
	// passagem: fica por confirmar e alguém trata dela.
	pay := newPending(t, "p1")
	pay.Provider = "gateway-desaparecido"
	store := newMemPayments(pay)

	applied := 0
	p := &Poller{
		Store:    store,
		Registry: payment.NewRegistry().Register(&stubProvider{paid: true}),
		Log:      quietLogger(),
		OnPaid:   func(context.Context, *payment.Payment, payment.ChargeStatus) error { applied++; return nil },
	}
	p.Run(context.Background())
	if applied != 0 {
		t.Error("um gateway desconhecido não confirma nada")
	}
}

func TestPollerSkipsProviderWithoutVerify(t *testing.T) {
	store := newMemPayments(newPending(t, "p1"))
	applied := 0
	p := &Poller{
		Store:    store,
		Registry: payment.NewRegistry().Register(noCapabilities{}),
		Log:      quietLogger(),
		OnPaid:   func(context.Context, *payment.Payment, payment.ChargeStatus) error { applied++; return nil },
	}
	p.Run(context.Background())
	if applied != 0 {
		t.Error("um gateway que não sabe consultar estado não confirma nada")
	}
}

func TestPollerHandlesVerifyError(t *testing.T) {
	store := newMemPayments(newPending(t, "p1"))
	p := &Poller{
		Store:    store,
		Registry: payment.NewRegistry().Register(&verifyFails{}),
		Log:      quietLogger(),
	}
	p.Run(context.Background())
	got, _ := store.ByID(context.Background(), "p1")
	if got.Status != payment.StatusPending {
		t.Errorf("estado = %s, queria continuar pendente", got.Status)
	}
}

func TestPollerWithoutOnPaidStillConfirms(t *testing.T) {
	// O efeito é opcional: há quem só queira o estado actualizado.
	store := newMemPayments(newPending(t, "p1"))
	p := &Poller{
		Store:    store,
		Registry: payment.NewRegistry().Register(&stubProvider{paid: true}),
		Log:      quietLogger(),
	}
	p.Run(context.Background())
	got, _ := store.ByID(context.Background(), "p1")
	if got.Status != payment.StatusApproved {
		t.Errorf("estado = %s", got.Status)
	}
}

func TestPollerOnAlreadyTerminalCharge(t *testing.T) {
	// Uma cobrança que mudou de estado entre a leitura e a confirmação não
	// pode ser aprovada por cima: o aviso fica no registo e segue-se.
	pay := newPending(t, "p1")
	store := newMemPayments(pay)
	pay.Status = payment.StatusApproved // já não é pendente

	p := &Poller{
		Store:    &alwaysListsStore{memPayments: store, item: pay},
		Registry: payment.NewRegistry().Register(&stubProvider{paid: true}),
		Log:      quietLogger(),
	}
	p.Run(context.Background())
	if pay.Status != payment.StatusApproved {
		t.Errorf("estado = %s", pay.Status)
	}
}

type alwaysListsStore struct {
	*memPayments
	item *payment.Payment
}

func (a *alwaysListsStore) PendingVerifiable(context.Context, string, int) ([]*payment.Payment, error) {
	return []*payment.Payment{a.item}, nil
}

func TestPollerUpdateFailureIsLogged(t *testing.T) {
	store := &failingPayments{
		memPayments: newMemPayments(newPending(t, "p1")),
		onUpdate:    errors.New("base de dados em baixo"),
	}
	p := &Poller{
		Store:    store,
		Registry: payment.NewRegistry().Register(&stubProvider{paid: true}),
		Log:      quietLogger(),
	}
	p.Run(context.Background()) // não pode entrar em pânico
}

func TestPollerStopsOnCancelledContext(t *testing.T) {
	store := newMemPayments(newPending(t, "p1"), newPending(t, "p2"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	applied := 0
	p := &Poller{
		Store:    store,
		Registry: payment.NewRegistry().Register(&stubProvider{paid: true}),
		Log:      quietLogger(),
		OnPaid:   func(context.Context, *payment.Payment, payment.ChargeStatus) error { applied++; return nil },
	}
	p.Run(ctx)
	if applied != 0 {
		t.Error("com o contexto cancelado não se trata nada")
	}
}

func TestRefOfFallsBackToProviderRef(t *testing.T) {
	p, _ := payment.NewPayment("p1", "s1", money.FromMajor(100, money.AOA), payment.MethodReference)
	p.ProviderRef = "tx-1"
	if got := refOf(p); got != "tx-1" {
		t.Errorf("sem referência de consulta = %q", got)
	}
	p.StatusRef = "op-1"
	if got := refOf(p); got != "op-1" {
		t.Errorf("com referência de consulta = %q", got)
	}
}

func TestPollerDefaultLogger(t *testing.T) {
	if (&Poller{}).log() == nil {
		t.Error("o registador por omissão não pode ser nil")
	}
	if (&Expirer{}).log() == nil {
		t.Error("o registador por omissão não pode ser nil")
	}
}

func TestExpirerListErrorStopsThePass(t *testing.T) {
	store := &failingPayments{memPayments: newMemPayments(), onList: errors.New("base de dados em baixo")}
	notified := 0
	e := &Expirer{
		Store: store, Registry: payment.NewRegistry(), Log: quietLogger(),
		OnExpired: func(context.Context, *payment.Payment) { notified++ },
	}
	e.Run(context.Background())
	if notified != 0 {
		t.Error("sem lista não há nada a expirar")
	}
}

func TestExpirerContinuesWhenRevocationFails(t *testing.T) {
	// O gateway pode recusar a revogação; a cobrança tem de ficar expirada na
	// mesma, senão volta a aparecer em todas as passagens.
	pay := newPending(t, "p1")
	pay.SetExpiry(time.Now().Add(-time.Hour))
	store := newMemPayments(pay)

	e := &Expirer{
		Store:    store,
		Registry: payment.NewRegistry().Register(&cancelFails{}),
		Log:      quietLogger(),
	}
	e.Run(context.Background())
	got, _ := store.ByID(context.Background(), "p1")
	if got.Status != payment.StatusExpired {
		t.Errorf("estado = %s, queria expirada", got.Status)
	}
}

func TestExpirerWithProviderWithoutCancel(t *testing.T) {
	pay := newPending(t, "p1")
	pay.SetExpiry(time.Now().Add(-time.Hour))
	store := newMemPayments(pay)

	e := &Expirer{
		Store:    store,
		Registry: payment.NewRegistry().Register(noCapabilities{}),
		Log:      quietLogger(),
	}
	e.Run(context.Background())
	got, _ := store.ByID(context.Background(), "p1")
	if got.Status != payment.StatusExpired {
		t.Errorf("estado = %s", got.Status)
	}
}

func TestExpirerSkipsAlreadyTerminalCharges(t *testing.T) {
	pay := newPending(t, "p1")
	pay.SetExpiry(time.Now().Add(-time.Hour))
	pay.Status = payment.StatusApproved

	notified := 0
	e := &Expirer{
		Store:     &alwaysExpiredStore{memPayments: newMemPayments(), item: pay},
		Registry:  payment.NewRegistry().Register(&stubProvider{}),
		Log:       quietLogger(),
		OnExpired: func(context.Context, *payment.Payment) { notified++ },
	}
	e.Run(context.Background())
	if notified != 0 {
		t.Error("uma cobrança já paga não se expira")
	}
	if pay.Status != payment.StatusApproved {
		t.Errorf("estado = %s", pay.Status)
	}
}

type alwaysExpiredStore struct {
	*memPayments
	item *payment.Payment
}

func (a *alwaysExpiredStore) ExpiredPending(context.Context, time.Time, int) ([]*payment.Payment, error) {
	return []*payment.Payment{a.item}, nil
}

func TestExpirerUpdateFailureSkipsNotification(t *testing.T) {
	// Se a gravação falhou, a cobrança não está mesmo expirada: avisar seria
	// comunicar uma coisa que não aconteceu.
	pay := newPending(t, "p1")
	pay.SetExpiry(time.Now().Add(-time.Hour))
	store := &failingPayments{
		memPayments: newMemPayments(pay),
		onUpdate:    errors.New("base de dados em baixo"),
	}
	notified := 0
	e := &Expirer{
		Store:     store,
		Registry:  payment.NewRegistry().Register(&stubProvider{}),
		Log:       quietLogger(),
		OnExpired: func(context.Context, *payment.Payment) { notified++ },
	}
	e.Run(context.Background())
	if notified != 0 {
		t.Error("não se avisa de uma expiração que não ficou gravada")
	}
}

func TestExpirerStopsOnCancelledContext(t *testing.T) {
	pay := newPending(t, "p1")
	pay.SetExpiry(time.Now().Add(-time.Hour))
	store := newMemPayments(pay)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	notified := 0
	e := &Expirer{
		Store: store, Registry: payment.NewRegistry().Register(&stubProvider{}),
		Log: quietLogger(), OnExpired: func(context.Context, *payment.Payment) { notified++ },
	}
	e.Run(ctx)
	if notified != 0 {
		t.Error("com o contexto cancelado não se trata nada")
	}
}

func TestExpirerBatchSizeDefault(t *testing.T) {
	pay := newPending(t, "p1")
	pay.SetExpiry(time.Now().Add(-time.Hour))
	store := newMemPayments(pay)
	e := &Expirer{
		Store: store, Registry: payment.NewRegistry().Register(&stubProvider{}),
		Log: quietLogger(), BatchSize: 5,
	}
	e.Run(context.Background())
	got, _ := store.ByID(context.Background(), "p1")
	if got.Status != payment.StatusExpired {
		t.Errorf("estado = %s", got.Status)
	}
}

func TestPollerBatchSizeExplicit(t *testing.T) {
	store := newMemPayments(newPending(t, "p1"))
	p := &Poller{
		Store: store, Registry: payment.NewRegistry().Register(&stubProvider{paid: true}),
		Log: quietLogger(), BatchSize: 10, Provider: "gateway",
	}
	p.Run(context.Background())
	got, _ := store.ByID(context.Background(), "p1")
	if got.Status != payment.StatusApproved {
		t.Errorf("estado = %s", got.Status)
	}
}
