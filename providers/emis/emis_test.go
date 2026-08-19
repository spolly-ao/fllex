package emis

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// servidor devolve um gateway de mentira que grava o corpo que recebeu.
func servidor(t *testing.T, resposta string, estado int) (*httptest.Server, *map[string]any) {
	t.Helper()
	recebido := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != pathFrameToken {
			t.Errorf("pedido inesperado: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&recebido)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(estado)
		fmt.Fprint(w, resposta)
	}))
	t.Cleanup(srv.Close)
	return srv, &recebido
}

func TestChargePedeTokenEDevolveOFrame(t *testing.T) {
	srv, corpo := servidor(t, `{"id":"tok-abc-123"}`, http.StatusOK)

	p := New(Config{
		Token:       "token-do-comerciante",
		POSID:       "433220",
		CallbackURL: "https://api.fllex.ao/v1/webhooks/emis",
		BaseURL:     srv.URL,
	})
	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "ch_7",
		Amount:    money.FromMajor(5900, money.AOA),
		Method:    payment.MethodMCX,
	})
	if err != nil {
		t.Fatalf("cobrança falhou: %v", err)
	}

	// O que se verifica é o que **foi enviado**: é aí que os enganos custam
	// dinheiro, e não no que devolvemos a nós próprios.
	if (*corpo)["reference"] != "ch_7" {
		t.Errorf("referência enviada = %v", (*corpo)["reference"])
	}
	// Decimal com duas casas, como a integração de referência faz com o
	// toFixed(2). Mandar 5900 em vez de 5900.00 é uma recusa do gateway.
	if (*corpo)["amount"] != "5900.00" {
		t.Errorf("valor enviado = %v, queria \"5900.00\"", (*corpo)["amount"])
	}
	if (*corpo)["token"] != "token-do-comerciante" {
		t.Errorf("token enviado = %v", (*corpo)["token"])
	}
	if (*corpo)["mobile"] != "PAYMENT" || (*corpo)["card"] != "DISABLED" {
		t.Errorf("canais = mobile:%v card:%v", (*corpo)["mobile"], (*corpo)["card"])
	}
	if (*corpo)["callbackUrl"] != "https://api.fllex.ao/v1/webhooks/emis" {
		t.Errorf("callback enviado = %v", (*corpo)["callbackUrl"])
	}
	// O `cssUrl` vai sempre, mesmo vazio: sem o campo o gateway recusa.
	if _, ok := (*corpo)["cssUrl"]; !ok {
		t.Error("o cssUrl não foi enviado")
	}

	if res.Kind != payment.KindRedirect || res.Status != payment.StatusPending {
		t.Errorf("desfecho = %v/%v, queria redirect/pending", res.Kind, res.Status)
	}
	if res.ProviderRef != "tok-abc-123" {
		t.Errorf("token guardado = %q", res.ProviderRef)
	}
	if res.Reference != "ch_7" {
		t.Errorf("referência guardada = %q", res.Reference)
	}
	if !strings.HasSuffix(res.URL, pathFrame+"?token=tok-abc-123") {
		t.Errorf("url do frame = %q", res.URL)
	}
	// Sem prazo pedido, não se inventa nenhum.
	if res.ExpiresAt != nil {
		t.Errorf("validade = %v, queria nenhuma", res.ExpiresAt)
	}
}

func TestChargeRespeitaOPrazoPedido(t *testing.T) {
	srv, _ := servidor(t, `{"id":"tok"}`, http.StatusOK)
	p := New(Config{Token: "t", BaseURL: srv.URL})

	prazo := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "ch_1", Amount: money.FromMajor(100, money.AOA), ExpiresAt: &prazo,
	})
	if err != nil {
		t.Fatalf("cobrança falhou: %v", err)
	}
	if res.ExpiresAt == nil || !res.ExpiresAt.Equal(prazo) {
		t.Errorf("validade = %v, queria %v", res.ExpiresAt, prazo)
	}
	// A cópia protege quem chama: mexer no resultado não pode mexer no pedido.
	if res.ExpiresAt == &prazo {
		t.Error("a validade partilha o apontador com o pedido")
	}
}

