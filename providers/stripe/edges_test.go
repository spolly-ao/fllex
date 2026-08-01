package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

func provider(t *testing.T, h http.HandlerFunc) (*Provider, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	return New(Config{SecretKey: "sk_teste", BaseURL: srv.URL}), srv.Close
}

func TestAccessorsAndDefaults(t *testing.T) {
	c := NewClient(Config{SecretKey: "sk"})
	if c.http.BaseURL != DefaultBaseURL {
		t.Errorf("base = %q", c.http.BaseURL)
	}
	p := NewWithClient(c)
	if p.Client() != c {
		t.Error("Client() devia devolver o cliente recebido")
	}
	if ms := p.Methods(); len(ms) != 1 || ms[0] != payment.MethodCard {
		t.Errorf("métodos = %v", ms)
	}
	// O Stripe processa todas as moedas: quem quiser um gateway local para o
	// kwanza resolve-o pela ordem de registo.
	for _, cur := range []money.Currency{money.AOA, money.EUR, money.USD, "JPY"} {
		if !p.SupportsCurrency(cur) {
			t.Errorf("%s devia ser suportada", cur)
		}
	}
}

func TestOneOffCheckoutMode(t *testing.T) {
	var got string
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm.Get("mode")
		fmt.Fprint(w, `{"id":"cs_1","url":"https://checkout/cs_1"}`)
	})
	defer done()

	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(15, money.EUR), Method: payment.MethodCard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "payment" {
		t.Errorf("modo = %q, queria pagamento único", got)
	}
}

func TestChargeWithExistingCustomerAndMetadata(t *testing.T) {
	var form url.Values
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		fmt.Fprint(w, `{"id":"cs_1","url":"https://checkout/cs_1","customer":"cus_1"}`)
	})
	defer done()

	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "org-1",
		Amount:    money.FromMajor(15, money.EUR), Method: payment.MethodCard,
		Mode: payment.ModeSubscription, Interval: "monthly",
		Customer: payment.Customer{ProviderRef: "cus_1", Email: "ignorado@exemplo.pt"},
		Metadata: map[string]string{"kind": "upgrade"},
		Items:    []payment.LineItem{{Description: "Plano Pro"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CustomerRef != "cus_1" {
		t.Errorf("cliente = %q", res.CustomerRef)
	}
	// Com cliente já existente não se envia o email: criaria um segundo
	// cliente no gateway para a mesma pessoa.
	if form.Get("customer") != "cus_1" || form.Get("customer_email") != "" {
		t.Errorf("formulário = %v", form)
	}
	if form.Get("metadata[kind]") != "upgrade" || form.Get("subscription_data[metadata][kind]") != "upgrade" {
		t.Error("os metadados livres deviam ir também para a subscrição")
	}
	// Sem descrição, o nome do produto vem da primeira linha.
	if form.Get("line_items[0][price_data][product_data][name]") != "Plano Pro" {
		t.Errorf("nome do produto = %q", form.Get("line_items[0][price_data][product_data][name]"))
	}
}

func TestChargeGuards(t *testing.T) {
	ctx := context.Background()
	p := New(Config{SecretKey: "sk_teste"})
	if _, err := p.Charge(ctx, payment.ChargeRequest{
		Amount: money.FromMajor(10, money.EUR), Method: payment.MethodMCX,
	}); !errors.Is(err, payment.ErrUnsupportedMethod) {
		t.Errorf("método errado = %v", err)
	}
	if _, err := p.Charge(ctx, payment.ChargeRequest{
		Amount: money.Zero(money.EUR), Method: payment.MethodCard,
	}); !errors.Is(err, payment.ErrAmountNotPositive) {
		t.Errorf("valor zero = %v", err)
	}

	unconfigured := New(Config{})
	for _, err := range []error{
		second(unconfigured.VerifyCharge(ctx, "cs_1", "")),
		unconfigured.CancelSubscription(ctx, "sub_1", true),
		second2(unconfigured.PortalURL(ctx, "cus_1", "https://x")),
		second3(unconfigured.Refund(ctx, payment.Refund{ChargeRef: "pi_1"})),
		second4(unconfigured.Balance(ctx)),
	} {
		if !errors.Is(err, payment.ErrNotConfigured) {
			t.Errorf("sem chave = %v", err)
		}
	}
}

func second(_ payment.ChargeStatus, err error) error    { return err }
func second2(_ string, err error) error                 { return err }
func second3(_ payment.RefundResult, err error) error   { return err }
func second4(_ []payment.BalanceEntry, err error) error { return err }

func TestVerifyCharge(t *testing.T) {
	status := "open"
	paid := "unpaid"
	p, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"id":"cs_1","status":%q,"payment_status":%q}`, status, paid)
	})
	defer done()
	ctx := context.Background()

	st, err := p.VerifyCharge(ctx, "cs_1", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != payment.StatusPending {
		t.Errorf("aberta = %s", st.Status)
	}

	status, paid = "complete", "paid"
	st, _ = p.VerifyCharge(ctx, "cs_1", "")
	if !st.Paid || st.PaidAt == nil {
		t.Errorf("paga = %+v", st)
	}

	status, paid = "expired", "unpaid"
	st, _ = p.VerifyCharge(ctx, "cs_1", "")
	if st.Status != payment.StatusExpired {
		t.Errorf("expirada = %s", st.Status)
	}

	// Sem referência não há nada a consultar.
	st, err = p.VerifyCharge(ctx, "  ", "")
	if err != nil || st.Paid {
		t.Errorf("sem referência = %+v, %v", st, err)
	}
}

func TestVerifyChargeError(t *testing.T) {
	p, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"message":"No such session"}}`)
	})
	defer done()
	if _, err := p.VerifyCharge(context.Background(), "cs_x", ""); err == nil {
		t.Error("queria erro")
	}
}

