package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// As renovações por cartão: o Stripe cobra cada ciclo sozinho, e o que fica
// para confirmar é a factura e a subscrição.

func TestVerifyChargeDeUmaFactura(t *testing.T) {
	body := ""
	var path string
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, body)
	})
	defer done()
	ctx := context.Background()

	body = `{"id":"in_1","status":"paid","amount_paid":2500,"currency":"usd","customer":"cus_1",
		"subscription":"sub_1","status_transitions":{"paid_at":1750000000}}`
	st, err := p.VerifyCharge(ctx, "in_1", "")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/invoices/in_1" {
		t.Errorf("consultou %s, queria a factura", path)
	}
	if !st.Paid || st.Status != payment.StatusApproved {
		t.Errorf("paga = %+v", st)
	}
	if st.Amount == nil || st.Amount.Minor != 2500 || st.Amount.Currency != "USD" {
		t.Errorf("valor = %v", st.Amount)
	}
	if st.PaidAt == nil || !st.PaidAt.Equal(time.Unix(1750000000, 0)) {
		t.Errorf("paga em = %v", st.PaidAt)
	}
	if st.SubscriptionRef != "sub_1" || st.CustomerRef != "cus_1" {
		t.Errorf("referências = %q %q", st.SubscriptionRef, st.CustomerRef)
	}

	// A forma nova da API (2025-03-31): a subscrição vem em parent, e uma
	// factura paga sem carimbo fica com a hora da consulta.
	body = `{"id":"in_2","status":"paid","amount_paid":2500,"currency":"usd",
		"parent":{"subscription_details":{"subscription":"sub_2"}}}`
	st, _ = p.VerifyCharge(ctx, "in_2", "")
	if st.SubscriptionRef != "sub_2" || st.PaidAt == nil {
		t.Errorf("forma nova = %+v", st)
	}

	// Aberta continua pendente: o Stripe volta a tentar cobrá-la.
	for estado, quer := range map[string]payment.Status{
		"open":          payment.StatusPending,
		"draft":         payment.StatusPending,
		"void":          payment.StatusCancelled,
		"uncollectible": payment.StatusRejected,
	} {
		body = fmt.Sprintf(`{"id":"in_3","status":%q,"amount_paid":0,"currency":"usd"}`, estado)
		st, err = p.VerifyCharge(ctx, "in_3", "")
		if err != nil || st.Status != quer || st.Paid {
			t.Errorf("%s = %+v, %v; queria %s", estado, st, err, quer)
		}
	}
}

func TestVerifyChargeDeUmaFacturaQueFalha(t *testing.T) {
	p, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"message":"No such invoice"}}`)
	})
	defer done()
	if _, err := p.VerifyCharge(context.Background(), "in_x", ""); err == nil {
		t.Error("queria erro")
	}
}

func TestVerifyChargeDeUmaSessaoTrazASubscricao(t *testing.T) {
	p, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id":"cs_1","status":"complete","payment_status":"paid","customer":"cus_1","subscription":"sub_1"}`)
	})
	defer done()
	st, err := p.VerifyCharge(context.Background(), "cs_1", "")
	if err != nil || !st.Paid || st.SubscriptionRef != "sub_1" || st.CustomerRef != "cus_1" {
		t.Errorf("= %+v, %v", st, err)
	}
}

func TestVerifySubscription(t *testing.T) {
	body := ""
	var path string
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, body)
	})
	defer done()
	ctx := context.Background()

	body = `{"id":"sub_1","customer":"cus_1","status":"past_due","current_period_end":1750000000,"cancel_at_period_end":true}`
	st, err := p.VerifySubscription(ctx, "sub_1")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/subscriptions/sub_1" {
		t.Errorf("consultou %s", path)
	}
	if st.Status != payment.StatusRejected || st.CustomerRef != "cus_1" || !st.CancelAtPeriodEnd {
		t.Errorf("em atraso = %+v", st)
	}
	if st.CurrentPeriodEnd == nil || !st.CurrentPeriodEnd.Equal(time.Unix(1750000000, 0)) {
		t.Errorf("fim do período = %v", st.CurrentPeriodEnd)
	}

	// A forma nova: o fim do período vem nos itens.
	body = `{"id":"sub_1","status":"active","items":{"data":[{"current_period_end":0},{"current_period_end":1760000000}]}}`
	st, _ = p.VerifySubscription(ctx, "sub_1")
	if st.Status != payment.StatusApproved || st.CurrentPeriodEnd == nil || st.CurrentPeriodEnd.Unix() != 1760000000 {
		t.Errorf("forma nova = %+v", st)
	}

	// Sem fim do período em lado nenhum, não se inventa um.
	body = `{"id":"sub_1","status":"canceled","items":{"data":[{"current_period_end":0}]}}`
	st, _ = p.VerifySubscription(ctx, "sub_1")
	if st.Status != payment.StatusCancelled || st.CurrentPeriodEnd != nil {
		t.Errorf("cancelada = %+v", st)
	}
}

