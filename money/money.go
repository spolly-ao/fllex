// Package money representa dinheiro em unidades menores inteiras (centavos,
// cêntimos), a única forma de o guardar sem acumular erro de vírgula flutuante.
//
// Um sistema de cobrança acaba quase sempre com três representações a
// conviver: unidades inteiras (int64), centavos (int64) e float64. Misturá-las
// é a origem clássica de cobranças com um cêntimo a mais ou a menos, por isso
// aqui há uma só representação canónica e conversores explícitos para o que
// cada gateway espera:
//
//   - Stripe recebe unidades menores, sem multiplicar nas moedas de zero casas.
//     Ver [Amount.Minor] e [Currency.IsZeroDecimal].
//   - MoMenu recebe kwanzas inteiros (unidade maior). Ver [Amount.Major].
//   - Proxypay recebe uma string decimal com duas casas. Ver [Amount.Decimal].
package money

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ErrCurrencyMismatch é devolvido ao operar sobre montantes de moedas
// diferentes. Somar kwanzas a euros nunca é o que quem chama queria.
var ErrCurrencyMismatch = errors.New("money: moedas diferentes")

// Amount é uma quantia numa moeda, guardada em unidades menores.
// O valor zero é utilizável (zero numa moeda vazia), mas prefira [New].
type Amount struct {
	// Minor é o valor em unidades menores da moeda: 1500 = 15,00 EUR,
	// 250000 = 2500,00 AOA. Pode ser negativo (estornos, débitos).
	Minor int64
	// Currency é o código ISO 4217.
	Currency Currency
}

// New cria um montante a partir do valor já em unidades menores.
func New(minor int64, currency Currency) Amount {
	return Amount{Minor: minor, Currency: NormalizeCurrency(string(currency))}
}

// FromMajor cria um montante a partir da unidade maior inteira: FromMajor(15,
// EUR) são 15,00 EUR e FromMajor(5900, AOA) são 5900,00 AOA.
//
// É o construtor a usar quando o preço vem de uma tabela em unidades inteiras,
// que é como os planos em kwanza são normalmente escritos.
func FromMajor(major int64, currency Currency) Amount {
	c := NormalizeCurrency(string(currency))
	return Amount{Minor: major * c.Factor(), Currency: c}
}

// FromFloat cria um montante a partir da unidade maior em vírgula flutuante,
// arredondando ao meio para cima. Existe para a fronteira com sistemas que só
// falam float: gateways que recebem e devolvem decimais, e colunas DECIMAL de
// bases de dados antigas. O valor deixa de ser float assim que entra.
func FromFloat(major float64, currency Currency) Amount {
	c := NormalizeCurrency(string(currency))
	return Amount{Minor: int64(math.Round(major * float64(c.Factor()))), Currency: c}
}

// Parse lê uma quantia escrita na unidade maior ("1234.56", "1 234,56",
// "-99"). Aceita ponto ou vírgula como separador decimal e ignora espaços e
// separadores de milhar, porque é isto que chega de formulários e de CSV.
func Parse(s string, currency Currency) (Amount, error) {
	c := NormalizeCurrency(string(currency))
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9', r == '-', r == '+':
			return r
		case r == ',' || r == '.':
			return '.'
		default:
			return -1 // remove espaços, símbolos de moeda e separadores de milhar
		}
	}, s)
	// Com dois separadores, o primeiro era de milhar ("1.234,56" -> "1.234.56").
	if n := strings.Count(clean, "."); n > 1 {
		i := strings.LastIndex(clean, ".")
		clean = strings.ReplaceAll(clean[:i], ".", "") + clean[i:]
	}
	if clean == "" || clean == "-" || clean == "+" {
		return Amount{}, fmt.Errorf("money: quantia inválida: %q", s)
	}
	f, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return Amount{}, fmt.Errorf("money: quantia inválida: %q", s)
	}
	return FromFloat(f, c), nil
}

// Zero devolve o zero da moeda.
func Zero(currency Currency) Amount { return New(0, currency) }

// IsZero indica se o montante é nulo.
func (a Amount) IsZero() bool { return a.Minor == 0 }

// IsPositive indica se o montante é maior do que zero.
func (a Amount) IsPositive() bool { return a.Minor > 0 }

// IsNegative indica se o montante é menor do que zero.
func (a Amount) IsNegative() bool { return a.Minor < 0 }

// Major devolve a parte inteira na unidade maior, truncando a subunidade.
// É o que o MoMenu espera no campo amount (kwanzas inteiros).
func (a Amount) Major() int64 { return a.Minor / a.Currency.Factor() }

// Float devolve o valor na unidade maior em vírgula flutuante. Use apenas na
// fronteira com APIs que exigem float; nunca para contas internas.
func (a Amount) Float() float64 {
	return float64(a.Minor) / float64(a.Currency.Factor())
}

// Decimal devolve o valor na unidade maior como string, com as casas decimais
// da moeda ("1234.56"). É o formato que o Proxypay exige no campo amount.
func (a Amount) Decimal() string {
	return strconv.FormatFloat(a.Float(), 'f', a.Currency.Exponent(), 64)
}

// String devolve uma representação legível ("1234.56 AOA").
func (a Amount) String() string { return a.Decimal() + " " + a.Currency.String() }

