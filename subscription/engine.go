package subscription

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/spolly-ao/fllex/cycle"
	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// Charge é a cobrança de um ciclo, tal como é entregue ao cliente.
type Charge struct {
	// PaymentID é a cobrança criada.
	PaymentID string
	// Amount é o valor.
	Amount money.Amount
	// Method é por onde se paga.
	Method payment.Method
	// DueAt é o prazo de pagamento (o fecho da janela).
	DueAt time.Time
	// Entity, Reference e DueDate são os dados de uma referência bancária.
	Entity    string
	Reference string
	DueDate   string
	// URL é o link de pagamento, quando existe.
	URL string
	// Result é o que o gateway devolveu, para quem precisar do detalhe.
	Result payment.ChargeResult
}

// CustomerResolver devolve os dados do pagador de uma subscrição. A biblioteca
// não conhece os seus clientes: esta é a porta por onde eles entram.
type CustomerResolver interface {
	Customer(ctx context.Context, s *Subscription) (payment.Customer, error)
}

// CustomerFunc adapta uma função a [CustomerResolver].
type CustomerFunc func(ctx context.Context, s *Subscription) (payment.Customer, error)

// Customer chama a função.
func (f CustomerFunc) Customer(ctx context.Context, s *Subscription) (payment.Customer, error) {
	return f(ctx, s)
}

// LinkBuilder gera o link público onde o cliente paga uma renovação.
//
// O link é o que permite trocar de método de pagamento sem falar com ninguém, e
// por isso vale a pena tê-lo mesmo nos métodos que não precisam dele. Ver
// [tokens.Issuer] para a geração de tokens de uso único.
type LinkBuilder interface {
	RenewalLink(ctx context.Context, s *Subscription, dueAt time.Time) (string, error)
}

// LinkFunc adapta uma função a [LinkBuilder].
type LinkFunc func(ctx context.Context, s *Subscription, dueAt time.Time) (string, error)

// RenewalLink chama a função.
func (f LinkFunc) RenewalLink(ctx context.Context, s *Subscription, dueAt time.Time) (string, error) {
	return f(ctx, s, dueAt)
}

// Config são os parâmetros do motor.
type Config struct {
	// Window é a janela de renovação.
	Window cycle.WindowConfig
	// WarnBeforeDays é a antecedência do aviso, em dias antes do fim do ciclo.
	// Deve ser maior do que o LeadDays da janela, para o aviso chegar antes da
	// cobrança. Zero usa 4.
	WarnBeforeDays int
	// MaxAttempts é o tecto de tentativas de cobrança automática dentro de uma
	// janela. Zero usa 5.
	//
	// Cinco em quinze dias chega: se o banco recusa cinco vezes, o problema não
	// é passageiro, e vale mais deixar expirar do que martelar a conta do
	// cliente.
	MaxAttempts int
	// BatchSize limita quantas subscrições cada passagem trata. Zero usa 200.
	BatchSize int
	// FallbackMethod é o método a usar quando a subscrição não tem nenhum
	// registado. Vazio usa a referência, que é a que produz uma cobrança
	// completa e que o cliente consegue pagar sem nada configurado.
	FallbackMethod payment.Method
}

func (c Config) withDefaults() Config {
	c.Window = c.Window.WithDefaults()
	if c.WarnBeforeDays <= 0 {
		c.WarnBeforeDays = 4
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 200
	}
	if c.FallbackMethod == "" {
		c.FallbackMethod = payment.MethodReference
	}
	return c
}

// Engine é o motor de renovações.
//
// Os seus métodos são passagens idempotentes, pensadas para correrem num
// agendador de tempos a tempos. Cada uma trata do que estiver por fazer e não
// se importa com quantas vezes já correu hoje: uma subscrição já avisada não é
// avisada outra vez, uma cobrança já emitida não é emitida de novo.
type Engine struct {
	store    Store
	payments payment.Store
	registry *payment.Registry
	notify   Notifier
	cfg      Config

	// Customers resolve os dados do pagador.
	Customers CustomerResolver
	// Links gera o link de pagamento. Opcional: sem ele, a cobrança segue sem
	// link.
	Links LinkBuilder
	// IDs gera identificadores para as cobranças criadas.
	IDs func() string
	// Now devolve a hora. Substituível em testes.
	Now func() time.Time
	// Log recebe o que corre mal. Nil usa o registador por omissão.
	Log *slog.Logger
}

