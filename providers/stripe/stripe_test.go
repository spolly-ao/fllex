package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

func sign(t *testing.T, payload []byte, secret string, ts time.Time) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts.Unix())
	mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", ts.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func TestVerifySignature(t *testing.T) {
	secret := "whsec_teste"
	c := NewClient(Config{SecretKey: "sk_teste", WebhookSecret: secret})
	payload := []byte(`{"id":"evt_1","type":"invoice.paid"}`)

	if err := c.VerifySignature(payload, sign(t, payload, secret, time.Now())); err != nil {
		t.Errorf("assinatura válida recusada: %v", err)
	}
	if err := c.VerifySignature(payload, sign(t, payload, "outro-segredo", time.Now())); !errors.Is(err, payment.ErrBadSignature) {
		t.Errorf("assinatura com segredo errado devolveu %v", err)
	}
	// Um corpo alterado depois de assinado é o caso que importa apanhar.
	if err := c.VerifySignature([]byte(`{"id":"evt_1","type":"forjado"}`), sign(t, payload, secret, time.Now())); !errors.Is(err, payment.ErrBadSignature) {
		t.Errorf("corpo adulterado devolveu %v", err)
	}
	// Um evento legítimo capturado e reenviado horas depois tem de ser
	// recusado, senão vale para sempre a quem o guardar.
	old := time.Now().Add(-time.Hour)
	if err := c.VerifySignature(payload, sign(t, payload, secret, old)); !errors.Is(err, payment.ErrBadSignature) {
		t.Errorf("evento antigo reenviado devolveu %v, queria recusa", err)
	}
	if err := c.VerifySignature(payload, "lixo"); !errors.Is(err, payment.ErrBadSignature) {
		t.Errorf("cabeçalho malformado devolveu %v", err)
	}
}

func TestVerifySignatureDisabledWithoutSecret(t *testing.T) {
	c := NewClient(Config{SecretKey: "sk_teste"})
	if err := c.VerifySignature([]byte(`{}`), "seja o que for"); err != nil {
		t.Errorf("sem segredo configurado a verificação devia estar desligada, deu %v", err)
	}
}

func TestParseWebhookCheckoutSubscription(t *testing.T) {
	secret := "whsec_teste"
	p := New(Config{SecretKey: "sk_teste", WebhookSecret: secret})
	payload := []byte(`{
		"id": "evt_1",
		"type": "checkout.session.completed",
		"created": 1750000000,
		"data": {"object": {
			"id": "cs_1",
			"mode": "subscription",
			"customer": "cus_1",
			"subscription": "sub_1",
			"payment_intent": "pi_1",
			"client_reference_id": "org-42",
			"metadata": {"reference": "org-42", "planId": "essencial", "interval": "monthly"}
		}}
	}`)

	ev, err := p.ParseWebhook(payload, sign(t, payload, secret, time.Now()))
	if err != nil {
		t.Fatalf("leitura do webhook falhou: %v", err)
	}
	if ev.Type != payment.EventSubscriptionActive {
		t.Errorf("tipo = %s, queria subscrição activa", ev.Type)
	}
	if ev.Reference != "org-42" || ev.SubscriptionRef != "sub_1" || ev.CustomerRef != "cus_1" {
		t.Errorf("referências mal lidas: %+v", ev)
	}
	if ev.PlanRef != "essencial" || ev.Interval != "monthly" {
		t.Errorf("plano/periodicidade mal lidos: %q / %q", ev.PlanRef, ev.Interval)
	}
}

