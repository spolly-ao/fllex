package pricing

import (
	"errors"
	"testing"

	"github.com/spolly-ao/fllex/money"
)

func aoa(v int64) money.Amount { return money.FromMajor(v, money.AOA) }

func table(mode Mode) Table {
	// As cinco primeiras a 1000, as dez seguintes a 800, daí para cima a 600.
	return Table{
		Mode:     mode,
		Currency: money.AOA,
		Tiers: []Tier{
			{UpTo: 5, UnitPrice: aoa(1000)},
			{UpTo: 15, UnitPrice: aoa(800)},
			{UnitPrice: aoa(600)},
		},
	}
}

func TestGraduatedChargesEachTierSeparately(t *testing.T) {
	tb := table(Graduated)
	tests := []struct {
		qty  int
		want int64
	}{
		{0, 0},
		{1, 1000},
		{5, 5000},
		{10, 5*1000 + 5*800},          // 9000
		{15, 5*1000 + 10*800},         // 13000
		{20, 5*1000 + 10*800 + 5*600}, // 16000
	}
	for _, tt := range tests {
		got, err := tb.Price(tt.qty)
		if err != nil {
			t.Fatalf("qty %d: %v", tt.qty, err)
		}
		if want := aoa(tt.want); got.Minor != want.Minor {
			t.Errorf("escalonado com %d unidades = %s, queria %s", tt.qty, got, want)
		}
	}
}

func TestVolumeChargesEverythingAtOneTier(t *testing.T) {
	tb := table(Volume)
	tests := []struct {
		qty  int
		want int64
	}{
		{5, 5 * 1000},
		{10, 10 * 800},
		{20, 20 * 600},
	}
	for _, tt := range tests {
		got, _ := tb.Price(tt.qty)
		if want := aoa(tt.want); got.Minor != want.Minor {
			t.Errorf("por volume com %d unidades = %s, queria %s", tt.qty, got, want)
		}
	}
}

func TestVolumeCreatesAPriceDrop(t *testing.T) {
	// O degrau que o modo por volume cria e o escalonado não: comprar mais
	// pode ficar mais barato. É a razão de existirem os dois modos, e vale a
	// pena estar coberto por um teste para ninguém mudar o padrão por engano.
	tb := table(Volume)
	at5, _ := tb.Price(5)
	at6, _ := tb.Price(6)
	if !at6.LessThan(at5) {
		t.Errorf("por volume: 6 unidades (%s) devia custar menos do que 5 (%s)", at6, at5)
	}

	grad := table(Graduated)
	g5, _ := grad.Price(5)
	g6, _ := grad.Price(6)
	if !g6.GreaterThan(g5) {
		t.Errorf("escalonado: 6 unidades (%s) tem de custar mais do que 5 (%s)", g6, g5)
	}
}

func TestIncludedQuantityIsNotCharged(t *testing.T) {
	tb := table(Graduated)
	tb.Included = 3
	tb.Base = aoa(5000)

	// Três incluídas: só o valor base.
	got, _ := tb.Price(3)
	if want := aoa(5000); got.Minor != want.Minor {
		t.Errorf("dentro do incluído = %s, queria só a base %s", got, want)
	}
	// Cinco pessoas: base mais duas unidades no primeiro escalão.
	got, _ = tb.Price(5)
	if want := aoa(5000 + 2*1000); got.Minor != want.Minor {
		t.Errorf("cinco = %s, queria %s", got, want)
	}
}

func TestFlatTier(t *testing.T) {
	tb := Table{
		Mode: Volume, Currency: money.AOA,
		Tiers: []Tier{
			{UpTo: 10, Flat: aoa(5000)},
			{UpTo: 50, Flat: aoa(20000)},
			{Flat: aoa(60000)},
		},
	}
	got, _ := tb.Price(7)
	if want := aoa(5000); got.Minor != want.Minor {
		t.Errorf("sete = %s, queria %s", got, want)
	}
	got, _ = tb.Price(200)
	if want := aoa(60000); got.Minor != want.Minor {
		t.Errorf("duzentos = %s, queria %s", got, want)
	}
}

