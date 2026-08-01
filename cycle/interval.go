// Package cycle trata das datas de um ciclo de cobrança: quanto dura, quando
// acaba, quantas prestações tem e quanto se cobra em cada uma.
//
// Toda a aritmética é de calendário, não de duração fixa. Somar 30 dias a uma
// subscrição mensal parece equivalente e não é: ao fim de um ano a data de
// cobrança já anda cinco dias fora do sítio, e o cliente que assinou a 31 passa
// a ser cobrado a 26.
package cycle

import (
	"strings"
	"time"
)

// Interval é a periodicidade de uma subscrição.
type Interval string

const (
	// Monthly cobra todos os meses.
	Monthly Interval = "monthly"
	// Yearly cobra uma vez por ano.
	Yearly Interval = "yearly"
	// Weekly cobra todas as semanas.
	Weekly Interval = "weekly"
	// Quarterly cobra de três em três meses.
	Quarterly Interval = "quarterly"
	// Semiannual cobra de seis em seis meses.
	Semiannual Interval = "semiannual"
)

// ParseInterval aceita as variantes que aparecem em esquemas e APIs diferentes
// ("monthly", "month", "mensal", "yearly", "year", "anual"...) e devolve o
// intervalo canónico. O que não reconhece cai em [Monthly], que é o mais comum.
func ParseInterval(s string) Interval {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "year", "yearly", "annual", "anual", "annually", "ano":
		return Yearly
	case "week", "weekly", "semanal", "semana":
		return Weekly
	case "quarter", "quarterly", "trimestral", "trimestre":
		return Quarterly
	case "semiannual", "semestral", "semestre", "biannual":
		return Semiannual
	default:
		return Monthly
	}
}

// Months devolve quantos meses o intervalo cobre. O semanal devolve 0, porque
// não se mede em meses: use [Interval.Add], que trata dos dois casos.
func (i Interval) Months() int {
	switch i {
	case Yearly:
		return 12
	case Quarterly:
		return 3
	case Semiannual:
		return 6
	case Weekly:
		return 0
	default:
		return 1
	}
}

// String devolve o valor canónico do intervalo.
func (i Interval) String() string { return string(i) }

// StripeInterval traduz o intervalo para o vocabulário do Stripe
// (recurring[interval] + recurring[interval_count]).
func (i Interval) StripeInterval() (unit string, count int) {
	switch i {
	case Yearly:
		return "year", 1
	case Weekly:
		return "week", 1
	case Quarterly:
		return "month", 3
	case Semiannual:
		return "month", 6
	default:
		return "month", 1
	}
}

// Add avança um instante por um intervalo de cobrança.
func (i Interval) Add(t time.Time) time.Time { return i.AddAnchored(t, t.Day()) }

// AddAnchored avança um instante por um intervalo, repondo o dia de origem.
// Ver [AddMonthsAnchored] para a razão de existir.
func (i Interval) AddAnchored(t time.Time, anchorDay int) time.Time {
	if i == Weekly {
		return t.AddDate(0, 0, 7)
	}
	return AddMonthsAnchored(t, i.Months(), anchorDay)
}

// AddMonths soma meses truncando ao último dia do mês de destino.
//
// O AddDate do Go normaliza o excesso para o mês seguinte: 31 de Março mais um
// mês dá 1 de Maio, e 31 de Janeiro mais um mês dá 3 de Março. Numa subscrição
// mensal isso é um dia a mais por ciclo, e ao fim de um ano a data de cobrança
// anda dias fora do sítio. Aqui, 31 de Março mais um mês é 30 de Abril, que é o
// que qualquer pessoa entende por "daqui a um mês".
//
// Atenção ao usá-la ciclo após ciclo, sem âncora: quem assina a 31 de Janeiro
// é truncado para 28 de Fevereiro e fica preso no dia 28 para sempre, porque a
// data seguinte já não sabe que o dia certo era o 31. Para uma subscrição, use
// [AddMonthsAnchored] ou [NthPeriodEnd], que repõem o dia de origem sempre que
// o mês de destino o tiver.
//
// Aceita meses negativos (recuar no calendário) com a mesma regra.
func AddMonths(t time.Time, months int) time.Time {
	return AddMonthsAnchored(t, months, t.Day())
}

