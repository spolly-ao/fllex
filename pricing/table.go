// Package pricing calcula preços que dependem da quantidade: escalões por
// volume e grupos de pessoas cobertas pelo mesmo contrato.
//
// É deliberadamente uma camada à parte da cobrança. O preço decide-se antes de
// existir cobrança nenhuma, e misturar as duas coisas é o que leva a
// recalcular preços dentro do motor de renovação, onde já não há contexto para
// o fazer bem.
package pricing

import (
	"errors"
	"fmt"

	"github.com/spolly-ao/fllex/money"
)

var (
	// ErrEmptyTable indica uma tabela sem escalões.
	ErrEmptyTable = errors.New("pricing: tabela sem escalões")
	// ErrBadTier indica escalões mal ordenados ou sobrepostos.
	ErrBadTier = errors.New("pricing: escalões fora de ordem")
	// ErrCurrencyMismatch indica escalões em moedas diferentes.
	ErrCurrencyMismatch = errors.New("pricing: escalões em moedas diferentes")
)

// Mode diz como os escalões se aplicam, e é a decisão que mais dinheiro move
// numa tabela de preços.
type Mode string

const (
	// Graduated: cada escalão cobra o seu preço só pelas unidades que caem
	// dentro dele. Dez unidades numa tabela de "as 5 primeiras a 100, as
	// seguintes a 80" custam 5x100 + 5x80.
	//
	// É o que o cliente percebe como justo, e o que não cria saltos de preço.
	Graduated Mode = "graduated"

	// Volume: todas as unidades pagam o preço do escalão onde a quantidade
	// total cai. As mesmas dez unidades custam 10x80.
	//
	// É mais generoso a partir do limiar, mas cria um degrau: chegar ao
	// escalão seguinte pode baixar a factura, e há sempre um cliente a
	// perguntar porque é que comprar mais lhe fica mais barato.
	Volume Mode = "volume"
)

// Tier é um escalão da tabela.
type Tier struct {
	// UpTo é a última quantidade que este escalão cobre. Zero ou negativo
	// significa "daqui para cima", e só faz sentido no último escalão.
	UpTo int
	// UnitPrice é quanto custa cada unidade dentro do escalão.
	UnitPrice money.Amount
	// Flat é um valor fixo cobrado quando a quantidade entra neste escalão,
	// a somar ao que as unidades custarem. Serve as tabelas do tipo "até 10
	// utilizadores, 5000 por mês" (Flat 5000, UnitPrice 0).
	Flat money.Amount
}

// Unbounded indica se o escalão não tem tecto.
func (t Tier) Unbounded() bool { return t.UpTo <= 0 }

// Table é uma tabela de preços por quantidade.
type Table struct {
	// Mode diz como os escalões se aplicam.
	Mode Mode
	// Tiers são os escalões, por ordem crescente de UpTo.
	Tiers []Tier
	// Currency é a moeda da tabela.
	Currency money.Currency
	// Included é a quantidade que já vem incluída no preço base e por isso não
	// é cobrada aqui. Um plano que inclui três utilizadores e cobra a partir do
	// quarto põe Included a 3.
	Included int
	// Base é o valor fixo do plano, cobrado sempre, independente da
	// quantidade.
	Base money.Amount
}

// Validate verifica a tabela. Corra-a ao carregar a configuração, e não a cada
// cálculo: uma tabela mal ordenada dá preços errados em silêncio, e é o tipo
// de erro que só se descobre na factura do cliente.
func (t Table) Validate() error {
	if len(t.Tiers) == 0 {
		return ErrEmptyTable
	}
	cur := money.NormalizeCurrency(string(t.Currency))
	last := 0
	for i, tier := range t.Tiers {
		if tier.UnitPrice.Currency != "" && money.NormalizeCurrency(string(tier.UnitPrice.Currency)) != cur {
			return fmt.Errorf("%w: escalão %d", ErrCurrencyMismatch, i)
		}
		if tier.Flat.Currency != "" && money.NormalizeCurrency(string(tier.Flat.Currency)) != cur {
			return fmt.Errorf("%w: escalão %d", ErrCurrencyMismatch, i)
		}
		if tier.Unbounded() {
			if i != len(t.Tiers)-1 {
				return fmt.Errorf("%w: só o último escalão pode ser aberto (escalão %d)", ErrBadTier, i)
			}
			continue
		}
		if tier.UpTo <= last {
			return fmt.Errorf("%w: escalão %d acaba em %d, depois de %d", ErrBadTier, i, tier.UpTo, last)
		}
		last = tier.UpTo
	}
	return nil
}

// Breakdown é a decomposição de um preço, para se poder mostrar ao cliente
// exactamente como se chegou ao valor.
//
// Existe porque uma tabela de escalões sem explicação é indistinguível de um
// número inventado, e a primeira coisa que quem recebe a factura quer saber é
// de onde veio.
type Breakdown struct {
	// Quantity é a quantidade pedida.
	Quantity int
	// Billable é a quantidade que sobra depois de descontar as incluídas.
	Billable int
	// Base é o valor fixo do plano.
	Base money.Amount
	// Lines são as parcelas por escalão.
	Lines []BreakdownLine
	// Total é a soma de tudo.
	Total money.Amount
}

