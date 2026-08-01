package money

import "strings"

// Currency é um código ISO 4217 em maiúsculas ("AOA", "EUR", "USD").
type Currency string

// Moedas dos mercados que esta biblioteca cobre.
const (
	AOA Currency = "AOA" // kwanza angolano
	EUR Currency = "EUR"
	USD Currency = "USD"
	BRL Currency = "BRL"
	GBP Currency = "GBP"
	MZN Currency = "MZN" // metical moçambicano
	CVE Currency = "CVE" // escudo cabo-verdiano
)

// exponents guarda as moedas cuja subunidade não tem duas casas decimais.
// Tudo o que não está aqui vale 2 (o caso comum: cêntimo, centavo, penny).
//
// A lista importa para o Stripe, que rejeita um montante multiplicado por 100
// numa moeda de zero casas, e para qualquer gateway que receba o valor na
// unidade maior.
var exponents = map[Currency]int{
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "JPY": 0, "KMF": 0, "KRW": 0,
	"MGA": 0, "PYG": 0, "RWF": 0, "UGX": 0, "VND": 0, "VUV": 0, "XAF": 0,
	"XOF": 0, "XPF": 0,
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,
}

// NormalizeCurrency limpa e passa a maiúsculas um código de moeda.
func NormalizeCurrency(s string) Currency {
	return Currency(strings.ToUpper(strings.TrimSpace(s)))
}

// Exponent devolve quantas casas decimais tem a subunidade da moeda.
func (c Currency) Exponent() int {
	if e, ok := exponents[NormalizeCurrency(string(c))]; ok {
		return e
	}
	return 2
}

// Factor devolve quantas unidades menores cabem numa unidade maior
// (100 no caso comum, 1 nas moedas de zero casas, 1000 nas de três).
func (c Currency) Factor() int64 {
	f := int64(1)
	for i := 0; i < c.Exponent(); i++ {
		f *= 10
	}
	return f
}

// IsZeroDecimal indica se a moeda não tem subunidade.
func (c Currency) IsZeroDecimal() bool { return c.Exponent() == 0 }

// String devolve o código normalizado.
func (c Currency) String() string { return string(NormalizeCurrency(string(c))) }

// Valid indica se o código tem a forma de um ISO 4217 (três letras).
func (c Currency) Valid() bool {
	s := c.String()
	if len(s) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}
