package examples_test

import (
	"fmt"

	"github.com/spolly-ao/fllex/coupon"
	"github.com/spolly-ao/fllex/money"
)

// Um cupão de percentagem, aplicado ao preço de tabela.
func Example_cupao() {
	c := &coupon.Coupon{
		Code: "BOASVINDAS", Kind: coupon.KindPercent, Percent: 20,
	}

	d := c.Apply(money.FromMajor(5900, money.AOA))
	fmt.Println(d.Label)
	fmt.Println("de", d.Gross, "por", d.Net)

	// Output:
	// 20% de desconto
	// de 5900.00 AOA por 4720.00 AOA
}

// Um desconto de valor fixo nunca deixa o total negativo: um cupão maior do que
// a compra fica pela compra, e não passa a dever dinheiro ao cliente.
func Example_cupaoMaiorDoQueACompra() {
	c := &coupon.Coupon{
		Code: "OFERTA5000", Kind: coupon.KindAmount, Amount: money.FromMajor(5000, money.AOA),
	}

	d := c.Apply(money.FromMajor(3000, money.AOA))
	fmt.Println("desconto:", d.Off, "a pagar:", d.Net)

	// Output:
	// desconto: 3000.00 AOA a pagar: 0.00 AOA
}

// Por omissão o desconto é do primeiro ciclo e não acompanha as renovações.
// Um cupão recorrente diz-se explicitamente, e por quantos ciclos.
func Example_cupaoRecorrente() {
	pontual := &coupon.Coupon{Kind: coupon.KindPercent, Percent: 20}
	recorrente := &coupon.Coupon{Kind: coupon.KindPercent, Percent: 20, Recurring: true, RecurringCycles: 3}

	gross := money.FromMajor(5900, money.AOA)
	for ciclo := 1; ciclo <= 4; ciclo++ {
		fmt.Printf("ciclo %d: pontual %v, recorrente %v\n",
			ciclo, pontual.Apply(gross).Applies(ciclo), recorrente.Apply(gross).Applies(ciclo))
	}

	// Output:
	// ciclo 1: pontual true, recorrente true
	// ciclo 2: pontual false, recorrente true
	// ciclo 3: pontual false, recorrente true
	// ciclo 4: pontual false, recorrente false
}