func TestVerifySubscriptionGuardas(t *testing.T) {
	ctx := context.Background()
	if _, err := New(Config{}).VerifySubscription(ctx, "sub_1"); !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("sem chave = %v", err)
	}
	if _, err := New(Config{SecretKey: "sk_teste"}).VerifySubscription(ctx, " "); !errors.Is(err, payment.ErrUnsupported) {
		t.Errorf("sem subscrição = %v", err)
	}
	p, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"message":"No such subscription"}}`)
	})
	defer done()
	if _, err := p.VerifySubscription(ctx, "sub_x"); err == nil {
		t.Error("queria erro")
	}
	var _ payment.SubscriptionVerifier = p
}

func TestChargeDeSubscricaoNoRelogioDeEnsaio(t *testing.T) {
	var cliente, sessao url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/v1/customers":
			cliente = r.PostForm
			fmt.Fprint(w, `{"id":"cus_relogio"}`)
		case "/v1/checkout/sessions":
			sessao = r.PostForm
			fmt.Fprint(w, `{"id":"cs_1","url":"https://checkout.stripe.com/c/cs_1"}`)
		}
	}))
	defer srv.Close()
	p := New(Config{SecretKey: "sk_test_x", BaseURL: srv.URL, TestClock: "clock_1"})
	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "pay_1", Amount: money.New(2500, "USD"), Mode: payment.ModeSubscription, Interval: "monthly",
		Customer: payment.Customer{Email: "a@b.ao", Name: "Ana"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cliente.Get("test_clock") != "clock_1" || cliente.Get("email") != "a@b.ao" || cliente.Get("name") != "Ana" {
		t.Errorf("cliente = %v", cliente)
	}
	if sessao.Get("customer") != "cus_relogio" || sessao.Get("customer_email") != "" {
		t.Errorf("sessão = %v", sessao)
	}
	if res.CustomerRef != "cus_relogio" {
		t.Errorf("cliente no resultado = %q", res.CustomerRef)
	}

	// Um pagamento único não passa pelo relógio: só as subscrições renovam.
	cliente = nil
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "pay_2", Amount: money.New(2500, "USD"), Customer: payment.Customer{Email: "a@b.ao"},
	}); err != nil {
		t.Fatal(err)
	}
	if cliente != nil || sessao.Get("customer_email") != "a@b.ao" {
		t.Errorf("pagamento único criou cliente: %v / %v", cliente, sessao)
	}
}

func TestChargeDeSubscricaoNoRelogioSemDados(t *testing.T) {
	var cliente url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path == "/v1/customers" {
			cliente = r.PostForm
			fmt.Fprint(w, `{"id":"cus_2"}`)
			return
		}
		fmt.Fprint(w, `{"id":"cs_1"}`)
	}))
	defer srv.Close()
	p := New(Config{SecretKey: "sk_test_x", BaseURL: srv.URL, TestClock: "clock_1"})
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.New(100, "USD"), Mode: payment.ModeSubscription, Interval: "monthly",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := cliente["email"]; ok {
		t.Errorf("mandou email vazio: %v", cliente)
	}
	if _, ok := cliente["name"]; ok {
		t.Errorf("mandou nome vazio: %v", cliente)
	}
}

func TestChargeNoRelogioRecusaChaveDeProducao(t *testing.T) {
	for _, k := range []string{"sk_live_x", "rk_live_x"} {
		p := New(Config{SecretKey: k, BaseURL: "http://127.0.0.1:1", TestClock: "clock_1"})
		_, err := p.Charge(context.Background(), payment.ChargeRequest{
			Amount: money.New(100, "USD"), Mode: payment.ModeSubscription, Interval: "monthly",
		})
		if !errors.Is(err, payment.ErrNotConfigured) {
			t.Errorf("%s = %v, queria recusa", k[:8], err)
		}
	}
	if New(Config{SecretKey: "sk_test_x"}).Client().Live() {
		t.Error("chave de teste lida como de produção")
	}
}

func TestChargeNoRelogioQuandoOClienteFalha(t *testing.T) {
	p, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"No such test clock"}}`)
	})
	defer done()
	p.client.cfg.TestClock = "clock_x"
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.New(100, "USD"), Mode: payment.ModeSubscription, Interval: "monthly",
	}); err == nil {
		t.Error("queria erro")
	}
}

