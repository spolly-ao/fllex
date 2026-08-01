package proxypaydds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spolly-ao/fllex/mandate"
	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

type fakeResolver struct {
	externalID int
	active     bool
	err        error
}

func (f fakeResolver) Resolve(context.Context, string) (int, bool, error) {
	return f.externalID, f.active, f.err
}

func TestChargeRequiresActiveMandate(t *testing.T) {
	p := New(Config{APIKey: "chave", EntityID: "AO1234567890"}, fakeResolver{externalID: 7, active: false})

	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount:    money.FromMajor(5900, money.AOA),
		Method:    payment.MethodDirectDebit,
		MandateID: "mandato-1",
	})
	// Um mandato por activar é o cliente que ainda não foi ao banco, e não uma
	// falha do sistema: quem chama tem de o poder distinguir para não gastar aí
	// uma tentativa de retentativa.
	if !errors.Is(err, payment.ErrMandateNotActive) {
		t.Errorf("erro = %v, queria ErrMandateNotActive", err)
	}
	if payment.IsRetryable(err) {
		t.Error("não vale a pena repetir enquanto o titular não activar")
	}
}

func TestChargeWithoutMandateID(t *testing.T) {
	p := New(Config{APIKey: "chave", EntityID: "AO1234567890"}, fakeResolver{})
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodDirectDebit,
	})
	if !errors.Is(err, payment.ErrMandateRequired) {
		t.Errorf("erro = %v, queria ErrMandateRequired", err)
	}
}

func TestChargePresentsPayment(t *testing.T) {
	var presented PresentPaymentRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer chave" {
			t.Errorf("autenticação = %q", got)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/sequences"):
			fmt.Fprint(w, `{"id":3}`)
		case strings.HasSuffix(r.URL.Path, "/payments"):
			_ = json.NewDecoder(r.Body).Decode(&presented)
			fmt.Fprintf(w, `{"id":3,"mandate_id":7,"transaction_id":%q}`, presented.TransactionID)
		default:
			t.Errorf("pedido inesperado: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := NewWithClient(
		NewClient(Config{APIKey: "chave", EntityID: "AO1234567890", BaseURL: srv.URL}),
		fakeResolver{externalID: 7, active: true},
	)

	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "cobranca-9",
		Amount:    money.FromMajor(5900, money.AOA),
		Method:    payment.MethodDirectDebit,
		MandateID: "mandato-1",
	})
	if err != nil {
		t.Fatalf("apresentação falhou: %v", err)
	}
	// O dinheiro só sai na data de liquidação e o banco ainda pode recusar: a
	// cobrança fica pendente, não paga.
	if res.Kind != payment.KindPending || res.Status != payment.StatusPending {
		t.Errorf("resultado = %+v, queria pendente", res)
	}
	if res.ExternalID != 3 {
		t.Errorf("identificador da cobrança = %d, queria 3", res.ExternalID)
	}
	if presented.Amount != "5900.00" {
		t.Errorf("valor enviado = %q, queria \"5900.00\"", presented.Amount)
	}
	// O identificador de transacção tem de ser reconstruível a partir dos dois
	// números do gateway, para o evento voltar a encontrar esta cobrança.
	if presented.TransactionID != "PAY-7-3" {
		t.Errorf("identificador de transacção = %q, queria PAY-7-3", presented.TransactionID)
	}
}

func TestParseTransactionID(t *testing.T) {
	m, p, ok := parseTransactionID("PAY-7-3")
	if !ok || m != 7 || p != 3 {
		t.Errorf("desmontagem = (%d, %d, %v)", m, p, ok)
	}
	for _, bad := range []string{"", "PAY-7", "XXX-7-3", "PAY-a-b"} {
		if _, _, ok := parseTransactionID(bad); ok {
			t.Errorf("%q não devia ser válido", bad)
		}
	}
}