func TestCancelSubscription(t *testing.T) {
	var method, path, flag string
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		method, path, flag = r.Method, r.URL.Path, r.PostForm.Get("cancel_at_period_end")
		fmt.Fprint(w, `{}`)
	})
	defer done()
	ctx := context.Background()

	// No fim do período: o cliente fica com o serviço que já pagou.
	if err := p.CancelSubscription(ctx, "sub_1", true); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || flag != "true" {
		t.Errorf("%s %s, cancel_at_period_end=%q", method, path, flag)
	}

	// De imediato.
	if err := p.CancelSubscription(ctx, "sub_1", false); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete {
		t.Errorf("método = %s, queria DELETE", method)
	}

	// Sem identificador não há nada a cancelar, e não se gasta uma chamada.
	method = ""
	if err := p.CancelSubscription(ctx, "  ", true); err != nil {
		t.Fatal(err)
	}
	if method != "" {
		t.Error("não devia ter chamado o gateway")
	}
}

func TestPortalURL(t *testing.T) {
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("customer") != "cus_1" {
			t.Errorf("cliente = %q", r.PostForm.Get("customer"))
		}
		fmt.Fprint(w, `{"url":"https://billing.stripe.com/p/1"}`)
	})
	defer done()

	got, err := p.PortalURL(context.Background(), "cus_1", "https://app/painel")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://billing.stripe.com/p/1" {
		t.Errorf("= %q", got)
	}

	// Sem cliente no gateway não há portal que se abra.
	if _, err := p.PortalURL(context.Background(), "  ", "https://app"); !errors.Is(err, payment.ErrUnsupported) {
		t.Errorf("sem cliente = %v", err)
	}
}

