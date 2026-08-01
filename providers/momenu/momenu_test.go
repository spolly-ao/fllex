package momenu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
	"github.com/spolly-ao/fllex/phone"
)

func newTestProvider(t *testing.T, h http.HandlerFunc) (*Provider, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
	return p, srv.Close
}

func TestChargeMCXIsPaidSynchronously(t *testing.T) {
	var body MCXRequest
	p, done := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "chave" {
			t.Errorf("chave de API em falta no pedido")
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"success":true,"transactionId":"tx-1","invoiceUrl":"https://f/1"}`)
	})
	defer done()

	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference:   "sub-1",
		Amount:      money.FromMajor(5900, money.AOA),
		Method:      payment.MethodMCX,
		Description: "Plano Essencial",
		Customer:    payment.Customer{Phone: "+244 921 234 567", Name: "Ana", TaxID: "5417..."},
	})
	if err != nil {
		t.Fatalf("cobrança falhou: %v", err)
	}
	// O Multicaixa Express é síncrono: sucesso significa dinheiro recebido.
	if res.Kind != payment.KindPaid || res.Status != payment.StatusApproved {
		t.Errorf("resultado = %+v, queria pago de imediato", res)
	}
	if res.ProviderRef != "tx-1" || res.InvoiceURL == "" {
		t.Errorf("referências mal lidas: %+v", res)
	}
	// O valor vai em kwanzas inteiros, que é o que o gateway espera.
	if body.PaymentInfo.Amount != 5900 {
		t.Errorf("valor enviado = %v, queria 5900", body.PaymentInfo.Amount)
	}
	if body.PaymentInfo.PhoneNumber != "244921234567" {
		t.Errorf("telemóvel enviado = %q, queria normalizado", body.PaymentInfo.PhoneNumber)
	}
	if !body.InstantWithdraw {
		t.Error("o gateway exige instantWithdraw em todos os pagamentos")
	}
	if body.Customer == nil || body.Customer.NIF == "" || body.Customer.Name != "Ana" {
		t.Errorf("bloco de cliente = %+v, queria nome com NIF", body.Customer)
	}
}

func TestChargeMCXRejectsBadPhoneBeforeNetwork(t *testing.T) {
	called := false
	p, done := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		fmt.Fprint(w, `{"success":true}`)
	})
	defer done()

	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount:   money.FromMajor(100, money.AOA),
		Method:   payment.MethodMCX,
		Customer: payment.Customer{Phone: "12345"},
	})
	if !errors.Is(err, phone.ErrInvalidAO) {
		t.Errorf("erro = %v, queria número inválido", err)
	}
	// Um número inválido não pode gastar uma chamada de três minutos para
	// voltar com um erro opaco do gateway.
	if called {
		t.Error("não devia ter chegado a chamar o gateway")
	}
}

func TestChargeReferenceSeparatesTheTwoIdentifiers(t *testing.T) {
	p, done := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"operationId":"op-1","transactionId":"tx-1",
			"referenceNumber":"987654321","entity":"01234","dueDate":"2025-08-05"}`)
	})
	defer done()

	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(5900, money.AOA),
		Method: payment.MethodReference,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != payment.KindReference || res.Status != payment.StatusPending {
		t.Errorf("resultado = %+v", res)
	}
	// Trocar os dois identificadores devolve sempre "não encontrado": o webhook
	// correlaciona pelo da transacção e a consulta usa o da operação.
	if res.ProviderRef != "tx-1" {
		t.Errorf("referência do gateway = %q, queria tx-1", res.ProviderRef)
	}
	if res.StatusRef != "op-1" {
		t.Errorf("referência de consulta = %q, queria op-1", res.StatusRef)
	}
	if res.Entity != "01234" || res.Reference != "987654321" {
		t.Errorf("dados de pagamento = %q / %q", res.Entity, res.Reference)
	}
	if res.ExpiresAt == nil {
		t.Error("a validade devia ter sido lida da resposta")
	}
}

func TestVerifyChargeReadsPaidStatus(t *testing.T) {
	p, done := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("merchantTransactionId"); got != "tx-1" {
			t.Errorf("identificador da ordem = %q, queria tx-1", got)
		}
		fmt.Fprint(w, `{"success":true,"payment":{"status":"paid"},"invoiceUrl":"https://f/1"}`)
	})
	defer done()

	st, err := p.VerifyCharge(context.Background(), "op-1", "tx-1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Paid || st.Status != payment.StatusApproved {
		t.Errorf("estado = %+v, queria pago", st)
	}
	if st.InvoiceURL == "" {
		t.Error("a factura devia vir preenchida")
	}
}

func TestVerifyChargePendingStaysPending(t *testing.T) {
	p, done := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"payment":{"status":"pending"}}`)
	})
	defer done()

	st, err := p.VerifyCharge(context.Background(), "op-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Paid {
		t.Error("uma referência por pagar não pode dar por paga")
	}
}

func TestParseWebhookIsOnlyAHint(t *testing.T) {
	// A entrega não é assinada: o evento serve para acordar a confirmação, e
	// nunca como prova de pagamento.
	p := New(Config{APIKey: "chave"})
	ev, err := p.ParseWebhook([]byte(`{"event":"payment.confirmed","merchantTransactionId":"tx-1","operationStatus":"1"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventChargeSucceeded {
		t.Errorf("tipo = %s", ev.Type)
	}
	if ev.Status == payment.StatusApproved {
		t.Error("um webhook não assinado não pode devolver o estado como aprovado")
	}
	// Um corpo ilegível não é erro: é ruído a ignorar.
	ev, err = p.ParseWebhook([]byte(`isto não é json`), "")
	if err != nil || !ev.Ignorable() {
		t.Errorf("corpo ilegível deu %v / %+v", err, ev)
	}
}

