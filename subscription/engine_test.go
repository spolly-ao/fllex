package subscription

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/cycle"
	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// --- duplos de teste ----------------------------------------------------------

type memStore struct {
	subs map[string]*Subscription
	// filas devolvidas por cada consulta, para o teste controlar o que o motor vê
	warn, charge, awaiting, retry, pastDue, expired, stale []string
}

func newMemStore(subs ...*Subscription) *memStore {
	m := &memStore{subs: map[string]*Subscription{}}
	for _, s := range subs {
		m.subs[s.ID] = s
	}
	return m
}

func (m *memStore) pick(ids []string) []*Subscription {
	out := make([]*Subscription, 0, len(ids))
	for _, id := range ids {
		if s := m.subs[id]; s != nil {
			out = append(out, s)
		}
	}
	return out
}

func (m *memStore) ByID(_ context.Context, id string) (*Subscription, error) {
	return m.subs[id], nil
}
func (m *memStore) Save(_ context.Context, s *Subscription) error {
	m.subs[s.ID] = s
	return nil
}
func (m *memStore) DueForWarning(context.Context, time.Time, int) ([]*Subscription, error) {
	return m.pick(m.warn), nil
}
func (m *memStore) DueForCharge(context.Context, time.Time, int) ([]*Subscription, error) {
	return m.pick(m.charge), nil
}
func (m *memStore) AwaitingPayment(context.Context, int) ([]*Subscription, error) {
	return m.pick(m.awaiting), nil
}
func (m *memStore) RetryDue(context.Context, time.Time, int, int) ([]*Subscription, error) {
	return m.pick(m.retry), nil
}
func (m *memStore) PastDueWithoutDeadline(context.Context, int) ([]*Subscription, error) {
	return m.pick(m.pastDue), nil
}
func (m *memStore) ExpiredWindows(context.Context, time.Time, int) ([]*Subscription, error) {
	return m.pick(m.expired), nil
}
func (m *memStore) StaleCharges(context.Context, time.Time, int) ([]*Subscription, error) {
	return m.pick(m.stale), nil
}

type memPayments struct {
	items map[string]*payment.Payment
}

func newMemPayments() *memPayments {
	return &memPayments{items: map[string]*payment.Payment{}}
}

func (m *memPayments) Create(_ context.Context, p *payment.Payment) error {
	m.items[p.ID] = p
	return nil
}
func (m *memPayments) Update(_ context.Context, p *payment.Payment) error {
	m.items[p.ID] = p
	return nil
}
func (m *memPayments) ByID(_ context.Context, id string) (*payment.Payment, error) {
	return m.items[id], nil
}
func (m *memPayments) ByProviderRef(context.Context, string, string) (*payment.Payment, error) {
	return nil, nil
}
func (m *memPayments) PendingBySubject(context.Context, string) ([]*payment.Payment, error) {
	return nil, nil
}
func (m *memPayments) PendingVerifiable(context.Context, string, int) ([]*payment.Payment, error) {
	return nil, nil
}
func (m *memPayments) ExpiredPending(context.Context, time.Time, int) ([]*payment.Payment, error) {
	return nil, nil
}
func (m *memPayments) RetryDue(context.Context, time.Time, int, int) ([]*payment.Payment, error) {
	return nil, nil
}

// fakeGateway devolve o resultado que o teste mandar.
type fakeGateway struct {
	name     string
	methods  []payment.Method
	result   payment.ChargeResult
	err      error
	charges  int
	verified payment.ChargeStatus
	verifyOK bool
	cancels  int
}

func (f *fakeGateway) Name() string                         { return f.name }
func (f *fakeGateway) Methods() []payment.Method            { return f.methods }
func (f *fakeGateway) Configured() bool                     { return true }
func (f *fakeGateway) SupportsCurrency(money.Currency) bool { return true }
func (f *fakeGateway) Charge(context.Context, payment.ChargeRequest) (payment.ChargeResult, error) {
	f.charges++
	return f.result, f.err
}
func (f *fakeGateway) VerifyCharge(context.Context, string, string) (payment.ChargeStatus, error) {
	if !f.verifyOK {
		return payment.ChargeStatus{}, nil
	}
	return f.verified, nil
}
func (f *fakeGateway) CancelCharge(context.Context, payment.ChargeRequest, payment.ChargeResult) error {
	f.cancels++
	return nil
}

