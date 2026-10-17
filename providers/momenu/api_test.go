package momenu

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// Os testes do que a API do MoMenu passou a exigir em 2026, e do que a nossa
// integração deixou de fazer bem por causa disso.

const segredoWebhook = "segredo-da-conta"

// fixo é a hora com que os testes do carimbo correm, para o relógio não decidir
// se um teste passa.
var fixo = time.Date(2026, time.October, 15, 10, 0, 0, 0, time.UTC)

func comRelogio(t *testing.T, h http.HandlerFunc) (*Provider, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	p := New(Config{
		APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second,
		WebhookSecret: segredoWebhook,
		Now:           func() time.Time { return fixo },
	})
	return p, srv.Close
}

// assina devolve o corpo e os cabeçalhos como o MoMenu os envia.
func assina(corpo string, quando time.Time) (string, map[string]string) {
	carimbo := quando.Format(time.RFC3339)
	mac := hmac.New(sha256.New, []byte(segredoWebhook))
	mac.Write([]byte(carimbo))
	mac.Write([]byte("."))
	mac.Write([]byte(corpo))
	return corpo, map[string]string{
		cabecalhoAssinatura: hex.EncodeToString(mac.Sum(nil)),
		cabecalhoCarimbo:    carimbo,
		cabecalhoSegredo:    segredoWebhook,
	}
}

// --- a assinatura do webhook --------------------------------------------------

func TestWebhookAssinadoEAceito(t *testing.T) {
	p := New(Config{APIKey: "c", WebhookSecret: segredoWebhook, Now: func() time.Time { return fixo }})
	corpo, h := assina(`{"event":"payment.confirmed","merchantTransactionId":"tx-1",`+
		`"operationStatus":"1","externalReferenceOpcional":"pay_1","invoiceUrl":"https://f/1"}`, fixo)

	ev, err := p.ParseWebhookWithHeaders([]byte(corpo), h)
	if err != nil {
		t.Fatalf("leitura falhou: %v", err)
	}
	if ev.Type != payment.EventChargeSucceeded || ev.Status != payment.StatusApproved {
		t.Errorf("evento = %v/%v", ev.Type, ev.Status)
	}
	// A nossa referência volta no webhook, e é por ela que o evento se liga à
	// cobrança sem depender só do identificador deles.
	if ev.Reference != "pay_1" {
		t.Errorf("referência = %q, queria pay_1", ev.Reference)
	}
	if ev.ChargeRef != "tx-1" || ev.InvoiceURL == "" {
		t.Errorf("evento = %+v", ev)
	}
}

// O aviso de uma referência traz o estado por nome, e não por número: é a
// forma do aviso que o MoMenu mandou a uma referência paga pelo multicaixa.
func TestWebhookDeReferenciaComEstadoPorNome(t *testing.T) {
	p := New(Config{APIKey: "c", WebhookSecret: segredoWebhook, Now: func() time.Time { return fixo }})
	casos := []struct {
		estado string
		tipo   payment.EventType
		status payment.Status
	}{
		{"paid", payment.EventChargeSucceeded, payment.StatusApproved},
		{"PAID", payment.EventChargeSucceeded, payment.StatusApproved},
		{"failed", payment.EventChargeFailed, payment.StatusRejected},
		{"open", payment.EventNone, payment.StatusPending},
	}
	for _, c := range casos {
		corpo, h := assina(`{"merchantTransactionId":"6fd551efad1e","operationStatus":"`+c.estado+`",`+
			`"operationData":{"transactionId":"t","processor":"emis-reference","statusReason":"2000",`+
			`"amount":"1000.0","currency":"aoa"},"operationId":"o","event":"payment.confirmed"}`, fixo)
		ev, err := p.ParseWebhookWithHeaders([]byte(corpo), h)
		if err != nil {
			t.Fatalf("%s: leitura falhou: %v", c.estado, err)
		}
		if ev.Type != c.tipo || ev.Status != c.status {
			t.Errorf("%s: evento = %v/%v, queria %v/%v", c.estado, ev.Type, ev.Status, c.tipo, c.status)
		}
		if ev.ID != "6fd551efad1e" || ev.ChargeRef != "6fd551efad1e" || ev.Reference != "6fd551efad1e" {
			t.Errorf("%s: identificadores = %+v", c.estado, ev)
		}
	}
}