func TestChargeLigaOCartaoQuandoPedido(t *testing.T) {
	srv, corpo := servidor(t, `{"id":"tok"}`, http.StatusOK)
	p := New(Config{Token: "t", BaseURL: srv.URL, Card: true, CSSURL: "https://loja.ao/frame.css"})

	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "ch_1", Amount: money.FromMajor(100, money.AOA),
	}); err != nil {
		t.Fatalf("cobrança falhou: %v", err)
	}
	if (*corpo)["card"] != "PAYMENT" {
		t.Errorf("cartão = %v, queria PAYMENT", (*corpo)["card"])
	}
	if (*corpo)["cssUrl"] != "https://loja.ao/frame.css" {
		t.Errorf("cssUrl = %v", (*corpo)["cssUrl"])
	}
}

func TestChargeRecusaOQueNaoPodeCobrar(t *testing.T) {
	srv, _ := servidor(t, `{"id":"tok"}`, http.StatusOK)
	ok := money.FromMajor(100, money.AOA)

	casos := []struct {
		nome  string
		cfg   Config
		req   payment.ChargeRequest
		quero error
	}{
		{"sem token", Config{BaseURL: srv.URL},
			payment.ChargeRequest{Reference: "ch", Amount: ok}, payment.ErrNotConfigured},
		{"outro método", Config{Token: "t", BaseURL: srv.URL},
			payment.ChargeRequest{Reference: "ch", Amount: ok, Method: payment.MethodCard},
			payment.ErrUnsupportedMethod},
		{"outra moeda", Config{Token: "t", BaseURL: srv.URL},
			payment.ChargeRequest{Reference: "ch", Amount: money.FromMajor(100, money.EUR)},
			payment.ErrUnsupportedCurrency},
		{"valor zero", Config{Token: "t", BaseURL: srv.URL},
			payment.ChargeRequest{Reference: "ch", Amount: money.Zero(money.AOA)},
			payment.ErrAmountNotPositive},
		{"sem referência", Config{Token: "t", BaseURL: srv.URL},
			payment.ChargeRequest{Reference: "   ", Amount: ok}, ErrReferenceRequired},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			_, err := New(c.cfg).Charge(context.Background(), c.req)
			if err != c.quero {
				t.Errorf("erro = %v, queria %v", err, c.quero)
			}
		})
	}
}

func TestChargeFalhaQuandoOGatewayRecusa(t *testing.T) {
	srv, _ := servidor(t, `{"message":"token inválido"}`, http.StatusUnauthorized)
	p := New(Config{Token: "t", BaseURL: srv.URL})

	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "ch_1", Amount: money.FromMajor(100, money.AOA),
	})
	if err == nil || !strings.Contains(err.Error(), "token inválido") {
		t.Fatalf("erro = %v, queria a mensagem do gateway", err)
	}
}

func TestChargeFalhaQuandoNaoVemToken(t *testing.T) {
	// 200 sem `id` é o caso que, aceite, deixava um pagamento pendente que
	// ninguém consegue pagar: não há frame para abrir.
	srv, _ := servidor(t, `{"status":"ok"}`, http.StatusOK)
	p := New(Config{Token: "t", BaseURL: srv.URL})

	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "ch_1", Amount: money.FromMajor(100, money.AOA),
	})
	if err == nil || !strings.Contains(err.Error(), "sem token de frame") {
		t.Fatalf("erro = %v", err)
	}
}

// --- o callback -------------------------------------------------------------

