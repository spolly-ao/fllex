// Package payment é o núcleo da biblioteca: o modelo canónico de um pagamento,
// o contrato que todos os gateways cumprem e o registo que decide qual usar.
//
// O modelo é deliberadamente independente de base de dados. A biblioteca não
// impõe tabelas nem migrações: quem a usa implementa [Store] sobre o seu
// Postgres, MySQL ou o que tiver, e mantém o esquema que já tem. O que se
// ganha é o comportamento, não o armazenamento.
package payment

import (
	"context"
	"time"

	"github.com/spolly-ao/fllex/money"
)

// Payment é uma cobrança: uma intenção de receber um valor, com um método, um
// estado e as referências que a ligam ao gateway.
//
// Os identificadores são strings e não um tipo próprio de UUID, de propósito:
// há sistemas a usar uuid.UUID, string e inteiro autoincremental, e impor um
// tipo era impedir metade deles de adoptar a biblioteca.
type Payment struct {
	// ID é o identificador da cobrança do lado de quem chama.
	ID string

	// SubjectID é o que está a ser pago: a subscrição, a encomenda, o destaque.
	// A biblioteca não lhe toca; existe para quem chama se reencontrar.
	SubjectID string

	// CustomerID é quem paga.
	CustomerID string

	// Amount é o valor a cobrar.
	Amount money.Amount

	// Method é como se cobra.
	Method Method

	// Status é o estado actual.
	Status Status

	// Provider é o gateway que a processou.
	Provider string

	// ProviderRef é o identificador da operação no gateway (transaction id,
	// session id, referência ATM).
	ProviderRef string

	// StatusRef é a referência de consulta de estado, quando difere.
	StatusRef string

	// ExternalID é o identificador sequencial atribuído pelo gateway, quando
	// existe (o payment_id do Proxypay DDS dentro do mandato).
	ExternalID int

	// Entity e Reference são a referência bancária, quando o método é esse.
	Entity    string
	Reference string

	// MandateID é o mandato contra o qual o débito directo foi apresentado.
	MandateID string

	// Description é o texto que o cliente vê.
	Description string

	// InvoiceURL é a factura fiscal emitida pelo gateway.
	InvoiceURL string

	// PeriodStart e PeriodEnd são o período que esta cobrança paga.
	PeriodStart *time.Time
	PeriodEnd   *time.Time

	// ExpiresAt é o prazo de pagamento.
	ExpiresAt *time.Time

	// ProcessedAt é quando o estado ficou terminal.
	ProcessedAt *time.Time

	// Attempts conta as tentativas de apresentação ao gateway, e NextRetryAt diz
	// a partir de quando a próxima é permitida. São o que impede uma cobrança
	// problemática de ser martelada contra o banco em ciclo.
	Attempts    int
	NextRetryAt *time.Time

	// FailureReason é o motivo da última recusa, tal como o gateway o deu.
	FailureReason string

	// Metadata são pares livres de quem chama.
	Metadata map[string]string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewPayment cria uma cobrança pendente.
//
// O prazo de validade não é definido aqui de propósito: uma cobrança avulsa
// vive 24 horas, mas a de uma renovação tem de viver até ao fecho da janela.
// Use [Payment.SetExpiry] com o prazo certo para o caso.
func NewPayment(id, subjectID string, amount money.Amount, method Method) (*Payment, error) {
	if !amount.IsPositive() {
		return nil, ErrAmountNotPositive
	}
	now := time.Now().UTC()
	return &Payment{
		ID:        id,
		SubjectID: subjectID,
		Amount:    amount,
		Method:    MethodOrDefault(method, MethodReference),
		Status:    StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// SetExpiry define o prazo de pagamento.
func (p *Payment) SetExpiry(t time.Time) {
	p.ExpiresAt = &t
	p.UpdatedAt = time.Now().UTC()
}

// Expired indica se o prazo já passou (uma cobrança sem prazo nunca expira).
func (p *Payment) Expired(now time.Time) bool {
	return p.ExpiresAt != nil && now.After(*p.ExpiresAt)
}

// Approve marca a cobrança como paga.
//
// A referência recebida é verificada contra a que está guardada quando ambas
// existem. É a defesa contra o webhook que chega com a referência de outra
// cobrança: sem esta verificação, um pagamento de 500 podia dar por liquidada
// uma cobrança de 50 000.
func (p *Payment) Approve(providerRef string) error {
	if !p.Status.CanTransitionTo(StatusApproved) {
		return ErrInvalidTransition
	}
	if providerRef != "" && p.ProviderRef != "" && p.ProviderRef != providerRef {
		return ErrInvalidTransition
	}
	now := time.Now().UTC()
	p.Status = StatusApproved
	p.ProcessedAt = &now
	p.UpdatedAt = now
	if p.ProviderRef == "" {
		p.ProviderRef = providerRef
	}
	return nil
}

// Reject marca a cobrança como recusada, guardando o motivo.
func (p *Payment) Reject(reason string) error {
	if !p.Status.CanTransitionTo(StatusRejected) {
		return ErrInvalidTransition
	}
	now := time.Now().UTC()
	p.Status = StatusRejected
	p.FailureReason = reason
	p.ProcessedAt = &now
	p.UpdatedAt = now
	return nil
}

// Cancel revoga uma cobrança ainda por pagar.
func (p *Payment) Cancel(reason string) error {
	if !p.Status.CanTransitionTo(StatusCancelled) {
		return ErrInvalidTransition
	}
	now := time.Now().UTC()
	p.Status = StatusCancelled
	p.FailureReason = reason
	p.ProcessedAt = &now
	p.UpdatedAt = now
	return nil
}

// Expire marca a cobrança como expirada por falta de pagamento.
func (p *Payment) Expire() error {
	if !p.Status.CanTransitionTo(StatusExpired) {
		return ErrInvalidTransition
	}
	now := time.Now().UTC()
	p.Status = StatusExpired
	p.ProcessedAt = &now
	p.UpdatedAt = now
	return nil
}

// Refund marca a cobrança como estornada.
func (p *Payment) Refund(reason string) error {
	if !p.Status.CanTransitionTo(StatusRefunded) {
		return ErrInvalidTransition
	}
	now := time.Now().UTC()
	p.Status = StatusRefunded
	p.FailureReason = reason
	p.UpdatedAt = now
	return nil
}

// ApplyResult grava na cobrança o que o gateway devolveu.
func (p *Payment) ApplyResult(provider string, res ChargeResult) {
	p.Provider = provider
	if res.ProviderRef != "" {
		p.ProviderRef = res.ProviderRef
	}
	if res.StatusRef != "" {
		p.StatusRef = res.StatusRef
	}
	if res.Entity != "" {
		p.Entity = res.Entity
	}
	if res.Reference != "" {
		p.Reference = res.Reference
	}
	if res.InvoiceURL != "" {
		p.InvoiceURL = res.InvoiceURL
	}
	if res.ExternalID != 0 {
		p.ExternalID = res.ExternalID
	}
	if res.ExpiresAt != nil {
		p.ExpiresAt = res.ExpiresAt
	}
	if res.Status != "" && res.Status != p.Status {
		if res.Status == StatusApproved {
			_ = p.Approve(res.ProviderRef)
		} else {
			p.Status = res.Status
		}
	}
	p.UpdatedAt = time.Now().UTC()
}

// RecordAttempt regista mais uma tentativa de apresentação e agenda a próxima
// com espera exponencial (1, 2, 4, 8... vezes a base, até ao tecto).
//
// Espaçar as tentativas não é cortesia para com o gateway: um débito directo
// reapresentado de imediato falha pela mesma razão que falhou, e cada tentativa
// pode custar comissão e irritar o banco do cliente.
func (p *Payment) RecordAttempt(base, max time.Duration) {
	p.Attempts++
	wait := base
	for i := 1; i < p.Attempts && wait < max; i++ {
		wait *= 2
	}
	if wait > max {
		wait = max
	}
	next := time.Now().UTC().Add(wait)
	p.NextRetryAt = &next
	p.UpdatedAt = time.Now().UTC()
}

// RetryDue indica se a cobrança pode ser reapresentada agora.
func (p *Payment) RetryDue(now time.Time) bool {
	return p.NextRetryAt == nil || !now.Before(*p.NextRetryAt)
}

// ToChargeRequest monta o pedido de cobrança a partir da cobrança guardada.
func (p *Payment) ToChargeRequest(customer Customer) ChargeRequest {
	return ChargeRequest{
		Reference:   p.ID,
		Amount:      p.Amount,
		Method:      p.Method,
		Description: p.Description,
		Customer:    customer,
		ExpiresAt:   p.ExpiresAt,
		MandateID:   p.MandateID,
		PeriodStart: p.PeriodStart,
		PeriodEnd:   p.PeriodEnd,
		Metadata:    p.Metadata,
	}
}

// Store é o armazenamento das cobranças. Implemente-o sobre a sua base de
// dados; a biblioteca não escreve SQL nenhum.
//
// Todos os métodos que devolvem um único registo devem devolver
// (nil, nil) quando não encontram nada, e não um erro: a ausência é um
// resultado normal em quase todos os caminhos que os chamam.
type Store interface {
	// Create persiste uma cobrança nova.
	Create(ctx context.Context, p *Payment) error

	// Update grava as alterações de uma cobrança existente.
	Update(ctx context.Context, p *Payment) error

	// ByID devolve uma cobrança pelo seu identificador.
	ByID(ctx context.Context, id string) (*Payment, error)

	// ByProviderRef devolve uma cobrança pela referência do gateway. É por aqui
	// que um webhook encontra a cobrança a que diz respeito.
	ByProviderRef(ctx context.Context, provider, ref string) (*Payment, error)

	// PendingBySubject devolve as cobranças por pagar de um assunto (as
	// referências ainda vivas de uma subscrição, por exemplo).
	PendingBySubject(ctx context.Context, subjectID string) ([]*Payment, error)

	// PendingVerifiable devolve as cobranças pendentes que se confirmam por
	// consulta de estado, para o poller as ir verificando. limit limita o lote.
	PendingVerifiable(ctx context.Context, provider string, limit int) ([]*Payment, error)

	// ExpiredPending devolve as cobranças pendentes cujo prazo já passou.
	ExpiredPending(ctx context.Context, at time.Time, limit int) ([]*Payment, error)

	// RetryDue devolve as cobranças recusadas que podem ser reapresentadas
	// agora e ainda não esgotaram as tentativas.
	RetryDue(ctx context.Context, at time.Time, maxAttempts, limit int) ([]*Payment, error)
}
