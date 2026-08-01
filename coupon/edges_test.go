package coupon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
)

type failingStore struct {
	*memStore
	onByCode, onCount error
}

func (f *failingStore) ByCode(ctx context.Context, code string) (*Coupon, error) {
	if f.onByCode != nil {
		return nil, f.onByCode
	}
	return f.memStore.ByCode(ctx, code)
}

func (f *failingStore) CustomerRedemptions(ctx context.Context, couponID, customerID string) (int, error) {
	if f.onCount != nil {
		return 0, f.onCount
	}
	return f.memStore.CustomerRedemptions(ctx, couponID, customerID)
}

func TestErrorInterface(t *testing.T) {
	e := &Error{Err: ErrExpired, Message: "Este código expirou."}
	if e.Error() != "Este código expirou." {
		t.Errorf("Error() = %q, queria a mensagem do cliente", e.Error())
	}
	if !errors.Is(e, ErrExpired) {
		t.Error("o motivo tem de sobreviver ao embrulho")
	}
}

func TestFreePeriodDefaultsToOne(t *testing.T) {
	// Um cupão de ciclos grátis sem número configurado oferece um, em vez de
	// zero, que seria um desconto sem efeito nenhum.
	c := &Coupon{ID: "c", Code: "X", Kind: KindFreePeriod, Active: true}
	d := c.Apply(aoa(5000))
	if d.FreePeriods != 1 || d.Label != "1 ciclo grátis" {
		t.Errorf("resultado = %+v", d)
	}
	if !d.Net.IsZero() {
		t.Errorf("líquido = %s", d.Net)
	}
}

func TestUnknownKindDiscountsNothing(t *testing.T) {
	// Um tipo desconhecido não pode oferecer o produto por engano.
	c := &Coupon{ID: "c", Code: "X", Kind: Kind("inventado"), Active: true}
	d := c.Apply(aoa(5000))
	if d.Net.Minor != aoa(5000).Minor || !d.Off.IsZero() {
		t.Errorf("resultado = %+v, queria não descontar nada", d)
	}
}

func TestEffectivePercentOnZeroAmount(t *testing.T) {
	c := &Coupon{ID: "c", Code: "X", Kind: KindAmount, Amount: aoa(100), Active: true}
	d := c.Apply(money.Zero(money.AOA))
	if d.Percent != 0 {
		t.Errorf("percentagem sobre zero = %d", d.Percent)
	}
}

func TestValidatePropagatesStoreErrors(t *testing.T) {
	boom := errors.New("base de dados em baixo")
	ctx := context.Background()

	// Falha a procurar o código.
	s := newService(newStore())
	s.store = &failingStore{memStore: newStore(percentCoupon()), onByCode: boom}
	if _, err := s.Validate(ctx, Request{Code: "BOASVINDAS", Amount: aoa(100)}); !errors.Is(err, boom) {
		t.Errorf("procurar = %v", err)
	}
	if _, err := s.Preview(ctx, Request{Code: "BOASVINDAS", Amount: aoa(100)}); !errors.Is(err, boom) {
		t.Errorf("pré-visualizar = %v", err)
	}

	// Falha a contar as utilizações do cliente.
	s.store = &failingStore{memStore: newStore(percentCoupon()), onCount: boom}
	_, err := s.Validate(ctx, Request{Code: "BOASVINDAS", CustomerID: "cli-1", Amount: aoa(100)})
	if !errors.Is(err, boom) {
		t.Errorf("contar = %v", err)
	}
}

func TestValidateWithoutCustomerSkipsThePerCustomerCheck(t *testing.T) {
	// Sem cliente identificado não há limite por cliente que se possa verificar,
	// e isso não pode impedir a pré-visualização de um preço.
	s := newService(newStore(percentCoupon()))
	if _, err := s.Validate(context.Background(), Request{Code: "BOASVINDAS", Amount: aoa(5000)}); err != nil {
		t.Errorf("sem cliente = %v", err)
	}
}

func TestValidateUsesTheGivenInstant(t *testing.T) {
	// O instante recebido manda sobre o relógio, para se poder validar um
	// cupão à data de uma compra passada.
	past := time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	expiry := time.Date(2021, time.January, 1, 0, 0, 0, 0, time.UTC)
	c := percentCoupon()
	c.ExpiresAt = &expiry
	s := newService(newStore(c))

	if _, err := s.Validate(context.Background(), Request{Code: "BOASVINDAS", Amount: aoa(100), At: past}); err != nil {
		t.Errorf("à data da compra = %v", err)
	}
	if _, err := s.Validate(context.Background(), Request{Code: "BOASVINDAS", Amount: aoa(100)}); err == nil {
		t.Error("à data de hoje já devia estar expirado")
	}
}

func TestServiceDefaults(t *testing.T) {
	// Sem relógio nem gerador de identificadores continua a funcionar.
	s := NewService(newStore(percentCoupon()))
	s.Now = nil
	s.IDs = nil
	ctx := context.Background()
	c, err := s.Validate(ctx, Request{Code: "BOASVINDAS", CustomerID: "cli-1", Amount: aoa(5000)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Redeem(ctx, c, "cli-1", "sub-1", aoa(1000)); err != nil {
		t.Fatal(err)
	}
	if c.Redemptions != 1 {
		t.Errorf("utilizações = %d", c.Redemptions)
	}
}

func TestPlanAndIntervalListsMatch(t *testing.T) {
	// O caminho positivo das listas de elegibilidade, que o teste das recusas
	// não toca.
	c := percentCoupon()
	c.PlanIDs = []string{"basico", "pro"}
	c.Intervals = []string{"monthly", "yearly"}
	s := newService(newStore(c))
	_, err := s.Validate(context.Background(), Request{
		Code: "BOASVINDAS", Amount: aoa(5000), PlanID: "pro", Interval: "yearly",
	})
	if err != nil {
		t.Errorf("plano e periodicidade elegíveis = %v", err)
	}
}

func TestItoaNegative(t *testing.T) {
	if got := itoa(-20); got != "-20" {
		t.Errorf("= %q", got)
	}
	if got := itoa(0); got != "0" {
		t.Errorf("= %q", got)
	}
}
