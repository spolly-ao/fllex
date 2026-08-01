package money

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConvertSameCurrencyIsIdentity(t *testing.T) {
	c := NewConverter(NewMemoryRateStore())
	a := FromMajor(100, EUR)
	got, err := c.Convert(context.Background(), a, EUR)
	if err != nil {
		t.Fatal(err)
	}
	if got.Minor != a.Minor {
		t.Errorf("conversão para a própria moeda alterou o valor: %s", got)
	}
}

func TestConvert(t *testing.T) {
	store := NewMemoryRateStore()
	ctx := context.Background()
	// Mil kwanzas por dólar.
	_ = store.PutRate(ctx, Rate{From: AOA, To: USD, Value: 1.0 / 1000, ValidUntil: time.Now().Add(time.Hour)})

	c := NewConverter(store)
	got, err := c.Convert(ctx, FromMajor(100000, AOA), USD)
	if err != nil {
		t.Fatal(err)
	}
	if want := FromMajor(100, USD); got.Minor != want.Minor {
		t.Errorf("conversão = %s, queria %s", got, want)
	}
	if got.Currency != USD {
		t.Errorf("moeda = %s", got.Currency)
	}
}

func TestConvertUsesInverseRate(t *testing.T) {
	store := NewMemoryRateStore()
	ctx := context.Background()
	_ = store.PutRate(ctx, Rate{From: AOA, To: USD, Value: 1.0 / 1000, ValidUntil: time.Now().Add(time.Hour)})

	c := NewConverter(store)
	got, err := c.Convert(ctx, FromMajor(100, USD), AOA)
	if err != nil {
		t.Fatalf("a taxa inversa devia servir: %v", err)
	}
	if want := FromMajor(100000, AOA); got.Minor != want.Minor {
		t.Errorf("conversão inversa = %s, queria %s", got, want)
	}
}

func TestConvertRefusesExpiredRate(t *testing.T) {
	// Converter por uma taxa que já não é verdade é uma diferença que só se
	// descobre na contabilidade; recusar é uma falha visível que alguém corrige.
	store := NewMemoryRateStore()
	ctx := context.Background()
	_ = store.PutRate(ctx, Rate{From: AOA, To: USD, Value: 1.0 / 1000, ValidUntil: time.Now().Add(-time.Hour)})

	c := NewConverter(store)
	if _, err := c.Convert(ctx, FromMajor(1000, AOA), USD); !errors.Is(err, ErrNoRate) {
		t.Errorf("taxa expirada devolveu %v, queria ErrNoRate", err)
	}
}

func TestConvertWithoutRate(t *testing.T) {
	c := NewConverter(NewMemoryRateStore())
	if _, err := c.Convert(context.Background(), FromMajor(100, AOA), EUR); !errors.Is(err, ErrNoRate) {
		t.Errorf("sem taxa devolveu %v, queria ErrNoRate", err)
	}
}

func TestApplyAcrossDifferentDecimals(t *testing.T) {
	// O iene não tem subunidade: a conversão tem de aterrar na representação
	// certa da moeda de destino, e não multiplicar por cem à mesma.
	got := Apply(FromMajor(100, USD), Rate{From: USD, To: "JPY", Value: 150})
	if want := FromMajor(15000, "JPY"); got.Minor != want.Minor {
		t.Errorf("conversão = %s (%d), queria %s (%d)", got, got.Minor, want, want.Minor)
	}
}