func TestParseWebhookOneOffCheckoutIsNotASubscription(t *testing.T) {
	// Um pagamento único não pode entrar no caminho das subscrições, senão
	// aparece uma subscrição que ninguém contratou.
	secret := "whsec_teste"
	p := New(Config{SecretKey: "sk_teste", WebhookSecret: secret})
	payload := []byte(`{
		"id": "evt_2", "type": "checkout.session.completed",
		"data": {"object": {"id": "cs_2", "mode": "payment", "payment_intent": "pi_2",
			"metadata": {"reference": "encomenda-9"}}}
	}`)
	ev, err := p.ParseWebhook(payload, sign(t, payload, secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventChargeSucceeded {
		t.Errorf("tipo = %s, queria cobrança paga", ev.Type)
	}
	if ev.ChargeRef != "pi_2" {
		t.Errorf("referência da cobrança = %q, queria pi_2", ev.ChargeRef)
	}
}

func TestParseWebhookInvoicePaidCarriesBillingReason(t *testing.T) {
	secret := "whsec_teste"
	p := New(Config{SecretKey: "sk_teste", WebhookSecret: secret})
	payload := []byte(`{
		"id": "evt_3", "type": "invoice.paid",
		"data": {"object": {
			"id": "in_1", "customer": "cus_1", "subscription": "sub_1",
			"amount_paid": 1500, "currency": "eur",
			"billing_reason": "subscription_cycle",
			"lines": {"data": [{"period": {"start": 1750000000, "end": 1752678400}}]}
		}}
	}`)
	ev, err := p.ParseWebhook(payload, sign(t, payload, secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventInvoicePaid {
		t.Errorf("tipo = %s", ev.Type)
	}
	if !ev.BillingCycle() {
		t.Error("devia ser reconhecida como factura de ciclo, que é a que renova")
	}
	if ev.Amount == nil || ev.Amount.Minor != 1500 || ev.Amount.Currency != money.EUR {
		t.Errorf("valor = %v", ev.Amount)
	}
	if ev.PeriodStart == nil || ev.PeriodEnd == nil {
		t.Error("o período coberto devia vir preenchido")
	}
	if ev.InvoiceRef != "in_1" {
		t.Errorf("referência da factura = %q, queria in_1 (é ela que evita emitir a dobrar)", ev.InvoiceRef)
	}
}

func TestParseWebhookUnknownTypeIsIgnored(t *testing.T) {
	// O Stripe envia dezenas de tipos; ignorar os que não interessam é o
	// comportamento normal, não uma falha.
	secret := "whsec_teste"
	p := New(Config{SecretKey: "sk_teste", WebhookSecret: secret})
	payload := []byte(`{"id":"evt_9","type":"radar.early_fraud_warning.created","data":{"object":{}}}`)
	ev, err := p.ParseWebhook(payload, sign(t, payload, secret, time.Now()))
	if err != nil {
		t.Fatalf("um evento desinteressante não devia dar erro: %v", err)
	}
	if !ev.Ignorable() {
		t.Errorf("tipo = %s, queria ignorável", ev.Type)
	}
}

func TestChargeBuildsSubscriptionSession(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cs_1","url":"https://checkout/cs_1","expires_at":1750000000}`)
	}))
	defer srv.Close()

	p := New(Config{SecretKey: "sk_teste", BaseURL: srv.URL})
	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference:   "org-42",
		Amount:      money.FromMajor(15, money.EUR),
		Method:      payment.MethodCard,
		Mode:        payment.ModeSubscription,
		Interval:    "yearly",
		Description: "Plano Essencial",
		Customer:    payment.Customer{Email: "cliente@exemplo.pt"},
		SuccessURL:  "https://app/ok",
		CancelURL:   "https://app/cancelado",
	})
	if err != nil {
		t.Fatalf("checkout falhou: %v", err)
	}
	if res.Kind != payment.KindRedirect || res.URL == "" || res.ProviderRef != "cs_1" {
		t.Errorf("resultado = %+v", res)
	}
	if got.Get("mode") != "subscription" {
		t.Errorf("modo = %q, queria subscription", got.Get("mode"))
	}
	if got.Get("line_items[0][price_data][unit_amount]") != "1500" {
		t.Errorf("valor enviado = %q, queria 1500 cêntimos", got.Get("line_items[0][price_data][unit_amount]"))
	}
	if got.Get("line_items[0][price_data][recurring][interval]") != "year" {
		t.Errorf("periodicidade = %q, queria year", got.Get("line_items[0][price_data][recurring][interval]"))
	}
	// Os metadados têm de ir também na subscrição, senão os webhooks seguintes
	// (renovações, falhas) não são correlacionáveis.
	if got.Get("subscription_data[metadata][reference]") != "org-42" {
		t.Error("a referência não foi propagada para a subscrição")
	}
}