func TestWebhookSemAssinaturaOuComAssinaturaErradaERecusado(t *testing.T) {
	// É a correcção que mais importa: é o webhook que marca uma referência como
	// paga, por isso aceitá-lo sem verificar é uma forma de qualquer pessoa
	// marcar pagamentos como pagos.
	p := New(Config{APIKey: "c", WebhookSecret: segredoWebhook, Now: func() time.Time { return fixo }})
	corpo, h := assina(`{"merchantTransactionId":"tx-1","operationStatus":"1"}`, fixo)

	casos := map[string]map[string]string{
		"sem cabeçalhos":        {},
		"sem carimbo":           {cabecalhoAssinatura: h[cabecalhoAssinatura]},
		"sem assinatura":        {cabecalhoCarimbo: h[cabecalhoCarimbo]},
		"assinatura de outrem":  {cabecalhoAssinatura: strings.Repeat("ab", 32), cabecalhoCarimbo: h[cabecalhoCarimbo]},
		"assinatura não é hexa": {cabecalhoAssinatura: "não é hexadecimal", cabecalhoCarimbo: h[cabecalhoCarimbo]},
		"carimbo ilegível":      {cabecalhoAssinatura: h[cabecalhoAssinatura], cabecalhoCarimbo: "ontem"},
	}
	for nome, cab := range casos {
		t.Run(nome, func(t *testing.T) {
			if _, err := p.ParseWebhookWithHeaders([]byte(corpo), cab); !errors.Is(err, payment.ErrBadSignature) {
				t.Errorf("erro = %v, queria assinatura inválida", err)
			}
		})
	}

	// E o corpo alterado em trânsito também não passa.
	adulterado := strings.Replace(corpo, `"operationStatus":"1"`, `"operationStatus":"4"`, 1)
	if _, err := p.ParseWebhookWithHeaders([]byte(adulterado), h); !errors.Is(err, payment.ErrBadSignature) {
		t.Errorf("corpo adulterado = %v", err)
	}
}

func TestWebhookSemSegredoConfiguradoNaoAceitaNada(t *testing.T) {
	// Sem segredo não há como verificar, e um provider que aceita o que não
	// consegue verificar é pior do que um que recusa tudo.
	p := New(Config{APIKey: "c", Now: func() time.Time { return fixo }})
	corpo, h := assina(`{"operationStatus":"1"}`, fixo)
	if _, err := p.ParseWebhookWithHeaders([]byte(corpo), h); !errors.Is(err, payment.ErrBadSignature) {
		t.Errorf("erro = %v", err)
	}
}

func TestOCarimboTemDeEstarDentroDaJanela(t *testing.T) {
	p := New(Config{APIKey: "c", WebhookSecret: segredoWebhook, Now: func() time.Time { return fixo }})

	// Dentro da janela, nos dois sentidos. Cinco minutos, que é o que o MoMenu
	// sugere na documentação.
	for _, desvio := range []time.Duration{0, -4 * time.Minute, 4 * time.Minute} {
		corpo, h := assina(`{"operationStatus":"1"}`, fixo.Add(desvio))
		if _, err := p.ParseWebhookWithHeaders([]byte(corpo), h); err != nil {
			t.Errorf("desvio de %v devia passar: %v", desvio, err)
		}
	}
	// Fora da janela, nos dois sentidos. O futuro também se recusa: um evento
	// assinado hoje não pode valer dentro de um ano.
	for _, desvio := range []time.Duration{-10 * time.Minute, 10 * time.Minute} {
		corpo, h := assina(`{"operationStatus":"1"}`, fixo.Add(desvio))
		if _, err := p.ParseWebhookWithHeaders([]byte(corpo), h); !errors.Is(err, payment.ErrBadSignature) {
			t.Errorf("desvio de %v devia ser recusado", desvio)
		}
	}
	// A janela é configurável, para quem souber o espaçamento das reentregas.
	apertado := New(Config{
		APIKey: "c", WebhookSecret: segredoWebhook,
		WebhookTolerance: time.Minute, Now: func() time.Time { return fixo },
	})
	corpo, h := assina(`{"operationStatus":"1"}`, fixo.Add(-3*time.Minute))
	if _, err := apertado.ParseWebhookWithHeaders([]byte(corpo), h); err == nil {
		t.Error("com janela de um minuto, três minutos devia ser recusado")
	}
}

