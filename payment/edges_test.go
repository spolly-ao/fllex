package payment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
)

func TestGatewayErrorMessage(t *testing.T) {
	// A mensagem do gateway é muitas vezes a única explicação de uma recusa.
	e := &GatewayError{Provider: "stripe", StatusCode: 402, Message: "card declined"}
	if got := e.Error(); got != "stripe: 402 card declined" {
		t.Errorf("= %q", got)
	}
	// Com código, o código aparece: é o que se procura na documentação.
	e.Code = "card_declined"
	if got := e.Error(); got != "stripe: 402 card declined (card_declined)" {
		t.Errorf("= %q", got)
	}
	// Sem mensagem, mostra-se o corpo em bruto em vez de um número solto.
	e = &GatewayError{Provider: "momenu", StatusCode: 500, Body: "<html>erro</html>"}
	if got := e.Error(); !strings.Contains(got, "<html>erro</html>") {
		t.Errorf("= %q", got)
	}
}

func TestGatewayErrorRetryable(t *testing.T) {
	tests := map[int]bool{
		0: true, 429: true, 500: true, 502: true, 503: true,
		400: false, 401: false, 402: false, 404: false, 422: false,
	}
	for status, want := range tests {
		if got := (&GatewayError{StatusCode: status}).Retryable(); got != want {
			t.Errorf("estado %d: repetível = %v, queria %v", status, got, want)
		}
	}
}

func TestIsRetryableWithNil(t *testing.T) {
	if IsRetryable(nil) {
		t.Error("sem erro não há nada a repetir")
	}
	// Um erro desconhecido conta como passageiro: não há informação que diga o
	// contrário, e desistir cedo perde pagamentos.
	if !IsRetryable(errors.New("qualquer coisa")) {
		t.Error("um erro sem contexto devia ser tratado como passageiro")
	}
	for _, err := range []error{
		ErrNotConfigured, ErrUnsupportedMethod, ErrUnsupportedCurrency, ErrUnsupported,
		ErrNoProvider, ErrBadSignature, ErrInvalidTransition, ErrMandateRequired,
		ErrMandateNotActive, ErrAmountNotPositive, ErrInsufficientFunds,
	} {
		if IsRetryable(err) {
			t.Errorf("%v não melhora com repetição", err)
		}
	}
}

func TestEventHelpersOnNil(t *testing.T) {
	var e *Event
	if e.Meta("x") != "" {
		t.Error("um evento inexistente não tem metadados")
	}
	if !e.Ignorable() {
		t.Error("um evento inexistente ignora-se")
	}
	if e.BillingCycle() {
		t.Error("um evento inexistente não é factura de ciclo")
	}

	e = &Event{Type: EventInvoicePaid}
	if e.Meta("x") != "" {
		t.Error("sem metadados devolve vazio")
	}
	e.Metadata = map[string]string{"orgId": "org-1"}
	if got := e.Meta("orgId"); got != "org-1" {
		t.Errorf("= %q", got)
	}
	if e.Ignorable() {
		t.Error("um evento com tipo não se ignora")
	}
	e.BillingReason = "subscription_cycle"
	if !e.BillingCycle() {
		t.Error("devia ser reconhecido como factura de ciclo")
	}
}