// NewEngine cria o motor.
func NewEngine(store Store, payments payment.Store, registry *payment.Registry, notify Notifier, cfg Config) *Engine {
	if notify == nil {
		notify = NopNotifier{}
	}
	return &Engine{
		store:    store,
		payments: payments,
		registry: registry,
		notify:   notify,
		cfg:      cfg.withDefaults(),
		Now:      func() time.Time { return time.Now().UTC() },
	}
}

// Config devolve a configuração em vigor, já com os valores por omissão.
func (e *Engine) Config() Config { return e.cfg }

// Run executa uma passagem completa, pela ordem que importa.
//
// A ordem não é arbitrária. Confirmar primeiro evita cobrar de novo quem já
// pagou; avisar antes de cobrar dá ao cliente tempo de reagir; e expirar por
// último garante que ninguém é cortado no mesmo instante em que a sua
// confirmação estava a ser processada.
func (e *Engine) Run(ctx context.Context) Report {
	var r Report
	r.Confirmed = e.ProcessConfirmations(ctx)
	r.Warned = e.ProcessWarnings(ctx)
	r.Charged = e.ProcessCharges(ctx)
	r.Retried = e.ProcessRetries(ctx)
	r.Dunned = e.ProcessDunning(ctx)
	r.Cleaned = e.ProcessStaleCharges(ctx)
	r.Expired = e.ProcessExpiry(ctx)
	return r
}

// Report conta o que uma passagem fez.
type Report struct {
	Confirmed int
	Warned    int
	Charged   int
	Retried   int
	Dunned    int
	Cleaned   int
	Expired   int
}

// ProcessConfirmations confirma, junto do gateway, as renovações pendentes que
// já foram pagas.
//
// É o passo indispensável nos gateways cujo webhook não é fiável: uma
// referência paga no ATM não gera aviso nenhum de que se pode confiar, e sem
// esta consulta o cliente paga e a subscrição expira na mesma.
func (e *Engine) ProcessConfirmations(ctx context.Context) int {
	subs, err := e.store.AwaitingPayment(ctx, e.cfg.BatchSize)
	if err != nil {
		e.log().Error("fllex: falha a obter renovações por confirmar", "err", err)
		return 0
	}
	n := 0
	for _, s := range subs {
		if s.RenewalStatusRef == "" || s.Provider == "" {
			continue
		}
		verifier, ok := e.registry.Verifier(s.Provider)
		if !ok {
			continue
		}
		status, err := verifier.VerifyCharge(ctx, s.RenewalStatusRef, s.RenewalPaymentID)
		if err != nil {
			e.log().Warn("fllex: falha a consultar estado da renovação", "err", err, "subscription", s.ID)
			continue
		}
		if !status.Paid {
			continue
		}
		if err := e.Confirm(ctx, s, s.Method, s.RenewalStatusRef, status.InvoiceURL); err != nil {
			e.log().Error("fllex: falha a confirmar renovação", "err", err, "subscription", s.ID)
			continue
		}
		n++
	}
	return n
}

// ProcessWarnings avisa quem está a chegar ao fim do ciclo.
//
// Quem paga sozinho recebe um lembrete informativo; quem tem de agir recebe um
// aviso de pagamento. A diferença importa: mandar alguém pagar quando o cartão
// vai ser cobrado automaticamente gera pagamentos em duplicado e chamadas para
// o suporte.
func (e *Engine) ProcessWarnings(ctx context.Context) int {
	now := e.now()
	before := now.AddDate(0, 0, e.cfg.WarnBeforeDays)
	subs, err := e.store.DueForWarning(ctx, before, e.cfg.BatchSize)
	if err != nil {
		e.log().Error("fllex: falha a obter avisos de renovação", "err", err)
		return 0
	}
	n := 0
	for _, s := range subs {
		renewsAt := now
		if s.CurrentPeriodEnd != nil {
			renewsAt = *s.CurrentPeriodEnd
		}
		if s.Recurring() {
			e.notify.RenewalReminder(ctx, s, renewsAt)
		} else {
			e.notify.RenewalWarning(ctx, s, e.cfg.Window.For(renewsAt).Closes)
		}
		s.RenewalWarnedAt = &now
		if s.RenewalState == RenewalNone {
			s.RenewalState = RenewalWarned
		}
		s.UpdatedAt = now
		if err := e.store.Save(ctx, s); err != nil {
			e.log().Error("fllex: falha a marcar aviso de renovação", "err", err, "subscription", s.ID)
			continue
		}
		n++
	}
	return n
}

