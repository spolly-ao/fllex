package proxypaydds

import (
	"context"
	"log/slog"

	"github.com/spolly-ao/fllex/payment"
)

// OffsetStore guarda a posição já lida do fluxo de eventos.
//
// A posição é o que torna o consumo retomável: guardá-la depois de processar
// cada lote faz com que uma paragem do serviço não perca eventos nem os repita
// desde o princípio. Guarde-a na mesma base de dados onde grava os efeitos, e
// não em memória.
type OffsetStore interface {
	// Offset devolve a próxima posição a ler.
	Offset(ctx context.Context) (int, error)
	// SetOffset guarda a próxima posição a ler.
	SetOffset(ctx context.Context, offset int) error
}

// Consumer lê o fluxo de eventos do Proxypay e entrega-os já traduzidos.
//
// É por aqui que se sabe o que aconteceu a um débito directo: se o titular
// activou o mandato, se o banco cobrou, se recusou e porquê. Ao contrário de um
// webhook, o fluxo é um registo ordenado e completo, e nada se perde por uma
// entrega falhada.
type Consumer struct {
	// Provider traduz os eventos.
	Provider *Provider
	// Offsets guarda a posição lida.
	Offsets OffsetStore
	// Handle trata de um evento traduzido. O evento em bruto vai junto para os
	// casos em que os códigos de motivo do banco importam.
	//
	// Devolver erro pára o lote sem avançar a posição, para que o evento seja
	// reentregue na passagem seguinte. É o comportamento certo: avançar por cima
	// de um evento que não foi tratado é perdê-lo para sempre.
	Handle func(ctx context.Context, ev *payment.Event, raw Event) error
	// PageSize é quantos eventos se pedem de cada vez. Zero usa 100.
	BatchSize int
	// Log recebe o que corre mal.
	Log *slog.Logger
}

// Run lê e processa os eventos disponíveis, avançando a posição à medida que
// avança.
func (c *Consumer) Run(ctx context.Context) {
	if c.Provider == nil || c.Offsets == nil || c.Handle == nil {
		c.log().Warn("fllex: consumidor de eventos do Proxypay sem dependências, não corre")
		return
	}

	offset, err := c.Offsets.Offset(ctx)
	if err != nil {
		c.log().Error("fllex: falha a ler a posição do fluxo de eventos", "err", err)
		return
	}

	size := c.BatchSize
	if size <= 0 {
		size = 100
	}

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		events, err := c.Provider.Client().Events(ctx, offset, size)
		if err != nil {
			c.log().Error("fllex: falha a ler o fluxo de eventos", "err", err, "offset", offset)
			return
		}
		if len(events) == 0 {
			return
		}

		for _, raw := range events {
			translated := c.Provider.TranslateEvent(raw)
			if err := c.Handle(ctx, translated, raw); err != nil {
				c.log().Error("fllex: falha a tratar evento do Proxypay, será reentregue",
					"err", err, "event", raw.Type, "offset", raw.Offset)
				// A posição não avança para lá deste evento.
				if err := c.Offsets.SetOffset(ctx, offset); err != nil {
					c.log().Error("fllex: falha a guardar a posição do fluxo", "err", err)
				}
				return
			}
			offset = raw.Offset + 1
		}

		if err := c.Offsets.SetOffset(ctx, offset); err != nil {
			c.log().Error("fllex: falha a guardar a posição do fluxo", "err", err)
			return
		}
		if len(events) < size {
			return // apanhámos o fim do fluxo
		}
	}
}

func (c *Consumer) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}
