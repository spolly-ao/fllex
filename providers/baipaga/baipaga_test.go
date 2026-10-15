package baipaga

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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

const chaveHMAC = "BancoBAI12345678"

// servidor devolve um BAI de mentira que grava o corpo e o caminho recebidos.
func servidor(t *testing.T, resposta string, estado int) (*httptest.Server, *map[string]any, *string) {
	t.Helper()
	recebido := map[string]any{}
	caminho := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caminho = r.URL.Path + "?" + r.URL.RawQuery
		if got := r.Header.Get(cabecalhoKey); got != "chave" {
			t.Errorf("cabeçalho da chave = %q", got)
		}
		_ = json.NewDecoder(r.Body).Decode(&recebido)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(estado)
		fmt.Fprint(w, resposta)
	}))
	t.Cleanup(srv.Close)
	return srv, &recebido, &caminho
}

func base(srv *httptest.Server) Config {
	return Config{APIKey: "chave", HMACKey: chaveHMAC, BaseURL: srv.URL}
}

func pedido() payment.ChargeRequest {
	return payment.ChargeRequest{
		Reference: "FACT_1023",
		Amount:    money.FromMajor(17000, money.AOA),
		Method:    payment.MethodBAIPaga,
		Customer:  payment.Customer{Phone: "+244 925 857 412"},
	}
}

// --- a assinatura -------------------------------------------------------------

func TestOAlgoritmoDaAssinaturaEOQueOExemploDoBAIProva(t *testing.T) {
	// A especificação diz "HMAC" e não nomeia o algoritmo: dá um exemplo com
	// chave, mensagem e resultado, e é dele que se deduz. Este é o único caso
	// oficial que existe, e é por isso que está aqui: uma mudança que o parta
	// parte a integração inteira.
	cb := Callback{
		ID:                "14234",
		Nonce:             "45287945845125",
		ExternalReference: "FACT_1023",
		Amount:            "17000",
		Currency:          "AKZ",
		LastChangeDate:    "2018-08-23T14:30:58.000+00:00",
		Status:            "SUCCESS",
	}
	cb.Merchant.ExternalID = "8f81cae4-4665-4db8-a4dd-a12b78515526"

	quero := "14234|45287945845125|FACT_1023|17000|AKZ|20180823143058|8f81cae4-4665-4db8-a4dd-a12b78515526|SUCCESS"
	if got := mensagemAssinada(cb); got != quero {
		t.Fatalf("mensagem = %q\n  queria = %q", got, quero)
	}

	cb.Signature = "7JV8XFrbdUj45H9VNm88KpOs/0idSgBaCLhxmwN6EJo="
	if !assinaturaValida(chaveHMAC, cb) {
		t.Error("a assinatura oficial do documento do BAI não passou")
	}
	// E com outra chave não passa, senão a verificação não verificava nada.
	if assinaturaValida("outra-chave", cb) {
		t.Error("uma chave errada não pode validar")
	}
}

func TestADataDaAssinaturaNaoEADataDoCallback(t *testing.T) {
	// O callback envia ISO 8601 com milissegundos e fuso; assina-se
	// yyyyMMddHHmmss, com a hora **tal como veio** e sem converter para UTC. O
	// exemplo do documento leva +01:00 e assina 160208, que é a hora local.
	casos := map[string]string{
		"2018-09-03T16:02:08.504+01:00": "20180903160208",
		"2018-09-03T16:02:08+01:00":     "20180903160208",
		"2018-07-18T15:35:08.507Z":      "20180718153508",
		"2018-09-03 16:02:08":           "20180903160208",
	}
	for entrada, quero := range casos {
		if got := dataDaAssinatura(entrada); got != quero {
			t.Errorf("%s = %q, queria %q", entrada, got, quero)
		}
	}
	// Uma data que não se lê passa como veio, para a assinatura falhar em vez
	// de passar: é o lado certo para falhar.
	if got := dataDaAssinatura("ontem"); got != "ontem" {
		t.Errorf("data ilegível = %q", got)
	}
}

func TestAssinaturaRecusaOQueNaoPodeVerificar(t *testing.T) {
	cb := Callback{ID: "1", Signature: "x"}
	if assinaturaValida("", cb) {
		t.Error("sem chave não se pode validar nada")
	}
	if assinaturaValida(chaveHMAC, Callback{ID: "1"}) {
		t.Error("sem assinatura não há o que validar")
	}
	if assinaturaValida(chaveHMAC, Callback{ID: "1", Signature: "não é base64!!"}) {
		t.Error("uma assinatura que não é base64 não pode passar")
	}
}

