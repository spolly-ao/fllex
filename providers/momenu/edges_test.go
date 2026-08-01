package momenu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestProviderAccessors(t *testing.T) {
	c := NewClient(Config{APIKey: "chave"})
	p := NewWithClient(c)
	if p.Client() != c {
		t.Error("Client() devia devolver o cliente recebido")
	}
	ms := p.Methods()
	if len(ms) != 3 {
		t.Errorf("métodos = %v, queria os três", ms)
	}
	// A ordem é a que a página de pagamento mostra ao cliente.
	if ms[0] != payment.MethodMCX {
		t.Errorf("primeiro método = %s, queria o Multicaixa Express", ms[0])
	}
}

func TestQAHeaderAndCustomTimeout(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("x-env-qa")
		fmt.Fprint(w, `{"success":true,"transactionId":"tx-1"}`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, QA: true, Timeout: 3 * time.Second})
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodMCX,
		Customer: payment.Customer{Phone: "921234567"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "true" {
		t.Errorf("cabeçalho de ambiente de teste = %q", got)
	}
}

func TestChargeEKwanza(t *testing.T) {
	var body EKwanzaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"success":true,"merchantTransactionId":"tx-1","code":"EKZ123",
			"qrCode":"data:image/png;base64,AAA","paymentTimeout":180}`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(5900, money.AOA), Method: payment.MethodEKwanza,
		Description: "Plano", Customer: payment.Customer{Phone: "921234567", TaxID: "5417"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != payment.KindCode || res.Status != payment.StatusPending {
		t.Errorf("resultado = %+v", res)
	}
	if res.Code != "EKZ123" || res.QRCode == "" {
		t.Errorf("código e QR = %q, %q", res.Code, res.QRCode)
	}
	// O estado consulta-se pelo código, não pelo identificador da transacção.
	if res.StatusRef != "EKZ123" {
		t.Errorf("referência de consulta = %q", res.StatusRef)
	}
	if res.ExpiresAt == nil {
		t.Error("o prazo devia vir do tempo limite da resposta")
	}
	// Ao contrário do Multicaixa Express, um telemóvel mal escrito não bloqueia
	// o eKwanza: a confirmação é por QR.
	if body.PaymentInfo.PhoneNumber != "244921234567" {
		t.Errorf("telemóvel = %q", body.PaymentInfo.PhoneNumber)
	}
}

func TestEKwanzaExpiryFromDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"merchantTransactionId":"tx-1","code":"EKZ1",
			"expirationDate":"2026-04-05T10:00:00Z"}`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodEKwanza,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExpiresAt == nil || res.ExpiresAt.Year() != 2026 {
		t.Errorf("prazo = %v", res.ExpiresAt)
	}
}

func TestEKwanzaRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":false}`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodEKwanza,
	})
	if err == nil {
		t.Error("uma resposta sem sucesso devia dar erro")
	}
}

func TestEKwanzaStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("merchantTransactionId"); got != "tx-1" {
			t.Errorf("identificador da ordem = %q", got)
		}
		fmt.Fprint(w, `{"success":true,"status":"paid","invoiceUrl":"https://f/1"}`)
	}))
	defer srv.Close()

	c := NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
	st, err := c.EKwanzaStatus(context.Background(), "EKZ1", "tx-1")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "paid" || st.InvoiceURL == "" {
		t.Errorf("estado = %+v", st)
	}

	// E o caminho de erro.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"código inválido"}`)
	}))
	defer bad.Close()
	c = NewClient(Config{APIKey: "chave", BaseURL: bad.URL, Timeout: 5 * time.Second})
	if _, err := c.EKwanzaStatus(context.Background(), "EKZ1", ""); err == nil {
		t.Error("queria erro")
	}
}

func TestMCXAndReferenceRefusals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":false,"message":"saldo insuficiente"}`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
	ctx := context.Background()

	_, err := p.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodMCX,
		Customer: payment.Customer{Phone: "921234567"},
	})
	// A mensagem do gateway tem de chegar a quem faz suporte.
	if err == nil || !contains(err.Error(), "saldo insuficiente") {
		t.Errorf("erro = %v", err)
	}

	_, err = p.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodReference,
	})
	if err == nil {
		t.Error("uma referência recusada devia dar erro")
	}
}

func TestChargeRejectsNonPositiveAndUnconfigured(t *testing.T) {
	ctx := context.Background()
	p := New(Config{APIKey: "chave"})
	if _, err := p.Charge(ctx, payment.ChargeRequest{
		Amount: money.Zero(money.AOA), Method: payment.MethodReference,
	}); !errors.Is(err, payment.ErrAmountNotPositive) {
		t.Errorf("valor zero = %v", err)
	}

	unconfigured := New(Config{})
	if _, err := unconfigured.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodReference,
	}); !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("sem chave = %v", err)
	}
	if _, err := unconfigured.VerifyCharge(ctx, "op-1", ""); !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("consulta sem chave = %v", err)
	}
}