// BreakdownLine é uma parcela do cálculo.
type BreakdownLine struct {
	// From e To delimitam as unidades cobradas nesta parcela (inclusive).
	From, To int
	// Units é quantas unidades esta parcela cobra.
	Units int
	// UnitPrice é o preço aplicado a cada uma.
	UnitPrice money.Amount
	// Flat é o valor fixo do escalão, quando existe.
	Flat money.Amount
	// Amount é o total da parcela.
	Amount money.Amount
}

// Price calcula o preço para uma quantidade.
func (t Table) Price(quantity int) (money.Amount, error) {
	b, err := t.Explain(quantity)
	if err != nil {
		return money.Amount{}, err
	}
	return b.Total, nil
}

// MustPrice é [Table.Price] sem erro, para tabelas já validadas. Uma tabela
// inválida devolve zero em vez de entrar em pânico: numa página de preços,
// mostrar zero é mau, mas derrubar o servidor é pior.
func (t Table) MustPrice(quantity int) money.Amount {
	a, err := t.Price(quantity)
	if err != nil {
		return money.Zero(t.Currency)
	}
	return a
}

// Explain calcula o preço e devolve a decomposição.
func (t Table) Explain(quantity int) (Breakdown, error) {
	if err := t.Validate(); err != nil {
		return Breakdown{}, err
	}
	cur := money.NormalizeCurrency(string(t.Currency))

	billable := quantity - t.Included
	if billable < 0 {
		billable = 0
	}

	out := Breakdown{
		Quantity: quantity,
		Billable: billable,
		Base:     t.Base,
		Total:    money.Zero(cur),
	}
	if t.Base.IsPositive() {
		out.Total = t.Base
	}

	if billable == 0 {
		return out, nil
	}

	switch t.Mode {
	case Volume:
		tier := t.tierFor(billable)
		line := BreakdownLine{
			From: 1, To: billable, Units: billable,
			UnitPrice: tier.UnitPrice, Flat: tier.Flat,
			Amount: tier.UnitPrice.Mul(int64(billable)),
		}
		if tier.Flat.IsPositive() {
			line.Amount = mustAdd(line.Amount, tier.Flat, cur)
		}
		out.Lines = append(out.Lines, line)
		out.Total = mustAdd(out.Total, line.Amount, cur)

	default: // Graduated
		// Os escalões vêm validados de [Table.Validate], logo estritamente
		// crescentes, e por isso cada volta cobre pelo menos uma unidade. Não
		// há aqui defesa contra escalões repetidos de propósito: o sítio de
		// apanhar uma tabela mal ordenada é a validação, não o cálculo.
		lower := 0
		for _, tier := range t.Tiers {
			if lower >= billable {
				break
			}
			upper := tier.UpTo
			if tier.Unbounded() || upper > billable {
				upper = billable
			}
			units := upper - lower
			line := BreakdownLine{
				From: lower + 1, To: upper, Units: units,
				UnitPrice: tier.UnitPrice, Flat: tier.Flat,
				Amount: tier.UnitPrice.Mul(int64(units)),
			}
			if tier.Flat.IsPositive() {
				line.Amount = mustAdd(line.Amount, tier.Flat, cur)
			}
			out.Lines = append(out.Lines, line)
			out.Total = mustAdd(out.Total, line.Amount, cur)
			lower = upper
		}
	}

	return out, nil
}

// tierFor devolve o escalão onde a quantidade cai. Uma quantidade acima do
// último escalão fechado usa esse mesmo, que é o comportamento seguro: cobrar
// pelo tecto é preferível a não cobrar nada.
func (t Table) tierFor(quantity int) Tier {
	for _, tier := range t.Tiers {
		if tier.Unbounded() || quantity <= tier.UpTo {
			return tier
		}
	}
	return t.Tiers[len(t.Tiers)-1]
}

// Capacity devolve a quantidade máxima que a tabela cobre, ou -1 se for
// aberta. É o que permite dizer a um cliente que o plano não chega para o que
// ele quer antes de o deixar comprar.
func (t Table) Capacity() int {
	if len(t.Tiers) == 0 {
		return 0
	}
	last := t.Tiers[len(t.Tiers)-1]
	if last.Unbounded() {
		return -1
	}
	return last.UpTo + t.Included
}

// Covers indica se a tabela cobre esta quantidade.
func (t Table) Covers(quantity int) bool {
	c := t.Capacity()
	return c < 0 || quantity <= c
}

func mustAdd(a, b money.Amount, cur money.Currency) money.Amount {
	if a.Currency == "" {
		a = money.Zero(cur)
	}
	if b.Currency == "" {
		return a
	}
	sum, err := a.Add(b)
	if err != nil {
		return a
	}
	return sum
}
