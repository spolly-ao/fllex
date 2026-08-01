package examples_test

import (
	"fmt"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/pricing"
)

// Escalonado: cada patamar cobra as unidades que lhe cabem, como o IRS. Três
// unidades vêm incluídas no plano; das que sobram, as dez primeiras custam 800
// cada e as seguintes 600.
func Example_escaloes() {
	tabela := pricing.Table{
		Mode:     pricing.Graduated,
		Currency: money.AOA,
		Included: 3,
		Base:     money.FromMajor(5000, money.AOA),
		Tiers: []pricing.Tier{
			{UpTo: 10, UnitPrice: money.FromMajor(800, money.AOA)},
			{UnitPrice: money.FromMajor(600, money.AOA)},
		},
	}

	detalhe, err := tabela.Explain(25)
	if err != nil {
		fmt.Println("erro:", err)
		return
	}

	fmt.Println("plano base:", detalhe.Base)
	for _, l := range detalhe.Lines {
		fmt.Printf("unidades %d a %d: %d x %s = %s\n", l.From, l.To, l.Units, l.UnitPrice, l.Amount)
	}
	fmt.Println("total:", detalhe.Total)

	// Output:
	// plano base: 5000.00 AOA
	// unidades 1 a 10: 10 x 800.00 AOA = 8000.00 AOA
	// unidades 11 a 22: 12 x 600.00 AOA = 7200.00 AOA
	// total: 20200.00 AOA
}

// Por volume: tudo é cobrado ao preço do patamar onde a quantidade cai. Cria um
// degrau em que comprar mais fica mais barato, e é preciso saber disso antes de
// escolher este modo.
func Example_escaloesPorVolume() {
	tabela := pricing.Table{
		Mode:     pricing.Volume,
		Currency: money.AOA,
		Tiers: []pricing.Tier{
			{UpTo: 10, UnitPrice: money.FromMajor(800, money.AOA)},
			{UnitPrice: money.FromMajor(600, money.AOA)},
		},
	}

	dez, _ := tabela.Price(10)
	onze, _ := tabela.Price(11)
	fmt.Println("10 unidades:", dez)
	fmt.Println("11 unidades:", onze, "(mais unidades, menos dinheiro)")

	// Output:
	// 10 unidades: 8000.00 AOA
	// 11 unidades: 6600.00 AOA (mais unidades, menos dinheiro)
}
