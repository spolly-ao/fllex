package proxypay

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

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestDefaultsAndAccessors(t *testing.T) {
	c := NewClient(Config{APIKey: "chave"}) // sem BaseURL nem Accept
	if c.http.BaseURL != DefaultBaseURL {
		t.Errorf("base = %q, queria a de produção", c.http.BaseURL)
	}
	if got := c.http.Header.Get("Accept"); got != DefaultAccept {
		t.Errorf("versão da API = %q, queria a por omissão", got)
	}
	p := NewWithClient(c)
	if p.Client() != c {
		t.Error("Client() devia devolver o cliente recebido")
	}
	if p.Name() != "proxypay" {
		t.Errorf("nome = %q", p.Name())
	}
	if ms := p.Methods(); len(ms) != 1 || ms[0] != payment.MethodReference {
		t.Errorf("métodos = %v", ms)
	}
}

func TestChargeGuards(t *testing.T) {
	ctx := context.Background()
	unconfigured := New(Config{})
	if _, err := unconfigured.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodReference,
	}); !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("sem chave = %v", err)
	}
	if _, err := unconfigured.VerifyCharge(ctx, "ref", ""); !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("consulta sem chave = %v", err)
	}
	if err := unconfigured.CancelCharge(ctx, payment.ChargeRequest{}, payment.ChargeResult{}); !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("revogar sem chave = %v", err)
	}

	p := New(Config{APIKey: "chave"})
	if _, err := p.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodCard,
	}); !errors.Is(err, payment.ErrUnsupportedMethod) {
		t.Errorf("método errado = %v", err)
	}
	if _, err := p.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(100, money.EUR), Method: payment.MethodReference,
	}); !errors.Is(err, payment.ErrUnsupportedCurrency) {
		t.Errorf("moeda errada = %v", err)
	}
	if _, err := p.Charge(ctx, payment.ChargeRequest{
		Amount: money.Zero(money.AOA), Method: payment.MethodReference,
	}); !errors.Is(err, payment.ErrAmountNotPositive) {
		t.Errorf("valor zero = %v", err)
	}
}

func TestGenerateReferenceErrors(t *testing.T) {
	// Resposta vazia: sem número não há referência que se possa dar ao cliente.
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `""`)
	}))
	defer empty.Close()
	c := NewClient(Config{APIKey: "chave", BaseURL: empty.URL})
	if _, err := c.GenerateReference(context.Background()); err == nil {
		t.Error("uma resposta vazia devia dar erro")
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	c = NewClient(Config{APIKey: "errada", BaseURL: bad.URL})
	if _, err := c.GenerateReference(context.Background()); err == nil {
		t.Error("um 401 devia dar erro")
	}
}

func TestCallbackURLIsSentWithTheReference(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `"123456789"`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := New(Config{
		APIKey: "chave", BaseURL: srv.URL, Entity: "01234",
		CallbackURL: "https://exemplo.ao/webhooks/proxypay",
	})
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodReference,
	}); err != nil {
		t.Fatal(err)
	}
	if body["callback_url"] != "https://exemplo.ao/webhooks/proxypay" {
		t.Errorf("callback = %v", body["callback_url"])
	}
}

func TestCancelChargeUsesReferenceOrProviderRef(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/references/"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Entity: "01234"})
	ctx := context.Background()

	if err := p.CancelCharge(ctx, payment.ChargeRequest{}, payment.ChargeResult{Reference: "111"}); err != nil {
		t.Fatal(err)
	}
	// Sem Reference, cai na referência do gateway.
	if err := p.CancelCharge(ctx, payment.ChargeRequest{}, payment.ChargeResult{ProviderRef: "222"}); err != nil {
		t.Fatal(err)
	}
	// Sem nenhuma das duas não há o que revogar, e não se gasta uma chamada.
	if err := p.CancelCharge(ctx, payment.ChargeRequest{}, payment.ChargeResult{}); err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 2 || deleted[0] != "111" || deleted[1] != "222" {
		t.Errorf("apagadas = %v", deleted)
	}
}

