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

// Um desconto em mais do que um ciclo: o cupão do Stripe é forever, ou
// repeating com os meses que os ciclos cobrem, e leva o nome pedido.
func TestChargeComDescontoDeVariosCiclos(t *testing.T) {
	var cupao url.Values
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.URL.Path == "/v1/coupons" {
			cupao = r.PostForm
			fmt.Fprint(w, `{"id":"cpn_x"}`)
			return
		}
		fmt.Fprint(w, `{"id":"cs_1"}`)
	})
	defer done()
	renovacao := money.New(2500, "USD")
	casos := []struct {
		nome      string
		intervalo string
		ciclos    int
		nomeDado  string
		duracao   string
		meses     string
		nomeVisto string
	}{
		{"sempre", "monthly", payment.DiscountForever, "", "forever", "", "Desconto"},
		{"três meses", "monthly", 3, "BEMVINDO", "repeating", "3", "BEMVINDO"},
		{"dois trimestres", "quarterly", 2, "", "repeating", "6", "Desconto"},
		{"dois semestres", "semiannual", 2, "", "repeating", "12", "Desconto"},
		{"dois anos", "yearly", 2, "", "repeating", "24", "Desconto"},
		{"um ciclo com nome", "monthly", 1, "PRIMEIRO", "once", "", "PRIMEIRO"},
	}
	for _, c := range casos {
		cupao = nil
		if _, err := p.Charge(context.Background(), payment.ChargeRequest{
			Amount: money.New(2000, "USD"), RenewalAmount: &renovacao, Mode: payment.ModeSubscription,
			Interval: c.intervalo, DiscountCycles: c.ciclos, DiscountName: c.nomeDado,
		}); err != nil {
			t.Fatalf("%s: %v", c.nome, err)
		}
		if cupao.Get("duration") != c.duracao || cupao.Get("duration_in_months") != c.meses ||
			cupao.Get("name") != c.nomeVisto || cupao.Get("amount_off") != "500" || cupao.Get("max_redemptions") != "1" {
			t.Errorf("%s: cupão = %v", c.nome, cupao)
		}
	}
}

// Um desconto de vários ciclos que o Stripe não sabe contar recusa-se antes
// de falar com ele. Sem desconto, os ciclos não contam.
func TestChargeComDescontoQueOStripeNaoSabe(t *testing.T) {
	chamadas := 0
	p, done := provider(t, func(w http.ResponseWriter, _ *http.Request) {
		chamadas++
		fmt.Fprint(w, `{"id":"x"}`)
	})
	defer done()
	renovacao := money.New(2500, "USD")
	for nome, req := range map[string]payment.ChargeRequest{
		"semanas":          {Amount: money.New(2000, "USD"), RenewalAmount: &renovacao, Mode: payment.ModeSubscription, Interval: "weekly", DiscountCycles: 2},
		"ciclos negativos": {Amount: money.New(2000, "USD"), RenewalAmount: &renovacao, Mode: payment.ModeSubscription, Interval: "monthly", DiscountCycles: -2},
	} {
		if _, err := p.Charge(context.Background(), req); !errors.Is(err, payment.ErrUnsupportedDiscount) {
			t.Errorf("%s: %v", nome, err)
		}
	}
	if chamadas != 0 {
		t.Errorf("%d pedidos ao Stripe", chamadas)
	}
	igual := money.New(2000, "USD")
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.New(2000, "USD"), RenewalAmount: &igual, Mode: payment.ModeSubscription, Interval: "weekly", DiscountCycles: 2,
	}); err != nil {
		t.Errorf("sem desconto: %v", err)
	}
}

// O CreateOnceCoupon é o CreateCoupon de uma vez, sem meses.
func TestCreateOnceCoupon(t *testing.T) {
	var cupao url.Values
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		cupao = r.PostForm
		fmt.Fprint(w, `{"id":"cpn_1"}`)
	})
	defer done()
	id, err := p.Client().CreateOnceCoupon(context.Background(), 300, "EUR", "")
	if err != nil || id != "cpn_1" {
		t.Fatalf("id %q, erro %v", id, err)
	}
	if cupao.Get("duration") != "once" || cupao.Get("duration_in_months") != "" || cupao.Get("currency") != "eur" || cupao.Has("name") {
		t.Errorf("cupão = %v", cupao)
	}
}