func TestPortalURLError(t *testing.T) {
	p, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"portal not configured"}}`)
	})
	defer done()
	if _, err := p.PortalURL(context.Background(), "cus_1", "https://app"); err == nil {
		t.Error("queria erro")
	}
}

func TestRefund(t *testing.T) {
	var form url.Values
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		fmt.Fprint(w, `{"id":"re_1","status":"succeeded","amount":1500,"currency":"eur"}`)
	})
	defer done()

	res, err := p.Refund(context.Background(), payment.Refund{
		ChargeRef: "pi_1", Amount: money.FromMajor(15, money.EUR), Reason: "duplicado",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RefundRef != "re_1" || res.Status != payment.StatusApproved {
		t.Errorf("estorno = %+v", res)
	}
	if res.Amount.Minor != 1500 || res.Amount.Currency != money.EUR {
		t.Errorf("valor = %s", res.Amount)
	}
	if form.Get("amount") != "1500" {
		t.Errorf("valor pedido = %q", form.Get("amount"))
	}
	// O motivo vai nos metadados: o campo próprio do gateway só aceita três
	// valores fixos e recusa texto livre.
	if form.Get("metadata[reason]") != "duplicado" {
		t.Errorf("motivo = %q", form.Get("metadata[reason]"))
	}
}

func TestRefundFullAndUnknownStatus(t *testing.T) {
	var form url.Values
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		fmt.Fprint(w, `{"id":"re_1","status":"algo_novo","amount":1500,"currency":"eur"}`)
	})
	defer done()

	// Valor a zero devolve tudo: não se envia o campo.
	res, err := p.Refund(context.Background(), payment.Refund{ChargeRef: "pi_1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := form["amount"]; has {
		t.Error("um estorno total não devia enviar o valor")
	}
	if _, has := form["metadata[reason]"]; has {
		t.Error("sem motivo não se escreve o campo")
	}
	// Um estado que não se reconheça fica pendente, e não aprovado.
	if res.Status != payment.StatusPending {
		t.Errorf("estado desconhecido = %s", res.Status)
	}
}

func TestRefundErrors(t *testing.T) {
	p := New(Config{SecretKey: "sk_teste"})
	if _, err := p.Refund(context.Background(), payment.Refund{}); err == nil {
		t.Error("um estorno sem cobrança devia falhar")
	}

	p2, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"charge already refunded"}}`)
	})
	defer done()
	if _, err := p2.Refund(context.Background(), payment.Refund{ChargeRef: "pi_1"}); err == nil {
		t.Error("queria erro")
	}
}

