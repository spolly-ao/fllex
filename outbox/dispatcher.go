package outbox

import (
	"context"
	"log/slog"
	"time"
)

// Dispatcher entrega as mensagens pendentes.
//
// A entrega é **pelo menos uma vez**, e não exactamente uma. Uma mensagem
// publicada com sucesso cuja marcação falhe a seguir volta a sair na passagem
// seguinte, e não há forma de evitar isso sem transacções distribuídas.
//
// O que isso obriga do outro lado é simples de dizer e fácil de esquecer: **o
// consumidor tem de ser idempotente**. Guarde o [Message.ID] dos que já tratou
// e ignore repetições. Um consumidor que envie um email por mensagem recebida
// acaba, mais cedo ou mais tarde, a enviar o mesmo email duas vezes.
type Dispatcher struct {
	// Store é o armazenamento das mensagens.
	Store Store
	// Publisher entrega ao destino.
	Publisher Publisher

	// BatchSize é quantas mensagens cada passagem trata. Zero usa 100.
	BatchSize int

	// MaxAttempts é o tecto de tentativas antes de desistir. Zero usa 8.
	//
	// Oito tentativas com a espera por omissão cobrem cerca de duas horas, que
	// chega para uma indisponibilidade normal do destino sem deixar uma
	// mensagem a bater à porta durante dias.
	MaxAttempts int

	// BaseDelay é a espera antes da primeira repetição. Zero usa 5 segundos.
	BaseDelay time.Duration
	// MaxDelay é o tecto da espera. Zero usa 30 minutos.
	MaxDelay time.Duration

	// OnDead é chamado quando uma mensagem esgota as tentativas.
	//
	// Ligue-o ao seu sistema de alarmes. Uma mensagem morta significa que algo
	// aconteceu no sistema e o resto do mundo não soube, e isso não pode ficar
	// só num registo que ninguém lê.
	OnDead func(ctx context.Context, m *Message, reason string)

	// Now devolve a hora. Substituível em testes.
	Now func() time.Time
	// Log recebe o que corre mal.
	Log *slog.Logger
}

// Report conta o que uma passagem fez.
type Report struct {
	Claimed    int
	Dispatched int
	Failed     int
	Dead       int
}

// Run faz uma passagem.
//
// As mensagens são tratadas por ordem e uma de cada vez, de propósito: entregar
// em paralelo é mais rápido e desfaz a ordem por chave, que é a única garantia
// que este mecanismo dá além da entrega. Se precisar de mais débito, corra mais
// instâncias com chaves diferentes, não mais concorrência dentro da mesma.
func (d *Dispatcher) Run(ctx context.Context) Report {
	var rep Report
	if d.Store == nil || d.Publisher == nil {
		d.log().Warn("outbox: sem armazenamento ou publicador, não corre")
		return rep
	}

	now := d.now()
	msgs, err := d.Store.Claim(ctx, d.batchSize(), now)
	if err != nil {
		d.log().Error("outbox: falha a reservar mensagens", "err", err)
		return rep
	}
	rep.Claimed = len(msgs)

	// Uma chave que falhe trava as mensagens seguintes da mesma chave nesta
	// passagem. Sem isto, a segunda alteração de uma subscrição sairia antes da
	// primeira sempre que a primeira falhasse, e o consumidor veria o estado
	// final antes do intermédio.
	blocked := map[string]bool{}

	for _, m := range msgs {
		if err := ctx.Err(); err != nil {
			return rep
		}
		if m.Key != "" && blocked[m.Key] {
			continue
		}
		switch d.deliver(ctx, m, now) {
		case outcomeDispatched:
			rep.Dispatched++
		case outcomeFailed:
			rep.Failed++
			if m.Key != "" {
				blocked[m.Key] = true
			}
		case outcomeDead:
			rep.Dead++
			if m.Key != "" {
				blocked[m.Key] = true
			}
		}
	}
	return rep
}

type outcome int

const (
	outcomeDispatched outcome = iota
	outcomeFailed
	outcomeDead
)

func (d *Dispatcher) deliver(ctx context.Context, m *Message, now time.Time) outcome {
	err := d.Publisher.Publish(ctx, m)
	if err == nil {
		if err := d.Store.MarkDispatched(ctx, m.ID, now); err != nil {
			// A mensagem saiu e a marcação falhou: vai voltar a sair. É
			// exactamente o caso que obriga o consumidor a ser idempotente, e
			// vale a pena aparecer no registo com essas palavras.
			d.log().Error("outbox: entregue mas não marcada, vai repetir",
				"err", err, "message", m.ID, "topic", m.Topic)
		}
		return outcomeDispatched
	}

	attempts := m.Attempts + 1
	if attempts >= d.maxAttempts() {
		reason := err.Error()
		if derr := d.Store.MarkDead(ctx, m.ID, reason); derr != nil {
			d.log().Error("outbox: falha a marcar mensagem como perdida", "err", derr, "message", m.ID)
		}
		d.log().Error("outbox: mensagem perdida depois de esgotar as tentativas",
			"message", m.ID, "topic", m.Topic, "attempts", attempts, "err", err)
		if d.OnDead != nil {
			d.OnDead(ctx, m, reason)
		}
		return outcomeDead
	}

	next := now.Add(Backoff(attempts, d.baseDelay(), d.maxDelay()))
	if ferr := d.Store.MarkFailed(ctx, m.ID, attempts, next, err.Error()); ferr != nil {
		d.log().Error("outbox: falha a marcar a repetição", "err", ferr, "message", m.ID)
	}
	d.log().Warn("outbox: entrega falhada, marcada para repetir",
		"message", m.ID, "topic", m.Topic, "attempt", attempts, "next", next, "err", err)
	return outcomeFailed
}

// Purge apaga as mensagens entregues há mais do que o período de retenção.
func (d *Dispatcher) Purge(ctx context.Context, retention time.Duration) (int, error) {
	return d.Store.Purge(ctx, d.now().Add(-retention))
}

// Backoff calcula a espera antes da tentativa n (a primeira repetição é 1),
// duplicando de cada vez até ao tecto.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	wait := base
	for i := 1; i < attempt; i++ {
		if wait >= max {
			return max
		}
		wait *= 2
	}
	if wait > max {
		return max
	}
	return wait
}

func (d *Dispatcher) batchSize() int {
	if d.BatchSize > 0 {
		return d.BatchSize
	}
	return 100
}

func (d *Dispatcher) maxAttempts() int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return 8
}

func (d *Dispatcher) baseDelay() time.Duration {
	if d.BaseDelay > 0 {
		return d.BaseDelay
	}
	return 5 * time.Second
}

func (d *Dispatcher) maxDelay() time.Duration {
	if d.MaxDelay > 0 {
		return d.MaxDelay
	}
	return 30 * time.Minute
}

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

func (d *Dispatcher) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}
