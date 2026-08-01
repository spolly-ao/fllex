package examples_test

import (
	"fmt"
	"time"

	"github.com/spolly-ao/fllex/cycle"
	"github.com/spolly-ao/fllex/money"
)

// A janela de renovação abre alguns dias antes do fim do ciclo e fecha alguns
// dias depois. Durante toda ela a cobertura mantém-se: é isso que faz da
// tolerância cobertura a sério, e não um adiamento do corte.
func Example_janelaDeRenovacao() {
	fimDoCiclo := time.Date(2026, time.March, 20, 0, 0, 0, 0, time.UTC)

	janela := cycle.WindowConfig{LeadDays: 10, GraceDays: 5}.For(fimDoCiclo)

	fmt.Println("cobra-se a partir de:", janela.Opens.Format("2006-01-02"))
	fmt.Println("último instante para pagar:", janela.Closes.Format("2006-01-02 15:04:05"))

	// Output:
	// cobra-se a partir de: 2026-03-11
	// último instante para pagar: 2026-03-25 23:59:59
}

// Quem assina a 31 é cobrado a 28 em Fevereiro e volta ao 31 em Março. Sem
// dia-âncora, o mês curto trunca a data e ela fica presa no 28 até ao fim do
// contrato.
func Example_diaAncora() {
	adesao := time.Date(2026, time.January, 31, 0, 0, 0, 0, time.UTC)

	for ciclo := 1; ciclo <= 3; ciclo++ {
		fim := cycle.NthPeriodEnd(adesao, 1, ciclo)
		fmt.Println(fim.Format("2006-01-02"))
	}

	// Output:
	// 2026-02-28
	// 2026-03-31
	// 2026-04-30
}

// Um contrato anual cobrado ao mês cobra um doze avos por mês. Enquanto o total
// do contrato e a prestação forem a mesma coisa, cobra-se o preço do ano doze
// vezes por ano.
func Example_prestacoes() {
	contrato := money.FromMajor(120000, money.AOA) // um ano

	primeira := cycle.InstalmentAmount(contrato, 0, 1, 12)
	fmt.Println("cada mês:", primeira)

	// Output:
	// cada mês: 10000.00 AOA
}

// Quem muda de plano a meio do ciclo paga o que falta, e não o mês inteiro.
func Example_prorratear() {
	inicio := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	fim := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	agora := time.Date(2026, time.March, 16, 0, 0, 0, 0, time.UTC)

	restante := cycle.Prorate(money.FromMajor(6200, money.AOA), agora, inicio, fim)
	fmt.Println("a pagar pelos dias que faltam:", restante)

	// Output:
	// a pagar pelos dias que faltam: 3200.00 AOA
}
