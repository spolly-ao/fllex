package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRunnerRunsOnceAtStartup(t *testing.T) {
	// Sem esta execução imediata, uma instância acabada de arrancar deixa por
	// tratar tudo o que a anterior deixou a meio, durante um intervalo inteiro.
	var runs atomic.Int32
	done := make(chan struct{})
	r := NewRunner(Job{
		Name:     "teste",
		Interval: time.Hour, // longe demais para disparar durante o teste
		Run: func(context.Context) {
			if runs.Add(1) == 1 {
				close(done)
			}
		},
	})
	r.Log = quietLogger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a tarefa não correu ao arranque")
	}
}

func TestRunnerSkipInitial(t *testing.T) {
	var runs atomic.Int32
	r := NewRunner(Job{
		Name: "teste", Interval: time.Hour, SkipInitial: true,
		Run: func(context.Context) { runs.Add(1) },
	})
	r.Log = quietLogger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)
	time.Sleep(200 * time.Millisecond)

	if n := runs.Load(); n != 0 {
		t.Errorf("execuções = %d, queria 0", n)
	}
}

func TestRunnerSurvivesPanic(t *testing.T) {
	// Uma tarefa que morre em silêncio leva consigo o processo que confirma
	// pagamentos, e ninguém dá por isso até o primeiro cliente reclamar.
	var runs atomic.Int32
	r := NewRunner(Job{
		Name:     "explosiva",
		Interval: 50 * time.Millisecond,
		Run: func(context.Context) {
			runs.Add(1)
			panic("algo correu muito mal")
		},
	})
	r.Log = quietLogger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.Start(ctx)

	deadline := time.After(3 * time.Second)
	for {
		if runs.Load() >= 3 {
			return // sobreviveu a dois pânicos e continuou a correr
		}
		select {
		case <-deadline:
			t.Fatalf("execuções = %d, queria pelo menos 3 apesar dos pânicos", runs.Load())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestRunnerStopsOnContextCancel(t *testing.T) {
	var runs atomic.Int32
	r := NewRunner(Job{
		Name: "teste", Interval: 20 * time.Millisecond,
		Run: func(context.Context) { runs.Add(1) },
	})
	r.Log = quietLogger()

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	time.Sleep(150 * time.Millisecond)
	cancel()

	after := runs.Load()
	time.Sleep(200 * time.Millisecond)
	if got := runs.Load(); got > after+1 {
		t.Errorf("continuou a correr depois do cancelamento: %d para %d", after, got)
	}
}

// --- verificador de estado -----------------------------------------------------

type memPayments struct {
	mu    sync.Mutex
	items map[string]*payment.Payment
}

func newMemPayments(ps ...*payment.Payment) *memPayments {
	m := &memPayments{items: map[string]*payment.Payment{}}
	for _, p := range ps {
		m.items[p.ID] = p
	}
	return m
}

func (m *memPayments) Create(_ context.Context, p *payment.Payment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[p.ID] = p
	return nil
}
func (m *memPayments) Update(_ context.Context, p *payment.Payment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[p.ID] = p
	return nil
}
func (m *memPayments) ByID(_ context.Context, id string) (*payment.Payment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.items[id], nil
}
func (m *memPayments) ByProviderRef(context.Context, string, string) (*payment.Payment, error) {
	return nil, nil
}
func (m *memPayments) PendingBySubject(context.Context, string) ([]*payment.Payment, error) {
	return nil, nil
}
func (m *memPayments) PendingVerifiable(context.Context, string, int) ([]*payment.Payment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*payment.Payment
	for _, p := range m.items {
		if p.Status == payment.StatusPending {
			out = append(out, p)
		}
	}
	return out, nil
}
func (m *memPayments) ExpiredPending(_ context.Context, at time.Time, _ int) ([]*payment.Payment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*payment.Payment
	for _, p := range m.items {
		if p.Status == payment.StatusPending && p.Expired(at) {
			out = append(out, p)
		}
	}
	return out, nil
}
func (m *memPayments) RetryDue(context.Context, time.Time, int, int) ([]*payment.Payment, error) {
	return nil, nil
}

type stubProvider struct {
	paid    bool
	cancels int
}

func (s *stubProvider) Name() string                         { return "gateway" }
func (s *stubProvider) Methods() []payment.Method            { return []payment.Method{payment.MethodReference} }
func (s *stubProvider) Configured() bool                     { return true }
func (s *stubProvider) SupportsCurrency(money.Currency) bool { return true }
func (s *stubProvider) Charge(context.Context, payment.ChargeRequest) (payment.ChargeResult, error) {
	return payment.ChargeResult{}, nil
}
func (s *stubProvider) VerifyCharge(context.Context, string, string) (payment.ChargeStatus, error) {
	if !s.paid {
		return payment.ChargeStatus{Status: payment.StatusPending}, nil
	}
	return payment.ChargeStatus{Status: payment.StatusApproved, Paid: true, InvoiceURL: "https://f/1"}, nil
}
func (s *stubProvider) CancelCharge(context.Context, payment.ChargeRequest, payment.ChargeResult) error {
	s.cancels++
	return nil
}

func newPending(t *testing.T, id string) *payment.Payment {
	t.Helper()
	p, err := payment.NewPayment(id, "sub-1", money.FromMajor(5900, money.AOA), payment.MethodReference)
	if err != nil {
		t.Fatal(err)
	}
	p.Provider = "gateway"
	p.ProviderRef = "tx-" + id
	p.StatusRef = "op-" + id
	return p
}

func TestPollerConfirmsPaidCharge(t *testing.T) {
	pay := newPending(t, "p1")
	store := newMemPayments(pay)
	gw := &stubProvider{paid: true}

	applied := 0
	p := &Poller{
		Store:    store,
		Registry: payment.NewRegistry().Register(gw),
		Log:      quietLogger(),
		OnPaid: func(context.Context, *payment.Payment, payment.ChargeStatus) error {
			applied++
			return nil
		},
	}
	p.Run(context.Background())

	if applied != 1 {
		t.Errorf("efeitos aplicados = %d, queria 1", applied)
	}
	got, _ := store.ByID(context.Background(), "p1")
	if got.Status != payment.StatusApproved {
		t.Errorf("estado = %s, queria aprovada", got.Status)
	}
	if got.InvoiceURL == "" {
		t.Error("a factura do gateway devia ter sido guardada")
	}
}

func TestPollerLeavesChargePendingWhenEffectFails(t *testing.T) {
	// Pela ordem contrária, uma falha a activar o serviço deixava a cobrança
	// marcada como paga e o cliente sem nada, e nenhuma passagem seguinte
	// voltaria a tentar.
	pay := newPending(t, "p1")
	store := newMemPayments(pay)

	p := &Poller{
		Store:    store,
		Registry: payment.NewRegistry().Register(&stubProvider{paid: true}),
		Log:      quietLogger(),
		OnPaid: func(context.Context, *payment.Payment, payment.ChargeStatus) error {
			return errors.New("base de dados em baixo")
		},
	}
	p.Run(context.Background())

	got, _ := store.ByID(context.Background(), "p1")
	if got.Status != payment.StatusPending {
		t.Errorf("estado = %s, queria continuar pendente para nova tentativa", got.Status)
	}
}

func TestPollerIgnoresUnpaidCharge(t *testing.T) {
	pay := newPending(t, "p1")
	store := newMemPayments(pay)

	applied := 0
	p := &Poller{
		Store:    store,
		Registry: payment.NewRegistry().Register(&stubProvider{paid: false}),
		Log:      quietLogger(),
		OnPaid:   func(context.Context, *payment.Payment, payment.ChargeStatus) error { applied++; return nil },
	}
	p.Run(context.Background())

	if applied != 0 {
		t.Error("uma cobrança por pagar não pode disparar o efeito")
	}
}

func TestExpirerRevokesAtGateway(t *testing.T) {
	// Sem revogar, a referência continua viva no ATM e quem a pagar fica com um
	// pagamento que alguém tem de devolver à mão.
	pay := newPending(t, "p1")
	pay.SetExpiry(time.Now().Add(-time.Hour))
	store := newMemPayments(pay)
	gw := &stubProvider{}

	notified := 0
	e := &Expirer{
		Store:     store,
		Registry:  payment.NewRegistry().Register(gw),
		Log:       quietLogger(),
		OnExpired: func(context.Context, *payment.Payment) { notified++ },
	}
	e.Run(context.Background())

	if gw.cancels != 1 {
		t.Errorf("revogações = %d, queria 1", gw.cancels)
	}
	got, _ := store.ByID(context.Background(), "p1")
	if got.Status != payment.StatusExpired {
		t.Errorf("estado = %s, queria expirada", got.Status)
	}
	if notified != 1 {
		t.Errorf("avisos = %d, queria 1", notified)
	}
}

func TestWaitEsperaPelasTarefasEmCurso(t *testing.T) {
	// Encerrar sem esperar é fechar a base de dados debaixo de uma tarefa a
	// meio, e uma cobrança interrompida a meio fica cobrada no gateway e por
	// confirmar do nosso lado.
	ctx, cancel := context.WithCancel(context.Background())
	acabou := make(chan struct{})

	r := NewRunner(Job{
		Name: "lenta", Interval: time.Hour,
		Run: func(context.Context) {
			time.Sleep(30 * time.Millisecond)
			close(acabou)
		},
	})
	r.Log = quietLogger()
	r.Start(ctx)

	cancel()
	if !r.Wait(2 * time.Second) {
		t.Fatal("com prazo suficiente, Wait tinha de devolver true")
	}
	select {
	case <-acabou:
	default:
		t.Error("Wait devolveu antes de a tarefa acabar")
	}
}

func TestWaitDesisteQuandoOPrazoPassa(t *testing.T) {
	// Uma tarefa que não responde ao cancelamento não pode prender o
	// encerramento para sempre: o serviço tem de conseguir sair e dizer que
	// saiu à força.
	presa := make(chan struct{})
	defer close(presa)

	r := NewRunner(Job{
		Name: "presa", Interval: time.Hour, Timeout: time.Hour,
		Run: func(context.Context) { <-presa },
	})
	r.Log = quietLogger()

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	cancel()

	if r.Wait(20 * time.Millisecond) {
		t.Error("com a tarefa presa, Wait tinha de devolver false")
	}
}

func TestWaitSemPrazoEsperaAteAoFim(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := NewRunner(Job{Name: "rápida", Interval: time.Hour, Run: func(context.Context) {}})
	r.Log = quietLogger()
	r.Start(ctx)
	cancel()

	if !r.Wait(0) {
		t.Error("sem prazo, Wait espera o que for preciso e devolve true")
	}
}
