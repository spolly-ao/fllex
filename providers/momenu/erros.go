package momenu

import "errors"

// Os erros que este pacote recusa antes de chegar à rede.
//
// São os dois que o MoMenu passou a impor e que se conseguem verificar aqui:
// descobri-los no gateway custa uma ida e volta e, no Multicaixa Express, uma
// ida e volta pode ser de três minutos.
var (
	// ErrBelowMinimum: o valor está abaixo do mínimo que o MoMenu aceita.
	// Corresponde ao BELOW_MINIMUM deles.
	ErrBelowMinimum = errors.New("momenu: valor abaixo do mínimo")

	// ErrAmountMismatch: o valor pedido e a soma dos produtos não batem.
	// Corresponde ao AMOUNT_MISMATCH deles, que antes não era imposto.
	ErrAmountMismatch = errors.New("momenu: o valor não bate com a soma dos produtos")
)