func TestOsEstadosDeFalhaDeixamDeSerIgnorados(t *testing.T) {
	// O MoMenu envia os estados que não são de sucesso **sem o campo `event`**.
	// A condição antiga exigia os dois, por isso cancelamentos e recusas
	// passavam em silêncio e a cobrança ficava a expirar.
	p := New(Config{APIKey: "c", WebhookSecret: segredoWebhook, Now: func() time.Time { return fixo }})

	casos := []struct {
		estado string
		tipo   payment.EventType
		st     payment.Status
	}{
		{"1", payment.EventChargeSucceeded, payment.StatusApproved},
		{"3", payment.EventChargeExpired, payment.StatusExpired},
		{"4", payment.EventChargeFailed, payment.StatusRejected},
		{"5", payment.EventChargeFailed, payment.StatusRejected},
		{"0", payment.EventNone, payment.StatusPending},
		{"", payment.EventNone, payment.StatusPending},
	}
	for _, c := range casos {
		// Sem o campo `event`, que é como eles chegam.
		corpo, h := assina(fmt.Sprintf(`{"merchantTransactionId":"tx-1","operationStatus":%q}`, c.estado), fixo)
		ev, err := p.ParseWebhookWithHeaders([]byte(corpo), h)
		if err != nil {
			t.Fatalf("estado %q: %v", c.estado, err)
		}
		if ev.Type != c.tipo || ev.Status != c.st {
			t.Errorf("estado %q deu %v/%v, queria %v/%v", c.estado, ev.Type, ev.Status, c.tipo, c.st)
		}
	}
}

func TestWebhookComCorpoQueNaoEJSONEIgnorado(t *testing.T) {
	p := New(Config{APIKey: "c", WebhookSecret: segredoWebhook, Now: func() time.Time { return fixo }})
	corpo, h := assina("isto não é json", fixo)
	ev, err := p.ParseWebhookWithHeaders([]byte(corpo), h)
	if err != nil {
		t.Fatalf("erro = %v, queria evento ignorado", err)
	}
	if !ev.Ignorable() {
		t.Errorf("evento = %v", ev.Type)
	}
}

func TestParseWebhookSemCabecalhosRecusaSempre(t *testing.T) {
	// A assinatura do MoMenu está partida por dois cabeçalhos, e a interface
	// antiga só passa um. Devolver um evento como se fosse inofensivo é o que
	// esta integração fazia.
	p := New(Config{APIKey: "c", WebhookSecret: segredoWebhook})
	if _, err := p.ParseWebhook([]byte(`{"operationStatus":"1"}`), "assinatura"); !errors.Is(err, payment.ErrBadSignature) {
		t.Errorf("erro = %v, queria assinatura inválida", err)
	}
	// E anuncia as duas capacidades, para quem só tiver a assinatura à mão
	// receber a recusa em vez de um nil.
	var _ payment.WebhookParser = p
	var _ payment.WebhookHeaderParser = p
}

func TestOCabecalhoLeSeComQualquerCapitalizacao(t *testing.T) {
	// Um mapa construído a partir de um http.Header vem capitalizado, e perder
	// a assinatura por causa de um "X" maiúsculo é recusar um pagamento por
	// causa de nada.
	p := New(Config{APIKey: "c", WebhookSecret: segredoWebhook, Now: func() time.Time { return fixo }})
	corpo, h := assina(`{"operationStatus":"1"}`, fixo)
	maiusculas := map[string]string{
		"X-Webhook-Signature": h[cabecalhoAssinatura],
		"X-Webhook-Timestamp": h[cabecalhoCarimbo],
	}
	if _, err := p.ParseWebhookWithHeaders([]byte(corpo), maiusculas); err != nil {
		t.Errorf("cabeçalhos capitalizados: %v", err)
	}
}

// --- idempotência e a nossa referência ----------------------------------------

