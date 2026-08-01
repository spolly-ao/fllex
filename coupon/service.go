package coupon

import (
	"context"
	"time"

	"github.com/spolly-ao/fllex/money"
)

// Redemption é o registo de uma utilização.
type Redemption struct {
	// ID é o identificador do registo.
	ID string
	// CouponID e Code identificam o cupão usado.
	CouponID string
	Code     string
	// CustomerID é quem o usou, e SubjectID o que ele pagou (a subscrição, a
	// encomenda).
	CustomerID string
	SubjectID  string
	// Off é quanto foi descontado.
	Off money.Amount
	// At é quando.
	At time.Time
}

// Store é o armazenamento dos cupões.
type Store interface {
	// ByCode devolve um cupão pelo código já normalizado, ou (nil, nil).
	ByCode(ctx context.Context, code string) (*Coupon, error)

	// CustomerRedemptions conta quantas vezes este cliente já usou o cupão.
	CustomerRedemptions(ctx context.Context, couponID, customerID string) (int, error)

	// Redeem regista a utilização e incrementa a contagem do cupão, tudo numa
	// só operação atómica.
	//
	// É aqui que se decide se o cupão esgotou, e tem de ser aqui: validar o
	// limite antes e gravar depois deixa vinte pessoas passarem por um cupão de
	// dez utilizações se chegarem ao mesmo tempo. A implementação deve fazer
	// um UPDATE condicional (`... WHERE redemptions < max_redemptions`) ou
	// travar a linha, e devolver [ErrExhausted] quando não houver lugar.
	//
	// Deve ser idempotente pelo par (couponID, subjectID): um webhook
	// reentregue não pode gastar duas utilizações pela mesma compra.
	Redeem(ctx context.Context, r *Redemption) error

	// Release devolve uma utilização ao cupão, quando o pagamento que a
	// justificava acaba por não se concretizar.
	Release(ctx context.Context, couponID, subjectID string) error
}

// Service valida e aplica cupões.
type Service struct {
	store Store
	// Now devolve a hora. Substituível em testes.
	Now func() time.Time
	// IDs gera identificadores para os registos de utilização.
	IDs func() string
}

// NewService cria o serviço.
func NewService(store Store) *Service {
	return &Service{store: store, Now: func() time.Time { return time.Now().UTC() }}
}

// Validate procura o cupão e verifica se pode ser usado neste contexto.
//
// Devolve sempre um [*Error] com mensagem pronta a mostrar ao cliente. Não
// consome utilização nenhuma: para isso é o [Service.Redeem], que só se chama
// quando o pagamento estiver confirmado.
func (s *Service) Validate(ctx context.Context, req Request) (*Coupon, error) {
	code := NormalizeCode(req.Code)
	if code == "" {
		return nil, &Error{Err: ErrNotFound, Message: "Escreva um código de desconto."}
	}

	c, err := s.store.ByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, &Error{Err: ErrNotFound, Message: "Este código não existe."}
	}

	at := req.At
	if at.IsZero() {
		at = s.now()
	}

	if !c.Active {
		return nil, &Error{Err: ErrInactive, Message: "Este código já não está disponível."}
	}
	if c.StartsAt != nil && at.Before(*c.StartsAt) {
		return nil, &Error{
			Err:     ErrNotStarted,
			Message: "Este código só é válido a partir de " + c.StartsAt.Format("02/01/2006") + ".",
		}
	}
	if c.ExpiresAt != nil && at.After(*c.ExpiresAt) {
		return nil, &Error{Err: ErrExpired, Message: "Este código expirou."}
	}
	if c.MaxRedemptions > 0 && c.Redemptions >= c.MaxRedemptions {
		return nil, &Error{Err: ErrExhausted, Message: "Este código já foi todo utilizado."}
	}
	if c.NewCustomersOnly && !req.NewCustomer {
		return nil, &Error{Err: ErrNewOnly, Message: "Este código é só para a primeira subscrição."}
	}
	if c.Currency != "" && req.Amount.Currency != "" &&
		money.NormalizeCurrency(string(c.Currency)) != money.NormalizeCurrency(string(req.Amount.Currency)) {
		return nil, &Error{Err: ErrCurrency, Message: "Este código não se aplica a pagamentos nesta moeda."}
	}
	if len(c.PlanIDs) > 0 && !contains(c.PlanIDs, req.PlanID) {
		return nil, &Error{Err: ErrPlanNotEligible, Message: "Este código não se aplica ao plano escolhido."}
	}
	if len(c.Intervals) > 0 && !contains(c.Intervals, req.Interval) {
		return nil, &Error{Err: ErrIntervalNotEligible, Message: "Este código não se aplica a esta periodicidade."}
	}
	if c.MinAmount.IsPositive() && req.Amount.LessThan(c.MinAmount) {
		return nil, &Error{
			Err:     ErrBelowMinimum,
			Message: "Este código exige uma compra de pelo menos " + c.MinAmount.String() + ".",
		}
	}

	// O limite por cliente fica para o fim porque é o único que custa uma ida
	// ao armazenamento.
	if req.CustomerID != "" {
		limit := c.MaxPerCustomer
		if limit <= 0 {
			limit = 1
		}
		used, err := s.store.CustomerRedemptions(ctx, c.ID, req.CustomerID)
		if err != nil {
			return nil, err
		}
		if used >= limit {
			return nil, &Error{Err: ErrAlreadyUsed, Message: "Já utilizou este código."}
		}
	}

	return c, nil
}

// Preview valida e calcula o desconto, sem consumir nada. É o que a página de
// checkout chama quando o cliente escreve o código.
func (s *Service) Preview(ctx context.Context, req Request) (Discount, error) {
	c, err := s.Validate(ctx, req)
	if err != nil {
		return Discount{}, err
	}
	return c.Apply(req.Amount), nil
}

// Redeem regista a utilização. Chame-o quando o pagamento estiver confirmado, e
// não no checkout: um cupão gasto por uma compra que nunca se concretizou é uma
// utilização que ninguém recupera.
func (s *Service) Redeem(ctx context.Context, c *Coupon, customerID, subjectID string, off money.Amount) error {
	return s.store.Redeem(ctx, &Redemption{
		ID:         s.newID(),
		CouponID:   c.ID,
		Code:       c.Code,
		CustomerID: customerID,
		SubjectID:  subjectID,
		Off:        off,
		At:         s.now(),
	})
}

// Release devolve a utilização ao cupão, quando a compra é anulada ou
// estornada.
func (s *Service) Release(ctx context.Context, couponID, subjectID string) error {
	return s.store.Release(ctx, couponID, subjectID)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

func (s *Service) newID() string {
	if s.IDs != nil {
		return s.IDs()
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