// --- cobrar -------------------------------------------------------------------

func TestChargeNotificaOClienteNoTelemovel(t *testing.T) {
	srv, corpo, caminho := servidor(t, `{"paymentId":4439}`, http.StatusOK)
	res, err := New(base(srv)).Charge(context.Background(), pedido())
	if err != nil {
		t.Fatalf("cobrança falhou: %v", err)
	}

	if !strings.HasPrefix(*caminho, pathPedido) {
		t.Errorf("caminho = %q, queria o do pedido sem confirmação", *caminho)
	}
	// O telemóvel vai na forma local de nove dígitos, e não no 244XXXXXXXXX do
	// Multicaixa Express: cada rede escolheu a sua.
	if (*corpo)["customerMsisdn"] != "925857412" {
		t.Errorf("telemóvel enviado = %v", (*corpo)["customerMsisdn"])
	}
	// A moeda vai como AKZ, que é como o BAI escreve o kwanza.
	if (*corpo)["currency"] != "AKZ" {
		t.Errorf("moeda enviada = %v, queria AKZ", (*corpo)["currency"])
	}
	// O montante vai como número, não como texto.
	if v, ok := (*corpo)["totalAmount"].(float64); !ok || v != 17000 {
		t.Errorf("montante enviado = %v (%T)", (*corpo)["totalAmount"], (*corpo)["totalAmount"])
	}
	if (*corpo)["externalReference"] != "FACT_1023" {
		t.Errorf("referência enviada = %v", (*corpo)["externalReference"])
	}
	// O carrinho vai sempre: é o que o cliente vê antes de autorizar.
	if _, ok := (*corpo)["shoppingCart"]; !ok {
		t.Error("o carrinho não foi enviado")
	}

	if res.Kind != payment.KindPending || res.Status != payment.StatusPending {
		t.Errorf("desfecho = %v/%v, queria pending/pending", res.Kind, res.Status)
	}
	if res.ProviderRef != "4439" || res.StatusRef != "FACT_1023" {
		t.Errorf("referências = %q e %q", res.ProviderRef, res.StatusRef)
	}
}

func TestChargeComConfirmacaoNaPaginaDevolveOEndereco(t *testing.T) {
	// A resposta do BAI traz a URL com aspas lá dentro, tal como o exemplo da
	// especificação a mostra. Passá-la assim ao browser dá um endereço que não
	// abre.
	resposta := `{"paymentId":4440,"confirmationUrl":"\"https://app.inm.pt/BAI-Paga-BOX/?paymentExternalId=f268b735\""}`
	srv, _, caminho := servidor(t, resposta, http.StatusOK)

	cfg := base(srv)
	cfg.ConfirmarNaPagina = true
	res, err := New(cfg).Charge(context.Background(), pedido())
	if err != nil {
		t.Fatalf("cobrança falhou: %v", err)
	}
	if !strings.HasPrefix(*caminho, pathIniciar) {
		t.Errorf("caminho = %q, queria o da confirmação na página", *caminho)
	}
	if res.Kind != payment.KindRedirect {
		t.Errorf("desfecho = %v, queria redirect", res.Kind)
	}
	if res.URL != "https://app.inm.pt/BAI-Paga-BOX/?paymentExternalId=f268b735" {
		t.Errorf("url = %q, ainda com aspas", res.URL)
	}
}