func TestWebhookRenovacaoPagaETrazAReferenciaDaSubscricao(t *testing.T) {
	secret := "whsec_teste"
	p := New(Config{SecretKey: "sk", WebhookSecret: secret})
	// A forma de 2022-11-15, a da conta da Spolly: subscription na raiz e os
	// metadados em subscription_details.
	antiga := `{"id":"evt_1","type":"invoice.paid","data":{"object":{
		"id":"in_1","customer":"cus_1","subscription":"sub_1","payment_intent":"pi_1",
		"amount_paid":2500,"currency":"usd","billing_reason":"subscription_cycle",
		"hosted_invoice_url":"https://invoice.stripe.com/i/1","metadata":{},
		"subscription_details":{"metadata":{"reference":"sub_nossa","planId":"pro","interval":"monthly"}},
		"lines":{"data":[{"period":{"start":1750000000,"end":1752592000}}]}}}}`
	ev, err := p.ParseWebhook([]byte(antiga), sign(t, []byte(antiga), secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventInvoicePaid || !ev.BillingCycle() || ev.Method != payment.MethodCard {
		t.Errorf("tipo = %+v", ev)
	}
	if ev.Reference != "sub_nossa" || ev.SubscriptionRef != "sub_1" || ev.InvoiceRef != "in_1" || ev.ChargeRef != "pi_1" {
		t.Errorf("referências = %q %q %q %q", ev.Reference, ev.SubscriptionRef, ev.InvoiceRef, ev.ChargeRef)
	}
	if ev.PlanRef != "pro" || ev.Interval != "monthly" || ev.InvoiceURL == "" {
		t.Errorf("plano = %+v", ev)
	}

	// A forma nova (2025-03-31 em diante): tudo em parent.
	nova := `{"id":"evt_2","type":"invoice.payment_failed","data":{"object":{
		"id":"in_2","customer":"cus_1","amount_due":2500,"currency":"usd","billing_reason":"subscription_cycle",
		"parent":{"subscription_details":{"subscription":"sub_2","metadata":{"reference":"sub_outra"}}}}}}`
	ev, err = p.ParseWebhook([]byte(nova), sign(t, []byte(nova), secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventChargeFailed || ev.Status != payment.StatusRejected {
		t.Errorf("falha = %+v", ev)
	}
	if ev.SubscriptionRef != "sub_2" || ev.Reference != "sub_outra" || ev.Amount == nil || ev.Amount.Minor != 2500 {
		t.Errorf("forma nova = %+v", ev)
	}
}

func TestWebhookSubscricaoComOFimDoPeriodoNosItens(t *testing.T) {
	secret := "whsec_teste"
	p := New(Config{SecretKey: "sk", WebhookSecret: secret})
	body := `{"id":"e1","type":"customer.subscription.deleted","data":{"object":{
		"id":"sub_1","status":"canceled","items":{"data":[{"current_period_end":1760000000}]}}}}`
	ev, err := p.ParseWebhook([]byte(body), sign(t, []byte(body), secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if ev.CurrentPeriodEnd == nil || ev.CurrentPeriodEnd.Unix() != 1760000000 {
		t.Errorf("fim do período = %v", ev.CurrentPeriodEnd)
	}
}
