// Package wallet é o saldo pré-pago do cliente.
//
// Existe por uma razão de mercado, não de arquitectura: onde a cobrança
// recorrente por cartão não é fiável, a renovação automática precisa de outra
// fonte de dinheiro. O cliente carrega a carteira quando lhe dá jeito e o saldo
// é descontado à medida que as renovações caem, sem depender de ele estar
// disponível no dia certo.
//
// O livro de movimentos é imutável: uma correcção é um movimento novo em
// sentido contrário, nunca uma alteração ao anterior. É o que permite explicar
// um saldo somando o que aconteceu, em vez de acreditar num número.
package wallet

import (
	"context"
	"time"

	"github.com/spolly-ao/fllex/money"
)

// Wallet é o saldo de um cliente numa moeda.
type Wallet struct {
	// ID é o identificador da carteira.
	ID string
	// CustomerID é o dono.
	CustomerID string
	// Balance é o saldo actual. Deve ser sempre igual à soma dos movimentos;
	// guardá-lo evita somar o livro inteiro a cada leitura.
	Balance   money.Amount
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Kind classifica um movimento.
type Kind string

const (
	// KindTopup: carregamento pelo cliente.
	KindTopup Kind = "topup"
	// KindCharge: desconto por uma cobrança (renovação, compra).
	KindCharge Kind = "charge"
	// KindRefund: devolução de uma cobrança anterior.
	KindRefund Kind = "refund"
	// KindAdjustment: acerto manual feito por um operador. Exige sempre um
	// motivo, porque é o único movimento que não decorre de um facto do sistema.
	KindAdjustment Kind = "adjustment"
	// KindReversal: anulação de um movimento anterior.
	KindReversal Kind = "reversal"
)

// Entry é um movimento do livro. Uma vez escrito, não se altera.
type Entry struct {
	// ID é o identificador do movimento.
	ID string
	// WalletID é a carteira a que pertence.
	WalletID string
	// Amount é o valor com sinal: positivo credita, negativo debita. Guardar o
	// sinal em vez de um tipo separado é o que faz com que a soma do livro seja,
	// literalmente, o saldo.
	Amount money.Amount
	// BalanceAfter é o saldo depois deste movimento, para reconstruir o extracto
	// sem somar tudo de novo.
	BalanceAfter money.Amount
	// Kind classifica o movimento.
	Kind Kind
	// Reference liga o movimento ao que lhe deu origem (a cobrança, a
	// encomenda, a subscrição).
	Reference string
	// OperatorID é quem o fez, nos acertos manuais.
	OperatorID string
	// Description explica o movimento ao cliente.
	Description string
	CreatedAt   time.Time
}

// Store é o armazenamento das carteiras e do seu livro.
//
// As implementações têm de garantir uma coisa que a biblioteca não pode
// garantir sozinha: [Store.Apply] tem de correr numa transacção que trave o
// registo da carteira. Sem esse travão, dois débitos concorrentes lêem o mesmo
// saldo, aprovam ambos, e a carteira fica negativa.
type Store interface {
	// ByCustomer devolve a carteira de um cliente, ou (nil, nil).
	ByCustomer(ctx context.Context, customerID string, currency money.Currency) (*Wallet, error)
	// Ensure devolve a carteira do cliente, criando-a se ainda não existir.
	Ensure(ctx context.Context, customerID string, currency money.Currency) (*Wallet, error)
	// Apply grava o movimento e actualiza o saldo, atomicamente e com o registo
	// da carteira travado.
	Apply(ctx context.Context, w *Wallet, e *Entry) error
	// Entries devolve o extracto, do mais recente para o mais antigo.
	Entries(ctx context.Context, walletID string, offset, limit int) ([]*Entry, int64, error)
	// EntryByReference devolve o movimento de uma referência, para não creditar
	// duas vezes o mesmo carregamento.
	EntryByReference(ctx context.Context, walletID, reference string) (*Entry, error)
}