// callback é o corpo tal como a EMIS o envia, com `reference` como objecto.
const callback = `{
  "id": "3d4f8e51",
  "reference": {"id": "ch_7"},
  "amount": 5900.00,
  "status": "ACCEPTED",
  "transactionType": "PAYMENT",
  "currency": "AOA",
  "merchantReferenceNumber": "MRN-991",
  "pointOfSale": {"id": "433220"},
  "clearingPeriod": "2026-08-19"
}`

func provider(t *testing.T, pos string) *Provider {
	t.Helper()
	return New(Config{Token: "t", POSID: pos})
}

func TestParseWebhookAceitaOPagamentoDaNossaConta(t *testing.T) {
	ev, err := provider(t, "433220").ParseWebhook([]byte(callback), "")
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	if ev.Type != payment.EventChargeSucceeded || ev.Status != payment.StatusApproved {
		t.Errorf("evento = %v/%v", ev.Type, ev.Status)
	}
	// A referência é a nossa, e vem de dentro do objecto `reference`. Lê-la
	// como string dava vazio, e um evento sem referência é um pagamento que
	// ninguém encontra.
	if ev.Reference != "ch_7" {
		t.Errorf("referência = %q, queria ch_7", ev.Reference)
	}
	if ev.ID != "3d4f8e51" || ev.ChargeRef != "3d4f8e51" {
		t.Errorf("identificadores = %q/%q", ev.ID, ev.ChargeRef)
	}
	if ev.Method != payment.MethodMCX {
		t.Errorf("método = %q", ev.Method)
	}
	// O valor tem de vir preenchido: é com ele que quem confirma apanha um
	// callback com a referência certa e o valor errado.
	if ev.Amount == nil || ev.Amount.Minor != money.FromMajor(5900, money.AOA).Minor {
		t.Errorf("valor = %v, queria 5900 AOA", ev.Amount)
	}
	if ev.Raw["merchantReferenceNumber"] != "MRN-991" {
		t.Errorf("corpo original perdido: %v", ev.Raw)
	}
}

func TestParseWebhookRecusaOQueNaoEDestaConta(t *testing.T) {
	troca := func(de, para string) string { return strings.Replace(callback, de, para, 1) }

	casos := []struct {
		nome  string
		pos   string
		corpo string
	}{
		{"corpo que não é JSON", "433220", `nada disto é json`},
		{"sem ponto de venda configurado", "", callback},
		{"ponto de venda de outra conta", "433220", troca(`"id": "433220"`, `"id": "999999"`)},
		{"moeda que não é kwanza", "433220", troca(`"currency": "AOA"`, `"currency": "USD"`)},
		{"autorização em vez de pagamento", "433220", troca(`"transactionType": "PAYMENT"`, `"transactionType": "AUTHORIZATION"`)},
		{"recusado pelo cliente", "433220", troca(`"status": "ACCEPTED"`, `"status": "REJECTED"`)},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			ev, err := provider(t, c.pos).ParseWebhook([]byte(c.corpo), "")
			if err != nil {
				t.Fatalf("erro = %v, queria evento ignorado e não erro", err)
			}
			if !ev.Ignorable() {
				t.Errorf("evento = %v, queria ignorado", ev.Type)
			}
		})
	}
}

func TestParseWebhookAceitaOsCamposComoTexto(t *testing.T) {
	// O mesmo corpo com o valor e o ponto de venda entre aspas. É como a
	// produção os manda, e um tipo fixo perdia o pagamento por causa disso.
	corpo := strings.NewReplacer(
		`"amount": 5900.00`, `"amount": "5900.00"`,
		`"id": "433220"`, `"id": 433220`,
	).Replace(callback)

	ev, _ := provider(t, "433220").ParseWebhook([]byte(corpo), "")
	if ev.Type != payment.EventChargeSucceeded {
		t.Fatalf("evento = %v, queria aceite", ev.Type)
	}
	if ev.Amount == nil || ev.Amount.Minor != money.FromMajor(5900, money.AOA).Minor {
		t.Errorf("valor = %v", ev.Amount)
	}
}