type recorder struct {
	warnings, reminders, charges, failures, renewals, expiries int
	lastCharge                                                 Charge
}

func (r *recorder) RenewalWarning(context.Context, *Subscription, time.Time)  { r.warnings++ }
func (r *recorder) RenewalReminder(context.Context, *Subscription, time.Time) { r.reminders++ }
func (r *recorder) ChargeIssued(_ context.Context, _ *Subscription, c Charge) {
	r.charges++
	r.lastCharge = c
}
func (r *recorder) PaymentFailed(context.Context, *Subscription, time.Time, string) { r.failures++ }
func (r *recorder) Renewed(context.Context, *Subscription)                          { r.renewals++ }
func (r *recorder) Expired(context.Context, *Subscription)                          { r.expiries++ }

// --- montagem -----------------------------------------------------------------

var testNow = time.Date(2025, time.July, 15, 10, 0, 0, 0, time.UTC)

func newSub(id string, method payment.Method) *Subscription {
	end := time.Date(2025, time.July, 20, 0, 0, 0, 0, time.UTC)
	return &Subscription{
		ID:               id,
		CustomerID:       "cliente-1",
		PlanName:         "Plano Essencial",
		Status:           StatusActive,
		Provider:         "gateway",
		Method:           method,
		Amount:           money.FromMajor(4000, money.AOA), // pagou com desconto
		GrossAmount:      money.FromMajor(5000, money.AOA), // preço de tabela
		DiscountPercent:  20,
		CouponID:         "BOASVINDAS",
		Interval:         cycle.Monthly,
		CurrentPeriodEnd: &end,
		StartDate:        time.Date(2025, time.June, 20, 0, 0, 0, 0, time.UTC),
		AutoRenew:        true,
		CycleNumber:      1,
	}
}

func newEngine(t *testing.T, store *memStore, pays *memPayments, gw *fakeGateway, notify Notifier) *Engine {
	t.Helper()
	registry := payment.NewRegistry().Register(gw)
	e := NewEngine(store, pays, registry, notify, Config{
		Window:         cycle.WindowConfig{LeadDays: 5, GraceDays: 4},
		WarnBeforeDays: 7,
	})
	e.Now = func() time.Time { return testNow }
	n := 0
	e.IDs = func() string {
		n++
		return fmt.Sprintf("pag-%d", n)
	}
	return e
}

// --- testes -------------------------------------------------------------------

func TestIssueChargeUsesGrossPriceNotDiscounted(t *testing.T) {
	// O cupão do primeiro ciclo não recorre: a renovação cobra o preço cheio.
	sub := newSub("s1", payment.MethodReference)
	store := newMemStore(sub)
	pays := newMemPayments()
	gw := &fakeGateway{
		name:    "gateway",
		methods: []payment.Method{payment.MethodReference},
		result: payment.ChargeResult{
			Kind: payment.KindReference, Status: payment.StatusPending,
			ProviderRef: "tx-1", StatusRef: "op-1",
			Entity: "01234", Reference: "987654321",
		},
	}
	rec := &recorder{}
	e := newEngine(t, store, pays, gw, rec)

	charge, err := e.IssueCharge(context.Background(), sub, payment.MethodReference)
	if err != nil {
		t.Fatalf("emitir cobrança falhou: %v", err)
	}
	if want := money.FromMajor(5000, money.AOA); charge.Amount.Minor != want.Minor {
		t.Errorf("valor cobrado = %s, queria o preço de tabela %s", charge.Amount, want)
	}
	if sub.DiscountPercent != 0 || sub.CouponID != "" {
		t.Errorf("o desconto do primeiro ciclo não foi limpo: %d%%, cupão %q", sub.DiscountPercent, sub.CouponID)
	}
	if sub.RenewalState != RenewalPending {
		t.Errorf("estado da renovação = %q, queria pendente", sub.RenewalState)
	}
	if sub.PendingEntity != "01234" || sub.PendingReference != "987654321" {
		t.Errorf("dados de pagamento não guardados: %q / %q", sub.PendingEntity, sub.PendingReference)
	}
	if rec.charges != 1 {
		t.Errorf("avisos de cobrança = %d, queria 1", rec.charges)
	}
}

