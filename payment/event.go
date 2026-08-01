package payment

import (
	"time"

	"github.com/spolly-ao/fllex/money"
)

// EventType é um evento de gateway já traduzido para o vocabulário comum.
// Normalizar aqui é o que permite ao código de negócio tratar um pagamento
// confirmado da mesma maneira, venha ele do Stripe, do MoMenu ou do Proxypay.
type EventType string

const (
	// EventNone: evento sem interesse para nós. Não é erro.
	EventNone EventType = ""

	// EventChargeSucceeded: uma cobrança foi paga.
	EventChargeSucceeded EventType = "charge_succeeded"
	// EventChargeFailed: uma cobrança foi recusada.
	EventChargeFailed EventType = "charge_failed"
	// EventChargeCancelled: uma cobrança pendente foi revogada.
	EventChargeCancelled EventType = "charge_cancelled"
	// EventChargeExpired: uma cobrança pendente passou o prazo.
	EventChargeExpired EventType = "charge_expired"
	// EventChargeRefunded: dinheiro já cobrado foi devolvido.
	EventChargeRefunded EventType = "charge_refunded"

	// EventSubscriptionActive: uma subscrição ficou activa.
	EventSubscriptionActive EventType = "subscription_active"
	// EventSubscriptionUpdated: mudou o plano, o período ou o estado.
	EventSubscriptionUpdated EventType = "subscription_updated"
	// EventSubscriptionCancelled: a subscrição terminou no gateway.
	EventSubscriptionCancelled EventType = "subscription_cancelled"

	// EventInvoicePaid: uma factura do gateway foi paga. Na renovação de um
	// ciclo é o sinal para emitir a nossa factura; na criação da subscrição
	// ignora-se, porque essa já saiu no evento do checkout.
	EventInvoicePaid EventType = "invoice_paid"

	// EventMandateActivated: o titular activou o mandato no seu banco e o
	// débito directo passa a poder ser apresentado.
	EventMandateActivated EventType = "mandate_activated"
	// EventMandateRejected: o banco recusou o mandato.
	EventMandateRejected EventType = "mandate_rejected"
	// EventMandateCancelled: o mandato foi cancelado.
	EventMandateCancelled EventType = "mandate_cancelled"
)

// Event é um evento de gateway normalizado e já validado.
type Event struct {
	// ID é o identificador do evento no gateway. Guarde-o: é por ele que se
	// deduplicam reentregas, e todos os gateways sérios reentregam.
	ID string

	// Type é o evento traduzido.
	Type EventType

	// Provider é quem o enviou.
	Provider string

	// Reference é o identificador do nosso lado, recuperado dos metadados. É o
	// que liga o evento à encomenda, ao pagamento ou à subscrição.
	Reference string

	// ChargeRef, SubscriptionRef e CustomerRef são os identificadores do lado do
	// gateway.
	ChargeRef       string
	SubscriptionRef string
	CustomerRef     string

	// Status é o estado normalizado que o evento comunica.
	Status Status

	// Method é o método de pagamento, quando o evento o identifica.
	Method Method

	// Amount é o valor envolvido.
	Amount *money.Amount

	// PeriodStart, PeriodEnd e CurrentPeriodEnd descrevem o período coberto.
	PeriodStart      *time.Time
	PeriodEnd        *time.Time
	CurrentPeriodEnd *time.Time

	// CancelAtPeriodEnd indica que a subscrição termina no fim do período pago.
	CancelAtPeriodEnd bool

	// PlanRef e Interval identificam o plano contratado.
	PlanRef  string
	Interval string

	// InvoiceRef e InvoiceURL são a factura do gateway. O InvoiceRef deduplica a
	// emissão da nossa factura por ciclo.
	InvoiceRef string
	InvoiceURL string

	// BillingReason distingue a primeira factura da de renovação
	// ("subscription_create" e "subscription_cycle" no Stripe). Sem ele,
	// emite-se factura a dobrar no primeiro ciclo.
	BillingReason string

	// MandateRef é o mandato a que o evento diz respeito.
	MandateRef string

	// Reason é o código ou o texto do motivo, nas recusas e cancelamentos.
	Reason string

	// OccurredAt é quando o evento aconteceu, segundo o gateway.
	OccurredAt *time.Time

	// Metadata são os pares livres que seguiram na cobrança e voltam aqui.
	Metadata map[string]string

	// Raw é o corpo original, para diagnóstico.
	Raw map[string]any
}

// Meta devolve um valor dos metadados, ou string vazia.
func (e *Event) Meta(key string) string {
	if e == nil || e.Metadata == nil {
		return ""
	}
	return e.Metadata[key]
}

// Ignorable indica se o evento não exige acção.
func (e *Event) Ignorable() bool { return e == nil || e.Type == EventNone }

// BillingCycle indica se o evento é a factura de uma renovação e não a da
// criação da subscrição. É a condição a testar antes de emitir a factura do
// ciclo.
func (e *Event) BillingCycle() bool {
	return e != nil && e.BillingReason == "subscription_cycle"
}
