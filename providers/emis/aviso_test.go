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
	// pagam, e o evento leva a referência curta, a que a assinatura assina.
	for nome, corpo := range map[string][]byte{
		"curta no objecto": avisoAceite(`{"id":"` + curta + `"}`),
		"inteira em texto": avisoAceite(`"` + longa + `"`),
		"sem referência":   avisoAceite(`null`),
		"no merchant ref":  []byte(`{"id":"e","merchantReferenceNumber":"` + curta + `","amount":"100.00","status":"ACCEPTED","transactionType":"PAYMENT","currency":"AOA","pointOfSale":{"id":"433220"}}`),
	} {
		ev, err := p.ParseWebhook(corpo, certa)
		if err != nil || ev.Type != payment.EventChargeSucceeded || ev.Reference != curta {
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

func TestSemPontoDeVendaSoOAvisoAssinadoPaga(t *testing.T) {
	longa := "pay_nnJJyqF1kzEWVwVNYyauMk"
	curta := ReferenciaCurta(longa)
	corpo := func(pos string) []byte {
		return []byte(`{"id":"emis-1","reference":{"id":"` + curta + `"},"amount":100,"status":"ACCEPTED",` +
			`"transactionType":"PAYMENT","currency":"AOA","pointOfSale":{"id":"` + pos + `"}}`)
	}

	// Com segredo e sem ponto de venda, como no Kubeta: a assinatura chega, e o
	// ponto de venda do corpo, seja qual for, não se confere.
	p := New(Config{Token: "token", CallbackURL: "https://api.exemplo.ao/gateways/emis/", CallbackSecret: []byte("segredo-dos-avisos")})
	certa := p.Client().Assinatura(longa)
	for _, pos := range []string{"433220", "999999", ""} {
		ev, err := p.ParseWebhook(corpo(pos), certa)
		if err != nil || ev.Type != payment.EventChargeSucceeded || ev.Reference != curta {
			t.Errorf("ponto de venda %q: (%+v, %v)", pos, ev, err)
		}
	}
	if ev, err := p.ParseWebhook(corpo("433220"), longa+".00"); !errors.Is(err, payment.ErrBadSignature) || ev != nil {
		t.Errorf("assinatura errada: (%v, %v)", ev, err)
	}

	// Com segredo e com ponto de venda, o ponto de venda continua a ter de bater.
	if ev, err := comSegredo("").ParseWebhook(corpo("999999"), certa); err != nil || ev.Type != payment.EventNone {
		t.Errorf("outro ponto de venda com ele configurado: (%+v, %v)", ev, err)
	}
}

func TestOAvisoPassaPeloEncaminhador(t *testing.T) {
	const ref = "pay_nnJJyqF1kzEWVwVNYyauMk"
	base := Config{CallbackURL: "https://api.exemplo.ao/gateways/emis", CallbackSecret: []byte("segredo-dos-avisos")}
	destino := New(base).Client().CallbackURLDe(ref)

	for nome, c := range map[string]struct{ relay, quer string }{
		"sem encaminhador": {"", destino},
		"com a marca":      {"https://relay.exemplo/v2/publish/{destino}?token=abc=", "https://relay.exemplo/v2/publish/" + destino + "?token=abc="},
		"sem a marca":      {" https://relay.exemplo/v2/publish/ ", "https://relay.exemplo/v2/publish/" + destino},
	} {
		cfg := base
		cfg.CallbackRelay = c.relay
		if got := New(cfg).Client().CallbackURLDe(ref); got != c.quer {
			t.Errorf("%s: %q, queria %q", nome, got, c.quer)
		}
	}
	// Sem segredo, o destino é o endereço fixo, também dentro do encaminhador.
	if got := New(Config{CallbackURL: "https://x.ao/cb", CallbackRelay: "https://r/{destino}"}).Client().CallbackURLDe(ref); got != "https://r/https://x.ao/cb" {
		t.Errorf("sem segredo: %q", got)
	}
}

func TestChargeMandaOEnderecoDoEncaminhador(t *testing.T) {
	srv, corpo := servidor(t, `{"id":"tok-1"}`, http.StatusOK)
	p := New(Config{
		Token: "token", CallbackURL: "https://api.exemplo.ao/gateways/emis",
		CallbackSecret: []byte("segredo-dos-avisos"), BaseURL: srv.URL,
		CallbackRelay: "https://relay.exemplo/v2/publish/{destino}?token=abc",
	})
	ref := "pay_nnJJyqF1kzEWVwVNYyauMk"
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{Reference: ref, Amount: money.FromMajor(100, money.AOA), Method: payment.MethodMCX}); err != nil {
		t.Fatal(err)
	}
	quer := "https://relay.exemplo/v2/publish/https://api.exemplo.ao/gateways/emis/" + p.Client().Assinatura(ref) + "?token=abc"
	if (*corpo)["callbackUrl"] != quer {
		t.Fatalf("callbackUrl = %v, queria %q", (*corpo)["callbackUrl"], quer)
	}
	// O aviso que o encaminhador entrega no endereço assinado paga como sem ele.
	ev, err := p.ParseWebhook(avisoAceite(`{"id":"`+ReferenciaCurta(ref)+`"}`), p.Client().Assinatura(ref))
	if err != nil || ev.Type != payment.EventChargeSucceeded || ev.Reference != ReferenciaCurta(ref) {
		t.Fatalf("aviso pelo encaminhador: (%+v, %v)", ev, err)
	}
}

// O endereço inteiro cabe nos 255 carateres da EMIS: com um encaminhador longo
// (o publish do QStash com um token de 128 carateres) o HMAC encurta, e o
// aviso continua a conferir; um HMAC encurtado de mais, ou adulterado, não.
func TestOEnderecoCabeNos255DaEMIS(t *testing.T) {
	ref := "pay_HdDfZpaOuNZH5NQsXgxP7K"
	relay := "https://qstash.upstash.io/v2/publish/{destino}?qstash_token=" + strings.Repeat("t", 128)
	p := New(Config{Token: "token", CallbackURL: "https://pay-api.vibra.ao/gateways/emis", CallbackSecret: []byte("segredo-dos-avisos"), CallbackRelay: relay})
	aviso := p.Client().CallbackURLDe(ref)
	if len(aviso) > TamanhoMaximoDoAviso {
		t.Fatalf("o endereço tem %d carateres: %s", len(aviso), aviso)
	}
	assinatura := p.Client().Assinatura(ref)
	mac := assinatura[strings.LastIndexByte(assinatura, '.')+1:]
	if len(aviso) != TamanhoMaximoDoAviso || len(mac) < TamanhoMinimoDoMAC || len(mac) >= tamanhoDoMAC {
		t.Fatalf("endereço %d, HMAC %d", len(aviso), len(mac))
	}
	if ev, err := p.ParseWebhook(avisoAceite(`{"id":"`+ReferenciaCurta(ref)+`"}`), assinatura); err != nil || ev.Type != payment.EventChargeSucceeded {
		t.Fatalf("o aviso com o HMAC encurtado: (%+v, %v)", ev, err)
	}
	// Sem encaminhador, o HMAC vai inteiro.
	inteiro := New(Config{CallbackURL: "https://pay-api.vibra.ao/gateways/emis", CallbackSecret: []byte("segredo-dos-avisos")}).Client().Assinatura(ref)
	if len(inteiro) != len(ReferenciaCurta(ref))+1+tamanhoDoMAC || !strings.HasPrefix(inteiro, assinatura) {
		t.Fatalf("sem encaminhador: %q, com: %q", inteiro, assinatura)
	}
	// O inteiro também confere, e um HMAC curto de mais, comprido de mais ou
	// com um caráter trocado não.
	if _, err := p.ParseWebhook(avisoAceite(`null`), inteiro); err != nil {
		t.Errorf("o HMAC inteiro: %v", err)
	}
	trocado := []byte(assinatura)
	trocado[len(trocado)-1] ^= 1
	for nome, a := range map[string]string{
		"curto de mais":    assinatura[:strings.LastIndexByte(assinatura, '.')+TamanhoMinimoDoMAC],
		"comprido de mais": inteiro + "A",
		"trocado":          string(trocado),
	} {
		if _, err := p.ParseWebhook(avisoAceite(`null`), a); !errors.Is(err, payment.ErrBadSignature) {
			t.Errorf("%s: %v, queria ErrBadSignature", nome, err)
		}
	}
}

// Quando nem o HMAC mais curto cabe, a cobrança recusa-se sem pedir o token.
func TestUmEnderecoQueNaoCabeNaoChegaAEMIS(t *testing.T) {
	srv, corpo := servidor(t, `{"id":"tok-1"}`, http.StatusOK)
	p := New(Config{
		Token: "token", CallbackURL: "https://pay-api.vibra.ao/gateways/emis", CallbackSecret: []byte("s"), BaseURL: srv.URL,
		CallbackRelay: "https://relay.exemplo/{destino}?t=" + strings.Repeat("t", 200),
	})
	_, err := p.Charge(context.Background(), payment.ChargeRequest{Reference: "pay_HdDfZpaOuNZH5NQsXgxP7K", Amount: money.FromMajor(100, money.AOA), Method: payment.MethodMCX})
	if !errors.Is(err, ErrCallbackTooLong) {
		t.Fatalf("erro = %v, queria ErrCallbackTooLong", err)
	}
	if len(*corpo) != 0 {
		t.Fatalf("o pedido chegou à EMIS: %v", *corpo)
	}
}