func TestACobrancaLevaAChaveDeIdempotenciaEANossaReferencia(t *testing.T) {
	// O MoMenu passou a aceitar Idempotency-Key, e é isso que torna recuperável
	// uma resposta de Multicaixa Express perdida a meio de três minutos.
	var chave string
	var corpo MCXRequest
	p, done := comRelogio(t, func(w http.ResponseWriter, r *http.Request) {
		chave = r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&corpo)
		fmt.Fprint(w, `{"success":true,"transactionId":"tx-1","externalReferenceOpcional":"pay_1"}`)
	})
	defer done()

	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "pay_1",
		Amount:    money.FromMajor(5900, money.AOA),
		Method:    payment.MethodMCX,
		Customer:  payment.Customer{Phone: "921234567"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if chave != "pay_1" {
		t.Errorf("chave de idempotência = %q, queria a nossa referência", chave)
	}
	// O nome do campo é mesmo `externalReferenceOpcional`, com o "Opcional"
	// colado. Não se corrige: é o que a API aceita.
	if corpo.ExternalReference != "pay_1" {
		t.Errorf("referência enviada = %q", corpo.ExternalReference)
	}
	if res.Raw["externalReference"] != "pay_1" {
		t.Errorf("referência devolvida = %v", res.Raw)
	}
}

func TestSemReferenciaNaoVaiChaveDeIdempotencia(t *testing.T) {
	// Sem chave, repetir emite outra cobrança, que é o comportamento antigo.
	// Mandar uma chave vazia era pior: o gateway podia tratá-la como uma chave.
	var presente bool
	p, done := comRelogio(t, func(w http.ResponseWriter, r *http.Request) {
		_, presente = r.Header["Idempotency-Key"]
		fmt.Fprint(w, `{"success":true,"transactionId":"tx-1"}`)
	})
	defer done()

	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodMCX,
		Customer: payment.Customer{Phone: "921234567"},
	}); err != nil {
		t.Fatal(err)
	}
	if presente {
		t.Error("sem referência não devia ir cabeçalho de idempotência")
	}
}

func TestAReferenciaECortadaAoQueOMoMenuAceita(t *testing.T) {
	// O campo deles aceita 200 caracteres. Passar mais dá um
	// INVALID_PARAMETERS no meio de uma cobrança, e não um erro que se perceba.
	longa := strings.Repeat("r", 250)
	var corpo MCXRequest
	p, done := comRelogio(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&corpo)
		fmt.Fprint(w, `{"success":true,"transactionId":"tx-1"}`)
	})
	defer done()

	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: longa, Amount: money.FromMajor(100, money.AOA),
		Method: payment.MethodMCX, Customer: payment.Customer{Phone: "921234567"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(corpo.ExternalReference) != 200 {
		t.Errorf("referência enviada com %d caracteres", len(corpo.ExternalReference))
	}
}

// --- o que se recusa antes da rede --------------------------------------------

func TestOMinimoEAsomaDosProdutosSaoVerificadosAntesDaRede(t *testing.T) {
	// São dois erros que o MoMenu passou a impor com nomes próprios. Verificá-los
	// aqui poupa uma ida e volta que, no Multicaixa Express, pode ser de três
	// minutos.
	chamou := false
	p, done := comRelogio(t, func(w http.ResponseWriter, _ *http.Request) {
		chamou = true
		fmt.Fprint(w, `{"success":true,"transactionId":"tx-1"}`)
	})
	defer done()

	// Abaixo do mínimo de 100 Kz.
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(50, money.AOA), Method: payment.MethodMCX,
		Customer: payment.Customer{Phone: "921234567"},
	})
	if !errors.Is(err, ErrBelowMinimum) {
		t.Errorf("erro = %v, queria abaixo do mínimo", err)
	}

	// As linhas não somam o total: é o caso de um desconto aplicado ao total e
	// não às linhas, que antes passava e dava uma factura estranha.
	_, err = p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(2500, money.AOA), Method: payment.MethodMCX,
		Customer: payment.Customer{Phone: "921234567"},
		Items: []payment.LineItem{
			{Description: "Plano", UnitPrice: money.FromMajor(3000, money.AOA), Quantity: 1},
		},
	})
	if !errors.Is(err, ErrAmountMismatch) {
		t.Errorf("erro = %v, queria valores que não batem", err)
	}
	if !strings.Contains(err.Error(), "2500") || !strings.Contains(err.Error(), "3000") {
		t.Errorf("a mensagem devia dizer os dois valores: %v", err)
	}

	if chamou {
		t.Error("nenhum dos dois devia ter chegado à rede")
	}

	// E quando batem, passa.
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(3000, money.AOA), Method: payment.MethodMCX,
		Customer: payment.Customer{Phone: "921234567"},
		Items: []payment.LineItem{
			{Description: "Plano", UnitPrice: money.FromMajor(1500, money.AOA), Quantity: 2},
		},
	}); err != nil {
		t.Errorf("valores que batem deviam passar: %v", err)
	}
}

