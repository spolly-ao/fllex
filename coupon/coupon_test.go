package coupon

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
)

func aoa(v int64) money.Amount { return money.FromMajor(v, money.AOA) }

type memStore struct {
	byCode  map[string]*Coupon
	perCust map[string]int         // couponID|customerID
	bySubj  map[string]*Redemption // couponID|subjectID
	fail    error
}

func newStore(cs ...*Coupon) *memStore {
	m := &memStore{
		byCode:  map[string]*Coupon{},
		perCust: map[string]int{},
		bySubj:  map[string]*Redemption{},
	}
	for _, c := range cs {
		m.byCode[NormalizeCode(c.Code)] = c
	}
	return m
}

func (m *memStore) ByCode(_ context.Context, code string) (*Coupon, error) {
	return m.byCode[code], nil
}

func (m *memStore) CustomerRedemptions(_ context.Context, couponID, customerID string) (int, error) {
	return m.perCust[couponID+"|"+customerID], nil
}

func (m *memStore) Redeem(_ context.Context, r *Redemption) error {
	if m.fail != nil {
		return m.fail
	}
	key := r.CouponID + "|" + r.SubjectID
	if _, done := m.bySubj[key]; done {
		return nil // idempotente pela compra
	}
	var c *Coupon
	for _, v := range m.byCode {
		if v.ID == r.CouponID {
			c = v
		}
	}
	if c == nil {
		return ErrNotFound
	}
	if c.MaxRedemptions > 0 && c.Redemptions >= c.MaxRedemptions {
		return ErrExhausted
	}
	c.Redemptions++
	m.perCust[r.CouponID+"|"+r.CustomerID]++
	m.bySubj[key] = r
	return nil
}

func (m *memStore) Release(_ context.Context, couponID, subjectID string) error {
	key := couponID + "|" + subjectID
	r, ok := m.bySubj[key]
	if !ok {
		return nil
	}
	delete(m.bySubj, key)
	m.perCust[couponID+"|"+r.CustomerID]--
	for _, v := range m.byCode {
		if v.ID == couponID && v.Redemptions > 0 {
			v.Redemptions--
		}
	}
	return nil
}

func percentCoupon() *Coupon {
	return &Coupon{ID: "c1", Code: "BOASVINDAS", Kind: KindPercent, Percent: 20, Active: true}
}

func newService(store *memStore) *Service {
	s := NewService(store)
	n := 0
	s.IDs = func() string { n++; return fmt.Sprintf("r-%d", n) }
	return s
}

// --- aplicação do desconto -----------------------------------------------------

func TestApplyPercent(t *testing.T) {
	d := percentCoupon().Apply(aoa(5000))
	if want := aoa(4000); d.Net.Minor != want.Minor {
		t.Errorf("líquido = %s, queria %s", d.Net, want)
	}
	if want := aoa(1000); d.Off.Minor != want.Minor {
		t.Errorf("desconto = %s, queria %s", d.Off, want)
	}
	if d.Label != "20% de desconto" {
		t.Errorf("etiqueta = %q", d.Label)
	}
}

func TestApplyAmountNeverGoesNegative(t *testing.T) {
	// Um total negativo faz o gateway recusar com um erro que ninguém
	// relaciona com o cupão.
	c := &Coupon{ID: "c", Code: "X", Kind: KindAmount, Amount: aoa(9000), Active: true}
	d := c.Apply(aoa(5000))
	if !d.Net.IsZero() {
		t.Errorf("líquido = %s, queria zero", d.Net)
	}
	if d.Off.Minor != aoa(5000).Minor {
		t.Errorf("desconto = %s, queria ficar pela compra", d.Off)
	}
	if d.Percent != 100 {
		t.Errorf("percentagem efectiva = %d, queria 100", d.Percent)
	}
}

func TestApplyFreePeriod(t *testing.T) {
	c := &Coupon{ID: "c", Code: "X", Kind: KindFreePeriod, FreePeriods: 2, Active: true}
	d := c.Apply(aoa(5000))
	if !d.Net.IsZero() || d.FreePeriods != 2 {
		t.Errorf("resultado = %+v", d)
	}
	if d.Label != "2 ciclos grátis" {
		t.Errorf("etiqueta = %q", d.Label)
	}
}

