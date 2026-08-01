package money

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFromMajor(t *testing.T) {
	tests := []struct {
		major    int64
		currency Currency
		want     int64
	}{
		{15, EUR, 1500},
		{5900, AOA, 590000},
		{1000, "JPY", 1000}, // moeda sem subunidade
		{5, "KWD", 5000},    // moeda de três casas
	}
	for _, tt := range tests {
		if got := FromMajor(tt.major, tt.currency).Minor; got != tt.want {
			t.Errorf("FromMajor(%d, %s) = %d, queria %d", tt.major, tt.currency, got, tt.want)
		}
	}
}

func TestDecimalAndMajor(t *testing.T) {
	a := New(123456, AOA)
	if got := a.Decimal(); got != "1234.56" {
		t.Errorf("Decimal() = %q, queria %q", got, "1234.56")
	}
	if got := a.Major(); got != 1234 {
		t.Errorf("Major() = %d, queria 1234", got)
	}
	// Numa moeda sem subunidade o valor menor é o maior.
	j := New(1000, "JPY")
	if got := j.Decimal(); got != "1000" {
		t.Errorf("Decimal() em JPY = %q, queria %q", got, "1000")
	}
	if got := j.Major(); got != 1000 {
		t.Errorf("Major() em JPY = %d, queria 1000", got)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"1234.56", 123456},
		{"1234,56", 123456},
		{"1.234,56", 123456}, // separador de milhar à europeia
		{"1 234.56", 123456},
		{"-99", -9900},
		{"0", 0},
	}
	for _, tt := range tests {
		got, err := Parse(tt.in, AOA)
		if err != nil {
			t.Errorf("Parse(%q) devolveu erro: %v", tt.in, err)
			continue
		}
		if got.Minor != tt.want {
			t.Errorf("Parse(%q) = %d, queria %d", tt.in, got.Minor, tt.want)
		}
	}
	if _, err := Parse("abc", AOA); err == nil {
		t.Error("Parse(\"abc\") devia falhar")
	}
}

func TestPercentOff(t *testing.T) {
	a := FromMajor(100, EUR)
	if got := a.PercentOff(0); got.Minor != 10000 {
		t.Errorf("sem desconto = %d, queria 10000", got.Minor)
	}
	if got := a.PercentOff(25); got.Minor != 7500 {
		t.Errorf("25%% = %d, queria 7500", got.Minor)
	}
	// Um cupão de oferta total tem de dar exactamente zero, e não um cêntimo
	// de resto que depois o gateway recusa por ser um valor inválido.
	if got := a.PercentOff(100); got.Minor != 0 {
		t.Errorf("100%% = %d, queria 0", got.Minor)
	}
}

func TestBasisPointsOff(t *testing.T) {
	// O desconto habitual de um compromisso anual: 17%, ou dois meses grátis.
	yearly := FromMajor(12000, AOA)
	got := yearly.BasisPointsOff(1700)
	if want := int64(996000); got.Minor != want {
		t.Errorf("17%% sobre 12000 = %d, queria %d", got.Minor, want)
	}
}

func TestSplitKeepsTotal(t *testing.T) {
	// Dividir 100,00 por 3 não pode perder nem inventar cêntimos.
	total := FromMajor(100, EUR)
	parts := total.Split(3)
	if len(parts) != 3 {
		t.Fatalf("Split(3) devolveu %d parcelas", len(parts))
	}
	var sum int64
	for _, p := range parts {
		sum += p.Minor
	}
	if sum != total.Minor {
		t.Errorf("soma das parcelas = %d, queria %d", sum, total.Minor)
	}
	if parts[0].Minor != 3334 || parts[2].Minor != 3333 {
		t.Errorf("repartição = %v, queria o resto nas primeiras parcelas", []int64{parts[0].Minor, parts[1].Minor, parts[2].Minor})
	}
}

func TestSplitNegative(t *testing.T) {
	total := New(-100, EUR)
	parts := total.Split(3)
	var sum int64
	for _, p := range parts {
		sum += p.Minor
	}
	if sum != -100 {
		t.Errorf("soma das parcelas negativas = %d, queria -100", sum)
	}
}

func TestRatio(t *testing.T) {
	a := FromMajor(1000, AOA)
	// 345 dias de 365, o exemplo do prorrateamento.
	got := a.Ratio(345, 365)
	if want := int64(94521); got.Minor != want {
		t.Errorf("Ratio(345, 365) = %d, queria %d", got.Minor, want)
	}
	if got := a.Ratio(1, 0); got.Minor != 0 {
		t.Errorf("divisão por zero devia dar 0, deu %d", got.Minor)
	}
}