func TestExplainAddsUpToTheTotal(t *testing.T) {
	tb := table(Graduated)
	tb.Base = aoa(2000)
	b, err := tb.Explain(20)
	if err != nil {
		t.Fatal(err)
	}
	sum := b.Base.Minor
	for _, line := range b.Lines {
		sum += line.Amount.Minor
	}
	if sum != b.Total.Minor {
		t.Errorf("as parcelas somam %d e o total diz %d", sum, b.Total.Minor)
	}
	if len(b.Lines) != 3 {
		t.Errorf("parcelas = %d, queria 3 (uma por escalão tocado)", len(b.Lines))
	}
	if b.Lines[0].From != 1 || b.Lines[0].To != 5 {
		t.Errorf("primeira parcela = %d a %d, queria 1 a 5", b.Lines[0].From, b.Lines[0].To)
	}
}

func TestValidate(t *testing.T) {
	if err := (Table{Currency: money.AOA}).Validate(); !errors.Is(err, ErrEmptyTable) {
		t.Errorf("tabela vazia devolveu %v", err)
	}
	// Escalões fora de ordem dão preços errados em silêncio.
	bad := Table{Currency: money.AOA, Tiers: []Tier{
		{UpTo: 10, UnitPrice: aoa(100)},
		{UpTo: 5, UnitPrice: aoa(80)},
	}}
	if err := bad.Validate(); !errors.Is(err, ErrBadTier) {
		t.Errorf("escalões desordenados devolveram %v", err)
	}
	// Um escalão aberto no meio esconde todos os que vêm depois.
	openMid := Table{Currency: money.AOA, Tiers: []Tier{
		{UnitPrice: aoa(100)},
		{UpTo: 20, UnitPrice: aoa(80)},
	}}
	if err := openMid.Validate(); !errors.Is(err, ErrBadTier) {
		t.Errorf("escalão aberto a meio devolveu %v", err)
	}
	// Moeda diferente num escalão.
	mixed := Table{Currency: money.AOA, Tiers: []Tier{{UpTo: 5, UnitPrice: money.FromMajor(10, money.EUR)}}}
	if err := mixed.Validate(); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("moedas misturadas devolveram %v", err)
	}
}

func TestCapacity(t *testing.T) {
	if got := table(Graduated).Capacity(); got != -1 {
		t.Errorf("tabela aberta = %d, queria -1", got)
	}
	closed := Table{Currency: money.AOA, Tiers: []Tier{{UpTo: 10, UnitPrice: aoa(100)}}, Included: 2}
	if got := closed.Capacity(); got != 12 {
		t.Errorf("capacidade = %d, queria 12", got)
	}
	if closed.Covers(13) {
		t.Error("13 não cabe numa tabela que cobre 12")
	}
}

// --- grupos --------------------------------------------------------------------

func group(members int, holderCovered bool) Group {
	g := Group{HolderCovered: holderCovered}
	g.People = append(g.People, Person{ID: "titular", Role: RoleHolder})
	for i := 0; i < members; i++ {
		g.People = append(g.People, Person{ID: "m" + itoa(i), Role: RoleMember})
	}
	return g
}

func TestGroupSizeRespectsHolderCoverage(t *testing.T) {
	// O titular que paga a família mas não compra cobertura para si não conta,
	// e escrever que conta dá cobertura a quem ninguém comprou.
	if got := group(3, true).Size(); got != 4 {
		t.Errorf("com titular coberto = %d, queria 4", got)
	}
	if got := group(3, false).Size(); got != 3 {
		t.Errorf("sem titular coberto = %d, queria 3", got)
	}
}

func TestSchemePrice(t *testing.T) {
	s := Scheme{Table: table(Graduated)}
	b, err := s.Price(group(3, true)) // quatro pessoas
	if err != nil {
		t.Fatal(err)
	}
	if want := aoa(4000); b.Total.Minor != want.Minor {
		t.Errorf("grupo de quatro = %s, queria %s", b.Total, want)
	}
	if b.Size != 4 {
		t.Errorf("tamanho = %d", b.Size)
	}
}