func TestDiscountAppliesOnlyToFirstCycleByDefault(t *testing.T) {
	// O valor por omissão é o que protege a margem: um desconto que recorre
	// para sempre sem ninguém ter decidido isso é a forma mais silenciosa de
	// perder dinheiro.
	d := percentCoupon().Apply(aoa(5000))
	if !d.Applies(1) {
		t.Error("o primeiro ciclo tem sempre desconto")
	}
	if d.Applies(2) {
		t.Error("por omissão o desconto não recorre")
	}

	c := percentCoupon()
	c.Recurring = true
	c.RecurringCycles = 3
	d = c.Apply(aoa(5000))
	if !d.Applies(3) {
		t.Error("o terceiro ciclo ainda tem desconto")
	}
	if d.Applies(4) {
		t.Error("o quarto já não tem")
	}

	c.RecurringCycles = 0 // para sempre
	d = c.Apply(aoa(5000))
	if !d.Applies(99) {
		t.Error("sem limite de ciclos o desconto acompanha sempre")
	}
}

// --- validação -----------------------------------------------------------------

func reason(t *testing.T, err error) error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("erro sem mensagem para o cliente: %v", err)
	}
	if e.Message == "" {
		t.Error("recusa sem mensagem: o cliente fica sem saber porquê")
	}
	return e.Err
}

func TestValidateHappyPath(t *testing.T) {
	s := newService(newStore(percentCoupon()))
	c, err := s.Validate(context.Background(), Request{
		Code: " boasvindas ", CustomerID: "cli-1", Amount: aoa(5000),
	})
	if err != nil {
		t.Fatalf("cupão válido recusado: %v", err)
	}
	if c.ID != "c1" {
		t.Errorf("cupão = %s", c.ID)
	}
}

func TestValidateRejections(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour)
	future := time.Now().Add(48 * time.Hour)

	tests := []struct {
		name   string
		mutate func(*Coupon)
		req    Request
		want   error
	}{
		{"desactivado", func(c *Coupon) { c.Active = false }, Request{Amount: aoa(5000)}, ErrInactive},
		{"ainda não começou", func(c *Coupon) { c.StartsAt = &future }, Request{Amount: aoa(5000)}, ErrNotStarted},
		{"expirado", func(c *Coupon) { c.ExpiresAt = &past }, Request{Amount: aoa(5000)}, ErrExpired},
		{"esgotado", func(c *Coupon) { c.MaxRedemptions = 10; c.Redemptions = 10 }, Request{Amount: aoa(5000)}, ErrExhausted},
		{"plano errado", func(c *Coupon) { c.PlanIDs = []string{"pro"} }, Request{Amount: aoa(5000), PlanID: "basico"}, ErrPlanNotEligible},
		{"periodicidade errada", func(c *Coupon) { c.Intervals = []string{"yearly"} }, Request{Amount: aoa(5000), Interval: "monthly"}, ErrIntervalNotEligible},
		{"abaixo do mínimo", func(c *Coupon) { c.MinAmount = aoa(10000) }, Request{Amount: aoa(5000)}, ErrBelowMinimum},
		{"moeda errada", func(c *Coupon) { c.Currency = money.AOA }, Request{Amount: money.FromMajor(50, money.EUR)}, ErrCurrency},
		{"só clientes novos", func(c *Coupon) { c.NewCustomersOnly = true }, Request{Amount: aoa(5000), NewCustomer: false}, ErrNewOnly},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := percentCoupon()
			tt.mutate(c)
			s := newService(newStore(c))
			req := tt.req
			req.Code = "BOASVINDAS"
			_, err := s.Validate(context.Background(), req)
			if err == nil {
				t.Fatal("queria recusa")
			}
			if got := reason(t, err); !errors.Is(got, tt.want) {
				t.Errorf("motivo = %v, queria %v", got, tt.want)
			}
		})
	}
}

func TestValidateUnknownCode(t *testing.T) {
	s := newService(newStore())
	_, err := s.Validate(context.Background(), Request{Code: "NAOEXISTE", Amount: aoa(100)})
	if got := reason(t, err); !errors.Is(got, ErrNotFound) {
		t.Errorf("motivo = %v", got)
	}
	_, err = s.Validate(context.Background(), Request{Code: "   ", Amount: aoa(100)})
	if got := reason(t, err); !errors.Is(got, ErrNotFound) {
		t.Errorf("código vazio: motivo = %v", got)
	}
}