func TestChargeSendsMinorUnitsForZeroDecimalCurrency(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		fmt.Fprint(w, `{"id":"cs_1","url":"https://checkout/cs_1"}`)
	}))
	defer srv.Close()

	p := New(Config{SecretKey: "sk_teste", BaseURL: srv.URL})
	// O iene não tem subunidade: multiplicar por 100 cobrava cem vezes mais.
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(1000, "JPY"), Method: payment.MethodCard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Get("line_items[0][price_data][unit_amount]") != "1000" {
		t.Errorf("valor em iene = %q, queria 1000", got.Get("line_items[0][price_data][unit_amount]"))
	}
}

func TestChargeWithoutKeyIsNotConfigured(t *testing.T) {
	p := New(Config{})
	if p.Configured() {
		t.Error("sem chave não devia estar configurado")
	}
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(10, money.EUR), Method: payment.MethodCard,
	})
	if !errors.Is(err, payment.ErrNotConfigured) {
		t.Errorf("devolveu %v, queria ErrNotConfigured", err)
	}
}

func TestGatewayErrorCarriesStripeMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"Amount must be at least 50 cents","code":"amount_too_small"}}`)
	}))
	defer srv.Close()

	p := New(Config{SecretKey: "sk_teste", BaseURL: srv.URL})
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.New(1, money.EUR), Method: payment.MethodCard,
	})
	if err == nil {
		t.Fatal("queria erro")
	}
	// A mensagem do gateway é muitas vezes a única explicação de uma recusa; um
	// "erro 400" sozinho não serve para o suporte.
	if !strings.Contains(err.Error(), "at least 50 cents") {
		t.Errorf("erro = %q, queria a mensagem do Stripe", err)
	}
	var ge *payment.GatewayError
	if !errors.As(err, &ge) || ge.Code != "amount_too_small" {
		t.Errorf("código = %+v", ge)
	}
	if payment.IsRetryable(err) {
		t.Error("um 400 não vale a pena repetir")
	}
}

func TestReuseOrChargeReusesOpenSession(t *testing.T) {
	created := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprintf(w, `{"id":"cs_1","url":"https://checkout/cs_1","status":"open","payment_status":"unpaid","expires_at":%d}`,
				time.Now().Add(time.Hour).Unix())
			return
		}
		created++
		fmt.Fprint(w, `{"id":"cs_2","url":"https://checkout/cs_2"}`)
	}))
	defer srv.Close()

	p := New(Config{SecretKey: "sk_teste", BaseURL: srv.URL})
	res, err := p.ReuseOrCharge(context.Background(), "cs_1", payment.ChargeRequest{
		Amount: money.FromMajor(15, money.EUR), Method: payment.MethodCard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ProviderRef != "cs_1" {
		t.Errorf("sessão = %q, queria reutilizar cs_1", res.ProviderRef)
	}
	if created != 0 {
		t.Errorf("criou %d sessões, queria reutilizar a aberta", created)
	}
}

func TestReuseOrChargeDetectsAlreadyPaidSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"id":"cs_1","status":"complete","payment_status":"paid","subscription":"sub_1","customer":"cus_1"}`)
			return
		}
		t.Error("não devia criar uma sessão nova para algo já pago")
	}))
	defer srv.Close()

	p := New(Config{SecretKey: "sk_teste", BaseURL: srv.URL})
	res, err := p.ReuseOrCharge(context.Background(), "cs_1", payment.ChargeRequest{
		Amount: money.FromMajor(15, money.EUR), Method: payment.MethodCard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != payment.KindPaid || res.SubscriptionRef != "sub_1" {
		t.Errorf("resultado = %+v, queria reconhecer o pagamento já feito", res)
	}
}

func TestProviderSatisfiesOptionalCapabilities(t *testing.T) {
	var p any = New(Config{SecretKey: "sk"})
	if _, ok := p.(payment.Verifier); !ok {
		t.Error("o Stripe devia saber consultar o estado")
	}
	if _, ok := p.(payment.Subscriber); !ok {
		t.Error("o Stripe devia saber gerir subscrições")
	}
	if _, ok := p.(payment.WebhookParser); !ok {
		t.Error("o Stripe devia saber ler webhooks")
	}
	if _, ok := p.(payment.Refunder); !ok {
		t.Error("o Stripe devia saber estornar")
	}
	if _, ok := p.(payment.Reporter); !ok {
		t.Error("o Stripe devia saber devolver o saldo")
	}
}