func TestIssueChargeExpiryCoversWholeWindow(t *testing.T) {
	// A referência tem de viver até ao fecho da janela. Uma que morra em 24
	// horas deixa o cliente com um número que não pode pagar durante a
	// tolerância que lhe foi prometida.
	sub := newSub("s1", payment.MethodReference)
	store := newMemStore(sub)
	pays := newMemPayments()
	gw := &fakeGateway{name: "gateway", methods: []payment.Method{payment.MethodReference},
		result: payment.ChargeResult{Kind: payment.KindReference, Status: payment.StatusPending, ProviderRef: "tx-1"}}
	e := newEngine(t, store, pays, gw, &recorder{})

	charge, err := e.IssueCharge(context.Background(), sub, payment.MethodReference)
	if err != nil {
		t.Fatal(err)
	}
	// Fim do ciclo a 20 de Julho, 4 dias de tolerância: 24 de Julho às 23:59:59.
	want := time.Date(2025, time.July, 24, 23, 59, 59, 0, time.UTC)
	if !charge.DueAt.Equal(want) {
		t.Errorf("prazo = %s, queria %s", charge.DueAt, want)
	}
	pay := pays.items["pag-1"]
	if pay == nil || pay.ExpiresAt == nil || !pay.ExpiresAt.Equal(want) {
		t.Errorf("validade da cobrança = %v, queria %s", pay.ExpiresAt, want)
	}
}

func TestIssueChargeInstantMethodRenewsImmediately(t *testing.T) {
	// O Multicaixa Express é síncrono: uma resposta com sucesso é o pagamento
	// feito, e não há nada a aguardar.
	sub := newSub("s1", payment.MethodMCX)
	store := newMemStore(sub)
	pays := newMemPayments()
	gw := &fakeGateway{name: "gateway", methods: []payment.Method{payment.MethodMCX},
		result: payment.ChargeResult{Kind: payment.KindPaid, Status: payment.StatusApproved, ProviderRef: "tx-1"}}
	rec := &recorder{}
	e := newEngine(t, store, pays, gw, rec)

	if _, err := e.IssueCharge(context.Background(), sub, payment.MethodMCX); err != nil {
		t.Fatal(err)
	}
	if sub.Status != StatusActive {
		t.Errorf("estado = %s, queria activa", sub.Status)
	}
	if sub.RenewalState != RenewalNone {
		t.Errorf("estado da renovação = %q, queria limpo", sub.RenewalState)
	}
	if rec.renewals != 1 {
		t.Errorf("avisos de renovação = %d, queria 1", rec.renewals)
	}
	// O novo ciclo ancora no fim previsto, não na data do pagamento.
	want := time.Date(2025, time.August, 20, 0, 0, 0, 0, time.UTC)
	if sub.CurrentPeriodEnd == nil || !sub.CurrentPeriodEnd.Equal(want) {
		t.Errorf("fim do novo ciclo = %v, queria %s", sub.CurrentPeriodEnd, want)
	}
	if sub.CycleNumber != 2 {
		t.Errorf("número do ciclo = %d, queria 2", sub.CycleNumber)
	}
	if pays.items["pag-1"].Status != payment.StatusApproved {
		t.Errorf("a cobrança ficou em %s, queria aprovada", pays.items["pag-1"].Status)
	}
}

func TestIssueChargeStillWarnsWhenGatewayFails(t *testing.T) {
	// Sem cobrança emitida, o cliente não pode ficar sem forma de pagar: a
	// subscrição fica em renovação pendente e o aviso segue com o link.
	sub := newSub("s1", payment.MethodReference)
	store := newMemStore(sub)
	gw := &fakeGateway{name: "gateway", methods: []payment.Method{payment.MethodReference},
		err: &payment.GatewayError{Provider: "gateway", StatusCode: 503, Message: "indisponível"}}
	rec := &recorder{}
	e := newEngine(t, store, newMemPayments(), gw, rec)
	e.Links = LinkFunc(func(context.Context, *Subscription, time.Time) (string, error) {
		return "https://exemplo/renovar?t=abc", nil
	})

	if _, err := e.IssueCharge(context.Background(), sub, payment.MethodReference); err == nil {
		t.Fatal("queria o erro do gateway propagado")
	}
	if sub.RenewalState != RenewalPending || sub.RenewalDueAt == nil {
		t.Errorf("a subscrição ficou sem cobrança pendente: %q, prazo %v", sub.RenewalState, sub.RenewalDueAt)
	}
	if rec.charges != 1 {
		t.Errorf("avisos de cobrança = %d, queria 1 mesmo com o gateway em baixo", rec.charges)
	}
	if rec.lastCharge.URL == "" {
		t.Error("o aviso tem de levar o link para o cliente escolher outro método")
	}
}