func TestAReferenciaTambemVerificaOValor(t *testing.T) {
	p, done := comRelogio(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"transactionId":"tx-1","referenceNumber":"9","entity":"0"}`)
	})
	defer done()
	if _, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(50, money.AOA), Method: payment.MethodReference,
	}); !errors.Is(err, ErrBelowMinimum) {
		t.Errorf("erro = %v", err)
	}
}

func TestValidarValorNosExtremos(t *testing.T) {
	// Sem valor ou sem produtos não há nada para comparar, e não se inventa um
	// erro.
	if err := validarValor(0, nil); err != nil {
		t.Errorf("sem valor = %v", err)
	}
	if err := validarValor(5900, nil); err != nil {
		t.Errorf("sem produtos = %v", err)
	}
	// Uma quantidade a zero conta como uma, senão a linha desaparecia da soma.
	if err := validarValor(100, []Product{{ProductPrice: 100, ProductQuantity: 0}}); err != nil {
		t.Errorf("quantidade a zero = %v", err)
	}
	// Um cêntimo de folga: os dois lados chegam por vírgula flutuante, e
	// recusar por 0,000001 era recusar por nada.
	if err := validarValor(100, []Product{{ProductPrice: 100.005, ProductQuantity: 1}}); err != nil {
		t.Errorf("folga de um cêntimo = %v", err)
	}
}

// --- a mensagem do gateway deixa de ser engolida --------------------------------

func TestOMotivoDaRecusaChegaAQuemFazSuporte(t *testing.T) {
	// Eram uma dúzia de códigos com nome a chegarem como "recusada" sem motivo,
	// e um pedido de suporte por cada falha.
	p, done := comRelogio(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":false,"message":"BANK_ACCOUNT_NOT_VERIFIED"}`)
	})
	defer done()

	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(5900, money.AOA), Method: payment.MethodReference,
	})
	if err == nil || !strings.Contains(err.Error(), "BANK_ACCOUNT_NOT_VERIFIED") {
		t.Fatalf("erro = %v, queria o motivo do gateway", err)
	}

	// E sem mensagem nenhuma, diz que não houve motivo em vez de ficar vazio.
	if err := recusa("emissão de referência", "  "); !strings.Contains(err.Error(), "sem motivo") {
		t.Errorf("erro = %v", err)
	}
}

// --- a consulta de estado -------------------------------------------------------

func TestAConsultaDeEstadoLeOsTresEstados(t *testing.T) {
	casos := map[string]payment.Status{
		"open":   payment.StatusPending,
		"paid":   payment.StatusApproved,
		"failed": payment.StatusRejected,
	}
	for estado, quero := range casos {
		p, done := comRelogio(t, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, `{"success":true,"status":%q,"amount":5900}`, estado)
		})
		st, err := p.VerifyCharge(context.Background(), "tx-1", "")
		done()
		if err != nil {
			t.Fatalf("%s: %v", estado, err)
		}
		if st.Status != quero {
			t.Errorf("%s deu %v, queria %v", estado, st.Status, quero)
		}
	}
}

