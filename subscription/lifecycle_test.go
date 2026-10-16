package subscription

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/payment"
)

// lifecycleStore é o memStore com as duas capacidades opcionais, e com as
// falhas que o teste mandar.
type lifecycleStore struct {
	*memStore
	overdue, ended       []string
	overdueErr, endedErr error
	saveErr              error
	overdueAt, endedAt   time.Time
}

func (l *lifecycleStore) Overdue(_ context.Context, at time.Time, _ int) ([]*Subscription, error) {
	l.overdueAt = at
	if l.overdueErr != nil {
		return nil, l.overdueErr
	}
	return l.pick(l.overdue), nil
}

func (l *lifecycleStore) PeriodEnded(_ context.Context, at time.Time, _ int) ([]*Subscription, error) {
	l.endedAt = at
	if l.endedErr != nil {
		return nil, l.endedErr
	}
	return l.pick(l.ended), nil
}

func (l *lifecycleStore) Save(ctx context.Context, s *Subscription) error {
	if l.saveErr != nil {
		return l.saveErr
	}
	return l.memStore.Save(ctx, s)
}

// failures guarda os avisos de falha, com o prazo e o motivo.
type failures struct {
	recorder
	dues    []time.Time
	reasons []string
}

func (f *failures) PaymentFailed(_ context.Context, _ *Subscription, due time.Time, reason string) {
	f.dues = append(f.dues, due)
	f.reasons = append(f.reasons, reason)
}

func TestMarkOverdueKeepsTheIssuedCharge(t *testing.T) {
	// Ninguém recusou a cobrança: continua emitida, e o cliente ainda a paga.
	s := newSub("s1", payment.MethodReference)
	s.RenewalState = RenewalPending
	due := testNow.AddDate(0, 0, 5)
	s.MarkOverdue(due, testNow)
	if s.Status != StatusPastDue || s.RenewalState != RenewalPending || !s.RenewalDueAt.Equal(due) || !s.UpdatedAt.Equal(testNow) {
		t.Fatalf("estado depois do atraso: %+v", s)
	}
	if !s.Status.Active() {
		t.Fatal("uma subscrição em atraso mantém o acesso durante a tolerância")
	}
}

func TestProcessOverdueMarksAndWarns(t *testing.T) {
	comPrazo := newSub("prazo", payment.MethodReference)
	prazo := testNow.AddDate(0, 0, 3)
	comPrazo.RenewalState, comPrazo.RenewalDueAt = RenewalPending, &prazo

	semPrazo := newSub("janela", payment.MethodMCX)
	fim := testNow.AddDate(0, 0, -1)
	semPrazo.CurrentPeriodEnd, semPrazo.RenewalState = &fim, RenewalPending

	semNada := newSub("nada", payment.MethodMCX)
	semNada.CurrentPeriodEnd = nil

	store := &lifecycleStore{memStore: newMemStore(comPrazo, semPrazo, semNada), overdue: []string{"prazo", "janela", "nada"}}
	n := &failures{}
	e := newEngine(t, store.memStore, newMemPayments(), &fakeGateway{name: "gateway"}, n)
	e.store = store

	if got := e.ProcessOverdue(context.Background()); got != 3 {
		t.Fatalf("marcadas = %d, queria 3", got)
	}
	if !store.overdueAt.Equal(testNow) {
		t.Fatalf("a consulta recebeu %v, e não a hora do motor", store.overdueAt)
	}
	janela := e.Config().Window.For(fim).Closes
	queria := []time.Time{prazo, janela, testNow}
	for i, d := range n.dues {
		if !d.Equal(queria[i]) || n.reasons[i] != ReasonOverdue {
			t.Fatalf("aviso %d: prazo %v motivo %q, queria %v %q", i, d, n.reasons[i], queria[i], ReasonOverdue)
		}
	}
	for _, id := range []string{"prazo", "janela", "nada"} {
		if s := store.subs[id]; s.Status != StatusPastDue {
			t.Fatalf("%s ficou %s", id, s.Status)
		}
	}
}