// Add soma dois montantes da mesma moeda.
func (a Amount) Add(b Amount) (Amount, error) {
	if !a.sameCurrency(b) {
		return Amount{}, ErrCurrencyMismatch
	}
	return Amount{Minor: a.Minor + b.Minor, Currency: a.Currency}, nil
}

// Sub subtrai b de a (mesma moeda).
func (a Amount) Sub(b Amount) (Amount, error) {
	if !a.sameCurrency(b) {
		return Amount{}, ErrCurrencyMismatch
	}
	return Amount{Minor: a.Minor - b.Minor, Currency: a.Currency}, nil
}

// Mul multiplica o montante por um inteiro (ex.: 12 meses de mensalidade).
func (a Amount) Mul(n int64) Amount {
	return Amount{Minor: a.Minor * n, Currency: a.Currency}
}

// Neg devolve o simétrico (usado nos débitos da carteira).
func (a Amount) Neg() Amount { return Amount{Minor: -a.Minor, Currency: a.Currency} }

// Abs devolve o valor absoluto.
func (a Amount) Abs() Amount {
	if a.Minor < 0 {
		return a.Neg()
	}
	return a
}

// Cmp compara dois montantes da mesma moeda: -1, 0 ou 1. Moedas diferentes
// comparam por código, para dar uma ordem estável em listagens.
func (a Amount) Cmp(b Amount) int {
	if !a.sameCurrency(b) {
		return strings.Compare(a.Currency.String(), b.Currency.String())
	}
	switch {
	case a.Minor < b.Minor:
		return -1
	case a.Minor > b.Minor:
		return 1
	default:
		return 0
	}
}

// Equal indica se são o mesmo valor na mesma moeda.
func (a Amount) Equal(b Amount) bool { return a.sameCurrency(b) && a.Minor == b.Minor }

// GreaterThan indica se a é maior do que b (mesma moeda).
func (a Amount) GreaterThan(b Amount) bool { return a.sameCurrency(b) && a.Minor > b.Minor }

// LessThan indica se a é menor do que b (mesma moeda).
func (a Amount) LessThan(b Amount) bool { return a.sameCurrency(b) && a.Minor < b.Minor }

func (a Amount) sameCurrency(b Amount) bool {
	return NormalizeCurrency(string(a.Currency)) == NormalizeCurrency(string(b.Currency))
}

// PercentOff aplica uma percentagem de desconto (0 a 100) e devolve o valor a
// pagar, arredondado ao meio para cima. Um desconto de 100% dá exactamente
// zero, que é o caso dos cupões de oferta total.
func (a Amount) PercentOff(percent int) Amount {
	switch {
	case percent <= 0:
		return a
	case percent >= 100:
		return Amount{Minor: 0, Currency: a.Currency}
	}
	keep := 100 - int64(percent)
	return Amount{
		Minor:    divRound(a.Minor*keep, 100),
		Currency: a.Currency,
	}
}

// BasisPointsOff aplica um desconto em pontos base (1700 = 17%), a forma
// habitual de guardar o desconto de um compromisso anual. Mais fino do que a
// percentagem inteira e sem vírgula flutuante.
func (a Amount) BasisPointsOff(bps int) Amount {
	switch {
	case bps <= 0:
		return a
	case bps >= 10000:
		return Amount{Minor: 0, Currency: a.Currency}
	}
	keep := 10000 - int64(bps)
	return Amount{
		Minor:    divRound(a.Minor*keep, 10000),
		Currency: a.Currency,
	}
}

// Ratio reparte o montante na proporção num/den, arredondando ao meio para
// cima. É a conta por trás do prorrateamento por dias.
func (a Amount) Ratio(num, den int64) Amount {
	if den == 0 {
		return Amount{Minor: 0, Currency: a.Currency}
	}
	return Amount{Minor: divRound(a.Minor*num, den), Currency: a.Currency}
}

// Split reparte o montante por n parcelas sem perder nem inventar unidades: o
// resto é distribuído uma unidade de cada vez pelas primeiras parcelas, por
// isso a soma das parcelas é sempre exactamente o total.
//
// É assim que se divide um contrato anual em 12 prestações sem que o cliente
// pague um cêntimo a mais nem a empresa receba um a menos.
func (a Amount) Split(n int) []Amount {
	if n <= 0 {
		return nil
	}
	out := make([]Amount, n)
	base := a.Minor / int64(n)
	rest := a.Minor % int64(n)
	// Com montantes negativos o resto também é negativo; o passo acompanha o
	// sinal para não somar onde devia subtrair.
	step := int64(1)
	if rest < 0 {
		step, rest = -1, -rest
	}
	for i := 0; i < n; i++ {
		v := base
		if int64(i) < rest {
			v += step
		}
		out[i] = Amount{Minor: v, Currency: a.Currency}
	}
	return out
}

// divRound divide arredondando ao meio para longe do zero, sem passar por
// float64 (que perde precisão em montantes grandes).
func divRound(num, den int64) int64 {
	if den == 0 {
		return 0
	}
	neg := (num < 0) != (den < 0)
	if num < 0 {
		num = -num
	}
	if den < 0 {
		den = -den
	}
	q := (num + den/2) / den
	if neg {
		return -q
	}
	return q
}
