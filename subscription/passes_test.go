package subscription

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/cycle"
	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// brokenStore avaria à ordem: as consultas e a gravação falham quando o teste
// mandar. É o que permite verificar que uma passagem que tropeça numa
// subscrição continua para a seguinte em vez de morrer.
type brokenStore struct {
	*memStore
	listErr error
	saveErr error
}

func (b brokenStore) Save(ctx context.Context, s *Subscription) error {
	if b.saveErr != nil {
		return b.saveErr
	}
	return b.memStore.Save(ctx, s)
}

func (b brokenStore) DueForWarning(ctx context.Context, t time.Time, n int) ([]*Subscription, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.memStore.DueForWarning(ctx, t, n)
}

func (b brokenStore) DueForCharge(ctx context.Context, t time.Time, n int) ([]*Subscription, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.memStore.DueForCharge(ctx, t, n)
}

func (b brokenStore) AwaitingPayment(ctx context.Context, n int) ([]*Subscription, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.memStore.AwaitingPayment(ctx, n)
}

func (b brokenStore) RetryDue(ctx context.Context, t time.Time, max, n int) ([]*Subscription, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.memStore.RetryDue(ctx, t, max, n)
}

func (b brokenStore) PastDueWithoutDeadline(ctx context.Context, n int) ([]*Subscription, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.memStore.PastDueWithoutDeadline(ctx, n)
}

func (b brokenStore) ExpiredWindows(ctx context.Context, t time.Time, n int) ([]*Subscription, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.memStore.ExpiredWindows(ctx, t, n)
}

func (b brokenStore) StaleCharges(ctx context.Context, t time.Time, n int) ([]*Subscription, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.memStore.StaleCharges(ctx, t, n)
}

// brokenPayments falha a criar, a actualizar ou a ler cobranças.
type brokenPayments struct {
	*memPayments
	createErr, updateErr, byIDErr error
}

func (b brokenPayments) Create(ctx context.Context, p *payment.Payment) error {
	if b.createErr != nil {
		return b.createErr
	}
	return b.memPayments.Create(ctx, p)
}

func (b brokenPayments) Update(ctx context.Context, p *payment.Payment) error {
	if b.updateErr != nil {
		return b.updateErr
	}
	return b.memPayments.Update(ctx, p)
}

func (b brokenPayments) ByID(ctx context.Context, id string) (*payment.Payment, error) {
	if b.byIDErr != nil {
		return nil, b.byIDErr
	}
	return b.memPayments.ByID(ctx, id)
}

// plainGateway não sabe consultar estado nem cancelar: serve para exercitar os
// caminhos em que a capacidade não existe.
type plainGateway struct{ name string }

