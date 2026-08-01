package proxypay

import (
	"context"
	"log/slog"

	"github.com/spolly-ao/fllex/money"
)

// Drainer esvazia a fila de pagamentos confirmados do Proxypay.
//
// O Proxypay guarda cada pagamento confirmado numa fila até nós o retirarmos. É
// isso que torna o sistema à prova de webhooks perdidos: enquanto o pagamento
// não for retirado, continua lá, e a passagem seguinte apanha-o. O webhook é
// apenas a via rápida.
//
// A ordem das operações é o que importa aqui, e é a mesma que evita perder
// dinheiro em qualquer sistema com uma fila: primeiro aplica-se o efeito do
// pagamento e só depois se retira da fila. Ao contrário, um processo que morra
// entre as duas coisas deixa o pagamento fora da fila e sem efeito nenhum do
// nosso lado, e ele nunca mais volta.
type Drainer struct {
	// Client fala com o Proxypay.
	Client *Client

	// ReferenceField é o campo personalizado onde vai a nossa referência.
	// Vazio usa "invoice".
	ReferenceField string

	// Apply aplica o efeito de um pagamento confirmado: dá a cobrança por paga,
	// activa a subscrição, emite a factura.
	//
	// Tem de ser idempotente: o mesmo pagamento pode ser entregue pelo webhook e
	// aparecer nesta fila, e a fila é relida sempre que algo falha a meio.
	//
	// Devolver erro deixa o pagamento na fila para a passagem seguinte.
	Apply func(ctx context.Context, p ConfirmedPayment, reference string, amount money.Amount) error

	// BatchSize limita quantos pagamentos cada passagem trata. Zero trata
	// todos os que a fila devolver.
	BatchSize int

	// Log recebe o que corre mal.
	Log *slog.Logger
}

// Run faz uma passagem: lê a fila, aplica cada pagamento e retira-o.
func (d *Drainer) Run(ctx context.Context) {
	if d.Client == nil || d.Apply == nil {
		d.log().Warn("fllex: escoamento da fila do Proxypay sem dependências, não corre")
		return
	}

	payments, err := d.Client.ListConfirmedPayments(ctx)
	if err != nil {
		d.log().Error("fllex: falha a ler a fila de pagamentos do Proxypay", "err", err)
		return
	}
	if len(payments) == 0 {
		return
	}
	if d.BatchSize > 0 && len(payments) > d.BatchSize {
		payments = payments[:d.BatchSize]
	}

	field := d.ReferenceField
	if field == "" {
		field = "invoice"
	}

	for _, p := range payments {
		if err := ctx.Err(); err != nil {
			return
		}
		amount, err := money.Parse(p.Amount, money.AOA)
		if err != nil {
			d.log().Error("fllex: valor ilegível num pagamento do Proxypay",
				"err", err, "reference", p.ReferenceID, "amount", p.Amount)
			continue
		}

		if err := d.Apply(ctx, p, p.Field(field), amount); err != nil {
			// Fica na fila. É exactamente o que se quer: mais vale voltar a
			// tentar do que retirar um pagamento cujo efeito não aconteceu.
			d.log().Error("fllex: falha a aplicar pagamento do Proxypay, fica na fila",
				"err", err, "reference", p.ReferenceID, "payment", p.ID)
			continue
		}

		if err := d.Client.AcknowledgePayment(ctx, p.ID); err != nil {
			// O efeito já aconteceu e o pagamento continua na fila: a próxima
			// passagem volta a aplicá-lo. Por isso é que Apply tem de ser
			// idempotente.
			d.log().Error("fllex: falha a retirar pagamento da fila do Proxypay",
				"err", err, "payment", p.ID)
		}
	}
}

func (d *Drainer) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}
