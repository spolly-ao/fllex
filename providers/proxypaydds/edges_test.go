package proxypaydds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/mandate"
	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func client(t *testing.T, h http.HandlerFunc) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	return NewClient(Config{
		APIKey: "chave", EntityID: "AO1234567890", BaseURL: srv.URL,
		CreditIBAN: "AO06000000000000000000000", Timeout: 5 * time.Second,
	}), srv.Close
}

func TestClientAccessorsAndDefaults(t *testing.T) {
	c := NewClient(Config{APIKey: "chave", EntityID: "AO1"})
	if c.http.BaseURL != DefaultBaseURL {
		t.Errorf("base = %q", c.http.BaseURL)
	}
	if c.EntityID() != "AO1" {
		t.Errorf("entidade = %q", c.EntityID())
	}
	if !c.Configured() {
		t.Error("com chave e entidade devia estar configurado")
	}
	// Sem entidade não se consegue montar o caminho da API.
	if NewClient(Config{APIKey: "chave"}).Configured() {
		t.Error("sem entidade não está configurado")
	}
	p := NewWithClient(c, fakeResolver{})
	if p.Client() != c {
		t.Error("Client() devia devolver o cliente recebido")
	}
	if ms := p.Methods(); len(ms) != 1 || ms[0] != payment.MethodDirectDebit {
		t.Errorf("métodos = %v", ms)
	}
	if p.SupportsCurrency(money.EUR) {
		t.Error("o débito directo é só em kwanza")
	}
}

func TestChargeGuards(t *testing.T) {
	ctx := context.Background()
	p := New(Config{APIKey: "chave", EntityID: "AO1"}, fakeResolver{externalID: 7, active: true})

	if _, err := p.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodCard, MandateID: "m1",
	}); !errors.Is(err, payment.ErrUnsupportedMethod) {
		t.Errorf("método errado = %v", err)
	}
	if _, err := p.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(100, money.EUR), Method: payment.MethodDirectDebit, MandateID: "m1",
	}); !errors.Is(err, payment.ErrUnsupportedCurrency) {
		t.Errorf("moeda errada = %v", err)
	}
	if _, err := p.Charge(ctx, payment.ChargeRequest{
		Amount: money.Zero(money.AOA), Method: payment.MethodDirectDebit, MandateID: "m1",
	}); !errors.Is(err, payment.ErrAmountNotPositive) {
		t.Errorf("valor zero = %v", err)
	}
}

func TestChargePropagatesResolverError(t *testing.T) {
	boom := errors.New("base de dados em baixo")
	p := New(Config{APIKey: "chave", EntityID: "AO1"}, fakeResolver{err: boom})
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodDirectDebit, MandateID: "m1",
	})
	if !errors.Is(err, boom) {
		t.Errorf("= %v", err)
	}
}

func TestChargeWithUnknownMandate(t *testing.T) {
	// Um identificador que o armazenamento não conhece devolve zero: é falta de
	// mandato, e não um mandato por activar.
	p := New(Config{APIKey: "chave", EntityID: "AO1"}, fakeResolver{externalID: 0, active: true})
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodDirectDebit, MandateID: "m1",
	})
	if !errors.Is(err, payment.ErrMandateRequired) {
		t.Errorf("= %v", err)
	}
}