func TestMethodForFallsBackWithoutMandate(t *testing.T) {
	// Um débito directo sem mandato não se pode cobrar por débito directo:
	// cai na referência em vez de gerar uma cobrança que nunca é apresentada.
	sub := newSub("s1", payment.MethodDirectDebit)
	e := newEngine(t, newMemStore(sub), newMemPayments(), &fakeGateway{name: "gateway"}, &recorder{})
	if got := e.methodFor(sub); got != payment.MethodReference {
		t.Errorf("método sem mandato = %s, queria referência", got)
	}
	sub.MandateID = "mandato-1"
	if got := e.methodFor(sub); got != payment.MethodDirectDebit {
		t.Errorf("método com mandato = %s, queria débito directo", got)
	}
}

func TestProcessWarningsSeparatesRemindersFromDemands(t *testing.T) {
	// Mandar pagar quem vai ser cobrado automaticamente gera pagamentos em
	// duplicado; não avisar quem tem de agir perde a renovação.
	auto := newSub("auto", payment.MethodCard)
	manual := newSub("manual", payment.MethodReference)
	store := newMemStore(auto, manual)
	store.warn = []string{"auto", "manual"}
	rec := &recorder{}
	e := newEngine(t, store, newMemPayments(), &fakeGateway{name: "gateway"}, rec)

	if n := e.ProcessWarnings(context.Background()); n != 2 {
		t.Errorf("avisadas = %d, queria 2", n)
	}
	if rec.reminders != 1 {
		t.Errorf("lembretes = %d, queria 1 (o cartão)", rec.reminders)
	}
	if rec.warnings != 1 {
		t.Errorf("avisos de pagamento = %d, queria 1 (a referência)", rec.warnings)
	}
	if auto.RenewalWarnedAt == nil || manual.RenewalWarnedAt == nil {
		t.Error("o aviso tem de ficar registado, senão repete-se a cada passagem")
	}
}

func TestProcessConfirmationsActivatesPaidRenewal(t *testing.T) {
	sub := newSub("s1", payment.MethodReference)
	sub.RenewalState = RenewalPending
	sub.RenewalStatusRef = "op-1"
	sub.RenewalPaymentID = "pag-1"
	store := newMemStore(sub)
	store.awaiting = []string{"s1"}

	pays := newMemPayments()
	pay, _ := payment.NewPayment("pag-1", "s1", money.FromMajor(5000, money.AOA), payment.MethodReference)
	pay.ProviderRef = "tx-1"
	_ = pays.Create(context.Background(), pay)

	gw := &fakeGateway{name: "gateway", methods: []payment.Method{payment.MethodReference},
		verifyOK: true,
		verified: payment.ChargeStatus{Status: payment.StatusApproved, Paid: true, InvoiceURL: "https://exemplo/f"},
	}
	rec := &recorder{}
	e := newEngine(t, store, pays, gw, rec)

	if n := e.ProcessConfirmations(context.Background()); n != 1 {
		t.Fatalf("confirmadas = %d, queria 1", n)
	}
	if sub.Status != StatusActive || sub.RenewalState != RenewalNone {
		t.Errorf("subscrição ficou %s / %q", sub.Status, sub.RenewalState)
	}
	if pays.items["pag-1"].Status != payment.StatusApproved {
		t.Errorf("a cobrança ficou em %s, queria aprovada", pays.items["pag-1"].Status)
	}
	if pays.items["pag-1"].InvoiceURL == "" {
		t.Error("a factura do gateway devia ter sido guardada")
	}
	if rec.renewals != 1 {
		t.Errorf("avisos de renovação = %d, queria 1", rec.renewals)
	}
}