// ProcessCharges emite a cobrança do ciclo seguinte a quem entrou na janela de
// renovação.
func (e *Engine) ProcessCharges(ctx context.Context) int {
	now := e.now()
	subs, err := e.store.DueForCharge(ctx, e.cfg.Window.Cutoff(now), e.cfg.BatchSize)
	if err != nil {
		e.log().Error("fllex: falha a obter renovações a cobrar", "err", err)
		return 0
	}
	n := 0
	for _, s := range subs {
		if _, err := e.IssueCharge(ctx, s, e.methodFor(s)); err != nil {
			if errors.Is(err, errNothingToCharge) {
				continue
			}
			e.log().Error("fllex: falha a emitir cobrança de renovação", "err", err, "subscription", s.ID)
			continue
		}
		n++
	}
	return n
}

var errNothingToCharge = errors.New("fllex: subscrição sem valor a cobrar")

// IssueCharge emite a cobrança de um ciclo e deixa a subscrição em renovação
// pendente até ao fecho da janela.
//
// É partilhada pela passagem automática e pela reemissão manual do backoffice,
// de propósito: uma cobrança reemitida à mão tem de ter exactamente os mesmos
// dados, prazos e avisos que uma automática, ou o cliente recebe duas coisas
// diferentes conforme quem carregou no botão.
func (e *Engine) IssueCharge(ctx context.Context, s *Subscription, method payment.Method) (Charge, error) {
	now := e.now()

	// O novo ciclo começa pelo preço de tabela: o desconto do primeiro ciclo não
	// recorre, e a cobrança tem de reflectir isso antes de ser calculada.
	s.ApplyRenewalPricing()

	// A cobrança é criada primeiro porque é ela que valida o valor. Enquanto a
	// validação estava aqui e no construtor, a segunda nunca chegava a correr, e
	// uma verificação que não corre não é uma verificação.
	pay, err := payment.NewPayment(e.newID(), s.ID, s.InstalmentAmount(), method)
	if err != nil {
		// Uma subscrição sem nada a cobrar não é uma avaria: é um plano gratuito
		// ou um contrato já pago por inteiro, e a passagem salta-o em silêncio.
		return Charge{}, fmt.Errorf("%w: %w", errNothingToCharge, err)
	}
	amount := pay.Amount

	periodEnd := s.CurrentPeriodEnd
	if periodEnd == nil {
		periodEnd = &now
	}
	window := e.cfg.Window.For(*periodEnd)
	due := window.Closes

	customer := payment.Customer{ID: s.CustomerID}
	if e.Customers != nil {
		if c, err := e.Customers.Customer(ctx, s); err == nil {
			customer = c
		} else {
			e.log().Warn("fllex: falha a obter dados do cliente", "err", err, "subscription", s.ID)
		}
	}

	link := ""
	if e.Links != nil {
		if l, err := e.Links.RenewalLink(ctx, s, due); err == nil {
			link = l
		} else {
			e.log().Warn("fllex: falha a gerar link de renovação", "err", err, "subscription", s.ID)
		}
	}

	nextEnd := s.NextPeriodEnd(now, false)
	pay.CustomerID = s.CustomerID
	pay.Description = s.PlanName
	pay.MandateID = s.MandateID
	pay.PeriodStart = periodEnd
	pay.PeriodEnd = &nextEnd
	// A validade cobre a janela inteira, e não as 24 horas de uma cobrança
	// avulsa: uma referência que morre no dia seguinte deixa o cliente com um
	// número que não pode pagar durante a tolerância que lhe foi prometida.
	pay.SetExpiry(due)

	req := pay.ToChargeRequest(customer)
	req.Description = s.PlanName
	req.SuccessURL = link
	req.Metadata = mergeMeta(s.Metadata, map[string]string{
		"subscription_id": s.ID,
		"cycle":           itoa(s.CycleNumber + 1),
	})

	result, providerName, err := e.registry.Charge(ctx, req)
	if err != nil {
		// Sem cobrança emitida, o cliente não pode ficar sem forma de pagar: a
		// subscrição fica na mesma em renovação pendente com o prazo marcado, e
		// o aviso segue com o link para o cliente escolher outro método.
		e.log().Error("fllex: falha a cobrar a renovação", "err", err, "subscription", s.ID, "method", method)
		s.RenewalState = RenewalPending
		s.RenewalDueAt = &due
		s.UpdatedAt = now
		if serr := e.store.Save(ctx, s); serr != nil {
			return Charge{}, serr
		}
		charge := Charge{Amount: amount, Method: method, DueAt: due, URL: link}
		e.notify.ChargeIssued(ctx, s, charge)
		return charge, err
	}

	pay.ApplyResult(providerName, result)
	if e.payments != nil {
		if err := e.payments.Create(ctx, pay); err != nil {
			return Charge{}, err
		}
	}

	s.Provider = providerName
	s.Method = method
	s.RenewalState = RenewalPending
	s.RenewalDueAt = &due
	s.RenewalPaymentID = pay.ID
	s.RenewalStatusRef = result.StatusRef
	s.PendingEntity = result.Entity
	s.PendingReference = result.Reference
	s.PendingDueDate = result.DueDate
	s.UpdatedAt = now

	// Uma cobrança já paga na própria chamada (Multicaixa Express, saldo) não
	// tem nada a aguardar: renova de imediato.
	if result.Status == payment.StatusApproved {
		if err := e.Confirm(ctx, s, method, result.ProviderRef, result.InvoiceURL); err != nil {
			return Charge{}, err
		}
		return Charge{
			PaymentID: pay.ID, Amount: amount, Method: method, DueAt: due,
			URL: link, Result: result,
		}, nil
	}

	if err := e.store.Save(ctx, s); err != nil {
		return Charge{}, err
	}

	charge := Charge{
		PaymentID: pay.ID,
		Amount:    amount,
		Method:    method,
		DueAt:     due,
		Entity:    result.Entity,
		Reference: result.Reference,
		DueDate:   result.DueDate,
		URL:       firstNonEmpty(result.URL, link),
		Result:    result,
	}
	e.notify.ChargeIssued(ctx, s, charge)
	return charge, nil
}

