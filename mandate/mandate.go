// Package mandate representa as autorizações de débito em conta: o documento
// pelo qual um titular deixa que lhe cobrem directamente, e sem o qual nenhum
// débito directo pode ser apresentado.
//
// Existe separado do pagamento porque tem um ciclo de vida próprio e muito mais
// longo: um mandato é criado uma vez, activado pelo titular no seu banco (o que
// pode demorar dias), e depois serve dezenas de cobranças ao longo de anos. Uma
// cobrança que falhe não invalida o mandato; um mandato cancelado invalida
// todas as cobranças futuras.
package mandate

import (
	"context"
	"time"
)

// Status é o estado de um mandato.
type Status string

const (
	// StatusPending: o mandato foi criado do nosso lado mas ainda não foi
	// submetido ao banco.
	StatusPending Status = "pending"
	// StatusSubmitted: foi registado no gateway e aguarda a activação do
	// titular. Este é o estado onde os mandatos ficam mais tempo, e é humano:
	// depende de o cliente ir ao banco ou à app.
	StatusSubmitted Status = "submitted"
	// StatusActive: o titular activou-o. Só neste estado se podem apresentar
	// cobranças.
	StatusActive Status = "active"
	// StatusRejected: o banco recusou o registo.
	StatusRejected Status = "rejected"
	// StatusCancelled: foi cancelado, por nós ou pelo titular.
	StatusCancelled Status = "cancelled"
	// StatusExpired: passou o prazo de activação sem o titular agir.
	StatusExpired Status = "expired"
)

// Terminal indica se o estado é final.
func (s Status) Terminal() bool {
	return s == StatusRejected || s == StatusCancelled || s == StatusExpired
}

// Type distingue como o mandato é autorizado.
type Type string

const (
	// TypeSelfActivated: o titular activa-o no seu próprio banco, com o código
	// da entidade e o do mandato. Não precisa de papel (SAP no Proxypay).
	TypeSelfActivated Type = "self_activated"
	// TypePreAuthorized: exige um formulário assinado, digitalizado e processado
	// (CAP no Proxypay).
	TypePreAuthorized Type = "pre_authorized"
)