func TestProcessOverdueFailures(t *testing.T) {
	s := newSub("s1", payment.MethodReference)
	store := &lifecycleStore{memStore: newMemStore(s), overdue: []string{"s1"}, overdueErr: errors.New("base em baixo")}
	n := &failures{}
	e := newEngine(t, store.memStore, newMemPayments(), &fakeGateway{name: "gateway"}, n)
	e.store = store
	if got := e.ProcessOverdue(context.Background()); got != 0 {
		t.Fatalf("com a consulta a falhar marcou %d", got)
	}

	store.overdueErr, store.saveErr = nil, errors.New("gravar falhou")
	if got := e.ProcessOverdue(context.Background()); got != 0 || len(n.dues) != 0 {
		t.Fatalf("com o Save a falhar marcou %d e avisou %d vezes", got, len(n.dues))
	}
}

func TestProcessOverdueNeedsTheCapability(t *testing.T) {
	// Um Store sem a capacidade não ganha passos que não pediu.
	e := newEngine(t, newMemStore(), newMemPayments(), &fakeGateway{name: "gateway"}, nil)
	if e.ProcessOverdue(context.Background()) != 0 || e.ProcessPeriodEnds(context.Background()) != 0 {
		t.Fatal("sem as capacidades, os passos não fazem nada")
	}
}

func TestProcessPeriodEndsCancelsOrExpires(t *testing.T) {
	pedida := newSub("pedida", payment.MethodReference)
	pedida.CancelAtPeriodEnd = true
	pedida.AutoRenew = false
	// Uma cobrança que ficou emitida morre no gateway com a subscrição.
	pedida.RenewalPaymentID = "pag-velho"

	acabou := newSub("acabou", payment.MethodReference)
	acabou.AutoRenew = false

	store := &lifecycleStore{memStore: newMemStore(pedida, acabou), ended: []string{"pedida", "acabou"}}
	pays := newMemPayments()
	velho, err := payment.NewPayment("pag-velho", "pedida", acabou.Amount, payment.MethodReference)
	if err != nil {
		t.Fatal(err)
	}
	velho.Provider = "gateway"
	pays.items[velho.ID] = velho
	gw := &fakeGateway{name: "gateway", methods: []payment.Method{payment.MethodReference}}
	e := newEngine(t, store.memStore, pays, gw, nil)
	e.store = store

	if got := e.ProcessPeriodEnds(context.Background()); got != 2 {
		t.Fatalf("terminadas = %d, queria 2", got)
	}
	if !store.endedAt.Equal(testNow) {
		t.Fatalf("a consulta recebeu %v", store.endedAt)
	}
	if s := store.subs["pedida"]; s.Status != StatusCancelled {
		t.Fatalf("a que pediu para sair ficou %s, e não cancelada", s.Status)
	}
	if s := store.subs["acabou"]; s.Status != StatusExpired {
		t.Fatalf("a do contrato acabado ficou %s, e não expirada", s.Status)
	}
	if gw.cancels != 1 || pays.items["pag-velho"].Status != payment.StatusExpired {
		t.Fatalf("a cobrança pendente não foi revogada: %d revogações, estado %s", gw.cancels, pays.items["pag-velho"].Status)
	}
}

func TestProcessPeriodEndsFailures(t *testing.T) {
	s := newSub("s1", payment.MethodReference)
	s.CancelAtPeriodEnd = true
	store := &lifecycleStore{memStore: newMemStore(s), ended: []string{"s1"}, endedErr: errors.New("base em baixo")}
	e := newEngine(t, store.memStore, newMemPayments(), &fakeGateway{name: "gateway"}, nil)
	e.store = store
	if got := e.ProcessPeriodEnds(context.Background()); got != 0 {
		t.Fatalf("com a consulta a falhar terminou %d", got)
	}
	store.endedErr, store.saveErr = nil, errors.New("gravar falhou")
	if got := e.ProcessPeriodEnds(context.Background()); got != 0 {
		t.Fatalf("com o Save a falhar terminou %d", got)
	}
}

func TestRunIncludesTheOptionalPasses(t *testing.T) {
	atrasada := newSub("atrasada", payment.MethodReference)
	atrasada.RenewalState = RenewalPending
	fim := newSub("fim", payment.MethodReference)
	fim.CancelAtPeriodEnd = true
	store := &lifecycleStore{memStore: newMemStore(atrasada, fim), overdue: []string{"atrasada"}, ended: []string{"fim"}}
	e := newEngine(t, store.memStore, newMemPayments(), &fakeGateway{name: "gateway"}, nil)
	e.store = store
	r := e.Run(context.Background())
	if r.Overdue != 1 || r.Ended != 1 {
		t.Fatalf("relatório %+v", r)
	}
}