func TestParseWebhookSemValorNaoInventaValor(t *testing.T) {
	corpo := strings.Replace(callback, `"amount": 5900.00`, `"amount": null`, 1)
	ev, _ := provider(t, "433220").ParseWebhook([]byte(corpo), "")
	if ev.Amount != nil {
		t.Errorf("valor = %v, queria nenhum", ev.Amount)
	}
	// Sem valor o evento passa na mesma: quem confirma é que decide se aceita
	// uma confirmação sem valor.
	if ev.Type != payment.EventChargeSucceeded {
		t.Errorf("evento = %v", ev.Type)
	}
}

// --- o resto da superfície --------------------------------------------------

func TestSuperficieDoProvider(t *testing.T) {
	p := New(Config{Token: "t"})
	if p.Name() != "emis" {
		t.Errorf("nome = %q", p.Name())
	}
	if ms := p.Methods(); len(ms) != 1 || ms[0] != payment.MethodMCX {
		t.Errorf("métodos = %v", ms)
	}
	if !p.SupportsCurrency("aoa") || p.SupportsCurrency(money.USD) {
		t.Error("moedas suportadas erradas")
	}
	if !p.Configured() || New(Config{}).Configured() {
		t.Error("configuração mal detectada")
	}
	if p.Client() == nil {
		t.Error("cliente inacessível")
	}

	// O provider implementa o que diz implementar, e nada mais. A ausência de
	// Verifier é deliberada: a EMIS não tem consulta de estado, e um verificador
	// que não verifica seria pior do que nenhum.
	var _ payment.Provider = p
	var _ payment.WebhookParser = p
	if _, ok := any(p).(payment.Verifier); ok {
		t.Error("o provider anuncia consulta de estado que a EMIS não tem")
	}
}

func TestClienteUsaOsValoresPorOmissao(t *testing.T) {
	c := NewClient(Config{Token: "t"})
	if c.http.HTTP.Timeout != defaultTimeout {
		t.Errorf("timeout = %v", c.http.HTTP.Timeout)
	}
	// Zero repetições: um segundo token de frame é uma segunda forma de pagar
	// o mesmo pedido.
	if c.http.MaxRetries != 0 {
		t.Errorf("repetições = %d, queria 0", c.http.MaxRetries)
	}
	if got := c.FrameURL("t o k"); got != DefaultBaseURL+pathFrame+"?token=t+o+k" {
		t.Errorf("url do frame = %q", got)
	}
	if c.Config().Token != "t" {
		t.Error("configuração inacessível")
	}

	comBarra := NewClient(Config{Token: "t", BaseURL: "https://ensaio.emis.co.ao/", Timeout: time.Second})
	if got := comBarra.FrameURL("x"); got != "https://ensaio.emis.co.ao"+pathFrame+"?token=x" {
		t.Errorf("url com barra final = %q", got)
	}
	if comBarra.http.HTTP.Timeout != time.Second {
		t.Errorf("timeout = %v", comBarra.http.HTTP.Timeout)
	}

	if p := NewWithClient(c); p.Client() != c {
		t.Error("NewWithClient não guardou o cliente")
	}
}

func TestTextoLeAsFormasQueChegam(t *testing.T) {
	casos := map[string]string{
		`"433220"`:           "433220",
		`433220`:             "433220",
		`5900.00`:            "5900.00",
		`123456789012345678`: "123456789012345678", // não passa por float
		`null`:               "",
		`true`:               "",
		`{"id":"x"}`:         "",
		`[1,2]`:              "",
	}
	for entrada, quero := range casos {
		var got Texto
		if err := json.Unmarshal([]byte(entrada), &got); err != nil {
			t.Fatalf("%s: erro = %v", entrada, err)
		}
		if got.String() != quero {
			t.Errorf("%s = %q, queria %q", entrada, got, quero)
		}
	}
}