func TestChargeSequenceAndPresentErrors(t *testing.T) {
	failSequence := true
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sequences") {
			if failSequence {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"id":3}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"mandato inactivo"}`)
	})
	defer done()

	p := NewWithClient(c, fakeResolver{externalID: 7, active: true})
	ctx := context.Background()
	req := payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodDirectDebit, MandateID: "m1",
	}

	if _, err := p.Charge(ctx, req); err == nil || !strings.Contains(err.Error(), "sequência") {
		t.Errorf("falha na sequência = %v", err)
	}
	failSequence = false
	if _, err := p.Charge(ctx, req); err == nil {
		t.Error("falha na apresentação devia dar erro")
	}
}

func TestChargeUsesTomorrowWithoutPeriodStart(t *testing.T) {
	var body PresentPaymentRequest
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sequences") {
			fmt.Fprint(w, `{"id":3}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"id":3,"mandate_id":7,"transaction_id":"PAY-7-3"}`)
	})
	defer done()

	p := NewWithClient(c, fakeResolver{externalID: 7, active: true})
	p.DefaultPurpose = "" // cai no valor por omissão
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodDirectDebit, MandateID: "m1",
	}); err != nil {
		t.Fatal(err)
	}
	// Sem período indicado, cobra-se amanhã: cobrar hoje um período que ainda
	// não começou é pedir dinheiro adiantado sem o cliente ter combinado isso.
	want := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	if body.CollectionDate != want {
		t.Errorf("data de liquidação = %q, queria %q", body.CollectionDate, want)
	}
	if body.Purpose != PurposeCash {
		t.Errorf("finalidade = %q", body.Purpose)
	}
}

func TestCancelCharge(t *testing.T) {
	var path string
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, `{"cancelation_id":"CXL-7-3","mandate_id":7,"payment_id":3}`)
	})
	defer done()

	p := NewWithClient(c, fakeResolver{externalID: 7, active: true})
	ctx := context.Background()
	req := payment.ChargeRequest{MandateID: "m1"}

	if err := p.CancelCharge(ctx, req, payment.ChargeResult{ExternalID: 3}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "/mandates/7/payments/3/cancelations") {
		t.Errorf("caminho = %q", path)
	}

	// Sem identificador de cobrança ou de mandato não há o que cancelar.
	path = ""
	if err := p.CancelCharge(ctx, req, payment.ChargeResult{}); err != nil {
		t.Fatal(err)
	}
	if err := p.CancelCharge(ctx, payment.ChargeRequest{}, payment.ChargeResult{ExternalID: 3}); err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Error("não devia ter chamado o gateway")
	}

	// Sem configuração.
	if err := New(Config{}, nil).CancelCharge(ctx, req, payment.ChargeResult{ExternalID: 3}); !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("= %v", err)
	}
}

func TestCancelChargeResolverErrorAndUnknownMandate(t *testing.T) {
	boom := errors.New("base de dados")
	p := New(Config{APIKey: "chave", EntityID: "AO1"}, fakeResolver{err: boom})
	err := p.CancelCharge(context.Background(), payment.ChargeRequest{MandateID: "m1"},
		payment.ChargeResult{ExternalID: 3})
	if !errors.Is(err, boom) {
		t.Errorf("= %v", err)
	}

	p = New(Config{APIKey: "chave", EntityID: "AO1"}, fakeResolver{externalID: 0})
	if err := p.CancelCharge(context.Background(), payment.ChargeRequest{MandateID: "m1"},
		payment.ChargeResult{ExternalID: 3}); err != nil {
		t.Errorf("mandato desconhecido = %v", err)
	}
}

func TestRefund(t *testing.T) {
	var path string
	var body ReversePaymentRequest
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"amount":"5900.00","mandate_id":7,"payment_id":3,"reversal_id":"REV-7-3"}`)
	})
	defer done()

	p := NewWithClient(c, fakeResolver{externalID: 7, active: true})
	res, err := p.Refund(context.Background(), payment.Refund{
		ChargeRef: "PAY-7-3", Amount: money.FromMajor(5900, money.AOA), Reason: ReversalReasonDuplicate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "/mandates/7/payments/3/reversals") {
		t.Errorf("caminho = %q", path)
	}
	// A reversão liquida no ciclo do banco: fica pendente, não aprovada.
	if res.Status != payment.StatusPending || res.RefundRef != "REV-7-3" {
		t.Errorf("resultado = %+v", res)
	}
	if body.Reason != ReversalReasonDuplicate {
		t.Errorf("motivo = %q", body.Reason)
	}
}