func TestVerifyChargeGuardsAndErrors(t *testing.T) {
	p := New(Config{APIKey: "chave"})
	st, err := p.VerifyCharge(context.Background(), "   ", "")
	if err != nil || st.Paid {
		t.Errorf("sem referência = %+v, %v", st, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	p = New(Config{APIKey: "chave", BaseURL: bad.URL})
	if _, err := p.VerifyCharge(context.Background(), "123", ""); err == nil {
		t.Error("queria erro")
	}
}

func TestVerifyChargeWithUnreadableAmountOrDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"id":1,"reference_id":"123","amount":"não é número","datetime":"ontem"}]`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL})
	st, err := p.VerifyCharge(context.Background(), "123", "")
	if err != nil {
		t.Fatal(err)
	}
	// O pagamento está na fila: dá-se por pago mesmo com os campos ilegíveis,
	// porque a presença na fila é que é a confirmação.
	if !st.Paid {
		t.Error("estar na fila é a confirmação")
	}
	if st.Amount != nil {
		t.Errorf("valor ilegível não devia ser preenchido: %v", st.Amount)
	}
	if st.PaidAt != nil {
		t.Errorf("data ilegível não devia ser preenchida: %v", st.PaidAt)
	}
}

func TestPaidAtVariants(t *testing.T) {
	for _, in := range []string{
		"2026-04-05T10:00:00Z", "2026-04-05T10:00:00", "2026-04-05 10:00:00",
	} {
		if _, ok := (ConfirmedPayment{Datetime: in}).PaidAt(); !ok {
			t.Errorf("não leu %q", in)
		}
	}
	if _, ok := (ConfirmedPayment{Datetime: "ontem"}).PaidAt(); ok {
		t.Error("não devia ler")
	}
}

func TestFieldTypes(t *testing.T) {
	p := ConfirmedPayment{CustomFields: map[string]any{
		"texto":  "abc",
		"numero": json.Number("42"),
		"bool":   true,
		"nulo":   nil,
	}}
	if got := p.Field("numero"); got != "42" {
		t.Errorf("json.Number = %q", got)
	}
	if got := p.Field("bool"); got != "true" {
		t.Errorf("booleano = %q", got)
	}
	if got := p.Field("nulo"); got != "" {
		t.Errorf("nulo = %q", got)
	}
	if got := (ConfirmedPayment{}).Field("x"); got != "" {
		t.Errorf("sem campos = %q", got)
	}
}

func TestAcknowledgeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := NewClient(Config{APIKey: "chave", BaseURL: srv.URL})
	if err := c.AcknowledgePayment(context.Background(), 42); err == nil {
		t.Error("queria erro")
	}
}

func TestWebhookParsing(t *testing.T) {
	p := New(Config{APIKey: "chave"})

	ev, err := p.ParseWebhook([]byte(`{"id":42,"reference_id":"123456789","amount":"5900.00",
		"datetime":"2026-04-05T10:00:00Z","custom_fields":{"invoice":"cobranca-7"}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventChargeSucceeded || ev.Reference != "cobranca-7" {
		t.Errorf("evento = %+v", ev)
	}
	if ev.Amount == nil || ev.OccurredAt == nil {
		t.Errorf("valor e data = %v, %v", ev.Amount, ev.OccurredAt)
	}

	// Corpo ilegível e corpo sem referência ignoram-se, em vez de darem erro.
	for _, body := range []string{`isto não é json`, `{"amount":"100"}`} {
		ev, err := p.ParseWebhook([]byte(body), "")
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if !ev.Ignorable() {
			t.Errorf("%s: tipo = %s", body, ev.Type)
		}
	}
}

func TestChargeUsesConfiguredTTL(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `"123456789"`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Entity: "01234"})
	p.TTL = 0 // cai no valor por omissão
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodReference,
	}); err != nil {
		t.Fatal(err)
	}
	end, _ := body["end_datetime"].(string)
	when, err := time.Parse(time.RFC3339, end)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(when); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("validade = %v, queria cerca de 24 horas", d)
	}
}

func TestDrainerWithoutDependencies(t *testing.T) {
	(&Drainer{Log: quiet()}).Run(context.Background())
	(&Drainer{Log: quiet(), Client: NewClient(Config{APIKey: "x"})}).Run(context.Background())
}

func TestDrainerHandlesListError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	applied := 0
	d := &Drainer{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL}), Log: quiet(),
		Apply: func(context.Context, ConfirmedPayment, string, money.Amount) error { applied++; return nil },
	}
	d.Run(context.Background())
	if applied != 0 {
		t.Error("sem lista não há nada a aplicar")
	}
}