func TestChargeRespeitaOPrazoEAsLinhasDaFactura(t *testing.T) {
	srv, corpo, _ := servidor(t, `{"paymentId":1}`, http.StatusOK)
	prazo := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)

	req := pedido()
	req.ExpiresAt = &prazo
	req.Items = []payment.LineItem{
		{Description: "Plano Essencial", UnitPrice: money.FromMajor(8500, money.AOA), Quantity: 2},
		{Description: "Extra", UnitPrice: money.FromMajor(0, money.AOA)},
	}
	res, err := New(base(srv)).Charge(context.Background(), req)
	if err != nil {
		t.Fatalf("cobrança falhou: %v", err)
	}
	if res.ExpiresAt == nil || !res.ExpiresAt.Equal(prazo) || res.ExpiresAt == &prazo {
		t.Errorf("validade = %v (tem de ser uma cópia)", res.ExpiresAt)
	}
	carrinho, _ := (*corpo)["shoppingCart"].(map[string]any)
	itens, _ := carrinho["items"].([]any)
	if len(itens) != 2 {
		t.Fatalf("linhas enviadas = %v", carrinho)
	}
	primeira, _ := itens[0].(map[string]any)
	if primeira["count"].(float64) != 2 || primeira["totalAmount"].(float64) != 17000 {
		t.Errorf("primeira linha = %v", primeira)
	}
	// Uma quantidade a zero conta como uma, senão a linha some da factura.
	segunda, _ := itens[1].(map[string]any)
	if segunda["count"].(float64) != 1 {
		t.Errorf("segunda linha = %v", segunda)
	}
}

func TestChargeRecusaOQueNaoPodeCobrar(t *testing.T) {
	srv, _, _ := servidor(t, `{"paymentId":1}`, http.StatusOK)
	ok := money.FromMajor(100, money.AOA)

	semTelefone := pedido()
	semTelefone.Customer.Phone = "12345"

	casos := []struct {
		nome  string
		cfg   Config
		req   payment.ChargeRequest
		quero error
	}{
		{"sem chave de API", Config{HMACKey: chaveHMAC, BaseURL: srv.URL},
			pedido(), payment.ErrNotConfigured},
		{"sem chave de HMAC", Config{APIKey: "chave", BaseURL: srv.URL},
			pedido(), payment.ErrNotConfigured},
		{"outro método", base(srv),
			payment.ChargeRequest{Reference: "r", Amount: ok, Method: payment.MethodCard},
			payment.ErrUnsupportedMethod},
		{"outra moeda", base(srv),
			payment.ChargeRequest{Reference: "r", Amount: money.FromMajor(1, money.EUR)},
			payment.ErrUnsupportedCurrency},
		{"valor zero", base(srv),
			payment.ChargeRequest{Reference: "r", Amount: money.Zero(money.AOA)},
			payment.ErrAmountNotPositive},
		{"sem referência", base(srv),
			payment.ChargeRequest{Reference: "  ", Amount: ok}, ErrReferenceRequired},
		{"telemóvel que não é angolano", base(srv), semTelefone, ErrPhoneRequired},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if _, err := New(c.cfg).Charge(context.Background(), c.req); err != c.quero {
				t.Errorf("erro = %v, queria %v", err, c.quero)
			}
		})
	}
}

func TestOErroChegaComEstadoDuzentos(t *testing.T) {
	// É a armadilha desta API: o `code` vem no corpo e o HTTP é 200. Sem ler o
	// corpo, uma chave recusada passava por sucesso e a cobrança ficava
	// pendente à espera de um cliente que nunca foi notificado.
	srv, _, _ := servidor(t,
		`{"code":"INVALID_API_KEY","message":"API Key is invalid"}`, http.StatusOK)

	_, err := New(base(srv)).Charge(context.Background(), pedido())
	if err == nil {
		t.Fatal("um erro com HTTP 200 passou por sucesso")
	}
	var ge *payment.GatewayError
	if !errorAs(err, &ge) || ge.Code != "INVALID_API_KEY" {
		t.Fatalf("erro = %v", err)
	}
	if !strings.Contains(ge.Message, "API Key") {
		t.Errorf("mensagem = %q", ge.Message)
	}
}

func TestChargeFalhaQuandoNaoVemIdentificador(t *testing.T) {
	srv, _, _ := servidor(t, `{"code":"OK"}`, http.StatusOK)
	_, err := New(base(srv)).Charge(context.Background(), pedido())
	if err == nil || !strings.Contains(err.Error(), "sem identificador") {
		t.Fatalf("erro = %v", err)
	}
}

func TestChargeFalhaQuandoOGatewayRecusaPorHTTP(t *testing.T) {
	srv, _, _ := servidor(t, `{"message":"nada feito"}`, http.StatusBadGateway)
	if _, err := New(base(srv)).Charge(context.Background(), pedido()); err == nil {
		t.Fatal("um 502 devia falhar")
	}
}

// --- consultar ----------------------------------------------------------------