func TestParseMethod(t *testing.T) {
	tests := map[string]Method{
		"card": MethodCard, "cartão": MethodCard, "stripe": MethodCard, "credit_card": MethodCard,
		"mcx": MethodMCX, "multicaixa": MethodMCX, "multicaixa_express": MethodMCX, "express": MethodMCX,
		"ekwanza": MethodEKwanza, "e-kwanza": MethodEKwanza, "ekz": MethodEKwanza,
		"REFERENCE": MethodReference, "referência": MethodReference, "atm": MethodReference, "proxypay": MethodReference,
		"direct_debit": MethodDirectDebit, "débito_directo": MethodDirectDebit, "dd": MethodDirectDebit, "dds": MethodDirectDebit,
		"wallet": MethodWallet, "carteira": MethodWallet, "saldo": MethodWallet, "balance": MethodWallet,
		"external": MethodExternal, "transferencia": MethodExternal, "depósito": MethodExternal, "deposit": MethodExternal,
		"manual": MethodManual, "cortesia": MethodManual, "free": MethodManual, "grant": MethodManual,
		"  Card  ":  MethodCard,
		"inventado": "", "": "",
	}
	for in, want := range tests {
		if got := ParseMethod(in); got != want {
			t.Errorf("ParseMethod(%q) = %q, queria %q", in, got, want)
		}
	}
}

func TestMethodOrDefaultAndString(t *testing.T) {
	if got := MethodOrDefault(MethodMCX, MethodReference); got != MethodMCX {
		t.Errorf("= %q", got)
	}
	if got := MethodOrDefault(Method("inventado"), MethodReference); got != MethodReference {
		t.Errorf("= %q", got)
	}
	if got := MethodOrDefault("", MethodReference); got != MethodReference {
		t.Errorf("= %q", got)
	}
	if got := MethodCard.String(); got != "card" {
		t.Errorf("= %q", got)
	}
	if Method("inventado").Valid() {
		t.Error("um método inventado não é válido")
	}
}

func TestParseStatus(t *testing.T) {
	tests := map[string]Status{
		"pending": StatusPending, "PENDING": StatusPending, "incomplete": StatusPending,
		"processing": StatusPending, "open": StatusPending,
		"approved": StatusApproved, "paid": StatusApproved, "succeeded": StatusApproved,
		"success": StatusApproved, "complete": StatusApproved, "completed": StatusApproved,
		"collected": StatusApproved,
		"rejected":  StatusRejected, "failed": StatusRejected, "declined": StatusRejected,
		"denied":    StatusRejected,
		"cancelled": StatusCancelled, "canceled": StatusCancelled, "revoked": StatusCancelled,
		"voided":   StatusCancelled,
		"expired":  StatusExpired,
		"refunded": StatusRefunded, "reversed": StatusRefunded,
		"inventado": "", "": "",
	}
	for in, want := range tests {
		if got := ParseStatus(in); got != want {
			t.Errorf("ParseStatus(%q) = %q, queria %q", in, got, want)
		}
	}
}

func TestStatusPredicates(t *testing.T) {
	if StatusPending.Terminal() || Status("").Terminal() {
		t.Error("pendente e vazio não são terminais")
	}
	for _, s := range []Status{StatusApproved, StatusRejected, StatusCancelled, StatusExpired, StatusRefunded} {
		if !s.Terminal() {
			t.Errorf("%s devia ser terminal", s)
		}
	}
	if !StatusApproved.Settled() || StatusPending.Settled() {
		t.Error("só o aprovado significa dinheiro recebido")
	}
	if got := StatusApproved.String(); got != "approved" {
		t.Errorf("= %q", got)
	}
}

func TestPaymentTransitionGuards(t *testing.T) {
	pay := func(status Status) *Payment {
		p, _ := NewPayment("p1", "s1", money.FromMajor(100, money.AOA), MethodReference)
		p.Status = status
		return p
	}

	// Rejeitar, cancelar e expirar só a partir de pendente.
	if err := pay(StatusPending).Reject("sem fundos"); err != nil {
		t.Errorf("rejeitar pendente = %v", err)
	}
	if err := pay(StatusApproved).Reject("x"); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("rejeitar aprovado = %v", err)
	}
	if err := pay(StatusPending).Cancel("desistiu"); err != nil {
		t.Errorf("cancelar pendente = %v", err)
	}
	if err := pay(StatusApproved).Cancel("x"); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("cancelar aprovado = %v", err)
	}
	if err := pay(StatusPending).Expire(); err != nil {
		t.Errorf("expirar pendente = %v", err)
	}
	if err := pay(StatusExpired).Expire(); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expirar duas vezes = %v", err)
	}
	// Estornar só a partir de aprovado.
	if err := pay(StatusApproved).Refund("devolvido"); err != nil {
		t.Errorf("estornar aprovado = %v", err)
	}
	if err := pay(StatusPending).Refund("x"); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("estornar pendente = %v", err)
	}
	if err := pay(StatusExpired).Approve("tx"); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("aprovar expirado = %v", err)
	}
}