func (p plainGateway) Name() string                         { return p.name }
func (p plainGateway) Methods() []payment.Method            { return []payment.Method{payment.MethodReference} }
func (p plainGateway) Configured() bool                     { return true }
func (p plainGateway) SupportsCurrency(money.Currency) bool { return true }
func (p plainGateway) Charge(context.Context, payment.ChargeRequest) (payment.ChargeResult, error) {
	return payment.ChargeResult{Kind: payment.KindReference, Status: payment.StatusPending}, nil
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func referenceGateway() *fakeGateway {
	return &fakeGateway{
		name:    "gateway",
		methods: []payment.Method{payment.MethodReference},
		result: payment.ChargeResult{
			Kind: payment.KindReference, Status: payment.StatusPending,
			ProviderRef: "tx-1", StatusRef: "op-1",
		},
	}
}

// --- configuração e valores por omissão ---------------------------------------

func TestEngineDefaults(t *testing.T) {
	e := NewEngine(newMemStore(), nil, payment.NewRegistry(), nil, Config{})
	cfg := e.Config()
	if cfg.WarnBeforeDays != 4 || cfg.MaxAttempts != 5 || cfg.BatchSize != 200 {
		t.Errorf("valores por omissão = %+v", cfg)
	}
	if cfg.FallbackMethod != payment.MethodReference {
		t.Errorf("método de recurso = %q", cfg.FallbackMethod)
	}
	if cfg.Window.LeadDays != cycle.DefaultWindow.LeadDays {
		t.Errorf("janela = %+v", cfg.Window)
	}
	if _, ok := e.notify.(NopNotifier); !ok {
		t.Errorf("sem notificador devia ficar o vazio, ficou %T", e.notify)
	}

	// Sem relógio, sem gerador e sem registador, o motor recorre aos seus.
	e.Now, e.IDs, e.Log = nil, nil, nil
	if e.now().IsZero() {
		t.Error("o relógio por omissão devia dar a hora")
	}
	if e.newID() != "" {
		t.Error("sem gerador, o identificador fica vazio para quem grava o decidir")
	}
	if e.log() == nil {
		t.Error("o registador por omissão nunca é nil")
	}
}

func TestNopNotifierAcceptsEverything(t *testing.T) {
	// Arrancar sem avisos tem de ser possível: quem integra a biblioteca pode
	// não ter ainda para onde os mandar.
	var n Notifier = NopNotifier{}
	ctx := context.Background()
	s := newSub("s1", payment.MethodReference)
	n.RenewalWarning(ctx, s, testNow)
	n.RenewalReminder(ctx, s, testNow)
	n.ChargeIssued(ctx, s, Charge{})
	n.PaymentFailed(ctx, s, testNow, "cartão recusado")
	n.Renewed(ctx, s)
	n.Expired(ctx, s)
}

func TestCustomerFuncAdapter(t *testing.T) {
	var r CustomerResolver = CustomerFunc(func(_ context.Context, s *Subscription) (payment.Customer, error) {
		return payment.Customer{ID: s.CustomerID, Name: "Ana"}, nil
	})
	c, err := r.Customer(context.Background(), newSub("s1", payment.MethodReference))
	if err != nil || c.Name != "Ana" {
		t.Errorf("= %+v, %v", c, err)
	}
}

// --- estado da subscrição -----------------------------------------------------

func TestStatusActiveIncludesPastDue(t *testing.T) {
	// Em falha de pagamento o acesso mantém-se: é essa a diferença entre dar
	// prazo ao cliente e cortar-lhe o serviço no primeiro erro do banco.
	if !StatusActive.Active() || !StatusPastDue.Active() {
		t.Error("activa e em falha de pagamento contam como activas")
	}
	if StatusCancelled.Active() || StatusExpired.Active() {
		t.Error("cancelada e expirada não estão activas")
	}
}

func TestExpiredInGraceAndPastDue(t *testing.T) {
	end := time.Date(2025, time.July, 10, 0, 0, 0, 0, time.UTC)
	due := time.Date(2025, time.July, 20, 23, 59, 59, 0, time.UTC)
	s := &Subscription{CurrentPeriodEnd: &end, RenewalDueAt: &due}

	if s.Expired(time.Date(2025, time.July, 5, 0, 0, 0, 0, time.UTC)) {
		t.Error("antes do fim do ciclo não está expirada")
	}
	if !s.Expired(testNow) {
		t.Error("depois do fim do ciclo está expirada")
	}
	if !s.InGrace(testNow) {
		t.Error("dentro do prazo é tolerância, e a tolerância dá acesso")
	}
	if s.PastDue(testNow) {
		t.Error("dentro do prazo ainda não passou o prazo")
	}

	after := time.Date(2025, time.July, 25, 0, 0, 0, 0, time.UTC)
	if s.InGrace(after) || !s.PastDue(after) {
		t.Error("passado o prazo, acabou a tolerância")
	}

	// Sem fim de ciclo nem prazo não há nada a decidir.
	empty := &Subscription{}
	if empty.Expired(testNow) || empty.InGrace(testNow) || empty.PastDue(testNow) {
		t.Error("uma subscrição sem datas não expira nem entra em prazo")
	}
}

func TestCancelNowAndAtPeriodEnd(t *testing.T) {
	s := newSub("s1", payment.MethodCard)
	s.Cancel(true, testNow)
	if s.Status != StatusActive || !s.CancelAtPeriodEnd || s.AutoRenew {
		t.Errorf("cancelar no fim do período mantém o acesso pago: %+v", s.Status)
	}

	s = newSub("s2", payment.MethodCard)
	s.Cancel(false, testNow)
	if s.Status != StatusCancelled || s.AutoRenew {
		t.Errorf("estado = %q, renovação = %v", s.Status, s.AutoRenew)
	}
}

func TestRenewalAmountFallsBackToCurrent(t *testing.T) {
	// As subscrições antigas não têm preço de tabela guardado; o valor corrente
	// é o melhor palpite que há.
	s := &Subscription{Amount: money.FromMajor(3000, money.AOA)}
	if got := s.RenewalAmount(); got.Minor != money.FromMajor(3000, money.AOA).Minor {
		t.Errorf("= %s", got)
	}
}

func TestInstalmentAmountOnFirstCycle(t *testing.T) {
	// Um contrato ainda sem ciclo contado está na primeira prestação, e não
	// numa prestação negativa.
	s := &Subscription{
		GrossAmount:    money.FromMajor(120000, money.AOA),
		ContractMonths: 12, BillingPeriodMonths: 1, CycleNumber: 0,
	}
	if got := s.InstalmentAmount(); got.Minor != money.FromMajor(10000, money.AOA).Minor {
		t.Errorf("prestação = %s, queria um doze avos", got)
	}
}

func TestNextPeriodEndFollowsTheContract(t *testing.T) {
	end := time.Date(2025, time.July, 31, 0, 0, 0, 0, time.UTC)
	s := &Subscription{
		CurrentPeriodEnd: &end, ContractMonths: 12, BillingPeriodMonths: 1,
		AnchorDay: 31, StartDate: time.Date(2025, time.July, 31, 0, 0, 0, 0, time.UTC),
	}
	// Quem assinou a 31 não pode ficar preso ao dia 30 por causa de Setembro.
	got := s.NextPeriodEnd(testNow, false)
	if got.Day() != 31 || got.Month() != time.August {
		t.Errorf("fim do ciclo = %s", got.Format("2006-01-02"))
	}
}

func TestNextPeriodEndWithoutIntervalIsMonthly(t *testing.T) {
	s := &Subscription{} // sem periodicidade, sem âncora e sem adesão
	got := s.NextPeriodEnd(testNow, false)
	if got.Month() != time.August {
		t.Errorf("sem periodicidade devia ser mensal, deu %s", got.Format("2006-01-02"))
	}
}

func TestActivateSetsStartDateOnFirstPayment(t *testing.T) {
	s := &Subscription{Status: StatusPending, Interval: cycle.Monthly,
		Amount: money.FromMajor(5000, money.AOA)}
	s.Activate(testNow)
	if !s.StartDate.Equal(testNow) {
		t.Errorf("adesão = %s, queria a data do primeiro pagamento", s.StartDate)
	}
	if s.AnchorDay != testNow.Day() {
		t.Errorf("dia de cobrança = %d", s.AnchorDay)
	}
}

// --- passagens ----------------------------------------------------------------

func TestRunExecutesEveryPass(t *testing.T) {
	// A passagem completa é o que corre no agendador. O que importa aqui é que
	// nenhum passo fica de fora e que a ordem não deixa ninguém cortado com o
	// pagamento já feito.
	sub := newSub("s1", payment.MethodReference)
	sub.RenewalStatusRef, sub.RenewalPaymentID = "op-1", "pag-antigo"

	toWarn := newSub("s2", payment.MethodCard)
	toCharge := newSub("s3", payment.MethodReference)
	toExpire := newSub("s4", payment.MethodReference)
	toDun := newSub("s5", payment.MethodCard)
	stale := newSub("s6", payment.MethodReference)

	store := newMemStore(sub, toWarn, toCharge, toExpire, toDun, stale)
	store.awaiting = []string{"s1"}
	store.warn = []string{"s2"}
	store.charge = []string{"s3"}
	store.pastDue = []string{"s5"}
	store.stale = []string{"s6"}
	store.expired = []string{"s4"}

	pays := newMemPayments()
	gw := referenceGateway()
	gw.verifyOK = true
	gw.verified = payment.ChargeStatus{Paid: true}

	rec := &recorder{}
	e := newEngine(t, store, pays, gw, rec)
	e.Log = discard()

	r := e.Run(context.Background())
	if r.Confirmed != 1 || r.Warned != 1 || r.Charged != 1 || r.Dunned != 1 || r.Cleaned != 1 || r.Expired != 1 {
		t.Errorf("relatório = %+v", r)
	}
	if rec.renewals != 1 || rec.expiries != 1 || rec.failures != 1 {
		t.Errorf("avisos: renovações %d, expirações %d, falhas %d", rec.renewals, rec.expiries, rec.failures)
	}
}

func TestPassesSurviveAFailingStore(t *testing.T) {
	// Uma consulta que rebenta não pode derrubar a passagem: o agendador volta
	// a correr daí a pouco, e entretanto ninguém é cortado por engano.
	boom := errors.New("base de dados em baixo")
	store := brokenStore{memStore: newMemStore(), listErr: boom}
	e := NewEngine(store, newMemPayments(), payment.NewRegistry(), &recorder{}, Config{})
	e.Now = func() time.Time { return testNow }
	e.Log = discard()

	if r := e.Run(context.Background()); r != (Report{}) {
		t.Errorf("com o armazenamento em baixo não se trata nada: %+v", r)
	}
}

func TestPassesSkipWhatFailsToSave(t *testing.T) {
	// Gravar é a última coisa que cada passo faz; se falhar, essa subscrição
	// não conta e a passagem segue para a seguinte.
	boom := errors.New("disco cheio")
	mem := newMemStore(
		newSub("s1", payment.MethodCard),
		newSub("s2", payment.MethodCard),
		newSub("s3", payment.MethodReference),
	)
	mem.warn = []string{"s1"}
	mem.pastDue = []string{"s2"}
	mem.expired = []string{"s3"}
	mem.stale = []string{"s3"}

	store := brokenStore{memStore: mem, saveErr: boom}
	rec := &recorder{}
	e := NewEngine(store, newMemPayments(), payment.NewRegistry(), rec, Config{})
	e.Now = func() time.Time { return testNow }
	e.Log = discard()

	ctx := context.Background()
	if n := e.ProcessWarnings(ctx); n != 0 {
		t.Errorf("avisos = %d", n)
	}
	if n := e.ProcessDunning(ctx); n != 0 {
		t.Errorf("prazos = %d", n)
	}
	if n := e.ProcessExpiry(ctx); n != 0 {
		t.Errorf("expirações = %d", n)
	}
	if n := e.ProcessStaleCharges(ctx); n != 0 {
		t.Errorf("limpezas = %d", n)
	}
	if rec.failures != 0 || rec.expiries != 0 {
		t.Error("o que não ficou gravado não se comunica ao cliente")
	}
}

func TestProcessConfirmationsSkipsWhatCannotBeChecked(t *testing.T) {
	semRef := newSub("s1", payment.MethodReference) // sem referência de consulta
	semProvider := newSub("s2", payment.MethodReference)
	semProvider.RenewalStatusRef, semProvider.Provider = "op-2", ""
	semVerifier := newSub("s3", payment.MethodReference)
	semVerifier.RenewalStatusRef, semVerifier.Provider = "op-3", "simples"
	desconhecido := newSub("s4", payment.MethodReference)
	desconhecido.RenewalStatusRef, desconhecido.Provider = "op-4", "gateway-que-nao-existe"

	store := newMemStore(semRef, semProvider, semVerifier, desconhecido)
	store.awaiting = []string{"s1", "s2", "s3", "s4"}

	registry := payment.NewRegistry().Register(plainGateway{name: "simples"})
	e := NewEngine(store, newMemPayments(), registry, &recorder{}, Config{})
	e.Now = func() time.Time { return testNow }
	e.Log = discard()

	if n := e.ProcessConfirmations(context.Background()); n != 0 {
		t.Errorf("confirmadas = %d, nenhuma dava para consultar", n)
	}
}

func TestProcessConfirmationsHandlesGatewayAndSaveFailures(t *testing.T) {
	sub := newSub("s1", payment.MethodReference)
	sub.RenewalStatusRef = "op-1"
	mem := newMemStore(sub)
	mem.awaiting = []string{"s1"}

	gw := referenceGateway()
	gw.verifyOK = false // consulta sem resposta útil: não pago
	registry := payment.NewRegistry().Register(gw)

	e := NewEngine(mem, newMemPayments(), registry, &recorder{}, Config{})
	e.Now = func() time.Time { return testNow }
	e.Log = discard()

	ctx := context.Background()
	if n := e.ProcessConfirmations(ctx); n != 0 {
		t.Errorf("por pagar não se confirma: %d", n)
	}

	// Pago, mas a gravação falha: não conta como confirmado, e a passagem
	// seguinte volta a tentar.
	gw.verifyOK, gw.verified = true, payment.ChargeStatus{Paid: true}
	e2 := NewEngine(brokenStore{memStore: mem, saveErr: errors.New("disco")},
		newMemPayments(), registry, &recorder{}, Config{})
	e2.Now = func() time.Time { return testNow }
	e2.Log = discard()
	if n := e2.ProcessConfirmations(ctx); n != 0 {
		t.Errorf("confirmadas = %d", n)
	}
}

func TestProcessConfirmationsPropagatesVerifyError(t *testing.T) {
	sub := newSub("s1", payment.MethodReference)
	sub.RenewalStatusRef = "op-1"
	store := newMemStore(sub)
	store.awaiting = []string{"s1"}

	registry := payment.NewRegistry().Register(&erringVerifier{})
	sub.Provider = "erro"
	e := NewEngine(store, newMemPayments(), registry, &recorder{}, Config{})
	e.Now = func() time.Time { return testNow }
	e.Log = discard()

	if n := e.ProcessConfirmations(context.Background()); n != 0 {
		t.Errorf("com o gateway em baixo não se confirma nada: %d", n)
	}
}

type erringVerifier struct{ plainGateway }

func (erringVerifier) Name() string { return "erro" }
func (erringVerifier) VerifyCharge(context.Context, string, string) (payment.ChargeStatus, error) {
	return payment.ChargeStatus{}, errors.New("gateway indisponível")
}

func TestProcessWarningsDistinguishesWhoHasToAct(t *testing.T) {
	auto := newSub("s1", payment.MethodCard)        // o gateway cobra sozinho
	manual := newSub("s2", payment.MethodReference) // o cliente tem de pagar
	store := newMemStore(auto, manual)
	store.warn = []string{"s1", "s2"}

	rec := &recorder{}
	e := newEngine(t, store, newMemPayments(), referenceGateway(), rec)
	if n := e.ProcessWarnings(context.Background()); n != 2 {
		t.Errorf("avisados = %d", n)
	}
	if rec.reminders != 1 || rec.warnings != 1 {
		t.Errorf("lembretes = %d, avisos = %d: mandar pagar quem já é cobrado gera duplicados",
			rec.reminders, rec.warnings)
	}
	if auto.RenewalState != RenewalWarned || manual.RenewalWarnedAt == nil {
		t.Error("o aviso tem de ficar registado, ou repete-se todos os dias")
	}
}

func TestProcessWarningsWithoutPeriodEnd(t *testing.T) {
	// Sem fim de ciclo registado avisa-se na mesma, com a data de hoje: mais
	// vale um aviso impreciso do que nenhum.
	s := newSub("s1", payment.MethodReference)
	s.CurrentPeriodEnd = nil
	store := newMemStore(s)
	store.warn = []string{"s1"}

	rec := &recorder{}
	e := newEngine(t, store, newMemPayments(), referenceGateway(), rec)
	if n := e.ProcessWarnings(context.Background()); n != 1 || rec.warnings != 1 {
		t.Errorf("avisados = %d, avisos = %d", n, rec.warnings)
	}
}

func TestProcessChargesSkipsWhatHasNothingToCharge(t *testing.T) {
	gratuito := newSub("s1", payment.MethodReference)
	gratuito.Amount, gratuito.GrossAmount = money.Zero(money.AOA), money.Zero(money.AOA)
	normal := newSub("s2", payment.MethodReference)

	store := newMemStore(gratuito, normal)
	store.charge = []string{"s1", "s2"}

	gw := referenceGateway()
	e := newEngine(t, store, newMemPayments(), gw, &recorder{})
	e.Log = discard()

	if n := e.ProcessCharges(context.Background()); n != 1 {
		t.Errorf("cobradas = %d, o plano sem valor não se cobra", n)
	}
	if gw.charges != 1 {
		t.Errorf("chamadas ao gateway = %d", gw.charges)
	}
}

func TestProcessChargesContinuesAfterAFailure(t *testing.T) {
	// O gateway em baixo não impede a passagem de tratar as restantes: a que
	// falhou fica com prazo marcado e o aviso segue com o link.
	s := newSub("s1", payment.MethodReference)
	store := newMemStore(s)
	store.charge = []string{"s1"}

	gw := referenceGateway()
	gw.err = errors.New("gateway em baixo")
	e := newEngine(t, store, newMemPayments(), gw, &recorder{})
	e.Log = discard()

	if n := e.ProcessCharges(context.Background()); n != 0 {
		t.Errorf("cobradas = %d", n)
	}
	if s.RenewalState != RenewalPending || s.RenewalDueAt == nil {
		t.Error("sem cobrança emitida, o cliente fica na mesma com prazo e forma de pagar")
	}
}

func TestIssueChargeWithoutPeriodEnd(t *testing.T) {
	// Uma subscrição sem ciclo em vigor (a primeira cobrança) conta a janela a
	// partir de agora.
	s := newSub("s1", payment.MethodReference)
	s.CurrentPeriodEnd = nil
	store := newMemStore(s)
	e := newEngine(t, store, newMemPayments(), referenceGateway(), &recorder{})

	charge, err := e.IssueCharge(context.Background(), s, payment.MethodReference)
	if err != nil {
		t.Fatal(err)
	}
	if !charge.DueAt.After(testNow) {
		t.Errorf("prazo = %s, tinha de ser depois de agora", charge.DueAt)
	}
}

func TestIssueChargeUsesCustomerAndLink(t *testing.T) {
	s := newSub("s1", payment.MethodReference)
	store := newMemStore(s)
	gw := referenceGateway()
	e := newEngine(t, store, newMemPayments(), gw, &recorder{})
	e.Customers = CustomerFunc(func(context.Context, *Subscription) (payment.Customer, error) {
		return payment.Customer{ID: "c1", Name: "Ana", Email: "ana@exemplo.ao"}, nil
	})
	e.Links = LinkFunc(func(context.Context, *Subscription, time.Time) (string, error) {
		return "https://exemplo.ao/renovar/abc", nil
	})

	charge, err := e.IssueCharge(context.Background(), s, payment.MethodReference)
	if err != nil {
		t.Fatal(err)
	}
	if charge.URL != "https://exemplo.ao/renovar/abc" {
		t.Errorf("link = %q", charge.URL)
	}
}

func TestIssueChargeSurvivesCustomerAndLinkFailures(t *testing.T) {
	// Nem os dados do cliente nem o link são indispensáveis: sem eles cobra-se
	// na mesma, que é melhor do que não cobrar de todo.
	s := newSub("s1", payment.MethodReference)
	store := newMemStore(s)
	e := newEngine(t, store, newMemPayments(), referenceGateway(), &recorder{})
	e.Log = discard()
	e.Customers = CustomerFunc(func(context.Context, *Subscription) (payment.Customer, error) {
		return payment.Customer{}, errors.New("sem ficha de cliente")
	})
	e.Links = LinkFunc(func(context.Context, *Subscription, time.Time) (string, error) {
		return "", errors.New("sem chave de assinatura")
	})

	charge, err := e.IssueCharge(context.Background(), s, payment.MethodReference)
	if err != nil {
		t.Fatal(err)
	}
	if charge.URL != "" || charge.PaymentID == "" {
		t.Errorf("cobrança = %+v", charge)
	}
}

func TestIssueChargeFailsWhenPaymentCannotBeStored(t *testing.T) {
	// Uma cobrança que o gateway aceitou mas que não ficou registada é dinheiro
	// a entrar sem nada a apontar-lhe: tem de dar erro em vez de seguir.
	s := newSub("s1", payment.MethodReference)
	pays := brokenPayments{memPayments: newMemPayments(), createErr: errors.New("disco")}
	e := newEngine(t, newMemStore(s), newMemPayments(), referenceGateway(), &recorder{})
	e.payments = pays

	if _, err := e.IssueCharge(context.Background(), s, payment.MethodReference); err == nil {
		t.Error("devia falhar")
	}
}

func TestIssueChargeFailsWhenSubscriptionCannotBeSaved(t *testing.T) {
	s := newSub("s1", payment.MethodReference)
	store := brokenStore{memStore: newMemStore(s), saveErr: errors.New("disco")}
	e := newEngine(t, newMemStore(s), newMemPayments(), referenceGateway(), &recorder{})
	e.store = store

	if _, err := e.IssueCharge(context.Background(), s, payment.MethodReference); err == nil {
		t.Error("devia falhar")
	}
}

func TestIssueChargeReportsSaveFailureAfterGatewayError(t *testing.T) {
	s := newSub("s1", payment.MethodReference)
	gw := referenceGateway()
	gw.err = errors.New("gateway em baixo")
	e := newEngine(t, newMemStore(s), newMemPayments(), gw, &recorder{})
	e.store = brokenStore{memStore: newMemStore(s), saveErr: errors.New("disco")}
	e.Log = discard()

	if _, err := e.IssueCharge(context.Background(), s, payment.MethodReference); err == nil {
		t.Error("devia falhar")
	}
}

func TestIssueChargeFailsWhenInstantConfirmCannotBeSaved(t *testing.T) {
	// O Multicaixa Express confirma na própria chamada; se a gravação falhar, o
	// erro tem de subir, ou fica um pagamento feito com a subscrição por renovar.
	s := newSub("s1", payment.MethodMCX)
	gw := &fakeGateway{name: "gateway", methods: []payment.Method{payment.MethodMCX},
		result: payment.ChargeResult{Kind: payment.KindPaid, Status: payment.StatusApproved, ProviderRef: "tx-9"}}
	e := newEngine(t, newMemStore(s), newMemPayments(), gw, &recorder{})
	e.store = brokenStore{memStore: newMemStore(s), saveErr: errors.New("disco")}

	if _, err := e.IssueCharge(context.Background(), s, payment.MethodMCX); err == nil {
		t.Error("devia falhar")
	}
}

func TestConfirmToleratesPaymentStoreFailures(t *testing.T) {
	// A renovação é o que importa: se a cobrança não conseguir ser marcada como
	// paga, regista-se o problema mas não se nega o serviço ao cliente.
	s := newSub("s1", payment.MethodReference)
	s.RenewalPaymentID = "pag-1"
	pay, _ := payment.NewPayment("pag-1", "s1", money.FromMajor(5000, money.AOA), payment.MethodReference)
	mem := newMemPayments()
	_ = mem.Create(context.Background(), pay)

	store := newMemStore(s)
	e := newEngine(t, store, mem, referenceGateway(), &recorder{})
	e.payments = brokenPayments{memPayments: mem, updateErr: errors.New("disco")}
	e.Log = discard()

	if err := e.Confirm(context.Background(), s, payment.MethodReference, "tx-1", ""); err != nil {
		t.Fatal(err)
	}
	if s.Status != StatusActive {
		t.Errorf("estado = %q", s.Status)
	}
}

func TestConfirmToleratesAMissingPayment(t *testing.T) {
	s := newSub("s1", payment.MethodReference)
	s.RenewalPaymentID = "pag-desaparecido"
	mem := newMemPayments()
	e := newEngine(t, newMemStore(s), mem, referenceGateway(), &recorder{})
	e.payments = brokenPayments{memPayments: mem, byIDErr: errors.New("base de dados")}

	if err := e.Confirm(context.Background(), s, "", "tx-1", ""); err != nil {
		t.Fatal(err)
	}
	if s.Method != payment.MethodReference {
		t.Errorf("sem método novo, mantém-se o antigo: %q", s.Method)
	}
}

func TestConfirmFailsWhenSubscriptionCannotBeSaved(t *testing.T) {
	s := newSub("s1", payment.MethodReference)
	e := newEngine(t, newMemStore(s), newMemPayments(), referenceGateway(), &recorder{})
	e.store = brokenStore{memStore: newMemStore(s), saveErr: errors.New("disco")}

	if err := e.Confirm(context.Background(), s, payment.MethodReference, "tx-1", ""); err == nil {
		t.Error("devia falhar")
	}
}

func TestProcessRetriesRespectsTheWindow(t *testing.T) {
	fechada := newSub("s1", payment.MethodCard)
	passado := testNow.AddDate(0, 0, -1)
	fechada.RenewalDueAt = &passado

	aberta := newSub("s2", payment.MethodCard)
	futuro := testNow.AddDate(0, 0, 3)
	aberta.RenewalDueAt = &futuro

	store := newMemStore(fechada, aberta)
	store.retry = []string{"s1", "s2"}

	gw := &fakeGateway{name: "gateway", methods: []payment.Method{payment.MethodCard},
		result: payment.ChargeResult{Kind: payment.KindPaid, Status: payment.StatusPending, ProviderRef: "tx-1"}}
	e := newEngine(t, store, newMemPayments(), gw, &recorder{})
	e.Log = discard()

	if n := e.ProcessRetries(context.Background()); n != 1 {
		t.Errorf("repetidas = %d, a janela fechada é da expiração", n)
	}
	if fechada.RenewalAttempts != 0 {
		t.Errorf("a janela fechada não conta tentativa: %d", fechada.RenewalAttempts)
	}
	if aberta.RenewalAttempts != 1 {
		t.Errorf("tentativas = %d", aberta.RenewalAttempts)
	}
}

func TestProcessRetriesCountsTheAttemptEvenWhenItFails(t *testing.T) {
	// A tentativa conta antes de correr, de propósito: senão um gateway que
	// falha sempre era repetido para sempre e nunca chegava ao tecto.
	s := newSub("s1", payment.MethodCard)
	futuro := testNow.AddDate(0, 0, 3)
	s.RenewalDueAt = &futuro
	s.Amount, s.GrossAmount = money.Zero(money.AOA), money.Zero(money.AOA)

	store := newMemStore(s)
	store.retry = []string{"s1"}
	e := newEngine(t, store, newMemPayments(), referenceGateway(), &recorder{})
	e.Log = discard()

	if n := e.ProcessRetries(context.Background()); n != 0 {
		t.Errorf("repetidas = %d", n)
	}
	if s.RenewalAttempts != 1 {
		t.Errorf("tentativas = %d", s.RenewalAttempts)
	}
}

func TestProcessDunningCountsFromThePaidPeriod(t *testing.T) {
	// O prazo conta do fim do período já pago: quem falhou a cobrança
	// antecipada não pode perder dias que já pagou.
	s := newSub("s1", payment.MethodCard) // fim do ciclo a 20 de Julho
	store := newMemStore(s)
	store.pastDue = []string{"s1"}

	rec := &recorder{}
	e := newEngine(t, store, newMemPayments(), referenceGateway(), rec)
	if n := e.ProcessDunning(context.Background()); n != 1 {
		t.Errorf("prazos marcados = %d", n)
	}
	// 20 de Julho mais 4 dias de tolerância.
	want := time.Date(2025, time.July, 24, 23, 59, 59, 0, time.UTC)
	if s.RenewalDueAt == nil || !s.RenewalDueAt.Equal(want) {
		t.Errorf("prazo = %v, queria %s", s.RenewalDueAt, want)
	}
	if s.Status != StatusPastDue || rec.failures != 1 {
		t.Errorf("estado = %q, avisos = %d", s.Status, rec.failures)
	}
}

func TestProcessDunningCountsFromTodayWhenThePeriodIsOver(t *testing.T) {
	s := newSub("s1", payment.MethodCard)
	passado := testNow.AddDate(0, 0, -10)
	s.CurrentPeriodEnd = &passado
	store := newMemStore(s)
	store.pastDue = []string{"s1"}

	e := newEngine(t, store, newMemPayments(), referenceGateway(), &recorder{})
	e.ProcessDunning(context.Background())
	want := cycle.EndOfDay(testNow.AddDate(0, 0, 4))
	if s.RenewalDueAt == nil || !s.RenewalDueAt.Equal(want) {
		t.Errorf("prazo = %v, queria %s", s.RenewalDueAt, want)
	}
}

func TestProcessExpiryRevokesThePendingCharge(t *testing.T) {
	// A referência de uma subscrição expirada tem de morrer no ATM, ou o
	// cliente paga por algo que já não tem.
	s := newSub("s1", payment.MethodReference)
	s.RenewalPaymentID = "pag-1"
	pay, _ := payment.NewPayment("pag-1", "s1", money.FromMajor(5000, money.AOA), payment.MethodReference)
	pay.Provider = "gateway"
	pay.ProviderRef, pay.Reference, pay.Entity = "tx-1", "987654321", "01234"
	mem := newMemPayments()
	_ = mem.Create(context.Background(), pay)

	store := newMemStore(s)
	store.expired = []string{"s1"}
	gw := referenceGateway()
	rec := &recorder{}
	e := newEngine(t, store, mem, gw, rec)

	if n := e.ProcessExpiry(context.Background()); n != 1 {
		t.Errorf("expiradas = %d", n)
	}
	if gw.cancels != 1 {
		t.Errorf("cancelamentos no gateway = %d", gw.cancels)
	}
	if pay.Status != payment.StatusExpired {
		t.Errorf("cobrança = %q", pay.Status)
	}
	if s.Status != StatusExpired || rec.expiries != 1 {
		t.Errorf("estado = %q, avisos = %d", s.Status, rec.expiries)
	}
}

func TestProcessStaleChargesCleansWithoutNotifying(t *testing.T) {
	s := newSub("s1", payment.MethodReference)
	s.Status = StatusCancelled
	s.RenewalPaymentID = "pag-1"
	s.RenewalState = RenewalPending
	pay, _ := payment.NewPayment("pag-1", "s1", money.FromMajor(5000, money.AOA), payment.MethodReference)
	pay.Provider = "gateway"
	mem := newMemPayments()
	_ = mem.Create(context.Background(), pay)

	store := newMemStore(s)
	store.stale = []string{"s1"}
	rec := &recorder{}
	e := newEngine(t, store, mem, referenceGateway(), rec)

	if n := e.ProcessStaleCharges(context.Background()); n != 1 {
		t.Errorf("limpas = %d", n)
	}
	if s.RenewalState != RenewalNone || s.RenewalPaymentID != "" {
		t.Errorf("a renovação devia ter sido limpa: %+v", s.RenewalState)
	}
	if rec.expiries != 0 || rec.failures != 0 {
		t.Error("uma subscrição já cancelada não recebe mais avisos")
	}
}

func TestCancelPendingChargeIsQuietWhenThereIsNothingToRevoke(t *testing.T) {
	ctx := context.Background()
	s := newSub("s1", payment.MethodReference)

	// Sem registo de cobranças.
	e := newEngine(t, newMemStore(s), newMemPayments(), referenceGateway(), &recorder{})
	e.payments = nil
	e.cancelPendingCharge(ctx, s)

	// Sem cobrança associada.
	mem := newMemPayments()
	e2 := newEngine(t, newMemStore(s), mem, referenceGateway(), &recorder{})
	e2.cancelPendingCharge(ctx, s)

	// Cobrança já paga: não se revoga o que já foi cobrado.
	s.RenewalPaymentID = "pag-1"
	pay, _ := payment.NewPayment("pag-1", "s1", money.FromMajor(5000, money.AOA), payment.MethodReference)
	pay.Provider = "gateway"
	_ = pay.Approve("tx-1")
	_ = mem.Create(ctx, pay)
	gw := referenceGateway()
	e3 := newEngine(t, newMemStore(s), mem, gw, &recorder{})
	e3.cancelPendingCharge(ctx, s)
	if gw.cancels != 0 {
		t.Errorf("cancelamentos = %d, uma cobrança paga não se revoga", gw.cancels)
	}
}

func TestCancelPendingChargeWithAGatewayThatCannotCancel(t *testing.T) {
	// Nem todos os gateways sabem revogar. O que não se pode é falhar a
	// expiração local por causa disso.
	ctx := context.Background()
	s := newSub("s1", payment.MethodReference)
	s.RenewalPaymentID = "pag-1"
	pay, _ := payment.NewPayment("pag-1", "s1", money.FromMajor(5000, money.AOA), payment.MethodReference)
	pay.Provider = "simples"
	mem := newMemPayments()
	_ = mem.Create(ctx, pay)

	registry := payment.NewRegistry().Register(plainGateway{name: "simples"})
	e := NewEngine(newMemStore(s), mem, registry, &recorder{}, Config{})
	e.Now = func() time.Time { return testNow }
	e.Log = discard()
	e.cancelPendingCharge(ctx, s)
	if pay.Status != payment.StatusExpired {
		t.Errorf("cobrança = %q", pay.Status)
	}
}

func TestCancelPendingChargeLogsGatewayAndStoreFailures(t *testing.T) {
	ctx := context.Background()
	s := newSub("s1", payment.MethodReference)
	s.RenewalPaymentID = "pag-1"
	pay, _ := payment.NewPayment("pag-1", "s1", money.FromMajor(5000, money.AOA), payment.MethodReference)
	pay.Provider = "recusa"
	mem := newMemPayments()
	_ = mem.Create(ctx, pay)

	registry := payment.NewRegistry().Register(&refusingCanceller{})
	e := NewEngine(newMemStore(s), brokenPayments{memPayments: mem, updateErr: errors.New("disco")},
		registry, &recorder{}, Config{})
	e.Now = func() time.Time { return testNow }
	e.Log = discard()
	e.cancelPendingCharge(ctx, s)
	// A expiração local acontece na mesma: o estado em memória tem de reflectir
	// que a cobrança morreu, mesmo que o registo não tenha ficado gravado.
	if pay.Status != payment.StatusExpired {
		t.Errorf("cobrança = %q", pay.Status)
	}
}

type refusingCanceller struct{ plainGateway }

func (refusingCanceller) Name() string { return "recusa" }
func (refusingCanceller) CancelCharge(context.Context, payment.ChargeRequest, payment.ChargeResult) error {
	return errors.New("já liquidada")
}

// --- eventos ------------------------------------------------------------------

func TestHandleEventIgnoresWhatItCannotApply(t *testing.T) {
	e := newEngine(t, newMemStore(), newMemPayments(), referenceGateway(), &recorder{})
	ctx := context.Background()

	if changed, err := e.HandleEvent(ctx, nil, &payment.Event{Type: payment.EventChargeSucceeded}); changed || err != nil {
		t.Errorf("sem subscrição não há nada a alterar: %v, %v", changed, err)
	}
	s := newSub("s1", payment.MethodCard)
	if changed, err := e.HandleEvent(ctx, s, &payment.Event{}); changed || err != nil {
		t.Errorf("evento sem tipo = %v, %v", changed, err)
	}
	if changed, err := e.HandleEvent(ctx, s, &payment.Event{Type: "coisa.desconhecida"}); changed || err != nil {
		t.Errorf("evento desconhecido = %v, %v", changed, err)
	}
}

func TestHandleEventChargeSucceededRenews(t *testing.T) {
	s := newSub("s1", payment.MethodCard)
	store := newMemStore(s)
	rec := &recorder{}
	e := newEngine(t, store, newMemPayments(), referenceGateway(), rec)

	changed, err := e.HandleEvent(context.Background(), s, &payment.Event{
		Type: payment.EventSubscriptionActive, SubscriptionRef: "sub_ext", CustomerRef: "cus_ext",
		ChargeRef: "tx-1",
	})
	if err != nil || !changed {
		t.Fatalf("= %v, %v", changed, err)
	}
	if s.SubscriptionRef != "sub_ext" || s.CustomerRef != "cus_ext" {
		t.Errorf("referências do gateway não guardadas: %q / %q", s.SubscriptionRef, s.CustomerRef)
	}
	if s.Status != StatusActive || rec.renewals != 1 {
		t.Errorf("estado = %q, renovações = %d", s.Status, rec.renewals)
	}
}

func TestHandleEventChargeSucceededPropagatesSaveError(t *testing.T) {
	s := newSub("s1", payment.MethodCard)
	e := newEngine(t, newMemStore(s), newMemPayments(), referenceGateway(), &recorder{})
	e.store = brokenStore{memStore: newMemStore(s), saveErr: errors.New("disco")}

	if _, err := e.HandleEvent(context.Background(), s, &payment.Event{
		Type: payment.EventChargeSucceeded, ChargeRef: "tx-1",
	}); err == nil {
		t.Error("devia falhar")
	}
}

func TestHandleEventInvoicePaidOnlyRenewsTheCycleInvoice(t *testing.T) {
	// A factura da adesão já foi tratada pelo checkout. Sem esta distinção
	// emite-se factura a dobrar no primeiro ciclo.
	s := newSub("s1", payment.MethodCard)
	store := newMemStore(s)
	rec := &recorder{}
	e := newEngine(t, store, newMemPayments(), referenceGateway(), rec)
	ctx := context.Background()

	if changed, err := e.HandleEvent(ctx, s, &payment.Event{
		Type: payment.EventInvoicePaid, BillingReason: "subscription_create",
	}); changed || err != nil {
		t.Errorf("a factura da adesão não renova: %v, %v", changed, err)
	}

	end := time.Date(2025, time.August, 20, 0, 0, 0, 0, time.UTC)
	s.RenewalWarnedAt = &testNow
	changed, err := e.HandleEvent(ctx, s, &payment.Event{
		Type: payment.EventInvoicePaid, BillingReason: "subscription_cycle", PeriodEnd: &end,
	})
	if err != nil || !changed {
		t.Fatalf("= %v, %v", changed, err)
	}
	if s.CurrentPeriodEnd == nil || !s.CurrentPeriodEnd.Equal(end) {
		t.Errorf("fim do ciclo = %v", s.CurrentPeriodEnd)
	}
	if s.CycleNumber != 2 || s.RenewalWarnedAt != nil {
		t.Errorf("ciclo = %d, aviso = %v: ciclo novo, aviso novo", s.CycleNumber, s.RenewalWarnedAt)
	}
	if rec.renewals != 1 {
		t.Errorf("renovações = %d", rec.renewals)
	}
}

func TestHandleEventInvoicePaidWithoutPeriodEnd(t *testing.T) {
	s := newSub("s1", payment.MethodCard)
	before := *s.CurrentPeriodEnd
	e := newEngine(t, newMemStore(s), newMemPayments(), referenceGateway(), &recorder{})

	if _, err := e.HandleEvent(context.Background(), s, &payment.Event{
		Type: payment.EventInvoicePaid, BillingReason: "subscription_cycle",
	}); err != nil {
		t.Fatal(err)
	}
	if !s.CurrentPeriodEnd.Equal(before) {
		t.Error("sem data no evento não se inventa um ciclo novo")
	}
}

func TestHandleEventInvoicePaidPropagatesSaveError(t *testing.T) {
	s := newSub("s1", payment.MethodCard)
	e := newEngine(t, newMemStore(s), newMemPayments(), referenceGateway(), &recorder{})
	e.store = brokenStore{memStore: newMemStore(s), saveErr: errors.New("disco")}

	if _, err := e.HandleEvent(context.Background(), s, &payment.Event{
		Type: payment.EventInvoicePaid, BillingReason: "subscription_cycle",
	}); err == nil {
		t.Error("devia falhar")
	}
}

func TestHandleEventChargeFailedRecordsTheAttempt(t *testing.T) {
	s := newSub("s1", payment.MethodCard)
	e := newEngine(t, newMemStore(s), newMemPayments(), referenceGateway(), &recorder{})

	changed, err := e.HandleEvent(context.Background(), s, &payment.Event{Type: payment.EventChargeFailed})
	if err != nil || !changed {
		t.Fatalf("= %v, %v", changed, err)
	}
	if s.Status != StatusPastDue || s.RenewalState != RenewalFailed || s.RenewalAttempts != 1 {
		t.Errorf("estado = %q/%q, tentativas = %d", s.Status, s.RenewalState, s.RenewalAttempts)
	}
	// O prazo fica para o passo de dunning, que sabe contar a partir do período
	// já pago.
	if s.RenewalDueAt != nil {
		t.Errorf("prazo = %v, queria nenhum", s.RenewalDueAt)
	}
}

func TestHandleEventChargeFailedPropagatesSaveError(t *testing.T) {
	s := newSub("s1", payment.MethodCard)
	e := newEngine(t, newMemStore(s), newMemPayments(), referenceGateway(), &recorder{})
	e.store = brokenStore{memStore: newMemStore(s), saveErr: errors.New("disco")}

	if _, err := e.HandleEvent(context.Background(), s, &payment.Event{Type: payment.EventChargeFailed}); err == nil {
		t.Error("devia falhar")
	}
}

func TestHandleEventSubscriptionUpdated(t *testing.T) {
	s := newSub("s1", payment.MethodCard)
	s.Status = StatusPastDue
	e := newEngine(t, newMemStore(s), newMemPayments(), referenceGateway(), &recorder{})
	ctx := context.Background()

	end := time.Date(2025, time.September, 20, 0, 0, 0, 0, time.UTC)
	changed, err := e.HandleEvent(ctx, s, &payment.Event{
		Type: payment.EventSubscriptionUpdated, CurrentPeriodEnd: &end,
		CancelAtPeriodEnd: true, Status: payment.StatusApproved,
	})
	if err != nil || !changed {
		t.Fatalf("= %v, %v", changed, err)
	}
	if !s.CurrentPeriodEnd.Equal(end) || !s.CancelAtPeriodEnd || s.Status != StatusActive {
		t.Errorf("subscrição = %+v", s.Status)
	}

	// Um evento que não traz novidade nenhuma não conta como alteração: dá
	// origem a gravações e a notificações que não têm razão de ser.
	if changed, err := e.HandleEvent(ctx, s, &payment.Event{
		Type: payment.EventSubscriptionUpdated, CancelAtPeriodEnd: true, Status: payment.StatusApproved,
	}); changed || err != nil {
		t.Errorf("= %v, %v", changed, err)
	}
}

func TestHandleEventSubscriptionCancelled(t *testing.T) {
	s := newSub("s1", payment.MethodCard)
	e := newEngine(t, newMemStore(s), newMemPayments(), referenceGateway(), &recorder{})

	changed, err := e.HandleEvent(context.Background(), s, &payment.Event{
		Type: payment.EventSubscriptionCancelled,
	})
	if err != nil || !changed {
		t.Fatalf("= %v, %v", changed, err)
	}
	if s.Status != StatusCancelled || s.AutoRenew {
		t.Errorf("estado = %q, renovação = %v", s.Status, s.AutoRenew)
	}
}

// --- utilitários --------------------------------------------------------------

func TestMethodForFallsBackOnWhatCannotBeCharged(t *testing.T) {
	e := newEngine(t, newMemStore(), newMemPayments(), referenceGateway(), &recorder{})

	// Um débito directo sem mandato não se cobra por débito directo.
	semMandato := &Subscription{Method: payment.MethodDirectDebit}
	if got := e.methodFor(semMandato); got != payment.MethodReference {
		t.Errorf("= %q", got)
	}
	comMandato := &Subscription{Method: payment.MethodDirectDebit, MandateID: "m1"}
	if got := e.methodFor(comMandato); got != payment.MethodDirectDebit {
		t.Errorf("= %q", got)
	}
	// Um método legado que já não existe cobra-se por referência, e não com uma
	// referência vazia.
	legado := &Subscription{Method: payment.Method("multicaixa_antigo")}
	if got := e.methodFor(legado); got != payment.MethodReference {
		t.Errorf("= %q", got)
	}
}

func TestItoaHandlesZeroAndNegatives(t *testing.T) {
	cases := map[int]string{0: "0", 7: "7", 42: "42", -1: "-1", -1024: "-1024"}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %q, queria %q", in, got, want)
		}
	}
}