func TestAConsultaSemDataUsaAHoraDeAgora(t *testing.T) {
	p, done := comRelogio(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"status":"paid","amount":5900,"paidAt":"ontem"}`)
	})
	defer done()
	st, err := p.VerifyCharge(context.Background(), "tx-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Paid || st.PaidAt == nil {
		t.Errorf("estado = %+v", st)
	}
}

func TestAConsultaUsaAReferenciaDoComercianteQuandoNaoHaOutra(t *testing.T) {
	var caminho string
	p, done := comRelogio(t, func(w http.ResponseWriter, r *http.Request) {
		caminho = r.URL.Path
		fmt.Fprint(w, `{"success":true,"status":"open"}`)
	})
	defer done()
	if _, err := p.VerifyCharge(context.Background(), "   ", "pay_1"); err != nil {
		t.Fatal(err)
	}
	if caminho != "/api/payment/status/pay_1" {
		t.Errorf("caminho = %q", caminho)
	}
}

// --- a factura é pelo número, não pelo identificador ----------------------------

func TestODetalheDaFacturaEPeloNumero(t *testing.T) {
	// A reconciliação passava o invoiceId e não encontrava nada, o que lhe
	// tirava justamente o telemóvel de que precisa para correlacionar.
	var caminho string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caminho = r.URL.Path
		fmt.Fprint(w, `{"success":true,"invoice":{"invoiceNumber":"FT 1/1"}}`)
	}))
	defer srv.Close()

	c := NewClient(Config{APIKey: "chave", BaseURL: srv.URL})
	if _, err := c.GetInvoice(context.Background(), "FT 1/1"); err != nil {
		t.Fatal(err)
	}
	if caminho != "/api/invoices/FT 1/1" {
		t.Errorf("caminho = %q", caminho)
	}
}

func TestOClienteExpoeAConfiguracaoEORelogio(t *testing.T) {
	c := NewClient(Config{APIKey: "chave", WebhookSecret: "s"})
	if c.Config().WebhookSecret != "s" {
		t.Error("configuração inacessível")
	}
	// Sem relógio configurado usa o do sistema.
	if c.agora().IsZero() {
		t.Error("o relógio por omissão devia dar a hora")
	}
}

func TestAReferenciaNormalizaOTelemovelDoClienteEViveSemReferenciaNossa(t *testing.T) {
	// O telemóvel na referência é só para a factura, e normaliza-se na mesma:
	// uma factura com o número em três formatos diferentes é uma factura que
	// ninguém consegue correlacionar depois.
	var corpo ReferenceRequest
	p, done := comRelogio(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&corpo)
		// Sem dueDate legível e sem a nossa referência de volta.
		fmt.Fprint(w, `{"success":true,"operationId":"op-1","transactionId":"tx-1",
			"referenceNumber":"987654321","entity":"01234","dueDate":"para a semana"}`)
	})
	defer done()

	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(5900, money.AOA),
		Method: payment.MethodReference,
		Customer: payment.Customer{
			Phone: "+244 921 234 567", Name: "Ana", TaxID: "5417000000",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if corpo.Customer == nil || corpo.Customer.Phone != "244921234567" {
		t.Errorf("telemóvel enviado = %+v", corpo.Customer)
	}
	// Sem validade legível não se inventa um prazo.
	if res.ExpiresAt != nil {
		t.Errorf("validade = %v, queria nenhuma", res.ExpiresAt)
	}
	// Sem referência nossa, o Raw não ganha um campo vazio.
	if _, existe := res.Raw["externalReference"]; existe {
		t.Errorf("raw = %v, não devia ter referência", res.Raw)
	}
}

func TestAReferenciaGuardaANossaReferenciaQuandoExiste(t *testing.T) {
	// É por ela que o webhook e a consulta se ligam de volta à cobrança sem
	// depender só do identificador do MoMenu.
	p, done := comRelogio(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":true,"operationId":"op-1","transactionId":"tx-1",
			"referenceNumber":"987654321","entity":"01234","dueDate":"2026-11-05",
			"externalReferenceOpcional":"pay_9"}`)
	})
	defer done()

	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "pay_9",
		Amount:    money.FromMajor(5900, money.AOA),
		Method:    payment.MethodReference,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Raw["externalReference"] != "pay_9" {
		t.Errorf("raw = %v", res.Raw)
	}
	if res.ExpiresAt == nil {
		t.Error("a validade devia ter sido lida")
	}
}

