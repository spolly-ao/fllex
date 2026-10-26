package stripe

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/payment"
)

// A consulta de uma sessão paga traz o payment intent e o nível do Radar da
// última cobrança dele; uma leitura do payment intent que falha não impede a
// confirmação, e deixa o nível vazio.
func TestVerifyChargeLeORiscoDoRadar(t *testing.T) {
	var corpo string
	falhar := false
	p, done := provider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/checkout/sessions/cs_1":
			fmt.Fprint(w, `{"id":"cs_1","status":"complete","payment_status":"paid","payment_intent":"pi_1","amount_total":900,"currency":"usd"}`)
		case "/v1/payment_intents/pi_1":
			if r.URL.Query().Get("expand[]") != "latest_charge" {
				t.Errorf("expand = %q", r.URL.RawQuery)
			}
			if falhar {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"message":"em baixo"}}`)
				return
			}
			fmt.Fprint(w, corpo)
		default:
			t.Errorf("caminho inesperado %s", r.URL.Path)
		}
	})
	defer done()

	for _, c := range []struct{ corpo, quer string }{
		{`{"id":"pi_1","status":"succeeded","latest_charge":{"id":"ch_1","outcome":{"risk_level":"elevated"}}}`, payment.RiskElevated},
		{`{"id":"pi_1","status":"succeeded","latest_charge":{"id":"ch_1","outcome":{"risk_level":"highest"}}}`, payment.RiskHighest},
		{`{"id":"pi_1","status":"succeeded","latest_charge":{"id":"ch_1","outcome":{"risk_level":"normal"}}}`, payment.RiskNormal},
		{`{"id":"pi_1","status":"succeeded","latest_charge":{"id":"ch_1","outcome":{"risk_level":"not_assessed"}}}`, payment.RiskNotAssessed},
		{`{"id":"pi_1","status":"succeeded","latest_charge":{"id":"ch_1","outcome":{"risk_level":"um_nivel_novo"}}}`, payment.RiskUnknown},
		{`{"id":"pi_1","status":"succeeded","latest_charge":{"id":"ch_1"}}`, ""},
		{`{"id":"pi_1","status":"succeeded"}`, ""},
	} {
		corpo = c.corpo
		st, err := p.VerifyCharge(context.Background(), "cs_1", "pay_1")
		if err != nil {
			t.Fatal(err)
		}
		if !st.Paid || st.ChargeRef != "pi_1" || st.RiskLevel != c.quer {
			t.Errorf("%s: pago=%v charge=%q risco=%q, queria %q", c.corpo, st.Paid, st.ChargeRef, st.RiskLevel, c.quer)
		}
	}

	falhar = true
	st, err := p.VerifyCharge(context.Background(), "cs_1", "pay_1")
	if err != nil {
		t.Fatalf("a leitura do risco falhou e a consulta falhou com ela: %v", err)
	}
	if !st.Paid || st.ChargeRef != "pi_1" || st.RiskLevel != "" {
		t.Errorf("com o payment intent em baixo: %+v", st)
	}
}

// Uma disputa chega como EventDisputeOpened, com o payment intent como
// referência do pagamento, o valor e o motivo; sem payment intent, vale a
// cobrança.
func TestWebhookDisputaAberta(t *testing.T) {
	secret := "whsec_teste"
	p := New(Config{SecretKey: "sk", WebhookSecret: secret})
	body := `{"id":"evt_d1","type":"charge.dispute.created","data":{"object":{
		"id":"dp_1","payment_intent":"pi_7","charge":"ch_7","amount":2500,"currency":"usd","reason":"fraudulent"}}}`
	ev, err := p.ParseWebhook([]byte(body), sign(t, []byte(body), secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventDisputeOpened || ev.ChargeRef != "pi_7" || ev.Reason != "fraudulent" {
		t.Errorf("disputa = %+v", ev)
	}
	if ev.Amount == nil || ev.Amount.Minor != 2500 || ev.Amount.Currency != "USD" {
		t.Errorf("valor = %v", ev.Amount)
	}

	body = `{"id":"evt_d2","type":"charge.dispute.created","data":{"object":{"id":"dp_2","charge":"ch_8"}}}`
	ev, err = p.ParseWebhook([]byte(body), sign(t, []byte(body), secret, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != payment.EventDisputeOpened || ev.ChargeRef != "ch_8" || ev.Amount != nil {
		t.Errorf("disputa sem payment intent = %+v", ev)
	}
}
