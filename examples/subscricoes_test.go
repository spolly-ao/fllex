package examples_test

import (
	"fmt"
	"time"

	"github.com/spolly-ao/fllex/cycle"
	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/subscription"
)

// Um pagamento confirmado põe a subscrição em vigor e avança o ciclo. A data do
// ciclo seguinte é calculada a partir do fim previsto, e não do dia do
// pagamento: pagar dois dias antes ou três depois não muda a data de renovação
// nem faz o cliente ganhar ou perder dias.
func Example_renovar() {
	fim := time.Date(2026, time.March, 20, 0, 0, 0, 0, time.UTC)
	sub := &subscription.Subscription{
		PlanName: "Plano Essencial", Status: subscription.StatusActive,
		Interval: cycle.Monthly, CurrentPeriodEnd: &fim, CycleNumber: 3,
		StartDate: time.Date(2025, time.December, 20, 0, 0, 0, 0, time.UTC),
		Amount:    money.FromMajor(5900, money.AOA),
	}

	sub.Activate(time.Date(2026, time.March, 18, 9, 30, 0, 0, time.UTC)) // pagou dois dias antes

	fmt.Println("ciclo:", sub.CycleNumber, "até", sub.CurrentPeriodEnd.Format("2006-01-02"))

	// Output:
	// ciclo: 4 até 2026-04-20
}

// A renovação cobra o preço de tabela, e não o que foi pago. Um cupão vale para
// o primeiro ciclo, e só acompanha as renovações quando alguém decidiu isso
// explicitamente.
func Example_renovarComDesconto() {
	sub := &subscription.Subscription{
		Amount:          money.FromMajor(4720, money.AOA), // pagou com 20% de desconto
		GrossAmount:     money.FromMajor(5900, money.AOA), // preço de tabela
		DiscountPercent: 20, CouponID: "BOASVINDAS", CycleNumber: 1,
	}

	fmt.Println("pagou:", sub.Amount)
	fmt.Println("renova por:", sub.RenewalAmount())

	sub.ApplyRenewalPricing() // o motor faz isto ao abrir a janela
	fmt.Println("depois de renovar:", sub.Amount, "cupão:", sub.CouponID == "")

	// Output:
	// pagou: 4720.00 AOA
	// renova por: 5900.00 AOA
	// depois de renovar: 5900.00 AOA cupão: true
}

// A tolerância é cobertura a sério: o ciclo acabou, o pagamento ainda não
// chegou, e o cliente continua a ter serviço até ao fecho da janela.
func Example_tolerancia() {
	fim := time.Date(2026, time.March, 20, 0, 0, 0, 0, time.UTC)
	prazo := cycle.WindowConfig{LeadDays: 10, GraceDays: 5}.For(fim).Closes
	sub := &subscription.Subscription{CurrentPeriodEnd: &fim, RenewalDueAt: &prazo}

	dias := []time.Time{
		time.Date(2026, time.March, 19, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 23, 0, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 26, 0, 0, 0, 0, time.UTC),
	}
	for _, d := range dias {
		fmt.Printf("%s: expirado %-5v em tolerância %-5v fora de prazo %v\n",
			d.Format("02/01"), sub.Expired(d), sub.InGrace(d), sub.PastDue(d))
	}

	// Output:
	// 19/03: expirado false em tolerância false fora de prazo false
	// 23/03: expirado true  em tolerância true  fora de prazo false
	// 26/03: expirado true  em tolerância false fora de prazo true
}