func TestTransitionsRecordReasonAndTime(t *testing.T) {
	p, _ := NewPayment("p1", "s1", money.FromMajor(100, money.AOA), MethodDirectDebit)
	if err := p.Reject("AM04 saldo insuficiente"); err != nil {
		t.Fatal(err)
	}
	if p.FailureReason == "" || p.ProcessedAt == nil {
		t.Errorf("recusa sem motivo nem data: %+v", p)
	}

	p, _ = NewPayment("p2", "s1", money.FromMajor(100, money.AOA), MethodReference)
	if err := p.Cancel("subscrição cancelada"); err != nil {
		t.Fatal(err)
	}
	if p.FailureReason == "" || p.ProcessedAt == nil {
		t.Errorf("cancelamento sem motivo nem data: %+v", p)
	}

	p, _ = NewPayment("p3", "s1", money.FromMajor(100, money.AOA), MethodMCX)
	_ = p.Approve("tx-1")
	if err := p.Refund("duplicado"); err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusRefunded || p.FailureReason != "duplicado" {
		t.Errorf("estorno = %+v", p)
	}
}

func TestApproveFillsMissingReference(t *testing.T) {
	// Sem referência guardada, a que vem no webhook é aceite e fica registada.
	p, _ := NewPayment("p1", "s1", money.FromMajor(100, money.AOA), MethodReference)
	if err := p.Approve("tx-nova"); err != nil {
		t.Fatal(err)
	}
	if p.ProviderRef != "tx-nova" {
		t.Errorf("referência = %q", p.ProviderRef)
	}
}

func TestExpiryHelpers(t *testing.T) {
	p, _ := NewPayment("p1", "s1", money.FromMajor(100, money.AOA), MethodReference)
	now := time.Now().UTC()
	// Uma cobrança sem prazo nunca expira.
	if p.Expired(now.Add(100 * time.Hour)) {
		t.Error("sem prazo não expira")
	}
	p.SetExpiry(now.Add(time.Hour))
	if p.ExpiresAt == nil || p.Expired(now) {
		t.Error("dentro do prazo")
	}
	if !p.Expired(now.Add(2 * time.Hour)) {
		t.Error("passado o prazo")
	}
}

func TestRetryDue(t *testing.T) {
	p, _ := NewPayment("p1", "s1", money.FromMajor(100, money.AOA), MethodDirectDebit)
	now := time.Now().UTC()
	// Sem tentativa marcada, pode ser apresentada já.
	if !p.RetryDue(now) {
		t.Error("sem espera marcada devia poder ser apresentada")
	}
	p.RecordAttempt(time.Hour, 24*time.Hour)
	if p.RetryDue(now) {
		t.Error("antes da hora marcada não se reapresenta")
	}
	if !p.RetryDue(now.Add(2 * time.Hour)) {
		t.Error("passada a hora marcada já se pode reapresentar")
	}
}

