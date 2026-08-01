package pricing

import (
	"github.com/spolly-ao/fllex/cycle"
	"github.com/spolly-ao/fllex/money"
)

// Um grupo é um contrato que cobre mais do que uma pessoa: uma família, uma
// equipa, os trabalhadores de uma empresa. Tem um titular, que é quem assina e
// quem paga, e membros, que são as pessoas cobertas.
//
// O nome importa aqui. Chamar-lhes "grupo", "titular" e "membros" é vocabulário
// que qualquer pessoa entende ao balcão, e é o que aparece nos documentos que o
// cliente recebe. Termos técnicos de um domínio específico só servem quem já
// está por dentro.

// Role distingue o papel de uma pessoa no grupo.
type Role string

const (
	// RoleHolder é o titular: quem assina o contrato e quem paga.
	RoleHolder Role = "holder"
	// RoleMember é uma pessoa coberta pelo contrato do titular.
	RoleMember Role = "member"
)

// Person é uma pessoa coberta pelo contrato.
type Person struct {
	// ID é o identificador do lado de quem chama.
	ID string
	// Role distingue o titular dos restantes.
	Role Role
	// Category permite cobrar preços diferentes conforme o tipo de pessoa
	// (por exemplo "adulto" e "criança"). Vazio usa o preço normal.
	Category string
}

// Group é a composição de um contrato.
type Group struct {
	// People são todas as pessoas do contrato, titular incluído.
	People []Person
	// HolderCovered indica se o titular está ele próprio coberto.
	//
	// Nem sempre está: há quem pague a cobertura da família sem a comprar para
	// si. Escrever que está quando não está dá cobertura a quem ninguém
	// comprou, e é um erro que só se descobre quando essa pessoa a tenta usar.
	HolderCovered bool
}

// Holder devolve o titular, se houver.
func (g Group) Holder() (Person, bool) {
	for _, p := range g.People {
		if p.Role == RoleHolder {
			return p, true
		}
	}
	return Person{}, false
}

