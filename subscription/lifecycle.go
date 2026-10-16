package subscription

import (
	"context"
	"time"
)

// ReasonOverdue é o motivo com que [Engine.ProcessOverdue] chama
// [Notifier.PaymentFailed]: o ciclo acabou e a cobrança emitida continua por
// pagar. Não houve recusa nenhuma, e o aviso ao cliente tem de o dizer assim.
const ReasonOverdue = "overdue"

// OverdueStore é a capacidade opcional de encontrar as renovações em atraso.
//
// Sem cobrança automática (referência, Multicaixa Express), ninguém recusa
// nada: o ciclo acaba, a cobrança emitida fica por pagar, e a subscrição
// continuaria "activa" até a janela fechar e ela expirar de repente. O produto
// que a vende quer saber antes disso, no momento em que o ciclo pago acaba, e é
// esse o estado que [StatusPastDue] descreve. Um [Store] que implemente esta
// interface faz o [Engine.Run] dar esse passo.
type OverdueStore interface {
	// Overdue devolve as subscrições em vigor (StatusActive) cujo ciclo já
	// acabou à hora dada, com a cobrança do ciclo seguinte emitida e por pagar
	// (RenewalPending ou RenewalFailed), e com a janela ainda aberta.
	//
	// Exclua as de cobrança automática: essas entram em falha pelo evento do
	// gateway e pelo [Engine.ProcessDunning], e passá-las aqui também marcava
	// duas vezes a mesma falha.
	Overdue(ctx context.Context, at time.Time, limit int) ([]*Subscription, error)
}

// PeriodEndStore é a capacidade opcional de encontrar as subscrições que
// chegaram ao fim sem nada a renovar.
//
// São duas: as marcadas para terminar no fim do período pago
// ([Subscription.CancelAtPeriodEnd]) e as que já não renovam
// ([Subscription.AutoRenew] desligado, o contrato com um número fixo de ciclos
// que chegou ao último). Nenhuma recebe aviso nem cobrança, e por isso nenhuma
// passa pela janela que a [Engine.ProcessExpiry] fecha: sem este passo ficavam
// activas para sempre, a dar acesso a quem já tinha pedido para sair.
type PeriodEndStore interface {
	// PeriodEnded devolve as subscrições activas ou em falha cujo
	// CurrentPeriodEnd já passou à hora dada e que não vão renovar:
	// CancelAtPeriodEnd ligado, ou AutoRenew desligado. Exclua as de cobrança
	// automática, que o gateway termina por evento
	// ([payment.EventSubscriptionCancelled]).
	PeriodEnded(ctx context.Context, at time.Time, limit int) ([]*Subscription, error)
}

// MarkOverdue põe em falha de pagamento uma subscrição cuja renovação emitida
// ficou por pagar no fim do ciclo, com o prazo até ao qual ainda pode ser paga.
//
// Ao contrário de [Subscription.MarkPastDue], o estado da renovação mantém-se:
// a cobrança não foi recusada, continua emitida e o cliente continua a poder
// pagá-la até ao prazo.
func (s *Subscription) MarkOverdue(due time.Time, now time.Time) {
	s.Status = StatusPastDue
	s.RenewalDueAt = &due
	s.UpdatedAt = now
}

// ProcessOverdue passa a falha de pagamento as subscrições sem cobrança
// automática cujo ciclo acabou com a renovação por pagar. Só corre quando o
// [Store] implementa [OverdueStore].
//
// O prazo é o que já estava marcado na emissão (o fecho da janela). Uma
// subscrição sem prazo (a emissão falhou antes de o gravar) recebe o da janela
// do ciclo que acabou, que é o mesmo número calculado da mesma forma.
func (e *Engine) ProcessOverdue(ctx context.Context) int {
	store, ok := e.store.(OverdueStore)
	if !ok {
		return 0
	}
	now := e.now()
	subs, err := store.Overdue(ctx, now, e.cfg.BatchSize)
	if err != nil {
		e.log().Error("fllex: falha a obter renovações em atraso", "err", err)
		return 0
	}
	n := 0
	for _, s := range subs {
		due := now
		switch {
		case s.RenewalDueAt != nil:
			due = *s.RenewalDueAt
		case s.CurrentPeriodEnd != nil:
			due = e.cfg.Window.For(*s.CurrentPeriodEnd).Closes
		}
		s.MarkOverdue(due, now)
		if err := e.store.Save(ctx, s); err != nil {
			e.log().Error("fllex: falha a marcar renovação em atraso", "err", err, "subscription", s.ID)
			continue
		}
		e.notify.PaymentFailed(ctx, s, due, ReasonOverdue)
		n++
	}
	return n
}

// ProcessPeriodEnds termina as subscrições que chegaram ao fim do período sem
// renovação a seguir. Só corre quando o [Store] implementa [PeriodEndStore].
//
// A marcada para terminar no fim do período passa a cancelada: alguém decidiu
// terminar, só que a contar do fim do que já estava pago. A que já não renova
// passa a expirada: o contrato chegou ao fim e ninguém decidiu nada. Não há
// aviso ao cliente em nenhuma das duas: a primeira foi pedida por ele, e a
// segunda é o fim que ele contratou. Quem quiser comunicá-lo fá-lo no Save, a
// partir da mudança de estado.
func (e *Engine) ProcessPeriodEnds(ctx context.Context) int {
	store, ok := e.store.(PeriodEndStore)
	if !ok {
		return 0
	}
	now := e.now()
	subs, err := store.PeriodEnded(ctx, now, e.cfg.BatchSize)
	if err != nil {
		e.log().Error("fllex: falha a obter subscrições no fim do período", "err", err)
		return 0
	}
	n := 0
	for _, s := range subs {
		e.cancelPendingCharge(ctx, s)
		if s.CancelAtPeriodEnd {
			s.Cancel(false, now)
		} else {
			s.Expire(now)
		}
		if err := e.store.Save(ctx, s); err != nil {
			e.log().Error("fllex: falha a terminar subscrição no fim do período", "err", err, "subscription", s.ID)
			continue
		}
		n++
	}
	return n
}