func TestRefundDefaultsAndErrors(t *testing.T) {
	var body ReversePaymentRequest
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"reversal_id":"REV-1","amount":"não é número"}`)
	})
	defer done()

	p := NewWithClient(c, fakeResolver{externalID: 7, active: true})
	ctx := context.Background()

	// Sem motivo indicado usa-se um por omissão: o banco exige um código.
	res, err := p.Refund(ctx, payment.Refund{ChargeRef: "PAY-7-3", Amount: money.FromMajor(10, money.AOA)})
	if err != nil {
		t.Fatal(err)
	}
	if body.Reason != ReversalReasonRefusedDebtor {
		t.Errorf("motivo = %q", body.Reason)
	}
	// Valor ilegível na resposta: fica o que pedimos.
	if res.Amount.Minor != money.FromMajor(10, money.AOA).Minor {
		t.Errorf("valor = %s", res.Amount)
	}

	// Referência mal formada.
	if _, err := p.Refund(ctx, payment.Refund{ChargeRef: "lixo"}); err == nil {
		t.Error("uma referência que não se desmonta devia dar erro")
	}
	// Sem configuração.
	if _, err := New(Config{}, nil).Refund(ctx, payment.Refund{ChargeRef: "PAY-1-1"}); !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("= %v", err)
	}
}

func TestRefundGatewayError(t *testing.T) {
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"já revertido"}`)
	})
	defer done()
	p := NewWithClient(c, fakeResolver{externalID: 7, active: true})
	if _, err := p.Refund(context.Background(), payment.Refund{
		ChargeRef: "PAY-7-3", Amount: money.FromMajor(10, money.AOA),
	}); err == nil {
		t.Error("queria erro")
	}
}

func TestRegisterCAPMandate(t *testing.T) {
	var body map[string]any
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sequences") {
			fmt.Fprint(w, `{"id":42}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"id":42,"debit_iban":"AO06111","credit_iban":"AO06000"}`)
	})
	defer done()

	p := NewWithClient(c, fakeResolver{})
	first := time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC)
	final := time.Date(2027, time.May, 1, 0, 0, 0, 0, time.UTC)

	m, err := p.RegisterMandate(context.Background(), RegisterRequest{
		SubjectID: "sub-1", ContractID: "contrato-1", Type: mandate.TypePreAuthorized,
		DebtorName: "Ana Silva", TaxID: "5417", DebitIBAN: "AO06111",
		SignatureDate: "2026-04-01", ImageID: "img-1", Email: "ana@exemplo.ao",
		FirstCollection: &first, FinalCollection: &final,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != mandate.TypePreAuthorized {
		t.Errorf("tipo = %s", m.Type)
	}
	if body["preauth"] != true {
		t.Errorf("preauth = %v, queria true num mandato pré-autorizado", body["preauth"])
	}
	if body["image_id"] != "img-1" || body["signature_date"] != "2026-04-01" {
		t.Errorf("formulário assinado = %v", body)
	}
	if body["first_collection_date"] != "2026-05-01" || body["final_collection_date"] != "2027-05-01" {
		t.Errorf("datas de cobrança = %v", body)
	}
	// O IBAN que recebe vem da configuração quando não é dado.
	if body["credit_iban"] != "AO06000000000000000000000" {
		t.Errorf("IBAN de crédito = %v", body["credit_iban"])
	}
}

func TestRegisterMandateErrors(t *testing.T) {
	ctx := context.Background()
	// Sem configuração.
	if _, err := New(Config{}, nil).RegisterMandate(ctx, RegisterRequest{}); !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("= %v", err)
	}

	// Falha a obter o número sequencial.
	failSeq := true
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sequences") {
			if failSeq {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `{"id":42}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"NIF inválido"}`)
	})
	defer done()

	p := NewWithClient(c, fakeResolver{})
	if _, err := p.RegisterMandate(ctx, RegisterRequest{}); err == nil ||
		!strings.Contains(err.Error(), "sequência") {
		t.Errorf("= %v", err)
	}
	failSeq = false
	if _, err := p.RegisterMandate(ctx, RegisterRequest{}); err == nil {
		t.Error("um registo recusado devia dar erro")
	}
}

