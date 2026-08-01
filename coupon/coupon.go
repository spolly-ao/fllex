// Package coupon é o motor de cupões de desconto.
//
// A parte difícil de um cupão não é aplicar a percentagem: é dizer não pelas
// razões certas e explicá-las a quem está no checkout. Um cupão recusado sem
// motivo é uma venda perdida e um pedido de suporte, por isso todas as recusas
// aqui trazem uma mensagem escrita para ser mostrada ao cliente.
//
// O outro cuidado é o resgate. Um cupão com dez utilizações que é validado por
// vinte pessoas ao mesmo tempo tem de esgotar em dez, e isso decide-se no
// armazenamento e não aqui. Ver [Store.Redeem].
package coupon

import (
	"errors"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/money"
)

// Kind é o tipo de desconto.
type Kind string

const (
	// KindPercent desconta uma percentagem do valor.
	KindPercent Kind = "percent"
	// KindAmount desconta um valor fixo. Nunca deixa o total negativo: um
	// desconto maior do que a compra fica pela compra.
	KindAmount Kind = "amount"
	// KindFreePeriod oferece ciclos inteiros. O primeiro pagamento é zero e a
	// cobrança seguinte só acontece passados os ciclos oferecidos.
	KindFreePeriod Kind = "free_period"
)

// Erros de validação. São todos sentinelas para quem chama poder distinguir os
// casos, mas a mensagem que se mostra ao cliente vem de [Error.Message].
var (
	ErrNotFound            = errors.New("coupon: cupão não encontrado")
	ErrInactive            = errors.New("coupon: cupão desactivado")
	ErrNotStarted          = errors.New("coupon: cupão ainda não começou")
	ErrExpired             = errors.New("coupon: cupão expirado")
	ErrExhausted           = errors.New("coupon: cupão esgotado")
	ErrAlreadyUsed         = errors.New("coupon: cupão já usado por este cliente")
	ErrPlanNotEligible     = errors.New("coupon: cupão não se aplica a este plano")
	ErrIntervalNotEligible = errors.New("coupon: cupão não se aplica a esta periodicidade")
	ErrBelowMinimum        = errors.New("coupon: valor abaixo do mínimo do cupão")
	ErrCurrency            = errors.New("coupon: cupão noutra moeda")
	ErrNewOnly             = errors.New("coupon: cupão só para clientes novos")
)