func TestVerifyChargeWithEmptyReference(t *testing.T) {
	p := New(Config{APIKey: "chave"})
	st, err := p.VerifyCharge(context.Background(), "  ", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Paid {
		t.Error("sem referência não há nada a confirmar")
	}
}

func TestVerifyChargeErrorAndUnknownStatus(t *testing.T) {
	// Um estado que o gateway devolva e nós não conheçamos fica pendente, que
	// é o seguro: dar por pago o que não se percebeu é o erro caro.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"payment":{"status":"em análise"}}`)
	}))
	defer srv.Close()
	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
	st, err := p.VerifyCharge(context.Background(), "op-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != payment.StatusPending || st.Paid {
		t.Errorf("estado desconhecido = %+v", st)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	p = New(Config{APIKey: "chave", BaseURL: bad.URL, Timeout: 5 * time.Second})
	if _, err := p.VerifyCharge(context.Background(), "op-1", ""); err == nil {
		t.Error("queria erro")
	}
}

func TestProductsFromExplicitItems(t *testing.T) {
	var body ReferenceRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"success":true,"transactionId":"tx-1","operationId":"op-1"}`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(1500, money.AOA), Method: payment.MethodReference,
		Items: []payment.LineItem{
			{Description: "Plano", Quantity: 1, UnitPrice: money.FromMajor(1000, money.AOA), TaxRate: 14},
			{Description: "Extra", UnitPrice: money.FromMajor(500, money.AOA)}, // sem quantidade
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(body.Products) != 2 {
		t.Fatalf("linhas de factura = %d", len(body.Products))
	}
	if body.Products[0].IVA != 14 {
		t.Errorf("taxa = %d", body.Products[0].IVA)
	}
	// Quantidade em falta lê-se como uma, senão a factura fiscal sai errada.
	if body.Products[1].ProductQuantity != 1 {
		t.Errorf("quantidade = %d", body.Products[1].ProductQuantity)
	}
}

func TestProductsFallBackToDefaultDescription(t *testing.T) {
	var body ReferenceRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		fmt.Fprint(w, `{"success":true,"transactionId":"tx-1"}`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
	p.DefaultDescription = "Serviço"
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodReference,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Sem linha, a factura sairia sem descrição do que foi vendido.
	if len(body.Products) != 1 || body.Products[0].ProductName != "Serviço" {
		t.Errorf("linhas = %+v", body.Products)
	}
}

func TestParseDateVariants(t *testing.T) {
	for _, in := range []string{
		"2026-04-05T10:00:00Z", "2026-04-05T10:00:00.000Z",
		"2026-04-05 10:00:00", "2026-04-05",
	} {
		if _, ok := parseDate(in); !ok {
			t.Errorf("não leu %q", in)
		}
	}
	for _, in := range []string{"", "   ", "cinco de Abril"} {
		if _, ok := parseDate(in); ok {
			t.Errorf("não devia ler %q", in)
		}
	}
}

func TestWebhookIgnoresUninterestingEvents(t *testing.T) {
	p := New(Config{APIKey: "chave"})
	ev, err := p.ParseWebhook([]byte(`{"event":"invoice.created","merchantTransactionId":"tx-1"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if !ev.Ignorable() {
		t.Errorf("tipo = %s, queria ignorável", ev.Type)
	}
	// Confirmação com estado que não é o de sucesso também se ignora.
	ev, _ = p.ParseWebhook([]byte(`{"event":"payment.confirmed","operationStatus":"0"}`), "")
	if !ev.Ignorable() {
		t.Errorf("tipo = %s", ev.Type)
	}
}