func TestRegisterMandateWithoutActivationDeadline(t *testing.T) {
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sequences") {
			fmt.Fprint(w, `{"id":42}`)
			return
		}
		fmt.Fprint(w, `{"id":42}`)
	})
	defer done()

	p := NewWithClient(c, fakeResolver{})
	p.ActivationTTL = 0
	m, err := p.RegisterMandate(context.Background(), RegisterRequest{DebtorName: "Ana"})
	if err != nil {
		t.Fatal(err)
	}
	if m.ExpiresAt != nil {
		t.Errorf("prazo = %v, queria nenhum", m.ExpiresAt)
	}
}

func TestActivationInstructionsWithoutMandate(t *testing.T) {
	p := New(Config{APIKey: "chave", EntityID: "AO1234567890"}, fakeResolver{})
	entity, code := p.ActivationInstructions(nil)
	if entity != "AO1234567890" || code != "" {
		t.Errorf("= %q, %q", entity, code)
	}
}

func TestCancelMandate(t *testing.T) {
	var path string
	var body CancelMandateRequest
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"mandate_id":42,"reason":"CUST"}`)
	})
	defer done()

	p := NewWithClient(c, fakeResolver{})
	ctx := context.Background()
	m := &mandate.Mandate{ExternalID: 42}

	if err := p.CancelMandate(ctx, m, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "/mandates/42/cancelations") {
		t.Errorf("caminho = %q", path)
	}
	if body.Reason != CancelReasonCustomerRequest {
		t.Errorf("motivo por omissão = %q", body.Reason)
	}

	// Sem mandato, ou sem número do gateway, não há nada a cancelar.
	path = ""
	if err := p.CancelMandate(ctx, nil, "CTCA"); err != nil {
		t.Fatal(err)
	}
	if err := p.CancelMandate(ctx, &mandate.Mandate{}, "CTCA"); err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Error("não devia ter chamado o gateway")
	}
	if err := New(Config{}, nil).CancelMandate(ctx, m, ""); !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("= %v", err)
	}
}

func TestEventsAndWaitHelpers(t *testing.T) {
	var offset int
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		offset++
		if offset == 1 {
			fmt.Fprint(w, `[{"id":"e1","offset":0,"type":"PresentPaymentSubmitted","data":{"mandate_id":7}}]`)
			return
		}
		fmt.Fprint(w, `[{"id":"e2","offset":1,"type":"MandateActivated","data":{"mandate_id":7}},
			{"id":"e3","offset":2,"type":"PaymentCollected","data":{"mandate_id":7,"payment_id":3}}]`)
	})
	defer done()

	ctx := context.Background()
	events, err := c.Events(ctx, 0, 0) // contagem a zero cai no valor por omissão
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("eventos = %d", len(events))
	}

	ev, err := c.WaitForMandateEvent(ctx, 7, []string{EventMandateActivated}, 0, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != EventMandateActivated {
		t.Errorf("= %s", ev.Type)
	}

	ev, err = c.WaitForPaymentEvent(ctx, 7, 3, []string{EventPaymentCollected}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Data.PaymentID != 3 {
		t.Errorf("= %+v", ev.Data)
	}
}

func TestWaitStopsOnCancelledContextAndErrors(t *testing.T) {
	// O titular pode demorar dias a activar: quem espera tem de poder desistir.
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	})
	defer done()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.WaitForMandateEvent(ctx, 7, []string{EventMandateActivated}, 0, time.Millisecond); err == nil {
		t.Error("queria o erro do contexto")
	}

	// Já cancelado à entrada.
	done2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if _, err := c.WaitForMandateEvent(done2, 7, nil, 0, time.Millisecond); err == nil {
		t.Error("queria o erro do contexto")
	}

	// Erro do gateway a meio da espera.
	bad, closeBad := client(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer closeBad()
	if _, err := bad.WaitForMandateEvent(context.Background(), 7, nil, 0, time.Millisecond); err == nil {
		t.Error("queria o erro do gateway")
	}
	if _, err := bad.Events(context.Background(), 0, 10); err == nil {
		t.Error("queria o erro do gateway")
	}
}

func TestAuthorizationFormAndImageProcessing(t *testing.T) {
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/authorization_forms"):
			_, _ = w.Write([]byte("%PDF-1.4 formulário"))
		case strings.HasSuffix(r.URL.Path, "/image_processings"):
			fmt.Fprint(w, `{"id":"job-1","status":1}`)
		case strings.Contains(r.URL.Path, "/image_processings/"):
			fmt.Fprint(w, `{"id":"job-1","status":0,"image_id":"img-1"}`)
		case strings.Contains(r.URL.Path, "/images/"):
			_, _ = w.Write([]byte("jpeg"))
		default:
			t.Errorf("caminho inesperado: %s", r.URL.Path)
		}
	})
	defer done()
	ctx := context.Background()

	pdf, err := c.AuthorizationForm(ctx, AuthorizationFormRequest{ID: 42, DebitorName: "Ana"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(pdf), "%PDF") {
		t.Errorf("formulário = %q", pdf)
	}

	job, err := c.SubmitImageProcessing(ctx, []byte("imagem digitalizada"))
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != 1 {
		t.Errorf("estado = %d, queria em fila", job.Status)
	}

	st, err := c.ImageProcessingStatus(ctx, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != 0 || st.ImageID == nil || *st.ImageID != "img-1" {
		t.Errorf("estado = %+v", st)
	}

	img, err := c.ProcessedImage(ctx, "img-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(img) != "jpeg" {
		t.Errorf("imagem = %q", img)
	}
}

func TestImageFlowErrors(t *testing.T) {
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer done()
	ctx := context.Background()

	if _, err := c.AuthorizationForm(ctx, AuthorizationFormRequest{}); err == nil {
		t.Error("queria erro no formulário")
	}
	if _, err := c.SubmitImageProcessing(ctx, []byte("x")); err == nil {
		t.Error("queria erro no envio da imagem")
	}
	if _, err := c.ImageProcessingStatus(ctx, "job-1"); err == nil {
		t.Error("queria erro no estado")
	}
	if _, err := c.ProcessedImage(ctx, "img-1"); err == nil {
		t.Error("queria erro na imagem")
	}
}

func TestAuthorizationFormUsesConfiguredIBAN(t *testing.T) {
	var body AuthorizationFormRequest
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte("%PDF"))
	})
	defer done()

	if _, err := c.AuthorizationForm(context.Background(), AuthorizationFormRequest{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if body.CreditIBAN != "AO06000000000000000000000" {
		t.Errorf("IBAN = %q", body.CreditIBAN)
	}
}

func TestSequenceErrors(t *testing.T) {
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer done()
	ctx := context.Background()
	if _, err := c.NextMandateID(ctx); err == nil {
		t.Error("queria erro")
	}
	if _, err := c.NextPaymentID(ctx, 7); err == nil {
		t.Error("queria erro")
	}
}

func TestClientCancelAndReverseErrors(t *testing.T) {
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	})
	defer done()
	ctx := context.Background()
	if _, err := c.CancelPayment(ctx, 7, 3, CancelPaymentRequest{}); err == nil {
		t.Error("queria erro")
	}
	if _, err := c.ReversePayment(ctx, 7, 3, ReversePaymentRequest{}); err == nil {
		t.Error("queria erro")
	}
	if _, err := c.CancelMandate(ctx, 7, CancelMandateRequest{}); err == nil {
		t.Error("queria erro")
	}
	if _, err := c.RegisterCAPMandate(ctx, CAPMandateRequest{}); err == nil {
		t.Error("queria erro")
	}
	if _, err := c.RegisterSAPMandate(ctx, SAPMandateRequest{}); err == nil {
		t.Error("queria erro")
	}
	if _, err := c.PresentPayment(ctx, 7, PresentPaymentRequest{}); err == nil {
		t.Error("queria erro")
	}
}

func TestTranslateEventDetails(t *testing.T) {
	p := New(Config{APIKey: "chave", EntityID: "AO1"}, fakeResolver{})

	ev := p.TranslateEvent(Event{
		ID: "e1", Type: EventMandateCanceled,
		Data: EventData{
			MandateID: 7, ContractID: "contrato-1", Reason: "CUST",
			Datetime: "2026-04-05T10:00:00Z", Amount: "não é número",
		},
	})
	if ev.Type != payment.EventMandateCancelled {
		t.Errorf("tipo = %s", ev.Type)
	}
	if ev.Reference != "contrato-1" || ev.MandateRef != "7" {
		t.Errorf("referências = %q, %q", ev.Reference, ev.MandateRef)
	}
	if ev.OccurredAt == nil {
		t.Error("a data devia ter sido lida")
	}
	// Um valor ilegível não pode partir a tradução do evento.
	if ev.Amount != nil {
		t.Errorf("valor = %v", ev.Amount)
	}

	// Sem data legível.
	ev = p.TranslateEvent(Event{Type: EventPaymentCanceled, Data: EventData{Datetime: "ontem"}})
	if ev.OccurredAt != nil {
		t.Errorf("data = %v", ev.OccurredAt)
	}
	if ev.Type != payment.EventChargeCancelled {
		t.Errorf("tipo = %s", ev.Type)
	}
	// Revogado também é cancelamento.
	if got := p.TranslateEvent(Event{Type: EventPaymentRevoked}); got.Type != payment.EventChargeCancelled {
		t.Errorf("tipo = %s", got.Type)
	}
	// Recusa de registo é recusa de mandato.
	if got := p.TranslateEvent(Event{Type: EventRegisterMandateRejected}); got.Type != payment.EventMandateRejected {
		t.Errorf("tipo = %s", got.Type)
	}
}

func TestParseDateTimeVariants(t *testing.T) {
	for _, in := range []string{
		"2026-04-05T10:00:00Z", "2026-04-05T10:00:00", "2026-04-05 10:00:00", "2026-04-05",
	} {
		if _, ok := parseDateTime(in); !ok {
			t.Errorf("não leu %q", in)
		}
	}
	for _, in := range []string{"", "   ", "ontem"} {
		if _, ok := parseDateTime(in); ok {
			t.Errorf("não devia ler %q", in)
		}
	}
}

func TestFormatDateNil(t *testing.T) {
	if got := formatDate(nil); got != "" {
		t.Errorf("= %q", got)
	}
}

func TestParseTransactionIDNonNumeric(t *testing.T) {
	if _, _, ok := parseTransactionID("PAY-7-x"); ok {
		t.Error("a segunda parte tem de ser um número")
	}
}

// --- consumidor de eventos ------------------------------------------------------

type memOffsets struct {
	value    int
	onGet    error
	onSet    error
	setCalls int
}

func (m *memOffsets) Offset(context.Context) (int, error) {
	if m.onGet != nil {
		return 0, m.onGet
	}
	return m.value, nil
}

func (m *memOffsets) SetOffset(_ context.Context, offset int) error {
	m.setCalls++
	if m.onSet != nil {
		return m.onSet
	}
	m.value = offset
	return nil
}

func TestConsumerWithoutDependencies(t *testing.T) {
	(&Consumer{Log: quiet()}).Run(context.Background())
	(&Consumer{Log: quiet(), Provider: New(Config{}, nil)}).Run(context.Background())
	(&Consumer{Log: quiet(), Provider: New(Config{}, nil), Offsets: &memOffsets{}}).Run(context.Background())
}

func TestConsumerReadsUntilTheEnd(t *testing.T) {
	page := 0
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		page++
		if page == 1 {
			fmt.Fprint(w, `[{"id":"e1","offset":0,"type":"MandateActivated","data":{"mandate_id":7}},
				{"id":"e2","offset":1,"type":"PaymentCollected","data":{"mandate_id":7,"payment_id":3}}]`)
			return
		}
		fmt.Fprint(w, `[]`)
	})
	defer done()

	offsets := &memOffsets{}
	var seen []payment.EventType
	consumer := &Consumer{
		Provider: NewWithClient(c, fakeResolver{}), Offsets: offsets, Log: quiet(),
		BatchSize: 2,
		Handle: func(_ context.Context, ev *payment.Event, _ Event) error {
			seen = append(seen, ev.Type)
			return nil
		},
	}
	consumer.Run(context.Background())

	if len(seen) != 2 {
		t.Errorf("eventos = %v", seen)
	}
	// A posição guardada é a do último mais um: é o que faz uma paragem não
	// perder nem repetir eventos.
	if offsets.value != 2 {
		t.Errorf("posição = %d, queria 2", offsets.value)
	}
}

func TestConsumerStopsAtTheEndOfAShortPage(t *testing.T) {
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"id":"e1","offset":0,"type":"MandateActivated","data":{"mandate_id":7}}]`)
	})
	defer done()

	offsets := &memOffsets{}
	handled := 0
	consumer := &Consumer{
		Provider: NewWithClient(c, fakeResolver{}), Offsets: offsets, Log: quiet(),
		BatchSize: 10, // a página veio mais curta do que o lote: é o fim do fluxo
		Handle:    func(context.Context, *payment.Event, Event) error { handled++; return nil },
	}
	consumer.Run(context.Background())
	if handled != 1 {
		t.Errorf("tratados = %d", handled)
	}
}