const estadoPago = `{"payment":{"id":195,"amount":17000,"currency":"AKZ",
	"lastChangeDate":"2018-07-18T15:35:08.507Z","status":"SUCCESS",
	"merchant":{"id":64,"externalId":"5b69","name":"Loja"},"maxReversible":0.01}}`

func TestVerifyChargeLeOEstadoNoBAI(t *testing.T) {
	srv, _, caminho := servidor(t, estadoPago, http.StatusOK)
	st, err := New(base(srv)).VerifyCharge(context.Background(), "FACT_1023", "pay_1")
	if err != nil {
		t.Fatalf("consulta falhou: %v", err)
	}
	if !strings.Contains(*caminho, "externalReference=FACT_1023") {
		t.Errorf("caminho = %q", *caminho)
	}
	if !st.Paid || st.Status != payment.StatusApproved {
		t.Errorf("estado = %v, pago = %v", st.Status, st.Paid)
	}
	// O valor volta em kwanzas, e é ele que apanha um pagamento parcial.
	if st.Amount == nil || st.Amount.Minor != money.FromMajor(17000, money.AOA).Minor {
		t.Errorf("valor = %v", st.Amount)
	}
	if st.PaidAt == nil {
		t.Error("um pagamento aprovado tem de dizer quando")
	}
}

func TestVerifyChargeSemReferenciaNaoInventaNada(t *testing.T) {
	srv, _, _ := servidor(t, estadoPago, http.StatusOK)
	p := New(base(srv))

	st, err := p.VerifyCharge(context.Background(), "", "")
	if err != nil || st.Paid {
		t.Errorf("sem referência = %v, %v", st, err)
	}
	// Com só a referência do comerciante, usa essa.
	if _, err := p.VerifyCharge(context.Background(), "  ", "pay_1"); err != nil {
		t.Errorf("com a referência do comerciante: %v", err)
	}
	// Sem configuração, nem se chega à rede.
	if _, err := New(Config{BaseURL: srv.URL}).VerifyCharge(context.Background(), "r", ""); err != payment.ErrNotConfigured {
		t.Errorf("sem configuração = %v", err)
	}
}

func TestVerifyChargeQuandoNaoHaPagamento(t *testing.T) {
	srv, _, _ := servidor(t, `{}`, http.StatusOK)
	if _, err := New(base(srv)).VerifyCharge(context.Background(), "r", ""); err != payment.ErrNotFound {
		t.Errorf("erro = %v, queria ErrNotFound", err)
	}
	srv2, _, _ := servidor(t, `{"code":"INVALID_PARAMETERS","message":"má"}`, http.StatusOK)
	if _, err := New(base(srv2)).VerifyCharge(context.Background(), "r", ""); err == nil {
		t.Error("um código de erro na consulta devia falhar")
	}
}

func TestVerifyChargeSemDataUsaAHoraDeAgora(t *testing.T) {
	srv, _, _ := servidor(t,
		`{"payment":{"id":1,"amount":"1","status":"COMPENSATED","lastChangeDate":"ontem"}}`, http.StatusOK)
	st, err := New(base(srv)).VerifyCharge(context.Background(), "r", "")
	if err != nil {
		t.Fatal(err)
	}
	// COMPENSATED é mais adiantado do que SUCCESS, nunca menos: conta como pago.
	if !st.Paid || st.PaidAt == nil {
		t.Errorf("estado = %+v", st)
	}
}

// --- o callback ---------------------------------------------------------------