func TestAddCurrencyMismatch(t *testing.T) {
	if _, err := FromMajor(1, EUR).Add(FromMajor(1, AOA)); err == nil {
		t.Error("somar euros a kwanzas devia falhar")
	}
}

func TestDivRoundHalfAwayFromZero(t *testing.T) {
	tests := []struct{ num, den, want int64 }{
		{5, 2, 3},
		{-5, 2, -3},
		{4, 2, 2},
		{1, 3, 0},
		{2, 3, 1},
	}
	for _, tt := range tests {
		if got := divRound(tt.num, tt.den); got != tt.want {
			t.Errorf("divRound(%d, %d) = %d, queria %d", tt.num, tt.den, got, tt.want)
		}
	}
}

// --- cobertura dos restantes construtores e operações ---------------------------

func TestCurrencyHelpers(t *testing.T) {
	if !Currency("jpy").IsZeroDecimal() {
		t.Error("o iene não tem subunidade")
	}
	if AOA.IsZeroDecimal() {
		t.Error("o kwanza tem subunidade na nossa representação")
	}
	valid := []Currency{AOA, EUR, USD, "jpy", " brl "}
	for _, c := range valid {
		if !c.Valid() {
			t.Errorf("%q devia ser válida", c)
		}
	}
	invalid := []Currency{"", "EU", "EURO", "eu1", "12A"}
	for _, c := range invalid {
		if c.Valid() {
			t.Errorf("%q não devia ser válida", c)
		}
	}
	if got := AOA.String(); got != "AOA" {
		t.Errorf("String() = %q", got)
	}
	// Uma moeda de três casas usa um factor de mil.
	if got := Currency("KWD").Factor(); got != 1000 {
		t.Errorf("factor do dinar = %d, queria 1000", got)
	}
}

func TestSignPredicates(t *testing.T) {
	zero, pos, neg := Zero(AOA), New(1, AOA), New(-1, AOA)
	if !zero.IsZero() || zero.IsPositive() || zero.IsNegative() {
		t.Errorf("zero = %+v", zero)
	}
	if pos.IsZero() || !pos.IsPositive() || pos.IsNegative() {
		t.Errorf("positivo = %+v", pos)
	}
	if neg.IsZero() || neg.IsPositive() || !neg.IsNegative() {
		t.Errorf("negativo = %+v", neg)
	}
}

func TestStringAndArithmetic(t *testing.T) {
	a := FromMajor(100, EUR)
	if got := a.String(); got != "100.00 EUR" {
		t.Errorf("String() = %q", got)
	}

	sum, err := a.Add(FromMajor(50, EUR))
	if err != nil || sum.Minor != 15000 {
		t.Errorf("soma = %v, %v", sum, err)
	}
	diff, err := a.Sub(FromMajor(30, EUR))
	if err != nil || diff.Minor != 7000 {
		t.Errorf("diferença = %v, %v", diff, err)
	}
	if _, err := a.Sub(FromMajor(1, AOA)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("subtrair moedas diferentes = %v", err)
	}
	if got := a.Mul(3); got.Minor != 30000 {
		t.Errorf("triplo = %v", got)
	}
	if got := a.Neg(); got.Minor != -10000 {
		t.Errorf("simétrico = %v", got)
	}
	if got := New(-500, EUR).Abs(); got.Minor != 500 {
		t.Errorf("absoluto de negativo = %v", got)
	}
	if got := a.Abs(); got.Minor != a.Minor {
		t.Errorf("absoluto de positivo = %v", got)
	}
}

func TestComparisons(t *testing.T) {
	a, b := FromMajor(100, EUR), FromMajor(200, EUR)
	if a.Cmp(b) != -1 || b.Cmp(a) != 1 || a.Cmp(a) != 0 {
		t.Error("comparação entre valores da mesma moeda")
	}
	// Moedas diferentes comparam por código, para dar ordem estável a
	// listagens com várias moedas.
	if FromMajor(1, AOA).Cmp(FromMajor(1, EUR)) >= 0 {
		t.Error("AOA devia ordenar antes de EUR")
	}
	if !a.Equal(FromMajor(100, EUR)) || a.Equal(b) || a.Equal(FromMajor(100, AOA)) {
		t.Error("igualdade")
	}
	if !a.LessThan(b) || a.GreaterThan(b) {
		t.Error("menor e maior")
	}
	// Moedas diferentes nunca são maiores nem menores uma do que a outra.
	if a.LessThan(FromMajor(999, AOA)) || a.GreaterThan(FromMajor(1, AOA)) {
		t.Error("moedas diferentes não se comparam por grandeza")
	}
}