func TestListInvoicesClampsPaging(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.RequestURI()
		fmt.Fprint(w, `{"success":true,"invoices":[]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
	// O gateway limita a página a 50: pedir mais devolve um erro em vez da
	// lista, por isso o pedido é aparado aqui.
	if _, err := c.ListInvoices(context.Background(), 500, -3); err != nil {
		t.Fatal(err)
	}
	if path != "/api/invoices?limit=50&offset=0" {
		t.Errorf("pedido = %q", path)
	}
	if _, err := c.ListInvoices(context.Background(), 0, 0); err != nil {
		t.Fatal(err)
	}
	if path != "/api/invoices?limit=50&offset=0" {
		t.Errorf("pedido = %q", path)
	}
}

func TestListAndGetInvoiceErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
	ctx := context.Background()
	if _, err := c.ListInvoices(ctx, 10, 0); err == nil {
		t.Error("queria erro na listagem")
	}
	if _, err := c.GetInvoice(ctx, "inv-1"); err == nil {
		t.Error("queria erro no detalhe")
	}
}

func TestReconcilerWithoutDependencies(t *testing.T) {
	r := &Reconciler{Log: quiet()}
	r.Run(context.Background()) // não pode entrar em pânico
}

func TestReconcilerHandlesPendingError(t *testing.T) {
	r := &Reconciler{
		Client:  NewClient(Config{APIKey: "chave"}),
		Log:     quiet(),
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) { return nil, errors.New("base de dados") },
		Confirm: func(context.Context, Match) error { return nil },
	}
	r.Run(context.Background())
}

func TestReconcilerWithNoCandidates(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		fmt.Fprint(w, `{"success":true,"invoices":[]}`)
	}))
	defer srv.Close()

	r := &Reconciler{
		Client:  NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		Log:     quiet(),
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) { return nil, nil },
		Confirm: func(context.Context, Match) error { return nil },
	}
	r.Run(context.Background())
	// Sem candidatos não se gasta uma chamada ao gateway.
	if called {
		t.Error("não devia ter listado facturas sem candidatos")
	}
}

func TestReconcilerHandlesInvoiceListError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := &Reconciler{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		Log:    quiet(),
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) {
			return []Attempt{{Reference: "e1", Amount: money.FromMajor(100, money.AOA), StartedAt: time.Now()}}, nil
		},
		Confirm: func(context.Context, Match) error { return nil },
	}
	r.Run(context.Background())
}

func TestReconcilerEmptyInvoiceList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"invoices":[]}`)
	}))
	defer srv.Close()

	confirmed := 0
	r := &Reconciler{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		Log:    quiet(),
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) {
			return []Attempt{{Reference: "e1", Amount: money.FromMajor(100, money.AOA), StartedAt: time.Now()}}, nil
		},
		Confirm: func(context.Context, Match) error { confirmed++; return nil },
	}
	r.Run(context.Background())
	if confirmed != 0 {
		t.Error("sem facturas não há nada a correlacionar")
	}
}

func TestReconcilerSkipsInvoicesWithUnreadableDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/invoices" {
			fmt.Fprint(w, `{"success":true,"invoices":[{"invoiceId":"inv-1","total":100,"createdAt":"não é data"}]}`)
			return
		}
		t.Error("não devia ter pedido o detalhe de uma factura sem data legível")
	}))
	defer srv.Close()

	confirmed := 0
	r := &Reconciler{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		Log:    quiet(),
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) {
			return []Attempt{{Reference: "e1", Phone: "921234567",
				Amount: money.FromMajor(100, money.AOA), StartedAt: time.Now()}}, nil
		},
		Confirm: func(context.Context, Match) error { confirmed++; return nil },
	}
	r.Run(context.Background())
	if confirmed != 0 {
		t.Error("uma factura sem data não se correlaciona")
	}
}

func TestReconcilerHandlesDetailErrorAndFailure(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	detailFails := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/invoices" {
			fmt.Fprintf(w, `{"success":true,"invoices":[{"invoiceId":"inv-1","total":100,"createdAt":%q}]}`,
				started.Format(time.RFC3339))
			return
		}
		if detailFails {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"success":true,"invoice":{"invoiceId":"inv-1","total":100,
			"customer":{"phone":"244921234567"}}}`)
	}))
	defer srv.Close()

	attempt := Attempt{Reference: "e1", Phone: "921234567",
		Amount: money.FromMajor(100, money.AOA), StartedAt: started}
	pending := func(context.Context, time.Duration, int) ([]Attempt, error) {
		return []Attempt{attempt}, nil
	}

	confirmed := 0
	r := &Reconciler{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		Log:    quiet(), Pending: pending,
		Confirm: func(context.Context, Match) error { confirmed++; return nil },
	}
	r.Run(context.Background())
	if confirmed != 0 {
		t.Error("sem o detalhe não se confirma")
	}

	// Agora o detalhe passa, mas a confirmação falha: repete-se na próxima
	// passagem em vez de se dar o pagamento por perdido.
	detailFails = false
	r.Confirm = func(context.Context, Match) error { return errors.New("base de dados") }
	r.Run(context.Background())
}

func TestReconcilerIgnoresUnsuccessfulDetail(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/invoices" {
			fmt.Fprintf(w, `{"success":true,"invoices":[{"invoiceId":"inv-1","total":100,"createdAt":%q}]}`,
				started.Format(time.RFC3339))
			return
		}
		fmt.Fprint(w, `{"success":false}`)
	}))
	defer srv.Close()

	confirmed := 0
	r := &Reconciler{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		Log:    quiet(),
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) {
			return []Attempt{{Reference: "e1", Phone: "921234567",
				Amount: money.FromMajor(100, money.AOA), StartedAt: started}}, nil
		},
		Confirm: func(context.Context, Match) error { confirmed++; return nil },
	}
	r.Run(context.Background())
	if confirmed != 0 {
		t.Error("um detalhe sem sucesso não se correlaciona")
	}
}

func TestReconcilerAbandonWithoutCallbackAndWithError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"invoices":[{"invoiceId":"inv-1","total":100,"createdAt":"2020-01-01T00:00:00Z"}]}`)
	}))
	defer srv.Close()

	old := Attempt{Reference: "e1", Phone: "921234567",
		Amount: money.FromMajor(100, money.AOA), StartedAt: time.Now().Add(-48 * time.Hour)}

	// Sem função de abandono, apenas se regista e segue.
	r := &Reconciler{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		Log:    quiet(), MaxAge: time.Hour,
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) { return []Attempt{old}, nil },
		Confirm: func(context.Context, Match) error { return nil },
	}
	r.Run(context.Background())

	// Com função de abandono que falha, também não pode partir a passagem.
	r.Abandon = func(context.Context, Attempt) error { return errors.New("base de dados") }
	r.Run(context.Background())
}