func TestCategoryFactorDiscountsWithoutLosingCents(t *testing.T) {
	s := Scheme{
		Table:          Table{Mode: Graduated, Currency: money.AOA, Tiers: []Tier{{UnitPrice: aoa(1000)}}},
		CategoryFactor: map[string]int{"crianca": 5000}, // metade
	}
	g := Group{
		HolderCovered: true,
		People: []Person{
			{ID: "t", Role: RoleHolder},
			{ID: "a", Role: RoleMember},
			{ID: "c1", Role: RoleMember, Category: "crianca"},
			{ID: "c2", Role: RoleMember, Category: "crianca"},
		},
	}
	b, err := s.Price(g)
	if err != nil {
		t.Fatal(err)
	}
	// Quatro a 1000 dá 4000; duas crianças a metade tiram 1000.
	if want := aoa(3000); b.Total.Minor != want.Minor {
		t.Errorf("total = %s, queria %s", b.Total, want)
	}
	if len(b.Adjustments) != 1 || b.Adjustments[0].People != 2 {
		t.Errorf("acertos = %+v", b.Adjustments)
	}
	if !b.Adjustments[0].Delta.IsNegative() {
		t.Errorf("o acerto de um desconto tem de ser negativo: %s", b.Adjustments[0].Delta)
	}
}

func TestCategoryFactorKeepsTheTotalExact(t *testing.T) {
	// Um total que não divide certo pelas pessoas não pode ganhar nem perder
	// unidades ao ser ajustado por categoria.
	s := Scheme{
		Table:          Table{Mode: Volume, Currency: money.AOA, Tiers: []Tier{{Flat: money.New(100001, money.AOA)}}},
		CategoryFactor: map[string]int{},
	}
	b, _ := s.Price(group(2, true))
	if b.Total.Minor != 100001 {
		t.Errorf("sem acertos o total mudou: %d", b.Total.Minor)
	}
}

func TestSchemeAccepts(t *testing.T) {
	s := Scheme{Table: table(Graduated), MinPeople: 2, MaxPeople: 8}
	if ok, _ := s.Accepts(group(0, true)); ok {
		t.Error("um grupo de uma pessoa não devia ser aceite com mínimo de duas")
	}
	if ok, why := s.Accepts(group(1, true)); !ok {
		t.Errorf("duas pessoas deviam ser aceites: %s", why)
	}
	if ok, why := s.Accepts(group(20, true)); ok {
		t.Errorf("vinte e uma pessoas não cabem: %s", why)
	}
}

func TestQuote(t *testing.T) {
	// Contrato de doze meses cobrado ao mês.
	q := Quote(aoa(10000), 12, 1, 0)
	if want := aoa(120000); q.Total.Minor != want.Minor {
		t.Errorf("total = %s, queria %s", q.Total, want)
	}
	if want := aoa(10000); q.Instalment.Minor != want.Minor {
		t.Errorf("prestação = %s, queria %s", q.Instalment, want)
	}
	if q.Instalments != 12 {
		t.Errorf("prestações = %d, queria 12", q.Instalments)
	}

	// O mesmo contrato pago de uma vez: uma prestação, o contrato inteiro.
	q = Quote(aoa(10000), 12, 12, 0)
	if q.Instalments != 1 || q.Instalment.Minor != q.Total.Minor {
		t.Errorf("pagamento único: %d prestações de %s", q.Instalments, q.Instalment)
	}

	// Com desconto de compromisso anual.
	q = Quote(aoa(10000), 12, 1, 1700)
	if want := aoa(99600); q.Total.Minor != want.Minor {
		t.Errorf("com 17%% = %s, queria %s", q.Total, want)
	}
}

// --- cobertura dos restantes caminhos -------------------------------------------