func TestOSegredoEmClaroEValidadoQuandoVem(t *testing.T) {
	// O MoMenu envia o segredo em claro quando a conta não tem "Assinatura
	// apenas", e a documentação é explícita: "validem por comparação exacta
	// (em tempo constante quando a linguagem o permitir) e rejeitem pedidos sem
	// correspondência". A implementação ignorava-o.
	p := New(Config{APIKey: "c", WebhookSecret: segredoWebhook, Now: func() time.Time { return fixo }})
	corpo, h := assina(`{"merchantTransactionId":"tx-1","operationStatus":"1"}`, fixo)

	// Com o segredo certo, passa.
	if _, err := p.ParseWebhookWithHeaders([]byte(corpo), h); err != nil {
		t.Errorf("com o segredo certo: %v", err)
	}

	// Ausente também passa: é o caso recomendado, em que o segredo nunca viaja.
	semSegredo := map[string]string{
		cabecalhoAssinatura: h[cabecalhoAssinatura],
		cabecalhoCarimbo:    h[cabecalhoCarimbo],
	}
	if _, err := p.ParseWebhookWithHeaders([]byte(corpo), semSegredo); err != nil {
		t.Errorf("sem o cabeçalho do segredo: %v", err)
	}

	// Com o segredo de outra conta, não passa, mesmo com a assinatura boa: uma
	// entrega que traz o segredo de outrem não é uma entrega nossa.
	deOutrem := map[string]string{
		cabecalhoAssinatura: h[cabecalhoAssinatura],
		cabecalhoCarimbo:    h[cabecalhoCarimbo],
		cabecalhoSegredo:    "o-segredo-de-outra-conta",
	}
	if _, err := p.ParseWebhookWithHeaders([]byte(corpo), deOutrem); !errors.Is(err, payment.ErrBadSignature) {
		t.Errorf("com o segredo de outrem = %v, queria recusa", err)
	}
}

func TestOEventoTrazOIdentificadorDeDeduplicacao(t *testing.T) {
	// A entrega é at-least-once e o gateway manda deduplicar pelo
	// merchantTransactionId: "um segundo payment.confirmed para o mesmo
	// identificador" tem de ser inofensivo. Sem o Event.ID preenchido, quem
	// recebe não tem por onde deduplicar.
	p := New(Config{APIKey: "c", WebhookSecret: segredoWebhook, Now: func() time.Time { return fixo }})
	corpo, h := assina(`{"event":"payment.confirmed","merchantTransactionId":"tx-77","operationStatus":"1"}`, fixo)

	ev, err := p.ParseWebhookWithHeaders([]byte(corpo), h)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID != "tx-77" {
		t.Errorf("identificador do evento = %q, queria tx-77", ev.ID)
	}
}

// --- o tempo que acaba não é uma recusa -----------------------------------------

func TestOTempoEsgotadoNoMulticaixaExpressNaoEUmaRecusa(t *testing.T) {
	// O telemóvel que não confirmou dentro da janela vinha como "recusada", e
	// o cliente lia que o pagamento tinha sido recusado quando ninguém o
	// recusou e nada foi cobrado.
	for _, mensagem := range []string{
		"TIMEOUT: o cliente não confirmou no telemóvel a tempo",
		"time_out",
		" EXPIRED ",
	} {
		p, done := comRelogio(t, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, `{"success":false,"message":%q}`, mensagem)
		})
		_, err := p.Charge(context.Background(), payment.ChargeRequest{
			Amount: money.FromMajor(5900, money.AOA), Method: payment.MethodMCX,
			Customer: payment.Customer{Phone: "923000000"},
		})
		done()
		if !errors.Is(err, payment.ErrExpired) {
			t.Fatalf("%q: erro = %v, queria payment.ErrExpired", mensagem, err)
		}
		if payment.IsRetryable(err) {
			t.Errorf("%q: o tempo esgotado não melhora com repetição", mensagem)
		}
		if strings.Contains(err.Error(), "recusada") {
			t.Errorf("%q: o tempo esgotado continua a dizer recusada: %v", mensagem, err)
		}
	}

	// E uma recusa a sério continua a ser recusa, com o motivo.
	p, done := comRelogio(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"success":false,"message":"O cliente recusou o pagamento"}`)
	})
	defer done()
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(5900, money.AOA), Method: payment.MethodMCX,
		Customer: payment.Customer{Phone: "923000000"},
	})
	if err == nil || errors.Is(err, payment.ErrExpired) || !strings.Contains(err.Error(), "recusada") {
		t.Fatalf("erro = %v, queria uma recusa", err)
	}
}

func TestOMoMenuNaoAplicaOPrazoPedido(t *testing.T) {
	// A API do MoMenu não recebe a validade da referência: escolhe-a ela. Por
	// isso o provider não diz que a aplica, e quem precisa de um prazo escolhe
	// outro gateway.
	var p payment.Provider = New(Config{APIKey: "chave"})
	if _, ok := p.(payment.ExpiryHonorer); ok {
		t.Fatal("o MoMenu não pode anunciar que aplica o ExpiresAt")
	}
}