func TestUnsupportedMethodAndCurrency(t *testing.T) {
	p := New(Config{APIKey: "chave"})
	if p.SupportsCurrency(money.EUR) {
		t.Error("o MoMenu só processa kwanza")
	}
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(10, money.EUR), Method: payment.MethodReference,
	})
	if !errors.Is(err, payment.ErrUnsupportedCurrency) {
		t.Errorf("moeda não suportada devolveu %v", err)
	}
	_, err = p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(10, money.AOA), Method: payment.MethodCard,
	})
	if !errors.Is(err, payment.ErrUnsupportedMethod) {
		t.Errorf("método não suportado devolveu %v", err)
	}
}

// --- reconciliação -------------------------------------------------------------

func TestReconcilerMatchesLostPayment(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/invoices" {
			fmt.Fprintf(w, `{"success":true,"invoices":[
				{"invoiceId":"inv-1","invoiceNumber":"FT 1/1","total":5900,"createdAt":%q},
				{"invoiceId":"inv-2","invoiceNumber":"FT 1/2","total":100,"createdAt":%q}
			]}`, started.Format(time.RFC3339), started.Format(time.RFC3339))
			return
		}
		fmt.Fprint(w, `{"success":true,"invoice":{"invoiceId":"inv-1","invoiceNumber":"FT 1/1",
			"total":5900,"customer":{"phone":"244921234567"}}}`)
	}))
	defer srv.Close()

	attempt := Attempt{
		ID: "a1", Reference: "encomenda-1", Phone: "921234567",
		Amount: money.FromMajor(5900, money.AOA), StartedAt: started,
	}
	var matched *Match
	r := &Reconciler{
		Client:  NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) { return []Attempt{attempt}, nil },
		Confirm: func(_ context.Context, m Match) error { matched = &m; return nil },
	}
	r.Run(context.Background())

	if matched == nil {
		t.Fatal("o pagamento perdido devia ter sido recuperado")
	}
	if matched.Invoice.InvoiceID != "inv-1" {
		t.Errorf("factura correspondida = %q, queria inv-1", matched.Invoice.InvoiceID)
	}
}

func TestReconcilerLeavesAmbiguityForHumans(t *testing.T) {
	// Promover a factura errada cobra a encomenda de outra pessoa: perante duas
	// candidatas, não se decide nada.
	started := time.Now().Add(-10 * time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/invoices" {
			fmt.Fprintf(w, `{"success":true,"invoices":[
				{"invoiceId":"inv-1","total":5900,"createdAt":%q},
				{"invoiceId":"inv-2","total":5900,"createdAt":%q}
			]}`, started.Format(time.RFC3339), started.Format(time.RFC3339))
			return
		}
		id := "inv-1"
		if r.URL.Path == "/api/invoices/inv-2" {
			id = "inv-2"
		}
		fmt.Fprintf(w, `{"success":true,"invoice":{"invoiceId":%q,"total":5900,
			"customer":{"phone":"244921234567"}}}`, id)
	}))
	defer srv.Close()

	confirmed := 0
	r := &Reconciler{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) {
			return []Attempt{{Reference: "e1", Phone: "921234567",
				Amount: money.FromMajor(5900, money.AOA), StartedAt: started}}, nil
		},
		Confirm: func(context.Context, Match) error { confirmed++; return nil },
	}
	r.Run(context.Background())

	if confirmed != 0 {
		t.Errorf("confirmou %d, queria deixar a ambiguidade para resolução manual", confirmed)
	}
}

func TestReconcilerIgnoresWrongPhone(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/invoices" {
			fmt.Fprintf(w, `{"success":true,"invoices":[{"invoiceId":"inv-1","total":5900,"createdAt":%q}]}`,
				started.Format(time.RFC3339))
			return
		}
		fmt.Fprint(w, `{"success":true,"invoice":{"invoiceId":"inv-1","total":5900,
			"customer":{"phone":"244999888777"}}}`)
	}))
	defer srv.Close()

	confirmed := 0
	r := &Reconciler{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) {
			return []Attempt{{Reference: "e1", Phone: "921234567",
				Amount: money.FromMajor(5900, money.AOA), StartedAt: started}}, nil
		},
		Confirm: func(context.Context, Match) error { confirmed++; return nil },
	}
	r.Run(context.Background())

	if confirmed != 0 {
		t.Error("uma factura de outro telemóvel não corresponde")
	}
}

func TestReconcilerGivesUpAfterMaxAge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"invoices":[{"invoiceId":"inv-1","total":5900,"createdAt":"2025-01-01T00:00:00Z"}]}`)
	}))
	defer srv.Close()

	abandoned := 0
	r := &Reconciler{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		MaxAge: time.Hour,
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) {
			return []Attempt{{Reference: "e1", Phone: "921234567",
				Amount: money.FromMajor(5900, money.AOA), StartedAt: time.Now().Add(-48 * time.Hour)}}, nil
		},
		Confirm: func(context.Context, Match) error { return nil },
		Abandon: func(context.Context, Attempt) error { abandoned++; return nil },
	}
	r.Run(context.Background())

	if abandoned != 1 {
		t.Errorf("abandonadas = %d, queria 1", abandoned)
	}
}

func TestAmountMatches(t *testing.T) {
	want := money.FromMajor(5900, money.AOA)
	if !amountMatches(5900, want) {
		t.Error("5900 devia corresponder")
	}
	if amountMatches(5901, want) {
		t.Error("um kwanza a mais não é o mesmo pagamento")
	}
	if amountMatches(590, want) {
		t.Error("um valor dez vezes menor não corresponde")
	}
}