func TestHolderAndCategoryCount(t *testing.T) {
	g := Group{
		HolderCovered: true,
		People: []Person{
			{ID: "t", Role: RoleHolder},
			{ID: "a", Role: RoleMember, Category: "adulto"},
			{ID: "c", Role: RoleMember, Category: "crianca"},
			{ID: "d", Role: RoleMember, Category: "crianca"},
		},
	}
	h, ok := g.Holder()
	if !ok || h.ID != "t" {
		t.Errorf("titular = %+v, %v", h, ok)
	}
	counts := g.CategoryCount()
	if counts["crianca"] != 2 || counts["adulto"] != 1 || counts[""] != 1 {
		t.Errorf("contagem por categoria = %v", counts)
	}

	// Sem titular coberto, ele não entra na contagem.
	g.HolderCovered = false
	if got := g.CategoryCount()[""]; got != 0 {
		t.Errorf("o titular não coberto continua a contar: %d", got)
	}
	// Um grupo sem titular nenhum.
	if _, ok := (Group{People: []Person{{ID: "a", Role: RoleMember}}}).Holder(); ok {
		t.Error("não havia titular")
	}
}

func TestMustPriceSwallowsBadTables(t *testing.T) {
	// Numa página de preços, mostrar zero é mau; derrubar o servidor é pior.
	bad := Table{Currency: money.AOA} // sem escalões
	if got := bad.MustPrice(10); !got.IsZero() {
		t.Errorf("tabela inválida = %s, queria zero", got)
	}
	if _, err := bad.Price(10); !errors.Is(err, ErrEmptyTable) {
		t.Errorf("Price devia devolver o erro: %v", err)
	}
	good := table(Graduated)
	if got := good.MustPrice(5); got.Minor != aoa(5000).Minor {
		t.Errorf("tabela válida = %s", got)
	}
}

func TestValidateCatchesFlatCurrencyMismatch(t *testing.T) {
	// O valor fixo de um escalão também tem de ser da moeda da tabela.
	bad := Table{Currency: money.AOA, Tiers: []Tier{
		{UpTo: 5, Flat: money.FromMajor(10, money.EUR)},
	}}
	if err := bad.Validate(); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("erro = %v", err)
	}
}

func TestExplainOnInvalidTable(t *testing.T) {
	if _, err := (Table{Currency: money.AOA}).Explain(5); !errors.Is(err, ErrEmptyTable) {
		t.Errorf("erro = %v", err)
	}
}

func TestIncludedAboveQuantity(t *testing.T) {
	// Mais incluídas do que pessoas não pode dar uma quantidade negativa.
	tb := table(Graduated)
	tb.Included = 10
	b, err := tb.Explain(3)
	if err != nil {
		t.Fatal(err)
	}
	if b.Billable != 0 || !b.Total.IsZero() {
		t.Errorf("decomposição = %+v", b)
	}
}

func TestGraduatedSkipsExhaustedTiers(t *testing.T) {
	// Um escalão que acabe antes do que já foi contado é saltado sem gerar uma
	// parcela de zero unidades.
	tb := Table{Mode: Graduated, Currency: money.AOA, Included: 8, Tiers: []Tier{
		{UpTo: 5, UnitPrice: aoa(1000)},
		{UpTo: 15, UnitPrice: aoa(800)},
	}}
	b, err := tb.Explain(11) // três facturáveis, tudo no primeiro escalão
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Lines) != 1 || b.Lines[0].Units != 3 {
		t.Errorf("parcelas = %+v", b.Lines)
	}
}

func TestGraduatedWithFlatPerTier(t *testing.T) {
	tb := Table{Mode: Graduated, Currency: money.AOA, Tiers: []Tier{
		{UpTo: 5, UnitPrice: aoa(100), Flat: aoa(1000)},
		{UnitPrice: aoa(50), Flat: aoa(500)},
	}}
	got, err := tb.Price(8)
	if err != nil {
		t.Fatal(err)
	}
	// 5x100 + 1000 fixo, mais 3x50 + 500 fixo.
	if want := aoa(500 + 1000 + 150 + 500); got.Minor != want.Minor {
		t.Errorf("total = %s, queria %s", got, want)
	}
}