func TestReconcilerDefaults(t *testing.T) {
	r := &Reconciler{}
	if got := r.minAge(); got != 4*time.Minute {
		t.Errorf("idade mínima = %v", got)
	}
	if got := r.maxAge(); got != 24*time.Hour {
		t.Errorf("idade máxima = %v", got)
	}
	if got := r.window(); got != 15*time.Minute {
		t.Errorf("janela = %v", got)
	}
	if got := r.batchSize(); got != 10 {
		t.Errorf("lote = %d", got)
	}
	if got := r.pageSize(); got != 50 {
		t.Errorf("página = %d", got)
	}
	if r.log() == nil {
		t.Error("o registador por omissão não pode ser nil")
	}
}

func TestHttpxFirstEmpty(t *testing.T) {
	if got := httpxFirst("", "  "); got != "" {
		t.Errorf("= %q", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestClientNetworkErrors(t *testing.T) {
	// Sem servidor do outro lado, os três pedidos de cobrança falham na rede.
	c := NewClient(Config{APIKey: "chave", BaseURL: "http://127.0.0.1:1", Timeout: time.Second})
	ctx := context.Background()

	if _, err := c.InitMCX(ctx, MCXRequest{
		PaymentInfo: PaymentInfo{Amount: 100, PhoneNumber: "921234567"},
	}); err == nil {
		t.Error("queria erro de rede no Multicaixa Express")
	}
	if _, err := c.InitEKwanza(ctx, EKwanzaRequest{PaymentInfo: PaymentInfo{Amount: 100}}); err == nil {
		t.Error("queria erro de rede no eKwanza")
	}
	if _, err := c.CreateReference(ctx, ReferenceRequest{PaymentInfo: PaymentInfo{Amount: 100}}); err == nil {
		t.Error("queria erro de rede na referência")
	}
}

func TestReconcilerExplicitTuning(t *testing.T) {
	r := &Reconciler{
		MinAge: time.Minute, MaxAge: time.Hour,
		Window: 2 * time.Minute, BatchSize: 3, PageSize: 20,
	}
	if got := r.minAge(); got != time.Minute {
		t.Errorf("idade mínima = %v", got)
	}
	if got := r.window(); got != 2*time.Minute {
		t.Errorf("janela = %v", got)
	}
	if got := r.batchSize(); got != 3 {
		t.Errorf("lote = %d", got)
	}
	if got := r.pageSize(); got != 20 {
		t.Errorf("página = %d", got)
	}
}

func TestReconcilerStopsOnCancelledContext(t *testing.T) {
	started := time.Now().Add(-10 * time.Minute)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"success":true,"invoices":[{"invoiceId":"inv-1","total":100,"createdAt":%q}]}`,
			started.Format(time.RFC3339))
	}))
	defer srv.Close()

	// O cancelamento acontece a meio do lote: a primeira tentativa é tratada e
	// a segunda já não chega a ser. É o que impede uma passagem longa de
	// continuar a chamar o gateway depois de o serviço estar a encerrar.
	ctx, cancel := context.WithCancel(context.Background())
	old := time.Now().Add(-48 * time.Hour)

	abandoned := 0
	r := &Reconciler{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second}),
		Log:    quiet(), MaxAge: time.Hour,
		Pending: func(context.Context, time.Duration, int) ([]Attempt, error) {
			return []Attempt{
				{Reference: "e1", Phone: "921234567", Amount: money.FromMajor(100, money.AOA), StartedAt: old},
				{Reference: "e2", Phone: "921234567", Amount: money.FromMajor(100, money.AOA), StartedAt: old},
			}, nil
		},
		Confirm: func(context.Context, Match) error { return nil },
		Abandon: func(context.Context, Attempt) error { abandoned++; cancel(); return nil },
	}
	r.Run(ctx)
	if abandoned != 1 {
		t.Errorf("abandonadas = %d, queria parar depois da primeira", abandoned)
	}
}