func TestSplitEdges(t *testing.T) {
	if got := FromMajor(10, EUR).Split(0); got != nil {
		t.Errorf("dividir por zero = %v", got)
	}
	if got := FromMajor(10, EUR).Split(-3); got != nil {
		t.Errorf("dividir por negativo = %v", got)
	}
	parts := FromMajor(10, EUR).Split(1)
	if len(parts) != 1 || parts[0].Minor != 1000 {
		t.Errorf("uma parcela = %v", parts)
	}
}

func TestPercentAndBasisPointsEdges(t *testing.T) {
	a := FromMajor(100, EUR)
	if got := a.PercentOff(-5); got.Minor != a.Minor {
		t.Errorf("percentagem negativa = %v, queria não descontar", got)
	}
	if got := a.PercentOff(150); !got.IsZero() {
		t.Errorf("acima de 100%% = %v, queria zero", got)
	}
	if got := a.BasisPointsOff(-1); got.Minor != a.Minor {
		t.Errorf("pontos base negativos = %v", got)
	}
	if got := a.BasisPointsOff(20000); !got.IsZero() {
		t.Errorf("acima de 10000 pontos base = %v", got)
	}
	if got := a.BasisPointsOff(0); got.Minor != a.Minor {
		t.Errorf("zero pontos base = %v", got)
	}
}

func TestParseInvalid(t *testing.T) {
	// Os três primeiros ficam vazios depois de limpos; os últimos sobrevivem à
	// limpeza e só falham a converter, que é um segundo caminho de erro.
	for _, in := range []string{"", "-", "+", "   ", "abc", "€", "--5", ".", "1-2"} {
		if _, err := Parse(in, AOA); err == nil {
			t.Errorf("Parse(%q) devia falhar", in)
		}
	}
	// Um sinal isolado com dígitos continua a valer.
	if got, err := Parse("+42", AOA); err != nil || got.Minor != 4200 {
		t.Errorf("Parse(\"+42\") = %v, %v", got, err)
	}
}

func TestDivRoundByZero(t *testing.T) {
	if got := divRound(10, 0); got != 0 {
		t.Errorf("divisão por zero = %d, queria 0", got)
	}
	// Denominador negativo com numerador positivo.
	if got := divRound(5, -2); got != -3 {
		t.Errorf("divRound(5, -2) = %d, queria -3", got)
	}
}

func TestConverterDefaultClock(t *testing.T) {
	// Sem relógio configurado usa o do sistema, e uma taxa válida passa.
	store := NewMemoryRateStore()
	_ = store.PutRate(context.Background(), Rate{
		From: AOA, To: USD, Value: 1.0 / 1000,
		ValidUntil: time.Now().Add(time.Hour),
	})
	c := &Converter{store: store}
	got, err := c.Convert(context.Background(), FromMajor(1000, AOA), USD)
	if err != nil {
		t.Fatal(err)
	}
	if want := FromMajor(1, USD); got.Minor != want.Minor {
		t.Errorf("conversão = %s, queria %s", got, want)
	}
}

func TestRateValidAndPutDefaults(t *testing.T) {
	// Uma taxa sem prazo é sempre válida; uma taxa a zero nunca é.
	if !(Rate{Value: 1}).Valid(time.Now()) {
		t.Error("taxa sem prazo devia ser válida")
	}
	if (Rate{Value: 0, ValidUntil: time.Now().Add(time.Hour)}).Valid(time.Now()) {
		t.Error("uma taxa a zero não serve")
	}
	// PutRate carimba a hora de obtenção quando quem chama não a der.
	store := NewMemoryRateStore()
	_ = store.PutRate(context.Background(), Rate{From: "aoa", To: "usd", Value: 0.001})
	got, err := store.Rate(context.Background(), AOA, USD)
	if err != nil {
		t.Fatal(err)
	}
	if got.FetchedAt.IsZero() {
		t.Error("a hora de obtenção devia ter sido preenchida")
	}
	if got.From != AOA || got.To != USD {
		t.Errorf("moedas não normalizadas: %s para %s", got.From, got.To)
	}
}