// Confirm dá o ciclo por pago e põe a subscrição em vigor.
func (e *Engine) Confirm(ctx context.Context, s *Subscription, method payment.Method, providerRef, invoiceURL string) error {
	now := e.now()

	if e.payments != nil && s.RenewalPaymentID != "" {
		if pay, err := e.payments.ByID(ctx, s.RenewalPaymentID); err == nil && pay != nil {
			if pay.Status == payment.StatusPending {
				// A referência que confirma a cobrança é a que ela própria
				// guarda. A que vem de fora pode ser a de consulta de estado,
				// que em vários gateways é legitimamente outra: no MoMenu, o
				// webhook fala em transactionId e a consulta em operationId.
				// Compará-las travava a aprovação de todas as referências.
				//
				// A aprovação não pode falhar aqui: a cobrança está pendente e a
				// referência é a que ela própria guarda, que são exactamente as
				// duas coisas que [payment.Payment.Approve] verifica. O erro é
				// descartado de propósito, e não por esquecimento.
				_ = pay.Approve(firstNonEmpty(pay.ProviderRef, providerRef))
				pay.InvoiceURL = firstNonEmpty(invoiceURL, pay.InvoiceURL)
				if err := e.payments.Update(ctx, pay); err != nil {
					e.log().Error("fllex: falha a marcar cobrança como paga", "err", err, "payment", pay.ID)
				}
			}
		}
	}

	if method != "" {
		s.Method = method
	}
	s.Activate(now)
	if err := e.store.Save(ctx, s); err != nil {
		return err
	}
	e.notify.Renewed(ctx, s)
	return nil
}

// ProcessRetries volta a apresentar as cobranças automáticas recusadas cuja
// janela ainda está aberta.
//
// Sem isto, uma recusa do banco é silêncio absoluto: a subscrição não é tocada
// e no ciclo seguinte cobra-se na mesma, e quem teve um problema pontual na
// conta perde a renovação sem perceber porquê.
func (e *Engine) ProcessRetries(ctx context.Context) int {
	now := e.now()
	subs, err := e.store.RetryDue(ctx, now, e.cfg.MaxAttempts, e.cfg.BatchSize)
	if err != nil {
		e.log().Error("fllex: falha a obter renovações a repetir", "err", err)
		return 0
	}
	n := 0
	for _, s := range subs {
		if s.RenewalDueAt != nil && now.After(*s.RenewalDueAt) {
			continue // a janela fechou: quem trata dela é a expiração
		}
		s.RenewalAttempts++
		if _, err := e.IssueCharge(ctx, s, e.methodFor(s)); err != nil {
			e.log().Warn("fllex: falha na repetição da cobrança", "err", err, "subscription", s.ID)
			continue
		}
		n++
	}
	return n
}

