package subscription

import (
	"context"
	"time"
)

// Store é o armazenamento das subscrições.
//
// As consultas do motor de renovação são deliberadamente explícitas em vez de
// um filtro genérico: cada uma corresponde a um passo do ciclo e traz no nome e
// na documentação o critério exacto, que é onde é mais fácil errar. Uma
// cláusula em falta aqui não dá erro nenhum: dá subscrições que nunca são
// avisadas, ou que são canceladas sem nunca terem sido cobradas.
type Store interface {
	// ByID devolve uma subscrição, ou (nil, nil).
	ByID(ctx context.Context, id string) (*Subscription, error)

	// Save persiste as alterações.
	Save(ctx context.Context, s *Subscription) error

	// DueForWarning devolve as subscrições a avisar: activas, com renovação
	// ligada, cujo ciclo acaba dentro da antecedência dada e que ainda não
	// foram avisadas neste ciclo.
	//
	// Filtre por RenewalWarnedAt nulo, senão o aviso repete-se a cada passagem
	// do processo. E exclua as marcadas para terminar no fim do período: avisar
	// da renovação quem já pediu para cancelar é ruído e gera reclamações.
	DueForWarning(ctx context.Context, before time.Time, limit int) ([]*Subscription, error)

	// DueForCharge devolve as subscrições cuja janela de renovação abriu e que
	// ainda não têm cobrança emitida (RenewalState vazio ou apenas avisado).
	//
	// Inclua as que já estão dentro da tolerância, e não só as que estão a
	// chegar ao fim: uma subscrição que passou o fim sem cobrança emitida (por
	// o processo ter estado parado) tem de a receber, não de ser saltada.
	//
	// Exclua as de valor zero: um plano genuinamente gratuito não tem nada para
	// cobrar nem para expirar.
	DueForCharge(ctx context.Context, at time.Time, limit int) ([]*Subscription, error)

	// AwaitingPayment devolve as subscrições com uma renovação emitida à espera
	// de confirmação, para o verificador de estado.
	//
	// Inclua as canceladas: uma cobrança reemitida para reactivar uma
	// subscrição cancelada também se confirma por aqui, e deixá-las de fora faz
	// com que o cliente pague e nada aconteça.
	AwaitingPayment(ctx context.Context, limit int) ([]*Subscription, error)

	// RetryDue devolve as subscrições com cobrança automática recusada, dentro
	// da janela ainda aberta e por baixo do tecto de tentativas.
	RetryDue(ctx context.Context, at time.Time, maxAttempts, limit int) ([]*Subscription, error)

	// PastDueWithoutDeadline devolve as subscrições cuja cobrança automática
	// falhou e que ainda não têm prazo de cancelamento marcado.
	//
	// É este passo que impede uma subscrição de ficar presa em falha de
	// pagamento indefinidamente, a dar acesso a quem não paga desde que o
	// gateway desistiu de cobrar.
	PastDueWithoutDeadline(ctx context.Context, limit int) ([]*Subscription, error)

	// ExpiredWindows devolve as subscrições cuja janela fechou sem pagamento.
	ExpiredWindows(ctx context.Context, at time.Time, limit int) ([]*Subscription, error)

	// StaleCharges devolve as cobranças pendentes de subscrições já canceladas
	// cujo prazo passou. Limpam-se em silêncio: a subscrição já estava
	// cancelada, não há novo cancelamento a comunicar a ninguém.
	StaleCharges(ctx context.Context, at time.Time, limit int) ([]*Subscription, error)
}

// Notifier são os avisos que o motor manda enviar. Implemente-o com o seu
// sistema de email, SMS ou fila de mensagens.
//
// Nenhum destes métodos devolve erro, de propósito: um aviso que não sai não
// pode travar uma renovação nem reverter uma cobrança já feita. Registe a falha
// dentro da implementação e siga.
type Notifier interface {
	// RenewalWarning avisa que a renovação se aproxima e que é preciso pagar.
	RenewalWarning(ctx context.Context, s *Subscription, dueAt time.Time)

	// RenewalReminder avisa que a renovação se aproxima e vai ser cobrada
	// automaticamente. É informativo: não há nada a fazer.
	RenewalReminder(ctx context.Context, s *Subscription, renewsAt time.Time)

	// ChargeIssued entrega a cobrança do ciclo: os dados de pagamento e o link
	// onde pagar.
	//
	// O link segue mesmo nos métodos que não precisam dele, e é de propósito: é
	// ele que deixa o cliente trocar de método sem depender do que estava
	// registado. Quem tinha débito directo e mudou de banco paga por referência
	// sem falar com ninguém.
	ChargeIssued(ctx context.Context, s *Subscription, charge Charge)

	// PaymentFailed avisa que a cobrança foi recusada e até quando há para
	// resolver.
	PaymentFailed(ctx context.Context, s *Subscription, dueAt time.Time, reason string)

	// Renewed confirma a renovação.
	Renewed(ctx context.Context, s *Subscription)

	// Expired comunica que a subscrição terminou por falta de pagamento.
	Expired(ctx context.Context, s *Subscription)
}

// NopNotifier é um [Notifier] que não faz nada, para arrancar sem avisos.
type NopNotifier struct{}

func (NopNotifier) RenewalWarning(context.Context, *Subscription, time.Time)        {}
func (NopNotifier) RenewalReminder(context.Context, *Subscription, time.Time)       {}
func (NopNotifier) ChargeIssued(context.Context, *Subscription, Charge)             {}
func (NopNotifier) PaymentFailed(context.Context, *Subscription, time.Time, string) {}
func (NopNotifier) Renewed(context.Context, *Subscription)                          {}
func (NopNotifier) Expired(context.Context, *Subscription)                          {}