func TestApplyResultFieldsAndNonApprovedStatus(t *testing.T) {
	p, _ := NewPayment("p1", "s1", money.FromMajor(100, money.AOA), MethodDirectDebit)
	exp := time.Now().Add(48 * time.Hour)
	p.ApplyResult("proxypay-dds", ChargeResult{
		Status: StatusPending, ProviderRef: "PAY-7-3", StatusRef: "PAY-7-3",
		Entity: "01234", Reference: "987654321", InvoiceURL: "https://f/1",
		ExternalID: 3, ExpiresAt: &exp,
	})
	if p.Entity != "01234" || p.Reference != "987654321" || p.ExternalID != 3 {
		t.Errorf("campos = %+v", p)
	}
	if p.ExpiresAt == nil || !p.ExpiresAt.Equal(exp) {
		t.Errorf("validade = %v", p.ExpiresAt)
	}
	if p.InvoiceURL == "" || p.Provider != "proxypay-dds" {
		t.Errorf("resultado = %+v", p)
	}

	// Um estado terminal que não seja aprovado é escrito directamente.
	p.ApplyResult("proxypay-dds", ChargeResult{Status: StatusRejected})
	if p.Status != StatusRejected {
		t.Errorf("estado = %s", p.Status)
	}
}

func TestToChargeRequest(t *testing.T) {
	p, _ := NewPayment("p1", "sub-1", money.FromMajor(5900, money.AOA), MethodDirectDebit)
	start := time.Now()
	end := start.AddDate(0, 1, 0)
	p.PeriodStart, p.PeriodEnd = &start, &end
	p.MandateID = "mandato-1"
	p.Description = "Plano Essencial"
	p.Metadata = map[string]string{"origem": "renovacao"}
	p.SetExpiry(end)

	req := p.ToChargeRequest(Customer{ID: "cli-1", Name: "Ana"})
	if req.Reference != "p1" || req.MandateID != "mandato-1" || req.Customer.Name != "Ana" {
		t.Errorf("pedido = %+v", req)
	}
	if req.PeriodStart == nil || req.PeriodEnd == nil || req.ExpiresAt == nil {
		t.Errorf("datas em falta: %+v", req)
	}
	if req.Metadata["origem"] != "renovacao" {
		t.Errorf("metadados = %v", req.Metadata)
	}
}

func TestLineItemTotal(t *testing.T) {
	l := LineItem{Description: "x", UnitPrice: money.FromMajor(100, money.AOA), Quantity: 3}
	if got := l.Total(); got.Minor != 30000 {
		t.Errorf("= %s", got)
	}
	// Quantidade em falta lê-se como uma.
	l.Quantity = 0
	if got := l.Total(); got.Minor != 10000 {
		t.Errorf("= %s", got)
	}
}

func TestRegistryAccessors(t *testing.T) {
	p := &fakeProvider{name: "gateway", methods: []Method{MethodReference},
		currencies: []money.Currency{money.AOA}, configured: true}
	r := NewRegistry().Register(p, nil) // um nil é ignorado em silêncio

	if names := r.Names(); len(names) != 1 || names[0] != "gateway" {
		t.Errorf("nomes = %v", names)
	}
	if all := r.All(); len(all) != 1 {
		t.Errorf("providers = %v", all)
	}
	if got := r.MustGet("GATEWAY"); got == nil {
		t.Error("a procura não distingue maiúsculas")
	}
	if got := r.MustGet("inexistente"); got != nil {
		t.Errorf("= %v", got)
	}
	if _, ok := r.ForCurrency(money.AOA); !ok {
		t.Error("devia haver provider para kwanza")
	}
	if _, ok := r.ForCurrency(money.EUR); ok {
		t.Error("não há provider para euro")
	}

	// Registar de novo o mesmo nome substitui sem duplicar a ordem.
	r.Register(&fakeProvider{name: "gateway", methods: []Method{MethodCard},
		currencies: []money.Currency{money.EUR}, configured: true})
	if len(r.Names()) != 1 {
		t.Errorf("nomes = %v", r.Names())
	}
	if _, ok := r.For(MethodCard, money.EUR); !ok {
		t.Error("o provider registado de novo é que passa a valer")
	}
}