func TestProcessExpiryCancelsPendingChargeAtGateway(t *testing.T) {
	// Sem revogar, a referência de uma subscrição já terminada continua viva no
	// ATM e o cliente paga por algo que já não tem.
	sub := newSub("s1", payment.MethodReference)
	sub.RenewalState = RenewalPending
	sub.RenewalPaymentID = "pag-1"
	store := newMemStore(sub)
	store.expired = []string{"s1"}

	pays := newMemPayments()
	pay, _ := payment.NewPayment("pag-1", "s1", money.FromMajor(5000, money.AOA), payment.MethodReference)
	pay.Provider = "gateway"
	pay.Reference = "987654321"
	_ = pays.Create(context.Background(), pay)

	gw := &fakeGateway{name: "gateway", methods: []payment.Method{payment.MethodReference}}
	rec := &recorder{}
	e := newEngine(t, store, pays, gw, rec)

	if n := e.ProcessExpiry(context.Background()); n != 1 {
		t.Fatalf("expiradas = %d, queria 1", n)
	}
	if gw.cancels != 1 {
		t.Errorf("revogações no gateway = %d, queria 1", gw.cancels)
	}
	if sub.Status != StatusExpired {
		t.Errorf("estado = %s, queria expirada", sub.Status)
	}
	if pays.items["pag-1"].Status != payment.StatusExpired {
		t.Errorf("a cobrança ficou em %s, queria expirada", pays.items["pag-1"].Status)
	}
	if rec.expiries != 1 {
		t.Errorf("avisos de expiração = %d, queria 1", rec.expiries)
	}
}

func TestProcessDunningSetsDeadlineFromPaidPeriod(t *testing.T) {
	// O prazo conta do fim do período já pago, para o cliente não perder dias
	// que pagou.
	sub := newSub("s1", payment.MethodCard)
	sub.Status = StatusPastDue
	store := newMemStore(sub)
	store.pastDue = []string{"s1"}
	rec := &recorder{}
	e := newEngine(t, store, newMemPayments(), &fakeGateway{name: "gateway"}, rec)

	if n := e.ProcessDunning(context.Background()); n != 1 {
		t.Fatalf("tratadas = %d, queria 1", n)
	}
	// Fim do período a 20 de Julho, 4 dias de tolerância.
	want := time.Date(2025, time.July, 24, 23, 59, 59, 0, time.UTC)
	if sub.RenewalDueAt == nil || !sub.RenewalDueAt.Equal(want) {
		t.Errorf("prazo = %v, queria %s", sub.RenewalDueAt, want)
	}
	if rec.failures != 1 {
		t.Errorf("avisos de falha = %d, queria 1", rec.failures)
	}
}