func callbackXML(t *testing.T, estado, referencia string) []byte {
	t.Helper()
	cb := Callback{
		ID: "14234", Nonce: "45287945845125", ExternalReference: referencia,
		Amount: "17000", Currency: "AKZ",
		LastChangeDate: "2018-09-03T16:02:08.504+01:00", Status: estado,
	}
	cb.Merchant.ExternalID = "49fe0529"
	mac := hmac.New(sha256.New, []byte(chaveHMAC))
	mac.Write([]byte(mensagemAssinada(cb)))
	assinatura := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<paymentCallback>
  <amount>17000</amount>
  <currency>AKZ</currency>
  <description>Teste</description>
  <id>14234</id>
  <lastChangeDate>2018-09-03T16:02:08.504+01:00</lastChangeDate>
  <merchant><name>Loja</name><externalId>49fe0529</externalId></merchant>
  <status>%s</status>
  <externalReference>%s</externalReference>
  <nonce>45287945845125</nonce>
  <signature>%s</signature>
</paymentCallback>`, estado, referencia, assinatura))
}

func TestParseWebhookAceitaOCallbackAssinado(t *testing.T) {
	p := New(Config{APIKey: "chave", HMACKey: chaveHMAC})
	ev, err := p.ParseWebhook(callbackXML(t, "SUCCESS", "FACT_1023"), "")
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	if ev.Type != payment.EventChargeSucceeded || ev.Status != payment.StatusApproved {
		t.Errorf("evento = %v/%v", ev.Type, ev.Status)
	}
	if ev.Reference != "FACT_1023" || ev.ID != "14234" {
		t.Errorf("referências = %q e %q", ev.Reference, ev.ID)
	}
	if ev.Amount == nil || ev.Amount.Minor != money.FromMajor(17000, money.AOA).Minor {
		t.Errorf("valor = %v", ev.Amount)
	}
	if ev.OccurredAt == nil {
		t.Error("o evento devia dizer quando aconteceu")
	}
	if ev.Method != payment.MethodBAIPaga {
		t.Errorf("método = %q", ev.Method)
	}
}

func TestParteWebhookTraduzOsEstadosFinais(t *testing.T) {
	p := New(Config{APIKey: "chave", HMACKey: chaveHMAC})
	casos := map[string]payment.EventType{
		"SUCCESS":                    payment.EventChargeSucceeded,
		"COMPENSATED":                payment.EventChargeSucceeded,
		"ERROR":                      payment.EventChargeFailed,
		"REJECTED":                   payment.EventChargeFailed,
		"TIMEOUT":                    payment.EventChargeExpired,
		"EXPIRED":                    payment.EventChargeExpired,
		"PROCESSING":                 payment.EventNone,
		"PENDING_CHALLENGE_RESPONSE": payment.EventNone,
		"PENDING_SIGNATURE":          payment.EventNone,
		"UM_ESTADO_QUE_NAO_CONHECO":  payment.EventNone,
	}
	for estado, quero := range casos {
		ev, err := p.ParseWebhook(callbackXML(t, estado, "r"), "")
		if err != nil {
			t.Fatalf("%s: %v", estado, err)
		}
		if ev.Type != quero {
			t.Errorf("%s deu %v, queria %v", estado, ev.Type, quero)
		}
	}
}

func TestParseWebhookRecusaUmaAssinaturaQueNaoBate(t *testing.T) {
	// Aqui há chave partilhada e há como provar, ao contrário do callback da
	// EMIS: um corpo que não confere é forjado ou foi alterado em trânsito, e
	// as duas coisas merecem erro e não um evento ignorado.
	corpo := callbackXML(t, "SUCCESS", "FACT_1023")
	adulterado := strings.Replace(string(corpo), "<amount>17000</amount>", "<amount>1</amount>", 1)

	p := New(Config{APIKey: "chave", HMACKey: chaveHMAC})
	if _, err := p.ParseWebhook([]byte(adulterado), ""); err != payment.ErrBadSignature {
		t.Errorf("corpo adulterado = %v, queria ErrBadSignature", err)
	}
	// Com outra chave também não passa.
	outro := New(Config{APIKey: "chave", HMACKey: "outra"})
	if _, err := outro.ParseWebhook(corpo, ""); err != payment.ErrBadSignature {
		t.Errorf("chave errada = %v", err)
	}
}

func TestParseWebhookIgnoraOQueNaoEXML(t *testing.T) {
	p := New(Config{APIKey: "chave", HMACKey: chaveHMAC})
	ev, err := p.ParseWebhook([]byte("isto não é xml nenhum"), "")
	if err != nil {
		t.Fatalf("erro = %v, queria evento ignorado", err)
	}
	if !ev.Ignorable() {
		t.Errorf("evento = %v", ev.Type)
	}
}

func TestParseWebhookSemValorNaoInventaValor(t *testing.T) {
	cb := Callback{ID: "1", Nonce: "n", ExternalReference: "r", Currency: "AKZ", Status: "SUCCESS"}
	mac := hmac.New(sha256.New, []byte(chaveHMAC))
	mac.Write([]byte(mensagemAssinada(cb)))
	corpo := fmt.Sprintf(`<paymentCallback><id>1</id><nonce>n</nonce>`+
		`<externalReference>r</externalReference><currency>AKZ</currency>`+
		`<status>SUCCESS</status><signature>%s</signature></paymentCallback>`,
		base64.StdEncoding.EncodeToString(mac.Sum(nil)))

	ev, err := New(Config{APIKey: "c", HMACKey: chaveHMAC}).ParseWebhook([]byte(corpo), "")
	if err != nil {
		t.Fatal(err)
	}
	if ev.Amount != nil || ev.OccurredAt != nil {
		t.Errorf("valor = %v, data = %v", ev.Amount, ev.OccurredAt)
	}
	if ev.Type != payment.EventChargeSucceeded {
		t.Errorf("evento = %v", ev.Type)
	}
}

// --- o resto da superfície ------------------------------------------------------

func TestSuperficieDoProvider(t *testing.T) {
	p := New(Config{APIKey: "c", HMACKey: "k"})
	if p.Name() != "baipaga" {
		t.Errorf("nome = %q", p.Name())
	}
	if ms := p.Methods(); len(ms) != 1 || ms[0] != payment.MethodBAIPaga {
		t.Errorf("métodos = %v", ms)
	}
	if !p.SupportsCurrency("aoa") || p.SupportsCurrency(money.USD) {
		t.Error("moedas suportadas erradas")
	}
	if !p.Configured() || New(Config{}).Configured() {
		t.Error("configuração mal detectada")
	}
	if p.Client() == nil || p.Client().Config().APIKey != "c" {
		t.Error("cliente inacessível")
	}
	var _ payment.Provider = p
	var _ payment.Verifier = p
	var _ payment.WebhookParser = p
	if p2 := NewWithClient(p.Client()); p2.Client() != p.Client() {
		t.Error("NewWithClient não guardou o cliente")
	}
}

func TestClienteUsaOsValoresPorOmissao(t *testing.T) {
	c := NewClient(Config{APIKey: "c", HMACKey: "k"})
	if c.http.HTTP.Timeout != defaultTimeout {
		t.Errorf("timeout = %v", c.http.HTTP.Timeout)
	}
	// Zero repetições: uma segunda tentativa com a mesma referência externa
	// não cobra duas vezes, falha com EXISTING_EXTERNAL_REFERENCE.
	if c.http.MaxRetries != 0 {
		t.Errorf("repetições = %d", c.http.MaxRetries)
	}
	outro := NewClient(Config{APIKey: "c", HMACKey: "k", BaseURL: QABaseURL, Timeout: time.Second})
	if outro.http.HTTP.Timeout != time.Second {
		t.Errorf("timeout = %v", outro.http.HTTP.Timeout)
	}
}

func TestUmCertificadoQueNaoCasaFalhaLogo(t *testing.T) {
	// A falha aparece na primeira cobrança e não desaparece no construtor: um
	// certificado mal montado tem de dar erro, e não uma ligação sem
	// certificado que o banco recusa com outra mensagem.
	p := New(Config{
		APIKey: "c", HMACKey: "k",
		ClientCertPEM: []byte("isto não é um certificado"),
		ClientKeyPEM:  []byte("nem isto é uma chave"),
	})
	if p.Configured() {
		t.Fatal("um certificado inválido não pode dar um provider configurado")
	}
	if _, err := p.Charge(context.Background(), pedido()); err != payment.ErrNotConfigured {
		t.Errorf("erro = %v", err)
	}
	// E o erro do certificado sai quando se tenta usar o cliente directamente.
	c := NewClient(Config{APIKey: "c", HMACKey: "k", ClientCertPEM: []byte("x"), ClientKeyPEM: []byte("y")})
	if _, err := c.Estado(context.Background(), "r"); err == nil ||
		!strings.Contains(err.Error(), "certificado de cliente inválido") {
		t.Errorf("erro do certificado = %v", err)
	}
}

// --- os extras que não cabem na interface -----------------------------------------

func TestValidarMSISDN(t *testing.T) {
	srv, _, caminho := servidor(t, `{"valid":true}`, http.StatusOK)
	c := NewClient(base(srv))

	ok, err := c.ValidarMSISDN(context.Background(), "925857412")
	if err != nil || !ok {
		t.Fatalf("validação = %v, %v", ok, err)
	}
	if !strings.Contains(*caminho, "/msisdn/925857412/validate") {
		t.Errorf("caminho = %q", *caminho)
	}

	srv2, _, _ := servidor(t, `{"valid":false}`, http.StatusOK)
	if ok, _ := NewClient(base(srv2)).ValidarMSISDN(context.Background(), "912"); ok {
		t.Error("um número sem BAI PAGA devia dar falso")
	}
	srv3, _, _ := servidor(t, `{"code":"INVALID_MSISDN","message":"mau"}`, http.StatusOK)
	if _, err := NewClient(base(srv3)).ValidarMSISDN(context.Background(), "912"); err == nil {
		t.Error("um código de erro devia falhar")
	}
}

func TestQRCode(t *testing.T) {
	srv, corpo, caminho := servidor(t,
		`{"imageExtension":"jpg","encodeToString":"/9j/4AAQ"}`, http.StatusOK)
	c := NewClient(Config{APIKey: "chave", HMACKey: "k", BaseURL: srv.URL, AcceptancePointID: 148})

	res, err := c.QRCode(context.Background(), "Teste-208", decimalJSON(money.FromMajor(200, money.AOA)), 500, 500)
	if err != nil {
		t.Fatalf("QR falhou: %v", err)
	}
	if res.EncodeToString != "/9j/4AAQ" || res.ImageExtension != "jpg" {
		t.Errorf("resposta = %+v", res)
	}
	if !strings.HasPrefix(*caminho, pathQRCode) {
		t.Errorf("caminho = %q", *caminho)
	}
	if (*corpo)["acceptancePointId"].(float64) != 148 {
		t.Errorf("ponto de aceitação = %v", (*corpo)["acceptancePointId"])
	}
	if (*corpo)["currency"] != "AKZ" {
		t.Errorf("moeda = %v", (*corpo)["currency"])
	}

	srv2, corpo2, _ := servidor(t, `{"encodeToString":""}`, http.StatusOK)
	if _, err := NewClient(base(srv2)).QRCode(context.Background(), "r", decimalJSON(money.FromMajor(1, money.AOA)), 1, 1); err == nil {
		t.Error("uma resposta sem imagem devia falhar")
	}
	if _, existe := (*corpo2)["acceptancePointId"]; existe {
		t.Error("sem ponto de aceitação configurado, o campo não vai")
	}
	srv3, _, _ := servidor(t, `{"code":"FATAL","message":"interno"}`, http.StatusOK)
	if _, err := NewClient(base(srv3)).QRCode(context.Background(), "r", decimalJSON(money.FromMajor(1, money.AOA)), 1, 1); err == nil {
		t.Error("um código de erro devia falhar")
	}
}

func TestTraducoesInternas(t *testing.T) {
	if got := itoa(0); got != "" {
		t.Errorf("itoa(0) = %q, queria vazio", got)
	}
	if got := itoa(-4439); got != "-4439" {
		t.Errorf("itoa(-4439) = %q", got)
	}
	if _, ok := montanteDe(json.RawMessage("null")); ok {
		t.Error("nulo não é um montante")
	}
	if _, ok := montanteDe(json.RawMessage(`"abc"`)); ok {
		t.Error("texto que não é número não é um montante")
	}
	if v, ok := montanteDe(json.RawMessage(`"1545.25"`)); !ok || v.Minor != 154525 {
		t.Errorf("montante entre aspas = %v, %v", v, ok)
	}
}

// errorAs evita importar errors só para uma asserção.
func errorAs(err error, alvo **payment.GatewayError) bool {
	ge, ok := err.(*payment.GatewayError)
	if ok {
		*alvo = ge
	}
	return ok
}

func TestUmCodigoSemMensagemUsaOProprioCodigo(t *testing.T) {
	// Acontece: o BAI devolve o código e deixa a mensagem vazia. Um erro sem
	// texto nenhum não diz nada a quem faz suporte.
	srv, _, _ := servidor(t, `{"code":"unknown","message":"   "}`, http.StatusOK)
	_, err := New(base(srv)).Charge(context.Background(), pedido())
	var ge *payment.GatewayError
	if !errorAs(err, &ge) {
		t.Fatalf("erro = %v", err)
	}
	if ge.Code != "UNKNOWN" || ge.Message != "UNKNOWN" {
		t.Errorf("erro = %+v, queria o código no lugar da mensagem", ge)
	}
}