// Mandate é uma autorização de débito em conta.
type Mandate struct {
	// ID é o identificador do lado de quem chama.
	ID string

	// SubjectID é o que o mandato serve: a subscrição, o contrato, o cliente.
	SubjectID string

	// CustomerID é o titular da conta.
	CustomerID string

	// ExternalID é o identificador sequencial atribuído pelo gateway. É o número
	// por onde as cobranças são apresentadas, e o que o titular usa (preenchido
	// a treze dígitos) para activar o mandato no banco.
	ExternalID int

	// ContractID é a referência do contrato comunicada ao banco. Aparece no
	// extracto do titular, por isso deve ser algo que ele reconheça.
	ContractID string

	// Provider é o gateway que o gere.
	Provider string

	// Type distingue auto-activado de pré-autorizado.
	Type Type

	// Status é o estado actual.
	Status Status

	// DebtorName, TaxID, Email e Phone são os dados do titular.
	DebtorName string
	TaxID      string
	Email      string
	Phone      string

	// DebitIBAN é a conta a debitar (só nos pré-autorizados; nos auto-activados
	// é o titular que a indica ao banco).
	DebitIBAN string
	// CreditIBAN é a conta que recebe.
	CreditIBAN string

	// Recurrence e Purpose são os códigos declarados ao banco.
	Recurrence string
	Purpose    string

	// MaxAmount é o tecto por cobrança autorizado pelo titular. Uma cobrança
	// acima dele é recusada pelo banco, por isso vale a pena verificá-lo antes
	// de apresentar.
	MaxAmount string

	// SignatureDate e ImageID dizem respeito ao formulário assinado (CAP).
	SignatureDate string
	ImageID       string

	// ActivatedAt é quando o titular o activou.
	ActivatedAt *time.Time

	// ExpiresAt é o prazo para a activação. Passado ele sem o titular agir, o
	// mandato deixa de valer a pena e a cobrança tem de seguir por outra via.
	ExpiresAt *time.Time

	// CancelledAt é quando foi cancelado, e Reason o motivo comunicado.
	CancelledAt *time.Time
	Reason      string

	// LastPaymentID é o último identificador de cobrança usado dentro do
	// mandato, para as sequências.
	LastPaymentID int

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Active indica se o mandato pode receber cobranças.
func (m *Mandate) Active() bool { return m != nil && m.Status == StatusActive }

// Expired indica se o prazo de activação já passou sem o titular agir.
func (m *Mandate) Expired(now time.Time) bool {
	return m != nil && m.Status == StatusSubmitted && m.ExpiresAt != nil && now.After(*m.ExpiresAt)
}

// Submit marca o mandato como registado no gateway, à espera do titular.
func (m *Mandate) Submit(externalID int) {
	m.ExternalID = externalID
	m.Status = StatusSubmitted
	m.UpdatedAt = time.Now().UTC()
}

// Activate marca o mandato como activo.
func (m *Mandate) Activate(at time.Time) {
	m.Status = StatusActive
	m.ActivatedAt = &at
	m.UpdatedAt = time.Now().UTC()
}

// Reject marca o mandato como recusado pelo banco.
func (m *Mandate) Reject(reason string) {
	m.Status = StatusRejected
	m.Reason = reason
	m.UpdatedAt = time.Now().UTC()
}

// Cancel marca o mandato como cancelado.
func (m *Mandate) Cancel(reason string, at time.Time) {
	m.Status = StatusCancelled
	m.Reason = reason
	m.CancelledAt = &at
	m.UpdatedAt = time.Now().UTC()
}

// Expire marca o mandato como expirado por falta de activação.
func (m *Mandate) Expire() {
	m.Status = StatusExpired
	m.UpdatedAt = time.Now().UTC()
}

// Store é o armazenamento dos mandatos.
type Store interface {
	// Create persiste um mandato novo.
	Create(ctx context.Context, m *Mandate) error
	// Update grava as alterações.
	Update(ctx context.Context, m *Mandate) error
	// ByID devolve um mandato pelo identificador interno.
	ByID(ctx context.Context, id string) (*Mandate, error)
	// ByExternalID devolve um mandato pelo identificador do gateway. É por aqui
	// que os eventos do fluxo do gateway encontram o mandato.
	ByExternalID(ctx context.Context, provider string, externalID int) (*Mandate, error)
	// ActiveForSubject devolve o mandato activo de um assunto, se houver.
	ActiveForSubject(ctx context.Context, subjectID string) (*Mandate, error)
	// PendingActivation devolve os mandatos à espera do titular, para o
	// verificador de prazos.
	PendingActivation(ctx context.Context, limit int) ([]*Mandate, error)
}

// Resolver traduz o identificador interno de um mandato para o do gateway e diz
// se está activo.
//
// É a porta que o provider de débito directo usa para não ter de conhecer o
// armazenamento: quem integra a biblioteca liga-a ao seu [Store] com
// [NewStoreResolver], ou a outra coisa qualquer se tiver os mandatos noutro
// sítio.
type Resolver interface {
	Resolve(ctx context.Context, mandateID string) (externalID int, active bool, err error)
}

// StoreResolver implementa [Resolver] sobre um [Store].
type StoreResolver struct{ store Store }

// NewStoreResolver liga um [Store] à interface [Resolver].
func NewStoreResolver(s Store) *StoreResolver { return &StoreResolver{store: s} }

// Resolve devolve o identificador do gateway e se o mandato está activo.
func (r *StoreResolver) Resolve(ctx context.Context, mandateID string) (int, bool, error) {
	m, err := r.store.ByID(ctx, mandateID)
	if err != nil {
		return 0, false, err
	}
	if m == nil {
		return 0, false, nil
	}
	return m.ExternalID, m.Active(), nil
}
