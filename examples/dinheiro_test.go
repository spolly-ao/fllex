package examples_test

import (
	"fmt"

	"github.com/spolly-ao/fllex/money"
)

// O dinheiro é guardado em unidades menores inteiras. Nunca em float64: 0,1 + 0,2
// não dá 0,3 em vírgula flutuante, e ao fim de mil facturas isso é um desvio na
// contabilidade que ninguém consegue explicar.
func Example_dinheiro() {
	preco := money.FromMajor(5900, money.AOA) // 5900,00 Kz
	fmt.Println(preco, "são", preco.Minor, "cêntimos")

	comDesconto := preco.PercentOff(20)
	fmt.Println("com 20% de desconto:", comDesconto)

	// Output:
	// 5900.00 AOA são 590000 cêntimos
	// com 20% de desconto: 4720.00 AOA
}

// Repartir não perde cêntimos: o que sobra da divisão é distribuído pelas
// primeiras parcelas, e a soma das partes é sempre igual ao todo.
func Example_repartir() {
	total := money.FromMajor(100, money.EUR)

	partes := total.Split(3)
	for _, p := range partes {
		fmt.Println(p)
	}

	// Output:
	// 33.34 EUR
	// 33.33 EUR
	// 33.33 EUR
}