func TestRegisterSAPMandate(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sequences") {
			fmt.Fprint(w, `{"id":42}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"id":42,"contract_id":"contrato-1","credit_iban":"AO06...","debitor_name":"Ana"}`)
	}))
	defer srv.Close()

	p := NewWithClient(
		NewClient(Config{APIKey: "chave", EntityID: "AO1234567890", BaseURL: srv.URL, CreditIBAN: "AO06..."}),
		fakeResolver{},
	)

	m, err := p.RegisterMandate(context.Background(), RegisterRequest{
		SubjectID:  "sub-1",
		ContractID: "contrato-1",
		DebtorName: "Ana Silva",
		TaxID:      "5417000000",
		Phone:      "921234567",
		MaxAmount:  money.FromMajor(10000, money.AOA),
	})
	if err != nil {
		t.Fatalf("registo falhou: %v", err)
	}
	if m.Status != mandate.StatusSubmitted {
		t.Errorf("estado = %s, queria submetido (à espera do titular)", m.Status)
	}
	if m.ExternalID != 42 {
		t.Errorf("identificador do gateway = %d, queria 42", m.ExternalID)
	}
	if m.Type != mandate.TypeSelfActivated {
		t.Errorf("tipo = %s, queria auto-activado", m.Type)
	}
	if m.ExpiresAt == nil {
		t.Error("um mandato à espera de activação precisa de prazo")
	}
	if body["preauth"] != false {
		t.Errorf("preauth = %v, queria false num mandato auto-activado", body["preauth"])
	}
	// O telemóvel vai no formato que o gateway exige.
	if body["mobile"] != "+244-921234567" {
		t.Errorf("telemóvel = %v, queria +244-921234567", body["mobile"])
	}
	if body["max_amount"] != "10000.00" {
		t.Errorf("tecto = %v, queria \"10000.00\"", body["max_amount"])
	}

	// O titular precisa destes dois números para activar no seu banco.
	entity, code := p.ActivationInstructions(m)
	if entity != "AO1234567890" {
		t.Errorf("entidade = %q", entity)
	}
	if code != "0000000000042" {
		t.Errorf("código de activação = %q, queria treze dígitos", code)
	}
}

func TestTranslateEvent(t *testing.T) {
	p := New(Config{APIKey: "chave", EntityID: "AO1"}, fakeResolver{})

	tests := []struct {
		raw  Event
		want payment.EventType
	}{
		{Event{Type: EventMandateActivated, Data: EventData{MandateID: 7}}, payment.EventMandateActivated},
		{Event{Type: EventMandateRejected, Data: EventData{MandateID: 7}}, payment.EventMandateRejected},
		{Event{Type: EventPaymentCollected, Data: EventData{MandateID: 7, PaymentID: 3}}, payment.EventChargeSucceeded},
		{Event{Type: EventPaymentRejected, Data: EventData{MandateID: 7, PaymentID: 3}}, payment.EventChargeFailed},
		{Event{Type: EventPaymentReversed, Data: EventData{MandateID: 7}}, payment.EventChargeRefunded},
		// Os eventos intermédios descrevem o processamento interno e não exigem
		// nada de nós.
		{Event{Type: EventPresentPaymentSubmitted, Data: EventData{MandateID: 7}}, payment.EventNone},
	}
	for _, tt := range tests {
		if got := p.TranslateEvent(tt.raw); got.Type != tt.want {
			t.Errorf("%s traduziu para %s, queria %s", tt.raw.Type, got.Type, tt.want)
		}
	}

	// O valor e o motivo do banco têm de sobreviver à tradução.
	ev := p.TranslateEvent(Event{
		Type: EventPaymentRejected,
		Data: EventData{MandateID: 7, PaymentID: 3, Amount: "5900.00", Reason: PayRejectInsufficientFunds,
			TransactionID: "PAY-7-3"},
	})
	if ev.Amount == nil || ev.Amount.Minor != money.FromMajor(5900, money.AOA).Minor {
		t.Errorf("valor = %v", ev.Amount)
	}
	if ev.Reason != PayRejectInsufficientFunds || ev.ChargeRef != "PAY-7-3" {
		t.Errorf("motivo/referência = %q / %q", ev.Reason, ev.ChargeRef)
	}
}

func TestBankWillRetry(t *testing.T) {
	yes := true
	if !BankWillRetry(Event{Data: EventData{Retry: &yes}}) {
		t.Error("a bandeira de reapresentação devia ser respeitada")
	}
	if !BankWillRetry(Event{Data: EventData{Reason: PayRejectInsufficientRetry}}) {
		t.Error("o código de reapresentação automática devia ser reconhecido")
	}
	// Reapresentar quando o banco já o vai fazer cobra duas vezes ao cliente.
	if BankWillRetry(Event{Data: EventData{Reason: PayRejectInsufficientFunds}}) {
		t.Error("saldo insuficiente sem reapresentação automática é nosso")
	}
}

func TestActivationCode(t *testing.T) {
	if got := ActivationCode(42); got != "0000000000042" {
		t.Errorf("código = %q, queria treze dígitos", got)
	}
}

func TestNotConfiguredWithoutResolver(t *testing.T) {
	p := NewWithClient(NewClient(Config{APIKey: "chave", EntityID: "AO1"}), nil)
	if p.Configured() {
		t.Error("sem resolvedor de mandatos não se pode cobrar")
	}
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodDirectDebit, MandateID: "m1",
	})
	if !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("erro = %v, queria ErrNotConfigured", err)
	}
}