func TestConsumerErrors(t *testing.T) {
	boom := errors.New("base de dados")

	// Falha a ler a posição.
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `[]`) })
	defer done()
	consumer := &Consumer{
		Provider: NewWithClient(c, fakeResolver{}), Offsets: &memOffsets{onGet: boom}, Log: quiet(),
		Handle: func(context.Context, *payment.Event, Event) error { return nil },
	}
	consumer.Run(context.Background())

	// Falha a ler o fluxo.
	bad, closeBad := client(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer closeBad()
	consumer.Provider = NewWithClient(bad, fakeResolver{})
	consumer.Offsets = &memOffsets{}
	consumer.Run(context.Background())

	// Falha a guardar a posição.
	consumer.Provider = NewWithClient(c, fakeResolver{})
	consumer.Offsets = &memOffsets{onSet: boom}
	consumer.Run(context.Background())
}

func TestConsumerStopsAtTheFirstUnhandledEvent(t *testing.T) {
	// Avançar por cima de um evento que não foi tratado é perdê-lo para
	// sempre: a posição não pode passar dele.
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"id":"e1","offset":0,"type":"MandateActivated","data":{"mandate_id":7}},
			{"id":"e2","offset":1,"type":"PaymentCollected","data":{"mandate_id":7}}]`)
	})
	defer done()

	offsets := &memOffsets{}
	handled := 0
	consumer := &Consumer{
		Provider: NewWithClient(c, fakeResolver{}), Offsets: offsets, Log: quiet(),
		Handle: func(context.Context, *payment.Event, Event) error {
			handled++
			return errors.New("consumidor em baixo")
		},
	}
	consumer.Run(context.Background())

	if handled != 1 {
		t.Errorf("tratados = %d, queria parar no primeiro", handled)
	}
	if offsets.value != 0 {
		t.Errorf("posição = %d, queria não avançar", offsets.value)
	}
}

func TestConsumerSaveFailureAfterHandleFailure(t *testing.T) {
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"id":"e1","offset":0,"type":"MandateActivated","data":{"mandate_id":7}}]`)
	})
	defer done()

	consumer := &Consumer{
		Provider: NewWithClient(c, fakeResolver{}),
		Offsets:  &memOffsets{onSet: errors.New("base de dados")}, Log: quiet(),
		Handle: func(context.Context, *payment.Event, Event) error { return errors.New("falhou") },
	}
	consumer.Run(context.Background()) // não pode entrar em pânico
}