// ProcessDunning marca o prazo de cancelamento das subscrições cuja cobrança
// automática falhou, e envia o último aviso.
//
// O gateway vai repetindo a cobrança nos dias seguintes por sua conta. Este
// passo não interfere com isso: fixa a data a partir da qual desistimos, para
// que uma subscrição em falha de pagamento não fique presa nesse estado para
// sempre, a dar acesso a quem já ninguém consegue cobrar.
func (e *Engine) ProcessDunning(ctx context.Context) int {
	now := e.now()
	subs, err := e.store.PastDueWithoutDeadline(ctx, e.cfg.BatchSize)
	if err != nil {
		e.log().Error("fllex: falha a obter subscrições em falha de pagamento", "err", err)
		return 0
	}
	n := 0
	for _, s := range subs {
		// O prazo conta do fim do período pago, quando esse ainda está por
		// chegar, para o cliente não perder dias que já pagou.
		base := now
		if s.CurrentPeriodEnd != nil && s.CurrentPeriodEnd.After(now) {
			base = *s.CurrentPeriodEnd
		}
		due := cycle.EndOfDay(base.AddDate(0, 0, e.cfg.Window.GraceDays))
		s.MarkPastDue(due, now)
		if err := e.store.Save(ctx, s); err != nil {
			e.log().Error("fllex: falha a marcar prazo de cancelamento", "err", err, "subscription", s.ID)
			continue
		}
		e.notify.PaymentFailed(ctx, s, due, "")
		n++
	}
	return n
}

// ProcessExpiry termina as subscrições cuja janela fechou sem pagamento.
//
// A subscrição passa a expirada e não a cancelada: o contrato chegou ao fim e
// não foi renovado, o que não é a mesma coisa que alguém ter decidido terminar.
// A distinção importa para os relatórios e porque uma expirada volta a ficar
// activa se o pagamento aparecer mais tarde.
func (e *Engine) ProcessExpiry(ctx context.Context) int {
	now := e.now()
	subs, err := e.store.ExpiredWindows(ctx, now, e.cfg.BatchSize)
	if err != nil {
		e.log().Error("fllex: falha a obter janelas expiradas", "err", err)
		return 0
	}
	n := 0
	for _, s := range subs {
		e.cancelPendingCharge(ctx, s)
		s.Expire(now)
		if err := e.store.Save(ctx, s); err != nil {
			e.log().Error("fllex: falha a expirar subscrição", "err", err, "subscription", s.ID)
			continue
		}
		e.notify.Expired(ctx, s)
		n++
	}
	return n
}

// ProcessStaleCharges limpa as cobranças pendentes de subscrições já canceladas
// cujo prazo passou. Não há aviso: a subscrição já estava cancelada.
func (e *Engine) ProcessStaleCharges(ctx context.Context) int {
	now := e.now()
	subs, err := e.store.StaleCharges(ctx, now, e.cfg.BatchSize)
	if err != nil {
		e.log().Error("fllex: falha a obter cobranças obsoletas", "err", err)
		return 0
	}
	n := 0
	for _, s := range subs {
		e.cancelPendingCharge(ctx, s)
		s.ResetRenewal()
		if err := e.store.Save(ctx, s); err != nil {
			e.log().Error("fllex: falha a limpar cobrança obsoleta", "err", err, "subscription", s.ID)
			continue
		}
		n++
	}
	return n
}

// cancelPendingCharge revoga no gateway a cobrança que ficou por pagar.
//
// Sem isto, a referência de uma subscrição já expirada continua viva no ATM: o
// cliente paga por algo que já não tem, e alguém tem de lhe devolver o dinheiro
// à mão.
func (e *Engine) cancelPendingCharge(ctx context.Context, s *Subscription) {
	if e.payments == nil || s.RenewalPaymentID == "" {
		return
	}
	pay, err := e.payments.ByID(ctx, s.RenewalPaymentID)
	if err != nil || pay == nil || pay.Status != payment.StatusPending {
		return
	}
	if provider, ok := e.registry.Get(pay.Provider); ok {
		if canceller, ok := provider.(payment.Canceller); ok {
			result := payment.ChargeResult{
				ProviderRef: pay.ProviderRef,
				Reference:   pay.Reference,
				Entity:      pay.Entity,
				ExternalID:  pay.ExternalID,
			}
			if err := canceller.CancelCharge(ctx, pay.ToChargeRequest(payment.Customer{ID: pay.CustomerID}), result); err != nil {
				e.log().Warn("fllex: falha a revogar cobrança no gateway", "err", err, "payment", pay.ID)
			}
		}
	}
	if err := pay.Expire(); err == nil {
		if err := e.payments.Update(ctx, pay); err != nil {
			e.log().Error("fllex: falha a expirar cobrança", "err", err, "payment", pay.ID)
		}
	}
}