func TestRegistryCapabilityLookups(t *testing.T) {
	r := NewRegistry().Register(&capableProvider{})

	if _, ok := r.Verifier("capaz"); !ok {
		t.Error("devia expor a consulta de estado")
	}
	if _, ok := r.WebhookParser("capaz"); !ok {
		t.Error("devia expor a leitura de webhooks")
	}
	if _, ok := r.Subscriber("capaz"); !ok {
		t.Error("devia expor a gestão de subscrições")
	}
	if _, ok := r.Refunder("capaz"); !ok {
		t.Error("devia expor o estorno")
	}

	// Um provider sem capacidades não as anuncia.
	r2 := NewRegistry().Register(&fakeProvider{name: "simples", configured: true})
	for _, look := range []func(string) bool{
		func(n string) bool { _, ok := r2.Verifier(n); return ok },
		func(n string) bool { _, ok := r2.WebhookParser(n); return ok },
		func(n string) bool { _, ok := r2.Subscriber(n); return ok },
		func(n string) bool { _, ok := r2.Refunder(n); return ok },
	} {
		if look("simples") {
			t.Error("um provider simples não tem capacidades opcionais")
		}
		if look("inexistente") {
			t.Error("um provider inexistente não tem capacidade nenhuma")
		}
	}
}

func TestRegistryChargeSucceeds(t *testing.T) {
	p := &fakeProvider{name: "gateway", methods: []Method{MethodMCX},
		currencies: []money.Currency{money.AOA}, configured: true}
	r := NewRegistry().Register(p)

	res, name, err := r.Charge(t.Context(), ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: MethodMCX,
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "gateway" || !p.charged || res.Status != StatusApproved {
		t.Errorf("cobrança = %q, %+v", name, res)
	}
}

func TestRegistryForWithoutMethodMatchesAnyProvider(t *testing.T) {
	p := &fakeProvider{name: "gateway", methods: []Method{MethodMCX},
		currencies: []money.Currency{money.AOA}, configured: true}
	r := NewRegistry().Register(p)
	if _, ok := r.For("", money.AOA); !ok {
		t.Error("sem método pedido, qualquer provider da moeda serve")
	}
}

// capableProvider implementa todas as capacidades opcionais.
type capableProvider struct{ fakeProvider }

func (c *capableProvider) Name() string                         { return "capaz" }
func (c *capableProvider) Methods() []Method                    { return []Method{MethodCard} }
func (c *capableProvider) Configured() bool                     { return true }
func (c *capableProvider) SupportsCurrency(money.Currency) bool { return true }
func (c *capableProvider) VerifyCharge(_ context.Context, _, _ string) (ChargeStatus, error) {
	return ChargeStatus{}, nil
}
func (c *capableProvider) ParseWebhook([]byte, string) (*Event, error)               { return &Event{}, nil }
func (c *capableProvider) CancelSubscription(context.Context, string, bool) error    { return nil }
func (c *capableProvider) PortalURL(context.Context, string, string) (string, error) { return "", nil }
func (c *capableProvider) Refund(context.Context, Refund) (RefundResult, error) {
	return RefundResult{}, nil
}

func TestMethodsForSkipsUnconfiguredProviders(t *testing.T) {
	// Um gateway sem chave não pode aparecer na lista de métodos: mostrá-lo é
	// deixar o cliente escolher um caminho que vai falhar no fim.
	broken := &fakeProvider{name: "momenu", methods: []Method{MethodMCX},
		currencies: []money.Currency{money.AOA}, configured: false}
	working := &fakeProvider{name: "proxypay", methods: []Method{MethodReference},
		currencies: []money.Currency{money.AOA}, configured: true}
	other := &fakeProvider{name: "stripe", methods: []Method{MethodCard},
		currencies: []money.Currency{money.EUR}, configured: true}

	r := NewRegistry().Register(broken, working, other)
	got := r.MethodsFor(money.AOA)
	if len(got) != 1 || got[0] != MethodReference {
		t.Errorf("métodos em kwanza = %v, queria só a referência", got)
	}
}
