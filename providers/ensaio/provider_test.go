package ensaio

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

func pedido(metodo payment.Method, kwanzas int64) payment.ChargeRequest {
	return payment.ChargeRequest{
		Reference: "pay_4b2awj3h26d5ftdmwdgjhbjs",
		Amount:    money.New(kwanzas, money.AOA),
		Method:    metodo,
		Mode:      payment.ModePayment,
	}
}

func TestReferenciaTemFormatoDeReferencia(t *testing.T) {
	// Quem integra vai mostrar isto a um cliente que o vai escrever num ATM. Se
	// o formato não for o verdadeiro, o ecrã de pagamento é desenhado à volta de
	// um número que não existe e parte no dia em que a rede a sério entrar.
	res, err := New().Charge(context.Background(), pedido(payment.MethodReference, 1500000))
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != payment.KindReference || res.Status != payment.StatusPending {
		t.Fatalf("kind=%v status=%v", res.Kind, res.Status)
	}
	if len(res.Entity) != 5 {
		t.Errorf("entidade = %q, esperava cinco dígitos", res.Entity)
	}
	if len(res.Reference) != 9 {
		t.Errorf("referência = %q, esperava nove dígitos", res.Reference)
	}
	if res.DueDate == "" || res.ExpiresAt == nil {
		t.Error("uma referência sem prazo é uma referência que nunca expira")
	}
}

func TestAMesmaCobrancaDaSempreAMesmaReferencia(t *testing.T) {
	// Quem integra guarda a referência e mostra-a ao cliente. Uma que mudasse
	// entre chamadas era um cliente a pagar um número que já não existe.
	p := New()
	a, _ := p.Charge(context.Background(), pedido(payment.MethodReference, 1500000))
	b, _ := p.Charge(context.Background(), pedido(payment.MethodReference, 1500000))
	if a.Reference != b.Reference {
		t.Fatalf("%q e %q", a.Reference, b.Reference)
	}

	outra := pedido(payment.MethodReference, 1500000)
	outra.Reference = "pay_outra"
	c, _ := p.Charge(context.Background(), outra)
	if c.Reference == a.Reference {
		t.Error("duas cobranças diferentes com a mesma referência")
	}
}

func TestOsQueEsperamPelaConfirmacaoFicamPendentes(t *testing.T) {
	// O ecrã de espera é obrigatório nestes três, e quem integra tem de o
	// escrever antes da primeira espera real.
	for _, m := range []payment.Method{
		payment.MethodMCX, payment.MethodWallet, payment.MethodDirectDebit,
		payment.MethodExternal,
	} {
		res, err := New().Charge(context.Background(), pedido(m, 1500000))
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		if res.Status != payment.StatusPending {
			t.Errorf("%s ficou %v", m, res.Status)
		}
	}
}

func TestOsQueFechamNaHoraNascemAprovados(t *testing.T) {
	for _, m := range []payment.Method{payment.MethodCard, payment.MethodManual} {
		res, err := New().Charge(context.Background(), pedido(m, 1500000))
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		if res.Status != payment.StatusApproved || res.Kind != payment.KindPaid {
			t.Errorf("%s: kind=%v status=%v", m, res.Kind, res.Status)
		}
	}
}

func TestValorNaoPositivoERecusado(t *testing.T) {
	_, err := New().Charge(context.Background(), pedido(payment.MethodReference, 0))
	if !errors.Is(err, payment.ErrAmountNotPositive) {
		t.Fatalf("= %v", err)
	}
}

func TestMetodoDesconhecidoERecusado(t *testing.T) {
	_, err := New().Charge(context.Background(), pedido(payment.Method("nenhum"), 1000))
	if !errors.Is(err, payment.ErrUnsupportedMethod) {
		t.Fatalf("= %v", err)
	}
}

func TestOPrazoDeQuemChamaGanhaAoNosso(t *testing.T) {
	// Quem cria a cobrança pode querer uma referência de uma hora. O nosso
	// prazo por omissão é para quando ninguém disse nada.
	quando := time.Now().UTC().Add(time.Hour)
	req := pedido(payment.MethodReference, 1500000)
	req.ExpiresAt = &quando

	res, _ := New().Charge(context.Background(), req)
	if !res.ExpiresAt.Equal(quando) {
		t.Fatalf("expira = %v, esperava %v", res.ExpiresAt, quando)
	}
}

func TestSemPrazoNenhumNaoHaData(t *testing.T) {
	// Um gateway de ensaio configurado sem TTL não inventa prazo nenhum.
	p := &Provider{Entidade: "00123"}
	res, _ := p.Charge(context.Background(), pedido(payment.MethodReference, 1500000))
	if res.ExpiresAt != nil || res.DueDate != "" {
		t.Fatalf("expira=%v data=%q", res.ExpiresAt, res.DueDate)
	}
}

func TestOResto(t *testing.T) {
	p := New()
	if p.Name() != "ensaio" {
		t.Errorf("nome = %q", p.Name())
	}
	// O nome aparece no painel e nos eventos: quem olhar para uma cobrança tem
	// de saber que não foi a sério.
	if !p.Configured() {
		t.Error("um gateway sem credenciais está sempre configurado")
	}
	if !p.SupportsCurrency(money.AOA) || !p.SupportsCurrency(money.Currency("XOF")) {
		t.Error("um ensaio não tem razão para recusar moedas")
	}
	if len(p.Methods()) != 7 {
		t.Errorf("métodos = %d, esperava os sete", len(p.Methods()))
	}
	if err := p.CancelCharge(context.Background(), payment.ChargeRequest{}, payment.ChargeResult{}); err != nil {
		t.Errorf("cancelar = %v", err)
	}
}
