// Package subscription é o motor de subscrições e renovações.
//
// Junta duas maneiras de renovar que costumam ser tratadas em separado, e que
// na verdade são a mesma coisa vista de dois lados:
//
//   - Com cobrança automática (cartão, débito directo): o gateway cobra sozinho
//     e nós reagimos ao resultado. O que falta aqui é o que fazer quando ele
//     falha, e é o que a tolerância e a cadeia de avisos resolvem.
//   - Sem cobrança automática (referência, Multicaixa Express, transferência):
//     é preciso emitir a cobrança do ciclo seguinte, avisar o cliente e esperar.
//     O que falta aqui é fazê-lo com antecedência suficiente para o cliente
//     pagar sem quebra de serviço.
//
// A resposta comum é a janela de renovação: um período que abre antes do fim do
// ciclo, em que a cobrança já existe e a cobertura ainda dura, e que fecha
// alguns dias depois. Durante a janela inteira o cliente tem serviço e tem como
// pagar. Fechada a janela sem pagamento, a subscrição expira.
package subscription

import (
	"time"

	"github.com/spolly-ao/fllex/cycle"
	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// Status é o estado de uma subscrição.
type Status string

const (
	// StatusPending: contratada, à espera do primeiro pagamento. Sem cobertura.
	StatusPending Status = "pending"
	// StatusActive: em vigor. É o único estado que dá acesso.
	StatusActive Status = "active"
	// StatusPastDue: o pagamento falhou e corre a tolerância. Mantém o acesso,
	// de propósito: cortar ao primeiro insucesso de cobrança perde clientes que
	// só tinham o cartão expirado.
	StatusPastDue Status = "past_due"
	// StatusCancelled: terminada por decisão de alguém.
	StatusCancelled Status = "cancelled"
	// StatusExpired: o contrato chegou ao fim e não foi renovado. Não é o mesmo
	// que cancelada: ninguém decidiu terminar, apenas não foi paga, e uma
	// expirada volta a ficar activa se o pagamento aparecer mais tarde.
	StatusExpired Status = "expired"
	// StatusSuspended: suspensa por um operador, sem terminar o contrato.
	StatusSuspended Status = "suspended"
)

// Active indica se o estado dá acesso ao serviço.
func (s Status) Active() bool { return s == StatusActive || s == StatusPastDue }

// RenewalState é o estado da renovação em curso.
type RenewalState string

const (
	// RenewalNone: não há renovação a decorrer.
	RenewalNone RenewalState = ""
	// RenewalWarned: o aviso de renovação já saiu, a cobrança ainda não.
	RenewalWarned RenewalState = "warned"
	// RenewalPending: a cobrança do ciclo seguinte foi emitida e aguarda
	// pagamento.
	RenewalPending RenewalState = "pending"
	// RenewalFailed: a cobrança foi recusada e a janela ainda não fechou.
	RenewalFailed RenewalState = "failed"
	// RenewalPaid: o ciclo seguinte está pago.
	RenewalPaid RenewalState = "paid"
)

// Subscription é uma subscrição.
//
// Reúne os dois vocabulários que a cobrança recorrente usa: o de periodicidade
// (mensal, anual), que serve os produtos vendidos por assinatura, e o de
// contrato com prestações (doze meses cobrados ao mês), que serve os seguros e
// os planos de saúde. Um produto usa um ou o outro; os campos do que não usar
// ficam a zero e são ignorados.
type Subscription struct {
	// ID é o identificador do lado de quem chama.
	ID string

	// CustomerID é o titular.
	CustomerID string

	// PlanID e PlanName identificam o que foi contratado. O nome é guardado por
	// cópia de propósito: um plano renomeado não deve reescrever o que dizem as
	// facturas já emitidas.
	PlanID   string
	PlanName string

	// Status é o estado actual.
	Status Status

	// Provider e Method dizem por onde esta subscrição é cobrada.
	Provider string
	Method   payment.Method

	// ProviderRef, CustomerRef e SubscriptionRef são os identificadores do lado
	// do gateway.
	ProviderRef     string
	CustomerRef     string
	SubscriptionRef string

	// Amount é o que se cobra por ciclo, já com descontos aplicados.
	Amount money.Amount

	// GrossAmount é o preço de tabela, sem desconto.
	//
	// Guardar os dois separados é o que permite renovar pelo preço certo: um
	// cupão ou um desconto de campanha valem para o primeiro ciclo e não
	// recorrem, por isso a renovação parte daqui e não do que foi pago.
	GrossAmount money.Amount

	// DiscountPercent e CouponID descrevem o desconto aplicado.
	DiscountPercent int
	CouponID        string

	// CouponRecurring e CouponCycles dizem se o desconto acompanha as
	// renovações e por quantos ciclos (zero com CouponRecurring é para
	// sempre). Preencha-os a partir de [coupon.Discount].
	//
	// Sem eles, todo o desconto é do primeiro ciclo, que é o padrão seguro.
	// Com eles, um cupão de "três meses a metade" continua a valer no segundo
	// e no terceiro ciclos sem ninguém ter de tratar disso à mão.
	CouponRecurring bool
	CouponCycles    int

	// Interval é a periodicidade, para os produtos vendidos por assinatura.
	Interval cycle.Interval

	// ContractMonths e BillingPeriodMonths descrevem um contrato com
	// prestações: quanto tempo dura e de quanto em quanto tempo se cobra. Ver
	// [cycle.InstalmentAmount].
	ContractMonths      int
	BillingPeriodMonths int

	// CycleNumber conta os ciclos já pagos. O contrato inicial é o 1.
	CycleNumber int

	// StartDate e CurrentPeriodEnd delimitam o ciclo em vigor.
	StartDate        time.Time
	CurrentPeriodEnd *time.Time

	// AnchorDay é o dia do mês em que esta subscrição é cobrada.
	//
	// Guardá-lo é o que impede a data de cobrança de derivar: quem assinou a 31
	// é cobrado a 28 em Fevereiro e volta ao 31 em Março. Sem âncora, o mês
	// curto trunca a data e ela fica presa no 28 até ao fim do contrato. Zero
	// usa o dia da data base, que é o comportamento antigo.
	AnchorDay int

	// EndDate é o fim do contrato, quando há um prazo contratado.
	EndDate *time.Time

	// DatesManual marca as datas escritas por um operador, e não calculadas.
	//
	// Serve as subscrições que existiam em papel e estão a ser passadas para o
	// sistema: o contrato começou há dois anos, e nem a activação nem o
	// pagamento podem voltar a carimbar essas datas com hoje.
	DatesManual bool

	// AutoRenew liga a renovação no fim do ciclo.
	AutoRenew bool

	// CancelAtPeriodEnd marca a subscrição para terminar no fim do período já
	// pago, sem cortar o acesso que o cliente pagou.
	CancelAtPeriodEnd bool

	// --- renovação em curso ---

	// RenewalState descreve a renovação a decorrer.
	RenewalState RenewalState
	// RenewalDueAt é o prazo para pagar o ciclo seguinte (o fecho da janela).
	RenewalDueAt *time.Time
	// RenewalWarnedAt é quando saiu o aviso, para não avisar duas vezes.
	RenewalWarnedAt *time.Time
	// RenewalPaymentID é a cobrança emitida para este ciclo.
	RenewalPaymentID string
	// RenewalStatusRef é a referência de consulta de estado dessa cobrança.
	RenewalStatusRef string
	// RenewalAttempts conta as tentativas de cobrança dentro da janela.
	RenewalAttempts int

	// PendingEntity, PendingReference e PendingDueDate são os dados de
	// pagamento que o cliente vê enquanto a renovação está por pagar.
	PendingEntity    string
	PendingReference string
	PendingDueDate   string

	// MandateID é o mandato de débito directo desta subscrição.
	MandateID string

	// Metadata são pares livres de quem chama.
	Metadata map[string]string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// RenewalAmount é o valor a cobrar na renovação: o preço de tabela.
//
// Cupões e descontos são pontuais e não recorrem, por isso a renovação nunca
// parte do que foi pago da primeira vez. Sem preço de tabela registado (o caso
// das subscrições antigas), recorre ao valor corrente, que é o melhor palpite
// disponível.
func (s *Subscription) RenewalAmount() money.Amount {
	if s.GrossAmount.IsPositive() {
		return s.GrossAmount
	}
	return s.Amount
}

// InstalmentAmount é o que se cobra numa prestação, para os contratos que se
// pagam a prestações. Para os produtos vendidos por periodicidade devolve o
// próprio valor do ciclo.
//
// É aqui que mora o erro mais caro deste domínio: cobrar o total do contrato
// uma vez por ciclo. Enquanto um ciclo for o contrato inteiro dá certo por
// acidente; num contrato anual cobrado ao mês passa a cobrar o preço do ano
// todos os meses, doze vezes, e nada a jusante o corrige.
func (s *Subscription) InstalmentAmount() money.Amount {
	if s.ContractMonths <= 0 || s.BillingPeriodMonths <= 0 {
		return s.RenewalAmount()
	}
	n := s.CycleNumber - 1
	if n < 0 {
		n = 0
	}
	return cycle.InstalmentAmount(s.RenewalAmount(), n, s.BillingPeriodMonths, s.ContractMonths)
}

// ApplyRenewalPricing acerta o preço no início de um novo ciclo.
//
// Por omissão repõe o preço de tabela: o desconto era do primeiro ciclo e não
// recorre. Um cupão marcado como recorrente é a excepção, e mantém-se enquanto
// lhe restarem ciclos.
func (s *Subscription) ApplyRenewalPricing() {
	gross := s.RenewalAmount()
	if s.DiscountApplies(s.CycleNumber + 1) {
		s.Amount = gross.PercentOff(s.DiscountPercent)
		s.UpdatedAt = time.Now().UTC()
		return
	}
	s.Amount = gross
	s.DiscountPercent = 0
	s.CouponID = ""
	s.CouponRecurring = false
	s.CouponCycles = 0
	s.UpdatedAt = time.Now().UTC()
}

// DiscountApplies indica se o desconto do cupão ainda vale no ciclo indicado
// (o primeiro é 1).
func (s *Subscription) DiscountApplies(cycleNumber int) bool {
	if s.DiscountPercent <= 0 {
		return false
	}
	if cycleNumber <= 1 {
		return true
	}
	if !s.CouponRecurring {
		return false
	}
	return s.CouponCycles <= 0 || cycleNumber <= s.CouponCycles
}

// NextPeriodEnd calcula o fim do ciclo seguinte.
//
// Num contrato com prestações conta a partir do fim do ciclo anterior; num
// produto por periodicidade usa [cycle.PeriodEnd], que trata da reactivação.
func (s *Subscription) NextPeriodEnd(now time.Time, reactivating bool) time.Time {
	anchor := s.anchorDay()
	if s.ContractMonths > 0 && s.BillingPeriodMonths > 0 && s.CurrentPeriodEnd != nil && !reactivating {
		return cycle.NextCycleEndAnchored(*s.CurrentPeriodEnd, s.BillingPeriodMonths, s.ContractMonths, anchor)
	}
	interval := s.Interval
	if interval == "" {
		interval = cycle.Monthly
	}
	return cycle.PeriodEndAnchored(now, s.CurrentPeriodEnd, interval, anchor, reactivating)
}

// anchorDay devolve o dia de cobrança, recorrendo à data de adesão quando não
// está definido.
func (s *Subscription) anchorDay() int {
	if s.AnchorDay > 0 {
		return s.AnchorDay
	}
	if !s.StartDate.IsZero() {
		return s.StartDate.Day()
	}
	return 0
}

// Recurring indica se o gateway cobra sozinho no ciclo seguinte.
func (s *Subscription) Recurring() bool { return s.Method.Recurring() }

// Expired indica se o ciclo em vigor já acabou.
func (s *Subscription) Expired(now time.Time) bool {
	return s.CurrentPeriodEnd != nil && now.After(*s.CurrentPeriodEnd)
}

// InGrace indica se o ciclo acabou mas a tolerância ainda corre. Durante a
// tolerância o acesso mantém-se: é essa a diferença entre tolerância a sério e
// um simples adiamento do corte.
func (s *Subscription) InGrace(now time.Time) bool {
	return s.Expired(now) && s.RenewalDueAt != nil && !now.After(*s.RenewalDueAt)
}

// PastDue indica se a subscrição já ultrapassou o prazo de pagamento.
func (s *Subscription) PastDue(now time.Time) bool {
	return s.RenewalDueAt != nil && now.After(*s.RenewalDueAt)
}

// ResetRenewal limpa o estado da renovação, no fim de um ciclo bem sucedido.
func (s *Subscription) ResetRenewal() {
	s.RenewalState = RenewalNone
	s.RenewalDueAt = nil
	s.RenewalWarnedAt = nil
	s.RenewalPaymentID = ""
	s.RenewalStatusRef = ""
	s.RenewalAttempts = 0
	s.PendingEntity = ""
	s.PendingReference = ""
	s.PendingDueDate = ""
	s.UpdatedAt = time.Now().UTC()
}

// Activate põe a subscrição em vigor a partir de um pagamento confirmado.
//
// A reactivação (uma subscrição cancelada ou expirada que volta) ancora o novo
// ciclo na data do pagamento, porque não se cobra tempo que o cliente não teve.
// A renovação normal ancora no fim previsto, para que pagar dois dias antes ou
// três depois não altere a data de renovação nem faça o cliente ganhar ou
// perder dias.
func (s *Subscription) Activate(now time.Time) {
	reactivating := s.Status == StatusCancelled || s.Status == StatusExpired

	if s.StartDate.IsZero() {
		s.StartDate = now
	}
	// A âncora é fixada antes de se calcular o fim do ciclo, porque é ela que o
	// determina. Numa reactivação passa a ser o dia do pagamento: o cliente
	// esteve sem serviço e o ciclo recomeça agora, não na data em que assinou.
	switch {
	case reactivating:
		s.AnchorDay = cycle.AnchorDay(now)
	case s.AnchorDay <= 0:
		s.AnchorDay = cycle.AnchorDay(s.StartDate)
	}

	end := s.NextPeriodEnd(now, reactivating)

	s.Status = StatusActive
	s.CancelAtPeriodEnd = false
	s.CurrentPeriodEnd = &end
	s.CycleNumber++
	s.ApplyRenewalPricing()
	s.ResetRenewal()
	s.UpdatedAt = now
}

// Cancel termina a subscrição. Com atPeriodEnd, marca-a para terminar no fim do
// período já pago em vez de cortar o acesso de imediato.
func (s *Subscription) Cancel(atPeriodEnd bool, now time.Time) {
	if atPeriodEnd {
		s.CancelAtPeriodEnd = true
		s.AutoRenew = false
		s.UpdatedAt = now
		return
	}
	s.Status = StatusCancelled
	s.AutoRenew = false
	s.UpdatedAt = now
}

// Expire marca a subscrição como não renovada.
func (s *Subscription) Expire(now time.Time) {
	s.Status = StatusExpired
	s.RenewalState = RenewalNone
	s.UpdatedAt = now
}

// MarkPastDue põe a subscrição em falha de pagamento e fixa o prazo até ao qual
// ainda pode ser salva.
func (s *Subscription) MarkPastDue(due time.Time, now time.Time) {
	s.Status = StatusPastDue
	s.RenewalState = RenewalFailed
	s.RenewalDueAt = &due
	s.UpdatedAt = now
}
