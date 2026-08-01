package offline

import (
	"context"
	"errors"
	"testing"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

func TestManualChargeIsPaidImmediately(t *testing.T) {
	// Uma atribuição de cortesia não tem dinheiro nenhum a esperar, mas deixa o
	// mesmo rasto e dá à subscrição um ciclo de onde renovar.
	p := New()
	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "sub-1", Amount: money.Zero(money.AOA), Method: payment.MethodManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != payment.KindPaid || res.Status != payment.StatusApproved {
		t.Errorf("resultado = %+v", res)
	}
}

func TestExternalChargeWaitsForOperator(t *testing.T) {
	p := New()
	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "sub-1", Amount: money.FromMajor(5900, money.AOA), Method: payment.MethodExternal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != payment.KindPending || res.Status != payment.StatusPending {
		t.Errorf("resultado = %+v, queria pendente", res)
	}
	if res.ExpiresAt == nil {
		t.Error("uma transferência por confirmar precisa de prazo")
	}
}

func TestConfirmRecordsWhoAndWhy(t *testing.T) {
	p := New()
	pay, _ := payment.NewPayment("p1", "sub-1", money.FromMajor(5900, money.AOA), payment.MethodExternal)

	if err := p.Confirm(pay, "operador-7", "extracto de 3 de Agosto"); err != nil {
		t.Fatal(err)
	}
	if pay.Status != payment.StatusApproved {
		t.Errorf("estado = %s", pay.Status)
	}
	// Este é o único pagamento cuja prova não existe em lado nenhum senão no
	// nosso registo: quem confirmou e com base em quê têm de ficar guardados.
	if pay.Metadata["confirmed_by"] != "operador-7" {
		t.Errorf("operador = %q", pay.Metadata["confirmed_by"])
	}
	if pay.Metadata["confirmation_note"] == "" {
		t.Error("a nota de confirmação devia ficar registada")
	}

	// Confirmar duas vezes é dar por recebido dinheiro que já estava recebido.
	if err := p.Confirm(pay, "operador-7", ""); !errors.Is(err, payment.ErrInvalidTransition) {
		t.Errorf("segunda confirmação devolveu %v", err)
	}
}

func TestConfirmRejectsOtherMethods(t *testing.T) {
	p := New()
	pay, _ := payment.NewPayment("p1", "sub-1", money.FromMajor(100, money.AOA), payment.MethodReference)
	if err := p.Confirm(pay, "operador-7", ""); !errors.Is(err, payment.ErrUnsupportedMethod) {
		t.Errorf("erro = %v, queria método não suportado", err)
	}
}

func TestMethodsAreAdminOnly(t *testing.T) {
	p := New()
	for _, m := range p.Methods() {
		if m.SelfService() {
			t.Errorf("o método %s não devia ser oferecido a um cliente", m)
		}
	}
}