// Covered devolve as pessoas efectivamente cobertas.
func (g Group) Covered() []Person {
	out := make([]Person, 0, len(g.People))
	for _, p := range g.People {
		if p.Role == RoleHolder && !g.HolderCovered {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Size é o número de pessoas cobertas, que é o que a tabela de preços cobra.
func (g Group) Size() int { return len(g.Covered()) }

// CategoryCount conta as pessoas cobertas de cada categoria.
func (g Group) CategoryCount() map[string]int {
	out := map[string]int{}
	for _, p := range g.Covered() {
		out[p.Category]++
	}
	return out
}

// Scheme é a regra de preço de um plano vendido a grupos.
type Scheme struct {
	// Table é a tabela por número de pessoas cobertas.
	Table Table
	// CategoryFactor ajusta o preço por categoria, em pontos base
	// (10000 = preço inteiro, 6000 = 60% do preço).
	//
	// Uma categoria sem entrada paga o preço inteiro. É o que permite dizer
	// "crianças pagam metade" sem duplicar a tabela.
	CategoryFactor map[string]int
	// MinPeople e MaxPeople delimitam o grupo. Zero em MaxPeople é sem limite.
	MinPeople int
	MaxPeople int
}

// GroupBreakdown é a decomposição do preço de um grupo.
type GroupBreakdown struct {
	// Size é o número de pessoas cobertas.
	Size int
	// Table é a decomposição da tabela para esse número.
	Table Breakdown
	// Adjustments são os acertos por categoria.
	Adjustments []CategoryAdjustment
	// Total é o preço por ciclo do grupo inteiro.
	Total money.Amount
}

// CategoryAdjustment é o desconto (ou agravamento) aplicado a uma categoria.
type CategoryAdjustment struct {
	Category string
	People   int
	// Factor em pontos base.
	Factor int
	// Delta é quanto o acerto mexeu no total (negativo quando desconta).
	Delta money.Amount
}

// Price calcula o preço por ciclo de um grupo.
//
// O cálculo é feito em dois tempos: primeiro a tabela, pelo número de pessoas;
// depois os acertos por categoria, repartindo o total pelas pessoas e aplicando
// o factor de cada uma. Repartir antes de ajustar é o que faz os descontos por
// categoria conviverem com os escalões por volume sem contas impossíveis de
// explicar ao cliente.
func (s Scheme) Price(g Group) (GroupBreakdown, error) {
	size := g.Size()
	cur := money.NormalizeCurrency(string(s.Table.Currency))

	table, err := s.Table.Explain(size)
	if err != nil {
		return GroupBreakdown{}, err
	}

	out := GroupBreakdown{Size: size, Table: table, Total: table.Total}
	if len(s.CategoryFactor) == 0 || size == 0 {
		return out, nil
	}

	// O total reparte-se pelas pessoas sem perder unidades, e cada parcela leva
	// o factor da sua categoria. A soma das parcelas ajustadas é o novo total.
	shares := table.Total.Split(size)
	people := g.Covered()

	adjusted := money.Zero(cur)
	deltas := map[string]money.Amount{}
	counts := map[string]int{}

	for i, p := range people {
		share := shares[i]
		factor, ok := s.CategoryFactor[p.Category]
		if !ok || factor == 10000 {
			adjusted = mustAdd(adjusted, share, cur)
			continue
		}
		if factor < 0 {
			factor = 0
		}
		newShare := share.Ratio(int64(factor), 10000)
		adjusted = mustAdd(adjusted, newShare, cur)

		delta, _ := newShare.Sub(share)
		if prev, seen := deltas[p.Category]; seen {
			deltas[p.Category] = money.New(prev.Minor+delta.Minor, cur)
		} else {
			deltas[p.Category] = delta
		}
		counts[p.Category]++
	}

	for category, delta := range deltas {
		out.Adjustments = append(out.Adjustments, CategoryAdjustment{
			Category: category,
			People:   counts[category],
			Factor:   s.CategoryFactor[category],
			Delta:    delta,
		})
	}
	out.Total = adjusted
	return out, nil
}

// Accepts indica se o grupo cabe no esquema, e explica porque não quando não
// cabe.
func (s Scheme) Accepts(g Group) (bool, string) {
	size := g.Size()
	switch {
	case s.MinPeople > 0 && size < s.MinPeople:
		return false, "o plano exige pelo menos " + itoa(s.MinPeople) + " pessoas cobertas"
	case s.MaxPeople > 0 && size > s.MaxPeople:
		return false, "o plano cobre no máximo " + itoa(s.MaxPeople) + " pessoas"
	case !s.Table.Covers(size):
		return false, "a tabela de preços não cobre " + itoa(size) + " pessoas"
	default:
		return true, ""
	}
}

// Contract é o preço total de um contrato de várias mensalidades, com a
// prestação já calculada.
type Contract struct {
	// PerCycle é o preço de um ciclo.
	PerCycle money.Amount
	// Total é o preço do contrato inteiro.
	Total money.Amount
	// Instalment é quanto se cobra de cada vez.
	Instalment money.Amount
	// Instalments é o número de cobranças.
	Instalments int
}

// Quote calcula o contrato completo a partir do preço por ciclo.
//
// Devolve o total, a prestação e quantas cobranças há. É o que se mostra ao
// cliente antes de assinar, e o que alimenta a subscrição depois: os três
// valores saem daqui juntos, para não haver hipótese de o contrato dizer uma
// coisa e a cobrança outra.
func Quote(perCycle money.Amount, contractMonths, billingPeriodMonths int, commitmentDiscountBps int) Contract {
	months := cycle.NormalizeContractDuration(contractMonths)
	period := cycle.NormalizeBillingPeriod(billingPeriodMonths, months)

	total := perCycle.Mul(int64(months))
	if commitmentDiscountBps > 0 {
		total = total.BasisPointsOff(commitmentDiscountBps)
	}

	return Contract{
		PerCycle:    perCycle,
		Total:       total,
		Instalment:  cycle.InstalmentAmount(total, 0, period, months),
		Instalments: cycle.InstalmentCount(period, months),
	}
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