func TestBalance(t *testing.T) {
	p, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"available":[{"amount":10000,"currency":"eur"},{"amount":500,"currency":"eur"},
			{"amount":250000,"currency":"aoa"}],
			"pending":[{"amount":2000,"currency":"eur"}]}`)
	})
	defer done()

	entries, err := p.Balance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byCur := map[money.Currency]payment.BalanceEntry{}
	for _, e := range entries {
		byCur[e.Currency] = e
	}
	// Várias entradas da mesma moeda somam-se, que é como o gateway as devolve.
	if got := byCur[money.EUR]; got.Available.Minor != 10500 || got.Pending.Minor != 2000 {
		t.Errorf("euro = %+v", got)
	}
	if got := byCur[money.AOA]; got.Available.Minor != 250000 || !got.Pending.IsZero() {
		t.Errorf("kwanza = %+v", got)
	}
}

func TestBalanceError(t *testing.T) {
	p, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	defer done()
	if _, err := p.Balance(context.Background()); err == nil {
		t.Error("queria erro")
	}
}

func TestChargesAndInvoices(t *testing.T) {
	var path string
	c := NewClient(Config{SecretKey: "sk_teste"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.RequestURI()
		if strings.HasPrefix(r.URL.Path, "/v1/charges") {
			fmt.Fprint(w, `{"data":[{"id":"ch_1","amount":1500,"currency":"eur","paid":true}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"in_1","number":"A-1","amount_due":1500,"currency":"eur"}]}`)
	}))
	defer srv.Close()
	c = NewClient(Config{SecretKey: "sk_teste", BaseURL: srv.URL})
	ctx := context.Background()

	charges, err := c.Charges(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(charges) != 1 || charges[0].ID != "ch_1" {
		t.Errorf("cobranças = %+v", charges)
	}
	if path != "/v1/charges?limit=5" {
		t.Errorf("pedido = %q", path)
	}
	// Limites fora do intervalo são aparados: o gateway recusa-os.
	if _, err := c.Charges(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/charges?limit=25" {
		t.Errorf("pedido = %q", path)
	}
	if _, err := c.Charges(ctx, 500); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/charges?limit=25" {
		t.Errorf("pedido = %q", path)
	}

	invoices, err := c.InvoicesForCustomer(ctx, "cus_1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(invoices) != 1 || invoices[0].Number != "A-1" {
		t.Errorf("facturas = %+v", invoices)
	}
	// Sem cliente não se pede nada.
	if got, err := c.InvoicesForCustomer(ctx, "  ", 5); err != nil || got != nil {
		t.Errorf("sem cliente = %v, %v", got, err)
	}
	if _, err := c.InvoicesForCustomer(ctx, "cus_1", 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "limit=25") {
		t.Errorf("pedido = %q", path)
	}
}

func TestChargesAndInvoicesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := NewClient(Config{SecretKey: "sk_teste", BaseURL: srv.URL})
	ctx := context.Background()
	if _, err := c.Charges(ctx, 5); err == nil {
		t.Error("queria erro nas cobranças")
	}
	if _, err := c.InvoicesForCustomer(ctx, "cus_1", 5); err == nil {
		t.Error("queria erro nas facturas")
	}
}

func TestReuseOrChargeWithBrokenSession(t *testing.T) {
	// Se a sessão anterior não se consegue ler, cria-se outra em vez de deixar
	// o cliente sem caminho para pagar.
	created := 0
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		created++
		fmt.Fprint(w, `{"id":"cs_novo","url":"https://checkout/cs_novo"}`)
	})
	defer done()

	res, err := p.ReuseOrCharge(context.Background(), "cs_antiga", payment.ChargeRequest{
		Amount: money.FromMajor(15, money.EUR), Method: payment.MethodCard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 || res.ProviderRef != "cs_novo" {
		t.Errorf("criadas = %d, resultado = %+v", created, res)
	}
}

func TestReuseOrChargeWithExpiredSession(t *testing.T) {
	created := 0
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"id":"cs_1","url":"https://c/1","status":"open","payment_status":"unpaid","expires_at":%d}`,
				time.Now().Add(-time.Hour).Unix())
			return
		}
		created++
		fmt.Fprint(w, `{"id":"cs_novo","url":"https://checkout/cs_novo"}`)
	})
	defer done()

	if _, err := p.ReuseOrCharge(context.Background(), "cs_1", payment.ChargeRequest{
		Amount: money.FromMajor(15, money.EUR), Method: payment.MethodCard,
	}); err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Error("uma sessão expirada não se reutiliza")
	}
}

