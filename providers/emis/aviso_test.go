package emis

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// comSegredo é uma conta com segredo de aviso, contra o gateway dado.
func comSegredo(base string) *Provider {
	return New(Config{
		Token: "token", POSID: "433220", CallbackURL: "https://api.exemplo.ao/gateways/emis/",
		CallbackSecret: []byte("segredo-dos-avisos"), BaseURL: base,
	})
}

func TestReferenciaCurtaCabeNaEMIS(t *testing.T) {
	if got := ReferenciaCurta("ch_7"); got != "ch_7" {
		t.Errorf("uma referência que cabe não muda: %q", got)
	}
	longa := "pay_nnJJyqF1kzEWVwVNYyauMk"
	curta := ReferenciaCurta(longa)
	if len(curta) != TamanhoMaximoDaReferencia {
		t.Fatalf("a curta tem %d carateres", len(curta))
	}
	if ReferenciaCurta(longa) != curta || ReferenciaCurta(longa+"x") == curta {
		t.Error("a curta tem de ser a mesma para a mesma referência, e outra para outra")
	}
}

func TestChargeComSegredoMandaAReferenciaCurtaEOEnderecoAssinado(t *testing.T) {
	srv, corpo := servidor(t, `{"id":"tok-1"}`, http.StatusOK)
	p := comSegredo(srv.URL)
	longa := "pay_nnJJyqF1kzEWVwVNYyauMk"
	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: longa, Amount: money.FromMajor(100, money.AOA), Method: payment.MethodMCX,
	})
	if err != nil {
		t.Fatalf("cobrança falhou: %v", err)
	}
	if (*corpo)["reference"] != ReferenciaCurta(longa) {
		t.Errorf("referência enviada = %v", (*corpo)["reference"])
	}
	aviso, _ := (*corpo)["callbackUrl"].(string)
	queria := "https://api.exemplo.ao/gateways/emis/" + p.Client().Assinatura(longa)
	if aviso != queria {
		t.Errorf("endereço de aviso = %q, queria %q", aviso, queria)
	}
	if res.Reference != longa {
		t.Errorf("a referência devolvida é a inteira: %q", res.Reference)
	}
	// O FrameToken público usa o endereço assinado da referência que recebe.
	if _, _, err := p.Client().FrameToken(context.Background(), "ch_9", "1.00"); err != nil {
		t.Fatal(err)
	}
	if (*corpo)["callbackUrl"] != "https://api.exemplo.ao/gateways/emis/"+p.Client().Assinatura("ch_9") {
		t.Errorf("FrameToken: endereço de aviso = %v", (*corpo)["callbackUrl"])
	}
}

func TestChargeSemSegredoRecusaUmaReferenciaQueNaoCabe(t *testing.T) {
	p := New(Config{Token: "token", POSID: "433220", CallbackURL: "https://x.ao/cb", BaseURL: "http://127.0.0.1:1"})
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "pay_nnJJyqF1kzEWVwVNYyauMk", Amount: money.FromMajor(100, money.AOA),
	})
	if !errors.Is(err, ErrReferenceTooLong) {
		t.Fatalf("erro = %v, queria ErrReferenceTooLong", err)
	}
	if p.Client().CallbackURLDe("ch_7") != "https://x.ao/cb" {
		t.Error("sem segredo, o endereço é o configurado")
	}
}

func avisoAceite(ref string) []byte {
	return []byte(`{"id":"emis-1","reference":` + ref + `,"amount":100,"status":"ACCEPTED",` +
		`"transactionType":"PAYMENT","currency":"AOA","pointOfSale":{"id":"433220"}}`)
}

func TestParseWebhookComSegredoExigeAAssinatura(t *testing.T) {
	p := comSegredo("")
	longa := "pay_nnJJyqF1kzEWVwVNYyauMk"
	curta := ReferenciaCurta(longa)
	certa := p.Client().Assinatura(longa)

	for nome, assinatura := range map[string]string{
		"vazia":            "",
		"sem ponto":        "abc",
		"ponto no início":  ".abc",
		"HMAC errado":      longa + ".00",
		"de outro segredo": New(Config{CallbackSecret: []byte("outro")}).Client().Assinatura(longa),
	} {
		if ev, err := p.ParseWebhook(avisoAceite(`{"id":"`+curta+`"}`), assinatura); !errors.Is(err, payment.ErrBadSignature) || ev != nil {
			t.Errorf("%s: (%v, %v), queria ErrBadSignature", nome, ev, err)
		}
	}

	// A referência curta no objecto, a inteira em texto, e nenhuma: todas
	// pagam, e o evento leva a referência inteira, a que a assinatura assina.
	for nome, corpo := range map[string][]byte{
		"curta no objecto": avisoAceite(`{"id":"` + curta + `"}`),
		"inteira em texto": avisoAceite(`"` + longa + `"`),
		"sem referência":   avisoAceite(`null`),
		"no merchant ref":  []byte(`{"id":"e","merchantReferenceNumber":"` + curta + `","amount":"100.00","status":"ACCEPTED","transactionType":"PAYMENT","currency":"AOA","pointOfSale":{"id":"433220"}}`),
	} {
		ev, err := p.ParseWebhook(corpo, certa)
		if err != nil || ev.Type != payment.EventChargeSucceeded || ev.Reference != longa {
			t.Errorf("%s: (%+v, %v)", nome, ev, err)
		}
	}

	// Um corpo com a referência de outra cobrança não paga esta.
	ev, err := p.ParseWebhook(avisoAceite(`{"id":"outra"}`), certa)
	if err != nil || ev.Type != payment.EventNone {
		t.Errorf("outra referência: (%+v, %v), queria EventNone", ev, err)
	}
	// Um corpo ilegível com a assinatura certa também não.
	if ev, err := p.ParseWebhook([]byte("não é json"), certa); err != nil || ev.Type != payment.EventNone {
		t.Errorf("corpo ilegível: (%+v, %v)", ev, err)
	}
}

func TestReferenciaDoAvisoLeAsFormasQueChegam(t *testing.T) {
	p := New(Config{POSID: "433220"})
	for corpo, queria := range map[string]string{
		`{"id":"ch_7"}`: "ch_7",
		`"ch_7"`:        "ch_7",
		`123`:           "",
		`[1]`:           "",
	} {
		ev, err := p.ParseWebhook(avisoAceite(corpo), "")
		if err != nil {
			t.Fatal(err)
		}
		if ev.Reference != queria {
			t.Errorf("reference %s: %q, queria %q", corpo, ev.Reference, queria)
		}
	}
	// Sem nada em reference, vale o merchantReferenceNumber.
	ev, _ := p.ParseWebhook([]byte(`{"merchantReferenceNumber":"  ch_8 ","status":"X"}`), "")
	if ev.Reference != "ch_8" {
		t.Errorf("merchantReferenceNumber: %q", ev.Reference)
	}
	if !strings.Contains(ErrReferenceTooLong.Error(), "15") {
		t.Error("o erro diz o limite")
	}
}
