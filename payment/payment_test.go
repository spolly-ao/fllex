package payment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
)

func TestStatusTransitions(t *testing.T) {
	tests := []struct {
		from, to Status
		want     bool
	}{
		{StatusPending, StatusApproved, true},
		{StatusPending, StatusRejected, true},
		{StatusPending, StatusCancelled, true},
		{StatusPending, StatusExpired, true},
		// Aprovar duas vezes cobra o cliente a dobrar.
		{StatusApproved, StatusApproved, false},
		// Cancelar algo já pago dá por não-recebido dinheiro que entrou.
		{StatusApproved, StatusCancelled, false},
		{StatusApproved, StatusRefunded, true},
		{StatusExpired, StatusApproved, false},
		{StatusCancelled, StatusApproved, false},
	}
	for _, tt := range tests {
		if got := tt.from.CanTransitionTo(tt.to); got != tt.want {
			t.Errorf("%s -> %s = %v, queria %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestApproveRejectsMismatchedReference(t *testing.T) {
	p, err := NewPayment("p1", "sub1", money.FromMajor(5000, money.AOA), MethodReference)
	if err != nil {
		t.Fatal(err)
	}
	p.ProviderRef = "tx-esperada"

	// Um webhook com a referência de outra cobrança não pode liquidar esta.
	if err := p.Approve("tx-de-outra"); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("aprovar com referência errada devolveu %v, queria ErrInvalidTransition", err)
	}
	if p.Status != StatusPending {
		t.Errorf("estado ficou %s, queria continuar pendente", p.Status)
	}

	if err := p.Approve("tx-esperada"); err != nil {
		t.Fatalf("aprovar com a referência certa falhou: %v", err)
	}
	if p.Status != StatusApproved || p.ProcessedAt == nil {
		t.Errorf("depois de aprovar: estado %s, processado %v", p.Status, p.ProcessedAt)
	}
}

func TestNewPaymentRejectsNonPositive(t *testing.T) {
	if _, err := NewPayment("p1", "s1", money.Zero(money.AOA), MethodReference); !errors.Is(err, ErrAmountNotPositive) {
		t.Errorf("valor zero devolveu %v, queria ErrAmountNotPositive", err)
	}
}

func TestRecordAttemptBacksOff(t *testing.T) {
	p, _ := NewPayment("p1", "s1", money.FromMajor(100, money.AOA), MethodDirectDebit)
	base, max := time.Minute, 30*time.Minute

	var waits []time.Duration
	for i := 0; i < 8; i++ {
		before := time.Now().UTC()
		p.RecordAttempt(base, max)
		waits = append(waits, p.NextRetryAt.Sub(before).Round(time.Minute))
	}
	if waits[0] != time.Minute {
		t.Errorf("primeira espera = %v, queria 1m", waits[0])
	}
	if waits[1] != 2*time.Minute {
		t.Errorf("segunda espera = %v, queria 2m", waits[1])
	}
	// Nunca ultrapassa o tecto, senão a espera cresce até nunca mais tentar.
	for i, w := range waits {
		if w > max {
			t.Errorf("espera %d = %v, acima do tecto de %v", i, w, max)
		}
	}
	if waits[len(waits)-1] != max {
		t.Errorf("última espera = %v, queria o tecto %v", waits[len(waits)-1], max)
	}
}

func TestApplyResultApprovesInstantCharge(t *testing.T) {
	p, _ := NewPayment("p1", "s1", money.FromMajor(5000, money.AOA), MethodMCX)
	p.ApplyResult("momenu", ChargeResult{
		Kind:        KindPaid,
		Status:      StatusApproved,
		ProviderRef: "tx-123",
		InvoiceURL:  "https://exemplo/factura",
	})
	if p.Status != StatusApproved {
		t.Errorf("estado = %s, queria aprovado", p.Status)
	}
	if p.ProcessedAt == nil {
		t.Error("uma cobrança aprovada tem de ficar com a data de processamento")
	}
	if p.ProviderRef != "tx-123" || p.InvoiceURL == "" || p.Provider != "momenu" {
		t.Errorf("resultado mal aplicado: %+v", p)
	}
}

// --- registo ------------------------------------------------------------------

type fakeProvider struct {
	name       string
	methods    []Method
	currencies []money.Currency
	configured bool
	charged    bool
}

func (f *fakeProvider) Name() string      { return f.name }
func (f *fakeProvider) Methods() []Method { return f.methods }
func (f *fakeProvider) Configured() bool  { return f.configured }
func (f *fakeProvider) SupportsCurrency(c money.Currency) bool {
	for _, v := range f.currencies {
		if v == c {
			return true
		}
	}
	return false
}
func (f *fakeProvider) Charge(context.Context, ChargeRequest) (ChargeResult, error) {
	f.charged = true
	return ChargeResult{Kind: KindPaid, Status: StatusApproved, ProviderRef: f.name}, nil
}

func TestRegistryPrefersRegistrationOrder(t *testing.T) {
	local := &fakeProvider{name: "momenu", methods: []Method{MethodMCX, MethodReference},
		currencies: []money.Currency{money.AOA}, configured: true}
	global := &fakeProvider{name: "stripe", methods: []Method{MethodCard},
		currencies: []money.Currency{money.AOA, money.EUR}, configured: true}

	r := NewRegistry().Register(local, global)

	// O kwanza por Multicaixa Express vai ao gateway local.
	if p, ok := r.For(MethodMCX, money.AOA); !ok || p.Name() != "momenu" {
		t.Errorf("MCX em AOA resolveu para %v", p)
	}
	// O cartão vai ao Stripe, mesmo em kwanza, porque o local não o cobra.
	if p, ok := r.For(MethodCard, money.AOA); !ok || p.Name() != "stripe" {
		t.Errorf("cartão em AOA resolveu para %v", p)
	}
	// O euro só tem um caminho.
	if p, ok := r.For(MethodCard, money.EUR); !ok || p.Name() != "stripe" {
		t.Errorf("cartão em EUR resolveu para %v", p)
	}
	if _, ok := r.For(MethodMCX, money.EUR); ok {
		t.Error("não devia haver Multicaixa Express em euros")
	}
}

func TestRegistrySkipsUnconfigured(t *testing.T) {
	// Um gateway registado mas sem chave não pode apanhar o pedido: quem está
	// abaixo dele na ordem tem de o substituir, senão a compra falha por uma
	// configuração em falta.
	broken := &fakeProvider{name: "momenu", methods: []Method{MethodReference},
		currencies: []money.Currency{money.AOA}, configured: false}
	working := &fakeProvider{name: "proxypay", methods: []Method{MethodReference},
		currencies: []money.Currency{money.AOA}, configured: true}

	r := NewRegistry().Register(broken, working)
	p, ok := r.For(MethodReference, money.AOA)
	if !ok || p.Name() != "proxypay" {
		t.Errorf("resolveu para %v, queria o gateway configurado", p)
	}
}

func TestRegistryMethodsForHidesAdminOnly(t *testing.T) {
	gateway := &fakeProvider{name: "momenu", methods: []Method{MethodMCX, MethodReference},
		currencies: []money.Currency{money.AOA}, configured: true}
	manual := &fakeProvider{name: "offline", methods: []Method{MethodExternal, MethodManual},
		currencies: []money.Currency{money.AOA}, configured: true}

	r := NewRegistry().Register(gateway, manual)

	public := r.MethodsFor(money.AOA)
	for _, m := range public {
		if !m.SelfService() {
			t.Errorf("o método %s não devia ser oferecido a um cliente", m)
		}
	}
	if len(public) != 2 {
		t.Errorf("métodos públicos = %v, queria só MCX e referência", public)
	}
	if len(r.AdminMethodsFor(money.AOA)) != 4 {
		t.Errorf("métodos de backoffice = %v, queria os quatro", r.AdminMethodsFor(money.AOA))
	}
}

func TestRegistryChargeWithoutProvider(t *testing.T) {
	r := NewRegistry()
	_, _, err := r.Charge(context.Background(), ChargeRequest{
		Amount: money.FromMajor(100, money.EUR), Method: MethodCard,
	})
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("sem providers devolveu %v, queria ErrNoProvider", err)
	}
}

func TestMethodClassification(t *testing.T) {
	if !MethodCard.Recurring() || !MethodDirectDebit.Recurring() {
		t.Error("cartão e débito directo cobram sozinhos")
	}
	if MethodReference.Recurring() || MethodMCX.Recurring() {
		t.Error("referência e Multicaixa Express não cobram sozinhos")
	}
	if !MethodMCX.Instant() {
		t.Error("o Multicaixa Express é síncrono")
	}
	if !MethodReference.Deferred() {
		t.Error("a referência é diferida")
	}
	if MethodExternal.SelfService() || MethodManual.SelfService() {
		t.Error("transferência e atribuição manual são de backoffice")
	}
}

func TestIsRetryable(t *testing.T) {
	if IsRetryable(ErrNotConfigured) {
		t.Error("uma configuração em falta não melhora com repetição")
	}
	if IsRetryable(ErrMandateNotActive) {
		t.Error("um mandato por activar depende do cliente, não de repetir")
	}
	if !IsRetryable(&GatewayError{StatusCode: 503}) {
		t.Error("um 503 vale a pena repetir")
	}
	if IsRetryable(&GatewayError{StatusCode: 400}) {
		t.Error("um 400 dá sempre o mesmo resultado")
	}
	if !IsRetryable(&GatewayError{StatusCode: 0, Message: "falha de rede"}) {
		t.Error("uma falha de rede vale a pena repetir")
	}
}