func TestReuseOrChargeWithoutPreviousSession(t *testing.T) {
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			t.Error("não devia consultar sessão nenhuma")
		}
		fmt.Fprint(w, `{"id":"cs_1","url":"https://c/1"}`)
	})
	defer done()

	if _, err := p.ReuseOrCharge(context.Background(), "  ", payment.ChargeRequest{
		Amount: money.FromMajor(15, money.EUR), Method: payment.MethodCard,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWebhookSubscriptionEvents(t *testing.T) {
	secret := "whsec_teste"
	p := New(Config{SecretKey: "sk", WebhookSecret: secret})

	tests := []struct {
		name    string
		payload string
		want    payment.EventType
		status  payment.Status
	}{
		{
			"criada", `{"id":"e1","type":"customer.subscription.created","data":{"object":{
				"id":"sub_1","customer":"cus_1","status":"active","current_period_end":1750000000,
				"cancel_at_period_end":false,"metadata":{"reference":"org-1","planId":"pro","interval":"monthly"}}}}`,
			payment.EventSubscriptionUpdated, payment.StatusApproved,
		},
		{
			"em atraso", `{"id":"e2","type":"customer.subscription.updated","data":{"object":{
				"id":"sub_1","status":"past_due"}}}`,
			payment.EventSubscriptionUpdated, payment.StatusRejected,
		},
		{
			"incompleta", `{"id":"e3","type":"customer.subscription.updated","data":{"object":{
				"id":"sub_1","status":"incomplete"}}}`,
			payment.EventSubscriptionUpdated, payment.StatusPending,
		},
		{
			"apagada", `{"id":"e4","type":"customer.subscription.deleted","data":{"object":{
				"id":"sub_1","status":"canceled"}}}`,
			payment.EventSubscriptionCancelled, payment.StatusCancelled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := p.ParseWebhook([]byte(tt.payload), sign(t, []byte(tt.payload), secret, time.Now()))
			if err != nil {
				t.Fatal(err)
			}
			if ev.Type != tt.want {
				t.Errorf("tipo = %s, queria %s", ev.Type, tt.want)
			}
			if ev.Status != tt.status {
				t.Errorf("estado = %s, queria %s", ev.Status, tt.status)
			}
			if ev.SubscriptionRef != "sub_1" {
				t.Errorf("subscrição = %q", ev.SubscriptionRef)
			}
		})
	}
}

func TestWebhookPaymentFailedAndRefunded(t *testing.T) {
	secret := "whsec_teste"
	p := New(Config{SecretKey: "sk", WebhookSecret: secret})

	failed := `{"id":"e1","type":"invoice.payment_failed","data":{"object":{
		"id":"in_1","customer":"cus_1","subscription":"sub_1"}}}`
	ev, err := p.ParseWebhook([]byte(failed), sign(t, []byte(failed), secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventChargeFailed || ev.Status != payment.StatusRejected {
		t.Errorf("falha = %+v", ev)
	}
	if ev.InvoiceRef != "in_1" {
		t.Errorf("factura = %q", ev.InvoiceRef)
	}

	refunded := `{"id":"e2","type":"charge.refunded","data":{"object":{
		"id":"ch_1","payment_intent":"pi_1","amount_refunded":1500,"currency":"eur",
		"metadata":{"reference":"enc-1"}}}}`
	ev, err = p.ParseWebhook([]byte(refunded), sign(t, []byte(refunded), secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventChargeRefunded || ev.ChargeRef != "pi_1" {
		t.Errorf("estorno = %+v", ev)
	}
	if ev.Amount == nil || ev.Amount.Minor != 1500 {
		t.Errorf("valor = %v", ev.Amount)
	}
	if ev.Reference != "enc-1" {
		t.Errorf("referência = %q", ev.Reference)
	}
}

func TestWebhookCheckoutExpired(t *testing.T) {
	secret := "whsec_teste"
	p := New(Config{SecretKey: "sk", WebhookSecret: secret})
	body := `{"id":"e1","type":"checkout.session.expired","data":{"object":{
		"id":"cs_1","client_reference_id":"enc-1"}}}`
	ev, err := p.ParseWebhook([]byte(body), sign(t, []byte(body), secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventChargeExpired || ev.Status != payment.StatusExpired {
		t.Errorf("= %+v", ev)
	}
	if ev.Reference != "enc-1" {
		t.Errorf("referência = %q", ev.Reference)
	}
}

func TestWebhookRejectsBadSignature(t *testing.T) {
	// Sem verificação, qualquer pessoa que descubra o endereço pode dar
	// subscrições por pagas.
	p := New(Config{SecretKey: "sk", WebhookSecret: "whsec_teste"})
	body := []byte(`{"id":"e1","type":"invoice.paid"}`)
	if _, err := p.ParseWebhook(body, sign(t, body, "outro-segredo", time.Now())); !errors.Is(err, payment.ErrBadSignature) {
		t.Errorf("= %v", err)
	}
}

func TestWebhookRejectsBrokenJSON(t *testing.T) {
	p := New(Config{SecretKey: "sk"}) // sem segredo, a assinatura não é verificada
	if _, err := p.ParseWebhook([]byte(`isto não é json`), ""); err == nil {
		t.Error("um corpo ilegível devia dar erro")
	}
}

func TestNormalizeSubscriptionStatus(t *testing.T) {
	tests := map[string]payment.Status{
		"active": payment.StatusApproved, "trialing": payment.StatusApproved,
		"past_due": payment.StatusRejected, "unpaid": payment.StatusRejected,
		"canceled": payment.StatusCancelled, "incomplete_expired": payment.StatusCancelled,
		"incomplete": payment.StatusPending, "inventado": payment.StatusPending,
	}
	for in, want := range tests {
		if got := normalizeSubscriptionStatus(in); got != want {
			t.Errorf("%q = %s, queria %s", in, got, want)
		}
	}
}

func TestDescriptionFallbacks(t *testing.T) {
	// O texto é aparado: um espaço à frente do nome do plano aparece no
	// extracto do cartão do cliente.
	if got := descriptionOf(payment.ChargeRequest{Description: " Plano "}); got != "Plano" {
		t.Errorf("= %q", got)
	}
	if got := descriptionOf(payment.ChargeRequest{
		Items: []payment.LineItem{{Description: "Da linha"}},
	}); got != "Da linha" {
		t.Errorf("= %q", got)
	}
	if got := descriptionOf(payment.ChargeRequest{}); got != "Pagamento" {
		t.Errorf("= %q", got)
	}
	// Uma linha sem descrição também cai no genérico.
	if got := descriptionOf(payment.ChargeRequest{Items: []payment.LineItem{{}}}); got != "Pagamento" {
		t.Errorf("= %q", got)
	}
}

func TestFirstNonEmptyHelper(t *testing.T) {
	if got := firstNonEmpty("", "  ", "x"); got != "x" {
		t.Errorf("= %q", got)
	}
	if got := firstNonEmpty("", " "); got != "" {
		t.Errorf("= %q", got)
	}
}

func TestSignatureToleranceIsConfigurable(t *testing.T) {
	secret := "whsec_teste"
	c := NewClient(Config{SecretKey: "sk", WebhookSecret: secret, SignatureTolerance: 2 * time.Hour})
	payload := []byte(`{"id":"e1"}`)
	// Uma hora atrás está fora da tolerância normal e dentro desta.
	if err := c.VerifySignature(payload, sign(t, payload, secret, time.Now().Add(-time.Hour))); err != nil {
		t.Errorf("= %v", err)
	}
	// Um carimbo que não é número não passa.
	if err := c.VerifySignature(payload, "t=ontem,v1=abc"); !errors.Is(err, payment.ErrBadSignature) {
		t.Errorf("= %v", err)
	}
	// Um cabeçalho com uma parte sem sinal de igual é ignorado sem partir nada.
	if err := c.VerifySignature(payload, "lixo,t=1,v1=abc"); !errors.Is(err, payment.ErrBadSignature) {
		t.Errorf("= %v", err)
	}
}

func TestCheckoutSessionHelpers(t *testing.T) {
	now := time.Now()
	s := CheckoutSession{Status: "open", URL: "https://c/1", ExpiresAt: now.Add(time.Hour).Unix()}
	if !s.Reusable(now) {
		t.Error("uma sessão aberta e por expirar é reutilizável")
	}
	// Sem URL não há para onde mandar o cliente.
	s.URL = ""
	if s.Reusable(now) {
		t.Error("sem URL não se reutiliza")
	}
	// Sem prazo declarado, conta como reutilizável enquanto estiver aberta.
	s = CheckoutSession{Status: "open", URL: "https://c/1"}
	if !s.Reusable(now) {
		t.Error("sem prazo declarado devia ser reutilizável")
	}
	if (CheckoutSession{Status: "complete", URL: "x"}).Reusable(now) {
		t.Error("uma sessão concluída não se reutiliza")
	}
	if !(CheckoutSession{PaymentStatus: "paid"}).Paid() {
		t.Error("paga")
	}
}
