package offline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

func TestConfiguredIsAlwaysTrue(t *testing.T) {
	// Não há nada para configurar: um depósito bancário não tem chave de API.
	if !New().Configured() {
		t.Error("devia estar sempre configurado")
	}
}

func TestCurrencyRestriction(t *testing.T) {
	p := New()
	// Sem restrição, aceita tudo.
	if !p.SupportsCurrency(money.EUR) || !p.SupportsCurrency(money.AOA) {
		t.Error("sem restrição devia aceitar qualquer moeda")
	}

	p.Currencies = []money.Currency{money.AOA, "usd"}
	if !p.SupportsCurrency(money.AOA) || !p.SupportsCurrency(money.USD) {
		t.Error("as moedas configuradas deviam ser aceites, sem distinguir maiúsculas")
	}
	if p.SupportsCurrency(money.EUR) {
		t.Error("uma moeda fora da lista não é aceite")
	}

	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(10, money.EUR), Method: payment.MethodExternal,
	})
	if !errors.Is(err, payment.ErrUnsupportedCurrency) {
		t.Errorf("= %v", err)
	}
}

func TestExternalChargeRejectsNonPositive(t *testing.T) {
	_, err := New().Charge(context.Background(), payment.ChargeRequest{
		Amount: money.Zero(money.AOA), Method: payment.MethodExternal,
	})
	if !errors.Is(err, payment.ErrAmountNotPositive) {
		t.Errorf("= %v", err)
	}
}

func TestExternalChargeRespectsExplicitDeadline(t *testing.T) {
	// Um prazo dado por quem chama manda sobre o prazo por omissão: numa
	// renovação é o fecho da janela, e não uma semana a contar de hoje.
	deadline := time.Date(2026, time.April, 30, 23, 59, 59, 0, time.UTC)
	res, err := New().Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodExternal,
		ExpiresAt: &deadline,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExpiresAt == nil || !res.ExpiresAt.Equal(deadline) {
		t.Errorf("prazo = %v, queria %v", res.ExpiresAt, deadline)
	}
}

func TestExternalChargeWithoutDeadline(t *testing.T) {
	p := New()
	p.TTL = 0 // sem prazo configurado
	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodExternal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExpiresAt != nil {
		t.Errorf("prazo = %v, queria nenhum", res.ExpiresAt)
	}
}

func TestChargeRejectsUnknownMethod(t *testing.T) {
	_, err := New().Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(100, money.AOA), Method: payment.MethodMCX,
	})
	if !errors.Is(err, payment.ErrUnsupportedMethod) {
		t.Errorf("= %v", err)
	}
}

func TestConfirmOnMissingPayment(t *testing.T) {
	if err := New().Confirm(nil, "op-1", ""); !errors.Is(err, payment.ErrNotFound) {
		t.Errorf("= %v", err)
	}
}

func TestConfirmWithoutNote(t *testing.T) {
	// A nota é opcional; o operador nunca é.
	p := New()
	pay, _ := payment.NewPayment("p1", "sub-1", money.FromMajor(100, money.AOA), payment.MethodExternal)
	if err := p.Confirm(pay, "operador-1", ""); err != nil {
		t.Fatal(err)
	}
	if pay.Metadata["confirmed_by"] != "operador-1" {
		t.Errorf("operador = %q", pay.Metadata["confirmed_by"])
	}
	if _, has := pay.Metadata["confirmation_note"]; has {
		t.Error("sem nota não se escreve o campo")
	}
}

func TestConfirmKeepsExistingMetadata(t *testing.T) {
	p := New()
	pay, _ := payment.NewPayment("p1", "sub-1", money.FromMajor(100, money.AOA), payment.MethodExternal)
	pay.Metadata = map[string]string{"origem": "renovacao"}
	if err := p.Confirm(pay, "operador-1", "extracto de 3 de Abril"); err != nil {
		t.Fatal(err)
	}
	if pay.Metadata["origem"] != "renovacao" {
		t.Error("os metadados que já existiam não podem ser apagados")
	}
	if pay.Provider != "offline" {
		t.Errorf("provider = %q", pay.Provider)
	}
}

func TestCancelChargeIsANoOp(t *testing.T) {
	// Não há nada do lado de fora para revogar: uma transferência que não
	// chegou simplesmente não chega.
	if err := New().CancelCharge(context.Background(), payment.ChargeRequest{}, payment.ChargeResult{}); err != nil {
		t.Errorf("= %v", err)
	}
}
