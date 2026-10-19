package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// Uma subscrição com o primeiro ciclo mais barato nasce com o preço da
// renovação e um cupão que desconta a diferença uma vez: o primeiro invoice
// cobra o que se pediu agora, e os seguintes o preço cheio.
func TestChargeComPrimeiroCicloMaisBarato(t *testing.T) {
	var cupao, sessao url.Values
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/v1/coupons":
			cupao = r.PostForm
			fmt.Fprint(w, `{"id":"cpn_primeiro"}`)
		case "/v1/checkout/sessions":
			sessao = r.PostForm
			fmt.Fprint(w, `{"id":"cs_1","url":"https://checkout.stripe.com/c/cs_1"}`)
		}
	})
	defer done()
	renovacao := money.New(2500, "USD")
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "pay_1", Amount: money.New(1250, "USD"), RenewalAmount: &renovacao,
		Mode: payment.ModeSubscription, Interval: "monthly",
	}); err != nil {
		t.Fatal(err)
	}
	if cupao.Get("amount_off") != "1250" || cupao.Get("currency") != "usd" || cupao.Get("duration") != "once" ||
		cupao.Get("max_redemptions") != "1" || cupao.Get("name") == "" {
		t.Errorf("cupão = %v", cupao)
	}
	if sessao.Get("line_items[0][price_data][unit_amount]") != "2500" || sessao.Get("discounts[0][coupon]") != "cpn_primeiro" ||
		sessao.Get("mode") != "subscription" {
		t.Errorf("sessão = %v", sessao)
	}

	// O mesmo preço nos dois: sem cupão.
	cupao, sessao = nil, nil
	igual := money.New(1250, "USD")
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.New(1250, "USD"), RenewalAmount: &igual, Mode: payment.ModeSubscription, Interval: "monthly",
	}); err != nil {
		t.Fatal(err)
	}
	if cupao != nil || sessao.Get("discounts[0][coupon]") != "" || sessao.Get("line_items[0][price_data][unit_amount]") != "1250" {
		t.Errorf("com o mesmo preço: cupão %v, sessão %v", cupao, sessao)
	}
}

// O que não serve recusa-se antes de falar com o Stripe: nenhum cupão fica
// criado por gastar.
func TestChargeComPrimeiroCicloRecusas(t *testing.T) {
	chamadas := 0
	p, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		chamadas++
		fmt.Fprint(w, `{"id":"x"}`)
	})
	defer done()
	usd := money.New(2500, "USD")
	eur := money.New(2500, "EUR")
	menor := money.New(1000, "USD")
	for nome, req := range map[string]payment.ChargeRequest{
		"pagamento único": {Amount: money.New(1250, "USD"), RenewalAmount: &usd},
		"outra moeda":     {Amount: money.New(1250, "USD"), RenewalAmount: &eur, Mode: payment.ModeSubscription, Interval: "monthly"},
		"renovação menor": {Amount: money.New(1250, "USD"), RenewalAmount: &menor, Mode: payment.ModeSubscription, Interval: "monthly"},
	} {
		if _, err := p.Charge(context.Background(), req); !errors.Is(err, payment.ErrInvalidRenewalAmount) {
			t.Errorf("%s: %v", nome, err)
		}
	}
	if chamadas != 0 {
		t.Errorf("%d pedidos ao Stripe", chamadas)
	}
}

// O Stripe recusa o cupão: a cobrança falha, e não se cria a sessão sem o
// desconto (o cliente pagaria o preço cheio no primeiro ciclo).
func TestChargeComPrimeiroCicloQuandoOCupaoFalha(t *testing.T) {
	sessoes := 0
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/coupons" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Invalid currency"}}`)
			return
		}
		sessoes++
		fmt.Fprint(w, `{"id":"cs_1"}`)
	})
	defer done()
	renovacao := money.New(2500, "USD")
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.New(1250, "USD"), RenewalAmount: &renovacao, Mode: payment.ModeSubscription, Interval: "monthly",
	}); err == nil {
		t.Error("queria erro")
	}
	if sessoes != 0 {
		t.Errorf("criou %d sessões sem o desconto", sessoes)
	}
}