// AddMonthsAnchored soma meses repondo o dia de origem sempre que o mês de
// destino o tenha.
//
// O dia-âncora é o dia em que o cliente assinou. Guardá-lo é o que impede a
// deriva: quem assinou a 31 é cobrado a 28 em Fevereiro e volta ao 31 em Março,
// em vez de ficar preso no 28 até ao fim do contrato.
//
// Um dia-âncora a zero ou negativo usa o dia de t, o que faz esta função
// comportar-se como [AddMonths].
func AddMonthsAnchored(t time.Time, months, anchorDay int) time.Time {
	if anchorDay <= 0 {
		anchorDay = t.Day()
	}
	y, m, _ := t.Date()
	target := time.Date(y, m+time.Month(months), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	d := anchorDay
	if last := lastDayOfMonth(target); d > last {
		d = last
	}
	return time.Date(target.Year(), target.Month(), d, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

// AnchorDay devolve o dia do mês a preservar nos ciclos seguintes. Guarde-o na
// subscrição a partir da data de adesão.
func AnchorDay(t time.Time) int { return t.Day() }

// NthPeriodEnd é o fim do n-ésimo ciclo contado a partir da adesão, sem deriva
// nenhuma: cada data é calculada da âncora original e não da anterior.
//
// É a forma mais segura de datar ciclos, e a que se deve preferir quando a data
// de adesão é conhecida. Somar mês a mês acumula os erros de todos os meses
// curtos pelo caminho.
func NthPeriodEnd(anchor time.Time, monthsPerCycle, n int) time.Time {
	if monthsPerCycle <= 0 {
		monthsPerCycle = 1
	}
	return AddMonthsAnchored(anchor, monthsPerCycle*n, anchor.Day())
}

// lastDayOfMonth devolve o número do último dia do mês de t. O dia 0 do mês
// seguinte é o último deste.
func lastDayOfMonth(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
}

// EndOfDay devolve o mesmo dia às 23:59:59, o instante até ao qual uma
// referência de pagamento ainda é aceite. Um prazo que acaba à meia-noite do
// dia indicado tira ao cliente o dia inteiro que lhe foi prometido.
func EndOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 23, 59, 59, 0, t.Location())
}

// StartOfDay devolve o mesmo dia às 00:00:00.
func StartOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// PeriodEnd calcula o fim do ciclo seguinte.
//
// Numa renovação normal o ciclo ancora no fim do anterior, mesmo que o
// pagamento chegue uns dias antes ou depois: o cliente não perde nem ganha dias
// por causa da data em que pagou. Numa reactivação (a subscrição já tinha sido
// cancelada e o acesso esteve suspenso) ancora no pagamento, porque não se
// cobra tempo que o cliente não teve.
//
// A salvaguarda final impede que um ciclo nasça já expirado, o que acontecia
// quando uma subscrição ficava meses parada e a renovação partia de um fim de
// período muito antigo.
func PeriodEnd(now time.Time, currentEnd *time.Time, interval Interval, reactivating bool) time.Time {
	return PeriodEndAnchored(now, currentEnd, interval, 0, reactivating)
}

// PeriodEndAnchored é [PeriodEnd] com dia-âncora, para que a data de cobrança
// não derive nos meses curtos. Um dia-âncora a zero usa o dia da data base.
func PeriodEndAnchored(now time.Time, currentEnd *time.Time, interval Interval, anchorDay int, reactivating bool) time.Time {
	base := now
	if !reactivating && currentEnd != nil {
		base = *currentEnd
	}
	if anchorDay <= 0 {
		anchorDay = base.Day()
	}
	end := interval.AddAnchored(base, anchorDay)
	if end.Before(now) {
		end = interval.AddAnchored(now, anchorDay)
	}
	return end
}

// NextCycleEnd é o fim do ciclo seguinte contado a partir do fim do ciclo
// anterior, para um contrato descrito em meses.
//
// Um ciclo é uma prestação, não o contrato: num contrato anual cobrado ao mês o
// ciclo é de um mês; num contrato anual pago de uma vez o ciclo é o ano inteiro,
// porque aí a prestação é o contrato todo.
func NextCycleEnd(previousEnd time.Time, billingPeriodMonths, contractMonths int) time.Time {
	return NextCycleEndAnchored(previousEnd, billingPeriodMonths, contractMonths, 0)
}

// NextCycleEndAnchored é [NextCycleEnd] com dia-âncora.
func NextCycleEndAnchored(previousEnd time.Time, billingPeriodMonths, contractMonths, anchorDay int) time.Time {
	return AddMonthsAnchored(previousEnd, NormalizeBillingPeriod(billingPeriodMonths, contractMonths), anchorDay)
}