func TestHandleEventIgnoresFirstInvoiceOfSubscription(t *testing.T) {
	// A factura da criação já foi tratada pelo evento do checkout. Só a do
	// ciclo é que renova; sem esta distinção emite-se factura a dobrar.
	sub := newSub("s1", payment.MethodCard)
	store := newMemStore(sub)
	e := newEngine(t, store, newMemPayments(), &fakeGateway{name: "gateway"}, &recorder{})

	changed, err := e.HandleEvent(context.Background(), sub, &payment.Event{
		Type: payment.EventInvoicePaid, BillingReason: "subscription_create",
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a factura de criação não devia renovar nada")
	}

	newEnd := time.Date(2025, time.August, 20, 0, 0, 0, 0, time.UTC)
	changed, err = e.HandleEvent(context.Background(), sub, &payment.Event{
		Type: payment.EventInvoicePaid, BillingReason: "subscription_cycle", PeriodEnd: &newEnd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a factura do ciclo devia renovar")
	}
	if sub.CurrentPeriodEnd == nil || !sub.CurrentPeriodEnd.Equal(newEnd) {
		t.Errorf("fim do período = %v, queria %s", sub.CurrentPeriodEnd, newEnd)
	}
	if sub.RenewalWarnedAt != nil {
		t.Error("um ciclo novo tem de poder ser avisado outra vez")
	}
}

func TestInstalmentAmountOnContractSubscription(t *testing.T) {
	// Contrato anual cobrado ao mês: cobra-se um doze avos, não o ano inteiro.
	sub := &Subscription{
		GrossAmount:         money.FromMajor(120000, money.AOA),
		ContractMonths:      12,
		BillingPeriodMonths: 1,
		CycleNumber:         1,
	}
	if got, want := sub.InstalmentAmount().Minor, int64(1000000); got != want {
		t.Errorf("prestação = %d, queria %d", got, want)
	}

	// Contrato anual pago de uma vez: cobra-se o contrato.
	sub.BillingPeriodMonths = 12
	if got, want := sub.InstalmentAmount().Minor, int64(12000000); got != want {
		t.Errorf("pagamento único = %d, queria %d", got, want)
	}
}

func TestActivateReactivationAnchorsOnPayment(t *testing.T) {
	// Esteve cancelada: o novo ciclo conta do pagamento, porque não se cobra
	// tempo que o cliente não teve.
	old := time.Date(2025, time.March, 31, 0, 0, 0, 0, time.UTC)
	sub := &Subscription{
		Status:           StatusCancelled,
		Interval:         cycle.Monthly,
		CurrentPeriodEnd: &old,
		GrossAmount:      money.FromMajor(5000, money.AOA),
		StartDate:        time.Date(2025, time.January, 31, 0, 0, 0, 0, time.UTC),
	}
	now := time.Date(2025, time.July, 10, 12, 0, 0, 0, time.UTC)
	sub.Activate(now)

	if sub.Status != StatusActive {
		t.Errorf("estado = %s, queria activa", sub.Status)
	}
	want := time.Date(2025, time.August, 10, 12, 0, 0, 0, time.UTC)
	if !sub.CurrentPeriodEnd.Equal(want) {
		t.Errorf("fim do ciclo = %s, queria %s", sub.CurrentPeriodEnd, want)
	}
	if sub.AnchorDay != 10 {
		t.Errorf("dia-âncora = %d, queria 10 (a reactivação muda a data de cobrança)", sub.AnchorDay)
	}
}

func TestRecurringCouponSurvivesRenewal(t *testing.T) {
	// Um cupão de "três ciclos a 20%" tem de continuar a valer no segundo e no
	// terceiro, e cair no quarto. Sem isto, ou o desconto morre no primeiro
	// ciclo (e o cliente reclama com razão) ou fica para sempre (e a margem
	// desaparece sem ninguém decidir).
	sub := newSub("s1", payment.MethodReference)
	sub.CouponRecurring = true
	sub.CouponCycles = 3
	sub.CycleNumber = 1

	sub.ApplyRenewalPricing() // vai para o ciclo 2
	if want := money.FromMajor(4000, money.AOA); sub.Amount.Minor != want.Minor {
		t.Errorf("ciclo 2 = %s, queria manter o desconto (%s)", sub.Amount, want)
	}
	if sub.DiscountPercent != 20 || sub.CouponID == "" {
		t.Errorf("o cupão foi limpo cedo de mais: %d%%, %q", sub.DiscountPercent, sub.CouponID)
	}

	sub.CycleNumber = 3
	sub.ApplyRenewalPricing() // vai para o ciclo 4, fora do alcance
	if want := money.FromMajor(5000, money.AOA); sub.Amount.Minor != want.Minor {
		t.Errorf("ciclo 4 = %s, queria o preço de tabela (%s)", sub.Amount, want)
	}
	if sub.DiscountPercent != 0 || sub.CouponID != "" || sub.CouponRecurring {
		t.Errorf("o cupão devia ter sido limpo: %+v", sub)
	}
}

func TestNonRecurringCouponDiesOnFirstRenewal(t *testing.T) {
	sub := newSub("s1", payment.MethodReference)
	sub.CycleNumber = 1
	sub.ApplyRenewalPricing()
	if want := money.FromMajor(5000, money.AOA); sub.Amount.Minor != want.Minor {
		t.Errorf("valor = %s, queria o preço de tabela %s", sub.Amount, want)
	}
}

func TestDiscountApplies(t *testing.T) {
	sub := &Subscription{DiscountPercent: 20}
	if !sub.DiscountApplies(1) {
		t.Error("o primeiro ciclo tem sempre o desconto")
	}
	if sub.DiscountApplies(2) {
		t.Error("sem recorrência o desconto acaba no primeiro")
	}
	sub.CouponRecurring = true
	if !sub.DiscountApplies(99) {
		t.Error("recorrente sem limite acompanha sempre")
	}
	sub.CouponCycles = 2
	if !sub.DiscountApplies(2) || sub.DiscountApplies(3) {
		t.Error("com limite de dois ciclos, o terceiro já não tem")
	}
	sub.DiscountPercent = 0
	if sub.DiscountApplies(1) {
		t.Error("sem desconto não há nada a aplicar")
	}
}