func TestPerCustomerLimitDefaultsToOne(t *testing.T) {
	// Zero tem de se ler como uma vez: um cupão que o mesmo cliente pode usar
	// sem conta é muito mais caro do que um restritivo de mais.
	store := newStore(percentCoupon())
	s := newService(store)
	ctx := context.Background()
	req := Request{Code: "BOASVINDAS", CustomerID: "cli-1", Amount: aoa(5000)}

	c, err := s.Validate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Redeem(ctx, c, "cli-1", "sub-1", aoa(1000)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(ctx, req); !errors.Is(reason(t, err), ErrAlreadyUsed) {
		t.Error("o segundo uso pelo mesmo cliente devia ser recusado")
	}
	// Outro cliente continua a poder usar.
	req.CustomerID = "cli-2"
	if _, err := s.Validate(ctx, req); err != nil {
		t.Errorf("outro cliente foi recusado: %v", err)
	}
}

// --- resgate --------------------------------------------------------------------

func TestRedeemIsIdempotentPerPurchase(t *testing.T) {
	// Os webhooks reentregam. Gastar duas utilizações pela mesma compra é um
	// erro que só se nota quando o cupão esgota antes de tempo.
	c := percentCoupon()
	c.MaxRedemptions = 5
	store := newStore(c)
	s := newService(store)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := s.Redeem(ctx, c, "cli-1", "sub-1", aoa(1000)); err != nil {
			t.Fatal(err)
		}
	}
	if c.Redemptions != 1 {
		t.Errorf("utilizações = %d, queria 1", c.Redemptions)
	}
}

func TestRedeemRespectsTheLimitAtTheStore(t *testing.T) {
	c := percentCoupon()
	c.MaxRedemptions = 2
	c.MaxPerCustomer = 10
	store := newStore(c)
	s := newService(store)
	ctx := context.Background()

	if err := s.Redeem(ctx, c, "a", "s1", aoa(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Redeem(ctx, c, "b", "s2", aoa(1)); err != nil {
		t.Fatal(err)
	}
	// O terceiro não cabe, e quem decide isso é o armazenamento.
	if err := s.Redeem(ctx, c, "d", "s3", aoa(1)); !errors.Is(err, ErrExhausted) {
		t.Errorf("terceira utilização devolveu %v, queria esgotado", err)
	}
}

func TestReleaseGivesTheRedemptionBack(t *testing.T) {
	c := percentCoupon()
	c.MaxRedemptions = 1
	store := newStore(c)
	s := newService(store)
	ctx := context.Background()

	if err := s.Redeem(ctx, c, "cli-1", "sub-1", aoa(1000)); err != nil {
		t.Fatal(err)
	}
	if c.Redemptions != 1 {
		t.Fatalf("utilizações = %d", c.Redemptions)
	}
	// A compra caiu: a utilização volta ao cupão.
	if err := s.Release(ctx, c.ID, "sub-1"); err != nil {
		t.Fatal(err)
	}
	if c.Redemptions != 0 {
		t.Errorf("depois de libertar = %d, queria 0", c.Redemptions)
	}
	if _, err := s.Validate(ctx, Request{Code: "BOASVINDAS", CustomerID: "cli-2", Amount: aoa(5000)}); err != nil {
		t.Errorf("o cupão devia estar outra vez disponível: %v", err)
	}
}

func TestPreview(t *testing.T) {
	s := newService(newStore(percentCoupon()))
	d, err := s.Preview(context.Background(), Request{
		Code: "boasvindas", CustomerID: "cli-1", Amount: aoa(5000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := aoa(4000); d.Net.Minor != want.Minor {
		t.Errorf("pré-visualização = %s, queria %s", d.Net, want)
	}
	// Pré-visualizar não pode gastar nada.
	c, _ := s.store.ByCode(context.Background(), "BOASVINDAS")
	if c.Redemptions != 0 {
		t.Errorf("a pré-visualização consumiu %d utilizações", c.Redemptions)
	}
}