func TestConsumerStopsOnCancelledContext(t *testing.T) {
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"id":"e1","offset":0,"type":"MandateActivated","data":{"mandate_id":7}}]`)
	})
	defer done()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handled := 0
	consumer := &Consumer{
		Provider: NewWithClient(c, fakeResolver{}), Offsets: &memOffsets{}, Log: quiet(),
		Handle: func(context.Context, *payment.Event, Event) error { handled++; return nil },
	}
	consumer.Run(ctx)
	if handled != 0 {
		t.Error("com o contexto cancelado não se trata nada")
	}
}

func TestConsumerDefaultLogger(t *testing.T) {
	if (&Consumer{}).log() == nil {
		t.Error("o registador por omissão não pode ser nil")
	}
}

func TestChargeUsesPeriodStartAsCollectionDate(t *testing.T) {
	// O ciclo que a cobrança paga é que manda: é essa a data em que o dinheiro
	// sai da conta do titular.
	var body PresentPaymentRequest
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sequences") {
			fmt.Fprint(w, `{"id":3}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"id":3,"mandate_id":7,"transaction_id":"PAY-7-3"}`)
	})
	defer done()

	start := time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC)
	p := NewWithClient(c, fakeResolver{externalID: 7, active: true})
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodDirectDebit,
		MandateID: "m1", PeriodStart: &start,
	}); err != nil {
		t.Fatal(err)
	}
	if body.CollectionDate != "2026-05-01" {
		t.Errorf("data de liquidação = %q", body.CollectionDate)
	}
}

func TestRegisterMandateWithExplicitCreditIBAN(t *testing.T) {
	// Quem tem várias contas a receber indica a conta no pedido; a da
	// configuração é só o valor por omissão.
	var body map[string]any
	c, done := client(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sequences") {
			fmt.Fprint(w, `{"id":42}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"id":42}`)
	})
	defer done()

	if _, err := c.RegisterSAPMandate(context.Background(), SAPMandateRequest{
		ID: 42, CreditIBAN: "AO06999999999999999999999", DebitorName: "Ana",
	}); err != nil {
		t.Fatal(err)
	}
	if body["credit_iban"] != "AO06999999999999999999999" {
		t.Errorf("IBAN = %v", body["credit_iban"])
	}
}

func TestWaitStopsWhenContextEndsBetweenPages(t *testing.T) {
	// O cancelamento entre duas leituras do fluxo: o titular pode demorar dias,
	// e o serviço tem de poder encerrar a meio da espera.
	//
	// O prazo é dado ao contexto e não cancelado dentro do handler: cancelar a
	// meio do pedido faz a leitura falhar por outra via, e o teste passava a
	// medir outra coisa em metade das vezes.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"id":"e1","offset":0,"type":"PresentPaymentSubmitted","data":{"mandate_id":9}}]`)
	})
	defer done()

	if _, err := c.WaitForMandateEvent(ctx, 7, []string{EventMandateActivated}, 0, time.Hour); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("= %v", err)
	}
}

func TestConsumerSaveFailureAfterASuccessfulPage(t *testing.T) {
	// A página foi tratada mas a posição não gravou: a passagem pára, e a
	// seguinte volta a ler os mesmos eventos. É o preço de não os perder.
	c, done := client(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"id":"e1","offset":0,"type":"MandateActivated","data":{"mandate_id":7}}]`)
	})
	defer done()

	handled := 0
	consumer := &Consumer{
		Provider: NewWithClient(c, fakeResolver{}),
		Offsets:  &memOffsets{onSet: errors.New("base de dados")}, Log: quiet(),
		BatchSize: 1,
		Handle:    func(context.Context, *payment.Event, Event) error { handled++; return nil },
	}
	consumer.Run(context.Background())
	if handled != 1 {
		t.Errorf("tratados = %d, queria parar depois da primeira página", handled)
	}
}