func TestDrainerOnEmptyQueue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	applied := 0
	d := &Drainer{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL}), Log: quiet(),
		Apply: func(context.Context, ConfirmedPayment, string, money.Amount) error { applied++; return nil },
	}
	d.Run(context.Background())
	if applied != 0 {
		t.Error("fila vazia")
	}
}

func TestDrainerSkipsUnreadableAmount(t *testing.T) {
	// Um valor que não se lê não pode ser aplicado por adivinhação, e também
	// não pode travar o resto da fila.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `[{"id":1,"reference_id":"a","amount":"não é número"},
				{"id":2,"reference_id":"b","amount":"100.00"}]`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var applied []string
	d := &Drainer{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL}), Log: quiet(),
		Apply: func(_ context.Context, p ConfirmedPayment, _ string, _ money.Amount) error {
			applied = append(applied, p.ReferenceID)
			return nil
		},
	}
	d.Run(context.Background())
	if len(applied) != 1 || applied[0] != "b" {
		t.Errorf("aplicados = %v, queria só o legível", applied)
	}
}

func TestDrainerBatchSizeAndCustomField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `[{"id":1,"reference_id":"a","amount":"100.00","custom_fields":{"pedido":"enc-1"}},
				{"id":2,"reference_id":"b","amount":"100.00"}]`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var refs []string
	d := &Drainer{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL}), Log: quiet(),
		ReferenceField: "pedido", BatchSize: 1,
		Apply: func(_ context.Context, _ ConfirmedPayment, ref string, _ money.Amount) error {
			refs = append(refs, ref)
			return nil
		},
	}
	d.Run(context.Background())
	if len(refs) != 1 || refs[0] != "enc-1" {
		t.Errorf("referências = %v, queria uma só, do campo configurado", refs)
	}
}

func TestDrainerAcknowledgeFailureIsLogged(t *testing.T) {
	// O efeito já aconteceu e a mensagem continua na fila: a próxima passagem
	// volta a aplicá-la, e é por isso que Apply tem de ser idempotente.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `[{"id":1,"reference_id":"a","amount":"100.00"}]`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	applied := 0
	d := &Drainer{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL}), Log: quiet(),
		Apply: func(context.Context, ConfirmedPayment, string, money.Amount) error { applied++; return nil },
	}
	d.Run(context.Background())
	if applied != 1 {
		t.Errorf("aplicados = %d", applied)
	}
}

func TestDrainerStopsOnCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `[{"id":1,"reference_id":"a","amount":"100.00"},
				{"id":2,"reference_id":"b","amount":"100.00"}]`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	applied := 0
	d := &Drainer{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL}), Log: quiet(),
		Apply: func(context.Context, ConfirmedPayment, string, money.Amount) error {
			applied++
			cancel()
			return nil
		},
	}
	d.Run(ctx)
	if applied != 1 {
		t.Errorf("aplicados = %d, queria parar depois do primeiro", applied)
	}
}

func TestDrainerDefaultLogger(t *testing.T) {
	if (&Drainer{}).log() == nil {
		t.Error("o registador por omissão não pode ser nil")
	}
}

func TestChargeFailsWhenReferenceCannotBeGenerated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Entity: "01234"})
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodReference,
	}); err == nil {
		t.Error("sem número não há referência que se possa emitir")
	}
}

func TestMetadataGoesIntoCustomFields(t *testing.T) {
	// Os campos personalizados voltam na confirmação: é por eles que se sabe a
	// que cobrança um pagamento pertence.
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `"123456789"`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Entity: "01234"})
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "cobranca-7",
		Amount:    money.FromMajor(100, money.AOA), Method: payment.MethodReference,
		Metadata: map[string]string{"tenant": "acme", "ciclo": "3"},
	}); err != nil {
		t.Fatal(err)
	}
	fields, _ := body["custom_fields"].(map[string]any)
	if fields["tenant"] != "acme" || fields["ciclo"] != "3" {
		t.Errorf("metadados = %v", fields)
	}
	if fields["invoice"] != "cobranca-7" {
		t.Errorf("a nossa referência devia estar lá: %v", fields)
	}
}