// HandleEvent aplica a uma subscrição um evento já normalizado de um gateway.
//
// Devolve a subscrição alterada e se houve alteração, para quem chama decidir o
// que fazer a seguir (emitir factura, notificar, aplicar limites de plano).
func (e *Engine) HandleEvent(ctx context.Context, s *Subscription, ev *payment.Event) (bool, error) {
	if s == nil || ev.Ignorable() {
		return false, nil
	}
	now := e.now()

	switch ev.Type {
	case payment.EventSubscriptionActive, payment.EventChargeSucceeded:
		if ev.SubscriptionRef != "" {
			s.SubscriptionRef = ev.SubscriptionRef
		}
		if ev.CustomerRef != "" {
			s.CustomerRef = ev.CustomerRef
		}
		if err := e.Confirm(ctx, s, s.Method, ev.ChargeRef, ev.InvoiceURL); err != nil {
			return false, err
		}
		return true, nil

	case payment.EventInvoicePaid:
		// A factura da criação da subscrição já foi tratada pelo evento do
		// checkout. Só a do ciclo é que renova, e sem esta distinção emite-se
		// factura a dobrar no primeiro ciclo.
		if !ev.BillingCycle() {
			return false, nil
		}
		if ev.PeriodEnd != nil {
			s.CurrentPeriodEnd = ev.PeriodEnd
			s.CycleNumber++
			s.RenewalWarnedAt = nil // ciclo novo, aviso novo
		}
		s.Status = StatusActive
		s.ResetRenewal()
		s.UpdatedAt = now
		if err := e.store.Save(ctx, s); err != nil {
			return false, err
		}
		e.notify.Renewed(ctx, s)
		return true, nil

	case payment.EventChargeFailed:
		// O prazo fica para o passo de dunning, que o calcula a partir do fim do
		// período pago. Aqui só se regista a falha.
		s.Status = StatusPastDue
		s.RenewalState = RenewalFailed
		s.RenewalAttempts++
		s.UpdatedAt = now
		if err := e.store.Save(ctx, s); err != nil {
			return false, err
		}
		return true, nil

	case payment.EventSubscriptionUpdated:
		changed := false
		if ev.CurrentPeriodEnd != nil {
			s.CurrentPeriodEnd = ev.CurrentPeriodEnd
			changed = true
		}
		if ev.CancelAtPeriodEnd != s.CancelAtPeriodEnd {
			s.CancelAtPeriodEnd = ev.CancelAtPeriodEnd
			changed = true
		}
		if ev.Status == payment.StatusApproved && s.Status != StatusActive {
			s.Status = StatusActive
			changed = true
		}
		if !changed {
			return false, nil
		}
		s.UpdatedAt = now
		return true, e.store.Save(ctx, s)

	case payment.EventSubscriptionCancelled:
		s.Cancel(false, now)
		return true, e.store.Save(ctx, s)

	default:
		return false, nil
	}
}

// methodFor decide por onde cobrar este ciclo.
//
// Decide-se uma vez e usa-se em toda a parte: a criar a cobrança, a escolher o
// gateway e a dizer ao cliente o que vai acontecer. Enquanto cada um destes
// decidia por si, um método legado ganhava uma cobrança por referência sem
// referência nenhuma gerada, e o aviso saía a mandar pagar um número que não
// existia.
//
// Um débito directo sem mandato não se pode cobrar por débito directo, e cai no
// método de recurso: cobra-se na mesma, só por outra via.
func (e *Engine) methodFor(s *Subscription) payment.Method {
	m := s.Method
	if m == payment.MethodDirectDebit && s.MandateID == "" {
		return e.cfg.FallbackMethod
	}
	if !m.Valid() {
		return e.cfg.FallbackMethod
	}
	return m
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

func (e *Engine) newID() string {
	if e.IDs != nil {
		return e.IDs()
	}
	return ""
}

func (e *Engine) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func mergeMeta(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