func TestEngineUsesTheWallClockByDefault(t *testing.T) {
	// O relógio que o construtor instala é o real, em UTC: as datas de ciclo
	// desta biblioteca são todas em UTC, e um motor a correr em hora local
	// renovava a subscrição no dia errado para metade do mundo.
	e := NewEngine(newMemStore(), newMemPayments(), payment.NewRegistry(), nil, Config{})
	got := e.now()
	if _, offset := got.Zone(); offset != 0 {
		t.Errorf("fuso = %d segundos, queria UTC", offset)
	}
	if time.Since(got) > time.Minute {
		t.Errorf("hora = %s, queria a de agora", got)
	}
}

func TestIssueChargeCarriesSubscriptionMetadata(t *testing.T) {
	// Os metadados da subscrição seguem para o gateway, porque é por eles que a
	// contabilidade reconcilia o extracto: quem os perde fica com pagamentos
	// que ninguém consegue atribuir.
	s := newSub("s1", payment.MethodReference)
	s.Metadata = map[string]string{"centro_custo": "clinica-luanda", "cycle": "vai ser substituido"}

	var sent map[string]string
	gw := &captureGateway{fakeGateway: referenceGateway(), meta: &sent}
	e := newEngine(t, newMemStore(s), newMemPayments(), gw.fakeGateway, &recorder{})
	e.registry = payment.NewRegistry().Register(gw)

	if _, err := e.IssueCharge(context.Background(), s, payment.MethodReference); err != nil {
		t.Fatal(err)
	}
	if sent["centro_custo"] != "clinica-luanda" {
		t.Errorf("metadados da subscrição = %v", sent)
	}
	if sent["subscription_id"] != "s1" || sent["cycle"] != "2" {
		t.Errorf("os do motor mandam sobre os da subscrição: %v", sent)
	}
}

// captureGateway guarda os metadados que lhe chegam.
type captureGateway struct {
	*fakeGateway
	meta *map[string]string
}

func (c *captureGateway) Charge(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	*c.meta = req.Metadata
	return c.fakeGateway.Charge(ctx, req)
}
