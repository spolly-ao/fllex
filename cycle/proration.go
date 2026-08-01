package cycle

import (
	"time"

	"github.com/spolly-ao/fllex/money"
)

// Prorate reparte um valor pelos dias que ainda faltam de um ciclo.
//
// Por dias, e não por meses: quem entra a 3 de Agosto num contrato que acaba a
// 14 de Julho paga 345 dias de 365, não "11 meses". É a conta que o cliente
// consegue conferir ao balcão, e é a que gera menos discussões.
//
// Nunca devolve mais do que o valor inteiro nem menos do que zero: um ciclo já
// terminado (ou com datas trocadas) cobra o total, e um ciclo que já acabou não
// gera cobrança negativa.
func Prorate(amount money.Amount, now, cycleStart, cycleEnd time.Time) money.Amount {
	if !amount.IsPositive() {
		return money.Zero(amount.Currency)
	}
	total := cycleEnd.Sub(cycleStart)
	remaining := cycleEnd.Sub(now)
	if total <= 0 || remaining >= total {
		return amount
	}
	if remaining <= 0 {
		return money.Zero(amount.Currency)
	}
	// Em horas, não em dias inteiros: uma alteração ao meio-dia não deve valer o
	// mesmo que uma à meia-noite quando o ciclo é curto.
	return amount.Ratio(int64(remaining.Hours()), int64(total.Hours()))
}

// ProrateDays é a variante que conta dias de calendário inteiros, para quando
// o acordo com o cliente é "paga-se por dia" e a hora não conta.
func ProrateDays(amount money.Amount, now, cycleStart, cycleEnd time.Time) money.Amount {
	if !amount.IsPositive() {
		return money.Zero(amount.Currency)
	}
	total := daysBetween(cycleStart, cycleEnd)
	remaining := daysBetween(now, cycleEnd)
	if total <= 0 || remaining >= total {
		return amount
	}
	if remaining <= 0 {
		return money.Zero(amount.Currency)
	}
	return amount.Ratio(int64(remaining), int64(total))
}

// daysBetween conta dias de calendário entre dois instantes, ignorando a hora.
func daysBetween(from, to time.Time) int {
	a := StartOfDay(from)
	b := StartOfDay(to)
	return int(b.Sub(a).Hours() / 24)
}

// UpgradeCredit calcula o acerto a cobrar numa mudança de plano a meio do
// ciclo: a diferença de preço, proporcional aos dias que faltam.
//
// Uma descida de preço devolve zero, não um crédito: quando não há via de
// reembolso (o caso das referências Multicaixa e do dinheiro já liquidado), o
// que se ajusta é o futuro, e o valor mais baixo entra na renovação seguinte.
// Passe allowCredit a true se o seu produto tiver saldo ou nota de crédito para
// onde mandar a diferença.
func UpgradeCredit(oldAmount, newAmount money.Amount, now, cycleStart, cycleEnd time.Time, allowCredit bool) money.Amount {
	diff, err := newAmount.Sub(oldAmount)
	if err != nil {
		return money.Zero(newAmount.Currency)
	}
	if diff.IsNegative() && !allowCredit {
		return money.Zero(newAmount.Currency)
	}
	if diff.IsNegative() {
		return Prorate(diff.Abs(), now, cycleStart, cycleEnd).Neg()
	}
	return Prorate(diff, now, cycleStart, cycleEnd)
}
