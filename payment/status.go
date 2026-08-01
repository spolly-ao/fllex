package payment

import "strings"

// Status é o estado de um pagamento. O ciclo de vida é deliberadamente curto:
// um pagamento nasce pendente e acaba aprovado, rejeitado, cancelado ou
// expirado, e nenhum desses quatro volta atrás.
type Status string

const (
	// StatusPending: a cobrança existe e aguarda o dinheiro.
	StatusPending Status = "pending"
	// StatusApproved: o dinheiro entrou. Estado terminal.
	StatusApproved Status = "approved"
	// StatusRejected: o gateway ou o banco recusou. Estado terminal.
	StatusRejected Status = "rejected"
	// StatusCancelled: a cobrança foi revogada antes de ser paga (por nós ou
	// pelo cliente). Estado terminal.
	StatusCancelled Status = "cancelled"
	// StatusExpired: o prazo passou sem pagamento. Estado terminal.
	StatusExpired Status = "expired"
	// StatusRefunded: foi devolvido dinheiro já cobrado. Estado terminal.
	StatusRefunded Status = "refunded"
)

// ParseStatus normaliza a escrita de um estado, incluindo as variantes em
// maiúsculas de esquemas antigos ("PENDING", "APPROVED") e o "paid" e
// "canceled" que aparecem nas respostas de gateways.
func ParseStatus(s string) Status {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pending", "incomplete", "processing", "open":
		return StatusPending
	case "approved", "paid", "succeeded", "success", "complete", "completed", "collected":
		return StatusApproved
	case "rejected", "failed", "declined", "denied":
		return StatusRejected
	case "cancelled", "canceled", "revoked", "voided":
		return StatusCancelled
	case "expired":
		return StatusExpired
	case "refunded", "reversed":
		return StatusRefunded
	default:
		return ""
	}
}

// String devolve o código do estado.
func (s Status) String() string { return string(s) }

// Terminal indica se o estado é final (não há transição possível a partir dele).
func (s Status) Terminal() bool { return s != StatusPending && s != "" }

// Settled indica se o dinheiro entrou.
func (s Status) Settled() bool { return s == StatusApproved }

// CanTransitionTo indica se a passagem de s para next é legítima.
//
// A regra é simples e existe para tornar impossíveis os dois erros que se
// pagam caro: aprovar duas vezes o mesmo pagamento (cobrando o cliente a
// dobrar) e cancelar um pagamento já aprovado (dando por não-recebido dinheiro
// que já entrou). O estorno é a única saída de um pagamento aprovado.
func (s Status) CanTransitionTo(next Status) bool {
	switch s {
	case StatusPending, "":
		return next == StatusApproved || next == StatusRejected ||
			next == StatusCancelled || next == StatusExpired
	case StatusApproved:
		return next == StatusRefunded
	default:
		return false
	}
}