func TestTierForBeyondLastClosedTier(t *testing.T) {
	// Acima do último escalão fechado, cobra-se pelo tecto: perder a cobrança é
	// pior do que cobrar a mais.
	tb := Table{Mode: Volume, Currency: money.AOA, Tiers: []Tier{
		{UpTo: 5, UnitPrice: aoa(100)},
		{UpTo: 10, UnitPrice: aoa(80)},
	}}
	got, _ := tb.Price(50)
	if want := aoa(50 * 80); got.Minor != want.Minor {
		t.Errorf("acima do tecto = %s, queria %s", got, want)
	}
}

func TestCapacityOfEmptyTable(t *testing.T) {
	if got := (Table{Currency: money.AOA}).Capacity(); got != 0 {
		t.Errorf("tabela vazia = %d", got)
	}
}

func TestSchemeRejectsUncoveredSize(t *testing.T) {
	s := Scheme{Table: Table{Currency: money.AOA, Tiers: []Tier{{UpTo: 4, UnitPrice: aoa(100)}}}}
	ok, why := s.Accepts(group(9, true)) // dez pessoas
	if ok {
		t.Error("dez pessoas não cabem numa tabela que cobre quatro")
	}
	if why == "" {
		t.Error("a recusa tem de explicar porquê")
	}
}

func TestSchemePriceOnInvalidTable(t *testing.T) {
	s := Scheme{Table: Table{Currency: money.AOA}, CategoryFactor: map[string]int{"x": 5000}}
	if _, err := s.Price(group(2, true)); !errors.Is(err, ErrEmptyTable) {
		t.Errorf("erro = %v", err)
	}
}

func TestNegativeCategoryFactorIsFree(t *testing.T) {
	// Um factor negativo é um erro de configuração; tratá-lo como zero é
	// preferível a devolver um valor negativo que ninguém sabe explicar.
	s := Scheme{
		Table:          Table{Mode: Graduated, Currency: money.AOA, Tiers: []Tier{{UnitPrice: aoa(1000)}}},
		CategoryFactor: map[string]int{"erro": -500},
	}
	g := Group{HolderCovered: true, People: []Person{
		{ID: "t", Role: RoleHolder},
		{ID: "x", Role: RoleMember, Category: "erro"},
	}}
	b, err := s.Price(g)
	if err != nil {
		t.Fatal(err)
	}
	if want := aoa(1000); b.Total.Minor != want.Minor {
		t.Errorf("total = %s, queria %s (a categoria com factor negativo não paga)", b.Total, want)
	}
}

func TestSchemePriceOnEmptyGroup(t *testing.T) {
	s := Scheme{Table: table(Graduated), CategoryFactor: map[string]int{"x": 5000}}
	b, err := s.Price(Group{})
	if err != nil {
		t.Fatal(err)
	}
	if !b.Total.IsZero() || b.Size != 0 {
		t.Errorf("grupo vazio = %+v", b)
	}
}

func TestMustAddEdges(t *testing.T) {
	// Uma parcela sem moeda não estraga a soma.
	if got := mustAdd(money.Amount{}, aoa(100), money.AOA); got.Minor != 10000 {
		t.Errorf("= %v", got)
	}
	if got := mustAdd(aoa(100), money.Amount{}, money.AOA); got.Minor != 10000 {
		t.Errorf("= %v", got)
	}
	// Moedas incompatíveis devolvem o que já havia, em vez de um erro que
	// ninguém trata a meio de um cálculo de preço.
	if got := mustAdd(aoa(100), money.FromMajor(1, money.EUR), money.AOA); got.Minor != 10000 {
		t.Errorf("= %v", got)
	}
}

func TestItoaNegatives(t *testing.T) {
	if got := itoa(-42); got != "-42" {
		t.Errorf("= %q", got)
	}
	if got := itoa(0); got != "0" {
		t.Errorf("= %q", got)
	}
}