// Error é uma recusa com mensagem para o cliente.
type Error struct {
	// Err é o motivo, para o código decidir.
	Err error
	// Message é o motivo, para a pessoa ler.
	Message string
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

// Coupon é um cupão de desconto.
type Coupon struct {
	// ID é o identificador do registo.
	ID string
	// Code é o que o cliente escreve. Comparado sem distinguir maiúsculas nem
	// espaços à volta, porque é copiado de emails e de cartazes.
	Code string
	// Name é a descrição interna, para os relatórios.
	Name string

	// Kind e os campos do desconto.
	Kind Kind
	// Percent é a percentagem, quando Kind é [KindPercent].
	Percent int
	// Amount é o valor fixo, quando Kind é [KindAmount].
	Amount money.Amount
	// FreePeriods são os ciclos oferecidos, quando Kind é [KindFreePeriod].
	FreePeriods int

	// Currency limita o cupão a uma moeda. Vazio aceita todas, o que só é
	// seguro nos cupões de percentagem: um desconto de 5000 aplicado a euros e
	// a kwanzas são duas ofertas muito diferentes.
	Currency money.Currency

	// MinAmount é o valor mínimo de compra. Zero não exige mínimo.
	MinAmount money.Amount

	// PlanIDs limita o cupão a certos planos. Vazio aceita todos.
	PlanIDs []string
	// Intervals limita o cupão a certas periodicidades. Vazio aceita todas.
	Intervals []string

	// MaxRedemptions é o total de utilizações. Zero é sem limite.
	MaxRedemptions int
	// MaxPerCustomer é quantas vezes o mesmo cliente o pode usar. Zero lê-se
	// como uma: um cupão que o mesmo cliente pode usar sem conta é um erro
	// muito mais caro do que um cupão restritivo de mais.
	MaxPerCustomer int

	// NewCustomersOnly limita o cupão a quem nunca subscreveu.
	NewCustomersOnly bool

	// Recurring diz se o desconto se repete nos ciclos seguintes.
	//
	// O valor por omissão é falso, e é o certo: um cupão de boas-vindas vale
	// para o primeiro ciclo. Um desconto que recorre para sempre sem ninguém
	// ter decidido isso é a forma mais silenciosa de perder margem.
	Recurring bool
	// RecurringCycles limita quantos ciclos o desconto acompanha, quando
	// Recurring é verdade. Zero com Recurring é para sempre.
	RecurringCycles int

	// StartsAt e ExpiresAt delimitam a validade.
	StartsAt  *time.Time
	ExpiresAt *time.Time

	// Active permite desligar um cupão sem o apagar, para o histórico de quem
	// já o usou continuar a fazer sentido.
	Active bool

	// Redemptions é o total já usado, para leitura. A contagem que decide se o
	// cupão esgotou é a do armazenamento, dentro da transacção.
	Redemptions int

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NormalizeCode limpa um código para comparação.
func NormalizeCode(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// Request é o contexto em que o cupão está a ser usado.
type Request struct {
	// Code é o que o cliente escreveu.
	Code string
	// CustomerID é quem o está a usar.
	CustomerID string
	// Amount é o valor da compra antes do desconto.
	Amount money.Amount
	// PlanID e Interval identificam o que está a ser comprado.
	PlanID   string
	Interval string
	// NewCustomer indica se é a primeira subscrição deste cliente.
	NewCustomer bool
	// At é o instante da validação. Zero usa agora.
	At time.Time
}

// Discount é o desconto apurado.
type Discount struct {
	// CouponID e Code identificam o cupão aplicado.
	CouponID string
	Code     string
	// Kind é o tipo de desconto.
	Kind Kind
	// Gross é o valor antes do desconto.
	Gross money.Amount
	// Off é quanto se desconta.
	Off money.Amount
	// Net é o valor a pagar.
	Net money.Amount
	// Percent é a percentagem efectiva, para mostrar na factura mesmo quando o
	// cupão era de valor fixo.
	Percent int
	// FreePeriods são os ciclos oferecidos.
	FreePeriods int
	// Recurring e RecurringCycles descrevem se o desconto acompanha as
	// renovações.
	Recurring       bool
	RecurringCycles int
	// Label é o texto para o cliente ("20% de desconto", "2 meses grátis").
	Label string
}

// Applies indica se o desconto ainda vale no ciclo indicado (o primeiro é 1).
func (d Discount) Applies(cycleNumber int) bool {
	if cycleNumber <= 1 {
		return true
	}
	if !d.Recurring {
		return false
	}
	return d.RecurringCycles <= 0 || cycleNumber <= d.RecurringCycles
}

// Apply calcula o desconto sobre um valor, sem validar nada.
//
// Existe separado de [Service.Validate] para se poder pré-visualizar um preço
// sem tocar no armazenamento, e para o motor de renovação poder recalcular o
// desconto de um ciclo seguinte a partir do cupão já guardado.
func (c *Coupon) Apply(gross money.Amount) Discount {
	d := Discount{
		CouponID: c.ID, Code: c.Code, Kind: c.Kind, Gross: gross,
		Recurring: c.Recurring, RecurringCycles: c.RecurringCycles,
		FreePeriods: c.FreePeriods,
	}

	switch c.Kind {
	case KindPercent:
		d.Net = gross.PercentOff(c.Percent)
		d.Off, _ = gross.Sub(d.Net)
		d.Percent = c.Percent
		d.Label = itoa(c.Percent) + "% de desconto"

	case KindAmount:
		off := c.Amount
		// Um desconto maior do que a compra fica pela compra. Deixar o total
		// negativo faria o gateway recusar o pedido com um erro que ninguém
		// relaciona com o cupão.
		if off.GreaterThan(gross) {
			off = gross
		}
		d.Off = off
		d.Net, _ = gross.Sub(off)
		d.Percent = effectivePercent(gross, off)
		d.Label = off.String() + " de desconto"

	case KindFreePeriod:
		d.Off = gross
		d.Net = money.Zero(gross.Currency)
		d.Percent = 100
		n := c.FreePeriods
		if n <= 0 {
			n = 1
		}
		d.FreePeriods = n
		if n == 1 {
			d.Label = "1 ciclo grátis"
		} else {
			d.Label = itoa(n) + " ciclos grátis"
		}

	default:
		d.Net = gross
		d.Off = money.Zero(gross.Currency)
	}

	return d
}

// effectivePercent devolve a percentagem que o desconto representa, arredondada
// ao inteiro. Serve a factura, que mostra sempre uma percentagem.
func effectivePercent(gross, off money.Amount) int {
	if gross.Minor <= 0 {
		return 0
	}
	return int((off.Minor*100 + gross.Minor/2) / gross.Minor)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
