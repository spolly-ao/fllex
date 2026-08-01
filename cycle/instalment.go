package cycle

import "github.com/spolly-ao/fllex/money"

// Duração do contrato e período de cobrança são coisas diferentes, ambas em
// meses:
//
//	contractMonths        quanto tempo o cliente está coberto e comprometido
//	billingPeriodMonths   de quanto em quanto tempo se cobra
//
// O preço do contrato é sempre o total. O que se cobra de cada vez é a
// prestação, que é [InstalmentAmount].
//
// Enquanto as duas coisas foram uma só, um contrato de 12 meses marcado como
// mensal tinha o preço anual a ser cobrado todos os meses, doze vezes, e nada a
// jusante o corrigia. É o erro que esta distinção existe para tornar
// impossível.

// NormalizeContractDuration devolve os meses de contrato, 12 quando não vem
// nada de jeito.
func NormalizeContractDuration(months int) int {
	if months <= 0 {
		return 12
	}
	return months
}

// NormalizeBillingPeriod devolve de quantos em quantos meses se cobra. Zero (o
// valor por omissão das colunas antigas) lê-se como "paga-se de uma vez", ou
// seja o contrato inteiro; e não se cobra em períodos maiores do que o próprio
// contrato.
func NormalizeBillingPeriod(period, contractMonths int) int {
	d := NormalizeContractDuration(contractMonths)
	if period <= 0 || period > d {
		return d
	}
	return period
}

// InstalmentCount é o número de cobranças de um ciclo de contrato. Arredonda
// para cima: 12 meses cobrados de 5 em 5 são 3 cobranças (5+5+2), não 2.
func InstalmentCount(billingPeriodMonths, contractMonths int) int {
	d := NormalizeContractDuration(contractMonths)
	p := NormalizeBillingPeriod(billingPeriodMonths, d)
	return (d + p - 1) / p
}

// InstalmentMonths diz quantos meses cobre a n-ésima cobrança (n começa em 0).
// Só difere do período na última, quando ele não divide o contrato.
func InstalmentMonths(n, billingPeriodMonths, contractMonths int) int {
	d := NormalizeContractDuration(contractMonths)
	p := NormalizeBillingPeriod(billingPeriodMonths, d)
	if remaining := d - n*p; remaining < p {
		if remaining <= 0 {
			return 0
		}
		return remaining
	}
	return p
}

// InstalmentAmount é quanto se cobra de cada vez: o total do contrato
// repartido pelos meses, vezes os meses que esta prestação cobre.
//
// Um pagamento único (período igual à duração) devolve o total tal e qual, sem
// passar pela divisão, para não perder unidades ao arredondar.
func InstalmentAmount(total money.Amount, n, billingPeriodMonths, contractMonths int) money.Amount {
	d := NormalizeContractDuration(contractMonths)
	p := NormalizeBillingPeriod(billingPeriodMonths, d)
	if p >= d {
		return total
	}
	months := InstalmentMonths(n, p, d)
	if months <= 0 {
		return money.Zero(total.Currency)
	}
	return total.Ratio(int64(months), int64(d))
}

// InstalmentPlan devolve todas as prestações de um contrato, na ordem em que
// são cobradas. A soma é exactamente o total: o resto da divisão é distribuído
// pelas primeiras prestações, em vez de se perder no arredondamento.
//
// É o que permite mostrar ao cliente um plano de pagamentos que fecha ao
// cêntimo, mesmo quando o contrato não divide pelo período.
func InstalmentPlan(total money.Amount, billingPeriodMonths, contractMonths int) []money.Amount {
	d := NormalizeContractDuration(contractMonths)
	p := NormalizeBillingPeriod(billingPeriodMonths, d)
	if p >= d {
		return []money.Amount{total}
	}
	n := InstalmentCount(p, d)
	out := make([]money.Amount, n)
	assigned := int64(0)
	for i := 0; i < n; i++ {
		if i == n-1 {
			// A última prestação leva o que sobrar, para a soma fechar no total.
			out[i] = money.New(total.Minor-assigned, total.Currency)
			break
		}
		out[i] = InstalmentAmount(total, i, p, d)
		assigned += out[i].Minor
	}
	return out
}

// BillingFrequency traduz o par (período, duração) para o vocabulário legado
// FIXED/MONTHLY, que continua a ser lido por painéis e integrações antigas.
// Não decida nada por ele: é derivado, não é a fonte de verdade.
const (
	// FrequencyFixed: paga-se uma vez, pelo contrato inteiro.
	FrequencyFixed = "FIXED"
	// FrequencyMonthly: paga-se todos os meses.
	FrequencyMonthly = "MONTHLY"
)

// BillingFrequency deriva a frequência legada do par (período, duração).
func BillingFrequency(billingPeriodMonths, contractMonths int) string {
	if NormalizeBillingPeriod(billingPeriodMonths, contractMonths) == 1 {
		return FrequencyMonthly
	}
	return FrequencyFixed
}
