package proxypaydds

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/mandate"
	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
	"github.com/spolly-ao/fllex/phone"
)

// Provider implementa [payment.Provider] sobre os débitos directos do Proxypay.
type Provider struct {
	client   *Client
	resolver mandate.Resolver
	// DefaultPurpose acompanha as cobranças que não trazem finalidade própria.
	DefaultPurpose string
	// DefaultRecurrence acompanha os mandatos que não trazem periodicidade.
	DefaultRecurrence string
	// ActivationTTL é o prazo que se dá ao titular para activar o mandato. Zero
	// deixa o mandato sem prazo.
	ActivationTTL time.Duration
}

// New cria o provider de débito directo.
//
// O resolver é o que liga o identificador de mandato de quem chama ao número
// que o Proxypay conhece. Sem ele, este provider não consegue cobrar coisa
// nenhuma: use [mandate.NewStoreResolver] com o seu armazenamento.
func New(cfg Config, resolver mandate.Resolver) *Provider {
	return &Provider{
		client:            NewClient(cfg),
		resolver:          resolver,
		DefaultPurpose:    PurposeCash,
		DefaultRecurrence: RecurrenceMonthly,
		ActivationTTL:     30 * 24 * time.Hour,
	}
}

// NewWithClient cria o provider sobre um cliente já construído.
func NewWithClient(c *Client, resolver mandate.Resolver) *Provider {
	return &Provider{
		client: c, resolver: resolver,
		DefaultPurpose:    PurposeCash,
		DefaultRecurrence: RecurrenceMonthly,
		ActivationTTL:     30 * 24 * time.Hour,
	}
}

// Client dá acesso ao cliente da API, para o fluxo de eventos e os mandatos.
func (p *Provider) Client() *Client { return p.client }

// Name devolve "proxypay-dds".
func (p *Provider) Name() string { return "proxypay-dds" }

// Methods: só débito directo.
func (p *Provider) Methods() []payment.Method {
	return []payment.Method{payment.MethodDirectDebit}
}

// SupportsCurrency: só kwanza.
func (p *Provider) SupportsCurrency(c money.Currency) bool {
	return money.NormalizeCurrency(string(c)) == money.AOA
}

// Configured indica se há chave, entidade e resolvedor de mandatos.
func (p *Provider) Configured() bool { return p.client.Configured() && p.resolver != nil }

// Charge apresenta uma cobrança contra o mandato indicado.
//
// Um mandato ainda não activado devolve [payment.ErrMandateNotActive], que não
// é uma falha do sistema nem do gateway: é o titular que ainda não foi ao
// banco. Quem chama deve distingui-lo de um erro a sério e não gastar aí uma
// tentativa de retentativa.
func (p *Provider) Charge(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	if !p.Configured() {
		return payment.ChargeResult{}, payment.ErrNotConfigured
	}
	if req.Method != "" && req.Method != payment.MethodDirectDebit {
		return payment.ChargeResult{}, payment.ErrUnsupportedMethod
	}
	if !p.SupportsCurrency(req.Amount.Currency) {
		return payment.ChargeResult{}, payment.ErrUnsupportedCurrency
	}
	if !req.Amount.IsPositive() {
		return payment.ChargeResult{}, payment.ErrAmountNotPositive
	}
	if strings.TrimSpace(req.MandateID) == "" {
		return payment.ChargeResult{}, payment.ErrMandateRequired
	}

	externalMandateID, active, err := p.resolver.Resolve(ctx, req.MandateID)
	if err != nil {
		return payment.ChargeResult{}, fmt.Errorf("proxypay-dds: resolver mandato: %w", err)
	}
	if externalMandateID == 0 {
		return payment.ChargeResult{}, payment.ErrMandateRequired
	}
	if !active {
		return payment.ChargeResult{}, payment.ErrMandateNotActive
	}

	paymentID, err := p.client.NextPaymentID(ctx, externalMandateID)
	if err != nil {
		return payment.ChargeResult{}, fmt.Errorf("proxypay-dds: sequência de cobrança: %w", err)
	}

	// O identificador de transacção é o que correlaciona o evento de volta a
	// esta cobrança. É construído a partir dos dois números do gateway para ser
	// estável e reconstruível, sem depender de outra consulta.
	txID := fmt.Sprintf("PAY-%d-%d", externalMandateID, paymentID)

	// A data de liquidação é o início do período que esta cobrança paga; sem
	// ele, amanhã. Cobrar hoje um período que só começa no mês que vem é pedir
	// dinheiro adiantado sem o cliente ter combinado isso.
	collection := time.Now().AddDate(0, 0, 1)
	if req.PeriodStart != nil {
		collection = *req.PeriodStart
	}

	purpose := p.DefaultPurpose
	if purpose == "" {
		purpose = PurposeCash
	}

	res, err := p.client.PresentPayment(ctx, externalMandateID, PresentPaymentRequest{
		ID:             paymentID,
		TransactionID:  txID,
		Amount:         req.Amount.Decimal(),
		CollectionDate: collection.Format("2006-01-02"),
		Purpose:        purpose,
	})
	if err != nil {
		return payment.ChargeResult{}, err
	}

	return payment.ChargeResult{
		// A instrução foi aceite pelo gateway, mas o dinheiro só sai na data de
		// liquidação e o banco ainda pode recusar. Fica pendente até o evento
		// PaymentCollected chegar.
		Kind:        payment.KindPending,
		Status:      payment.StatusPending,
		ProviderRef: res.TransactionID,
		StatusRef:   res.TransactionID,
		ExternalID:  paymentID,
	}, nil
}

// CancelCharge cancela uma cobrança antes da data de liquidação.
func (p *Provider) CancelCharge(ctx context.Context, req payment.ChargeRequest, res payment.ChargeResult) error {
	if !p.Configured() {
		return payment.ErrNotConfigured
	}
	if res.ExternalID == 0 || strings.TrimSpace(req.MandateID) == "" {
		return nil
	}
	externalMandateID, _, err := p.resolver.Resolve(ctx, req.MandateID)
	if err != nil {
		return fmt.Errorf("proxypay-dds: resolver mandato: %w", err)
	}
	if externalMandateID == 0 {
		return nil
	}
	_, err = p.client.CancelPayment(ctx, externalMandateID, res.ExternalID, CancelPaymentRequest{
		CancelationID: fmt.Sprintf("CXL-%d-%d", externalMandateID, res.ExternalID),
		Reason:        CancelReasonCustomerRequest,
	})
	return err
}

// Refund reverte uma cobrança já liquidada.
func (p *Provider) Refund(ctx context.Context, r payment.Refund) (payment.RefundResult, error) {
	if !p.Configured() {
		return payment.RefundResult{}, payment.ErrNotConfigured
	}
	mandateID, paymentID, ok := parseTransactionID(r.ChargeRef)
	if !ok {
		return payment.RefundResult{}, fmt.Errorf("proxypay-dds: referência de cobrança inválida: %q", r.ChargeRef)
	}
	reason := r.Reason
	if reason == "" {
		reason = ReversalReasonRefusedDebtor
	}
	res, err := p.client.ReversePayment(ctx, mandateID, paymentID, ReversePaymentRequest{
		Amount:     r.Amount.Decimal(),
		Reason:     reason,
		ReversalID: fmt.Sprintf("REV-%d-%d", mandateID, paymentID),
	})
	if err != nil {
		return payment.RefundResult{}, err
	}
	amount := r.Amount
	if parsed, perr := money.Parse(res.Amount, money.AOA); perr == nil {
		amount = parsed
	}
	return payment.RefundResult{
		RefundRef: res.ReversalID,
		Status:    payment.StatusPending, // liquida-se no ciclo do banco
		Amount:    amount,
	}, nil
}

// --- mandatos ----------------------------------------------------------------

// RegisterRequest são os dados para criar um mandato.
type RegisterRequest struct {
	// SubjectID e CustomerID são os identificadores do lado de quem chama.
	SubjectID  string
	CustomerID string
	// ContractID aparece no extracto do titular; use algo que ele reconheça.
	ContractID string
	// Type distingue auto-activado de pré-autorizado. Vazio usa o auto-activado,
	// que é o que não precisa de papel.
	Type mandate.Type
	// Dados do titular.
	DebtorName string
	TaxID      string
	Email      string
	Phone      string
	DebitIBAN  string // só nos pré-autorizados
	// Recurrence e Purpose são os códigos declarados ao banco.
	Recurrence string
	Purpose    string
	// MaxAmount é o tecto por cobrança. Defina-o com folga sobre a prestação:
	// um aumento de preço acima do tecto passa a ser recusado pelo banco sem
	// aviso nenhum.
	MaxAmount money.Amount
	// FirstCollection e FinalCollection delimitam quando se pode cobrar.
	FirstCollection *time.Time
	FinalCollection *time.Time
	// SignatureDate e ImageID dizem respeito ao formulário assinado (CAP).
	SignatureDate string
	ImageID       string
}

// RegisterMandate cria o mandato no gateway e devolve-o já preenchido, pronto a
// persistir.
//
// Nos auto-activados, o passo seguinte é humano: entregue ao titular o
// identificador da entidade ([Client.EntityID]) e o código de activação
// ([ActivationCode]), e espere pelo evento de activação.
func (p *Provider) RegisterMandate(ctx context.Context, req RegisterRequest) (*mandate.Mandate, error) {
	if !p.client.Configured() {
		return nil, payment.ErrNotConfigured
	}

	externalID, err := p.client.NextMandateID(ctx)
	if err != nil {
		return nil, fmt.Errorf("proxypay-dds: sequência de mandato: %w", err)
	}

	recurrence := firstNonEmpty(req.Recurrence, p.DefaultRecurrence, RecurrenceMonthly)
	purpose := firstNonEmpty(req.Purpose, p.DefaultPurpose, PurposeCash)
	mobile := phone.FormatAODDS(req.Phone)

	maxAmount := ""
	if req.MaxAmount.IsPositive() {
		maxAmount = req.MaxAmount.Decimal()
	}

	typ := req.Type
	if typ == "" {
		typ = mandate.TypeSelfActivated
	}

	var resp *MandateResponse
	if typ == mandate.TypePreAuthorized {
		resp, err = p.client.RegisterCAPMandate(ctx, CAPMandateRequest{
			ID:                  externalID,
			ContractID:          req.ContractID,
			DebitIBAN:           req.DebitIBAN,
			DebitorName:         req.DebtorName,
			TaxID:               req.TaxID,
			SignatureDate:       req.SignatureDate,
			ImageID:             req.ImageID,
			Recurrence:          recurrence,
			Purpose:             purpose,
			Email:               req.Email,
			Mobile:              mobile,
			MaxAmount:           maxAmount,
			FirstCollectionDate: formatDate(req.FirstCollection),
			FinalCollectionDate: formatDate(req.FinalCollection),
		})
	} else {
		resp, err = p.client.RegisterSAPMandate(ctx, SAPMandateRequest{
			ID:                  externalID,
			ContractID:          req.ContractID,
			DebitorName:         req.DebtorName,
			TaxID:               req.TaxID,
			Recurrence:          recurrence,
			Purpose:             purpose,
			Email:               req.Email,
			Mobile:              mobile,
			MaxAmount:           maxAmount,
			FirstCollectionDate: formatDate(req.FirstCollection),
			FinalCollectionDate: formatDate(req.FinalCollection),
		})
	}
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	m := &mandate.Mandate{
		SubjectID:     req.SubjectID,
		CustomerID:    req.CustomerID,
		ExternalID:    resp.ID,
		ContractID:    req.ContractID,
		Provider:      p.Name(),
		Type:          typ,
		Status:        mandate.StatusSubmitted,
		DebtorName:    req.DebtorName,
		TaxID:         req.TaxID,
		Email:         req.Email,
		Phone:         req.Phone,
		DebitIBAN:     firstNonEmpty(resp.DebitIBAN, req.DebitIBAN),
		CreditIBAN:    firstNonEmpty(resp.CreditIBAN, p.client.CreditIBAN()),
		Recurrence:    recurrence,
		Purpose:       purpose,
		MaxAmount:     maxAmount,
		SignatureDate: req.SignatureDate,
		ImageID:       req.ImageID,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if p.ActivationTTL > 0 {
		exp := now.Add(p.ActivationTTL)
		m.ExpiresAt = &exp
	}
	return m, nil
}

// ActivationInstructions devolve o que o titular precisa de introduzir no seu
// banco para activar o mandato.
func (p *Provider) ActivationInstructions(m *mandate.Mandate) (entityID, activationCode string) {
	if m == nil {
		return p.client.EntityID(), ""
	}
	return p.client.EntityID(), ActivationCode(m.ExternalID)
}

// CancelMandate cancela um mandato no gateway.
func (p *Provider) CancelMandate(ctx context.Context, m *mandate.Mandate, reason string) error {
	if !p.client.Configured() {
		return payment.ErrNotConfigured
	}
	if m == nil || m.ExternalID == 0 {
		return nil
	}
	if reason == "" {
		reason = CancelReasonCustomerRequest
	}
	_, err := p.client.CancelMandate(ctx, m.ExternalID, CancelMandateRequest{Reason: reason})
	return err
}

// --- eventos -----------------------------------------------------------------

// TranslateEvent traduz um evento do fluxo do Proxypay para o vocabulário
// comum. Devolve o tipo [payment.EventNone] nos eventos intermédios, que
// descrevem o processamento interno e não exigem nada de nós.
func (p *Provider) TranslateEvent(ev Event) *payment.Event {
	out := &payment.Event{
		ID:         ev.ID,
		Provider:   p.Name(),
		Type:       payment.EventNone,
		Method:     payment.MethodDirectDebit,
		MandateRef: fmt.Sprint(ev.Data.MandateID),
		Reason:     ev.Data.Reason,
	}
	if ev.Data.TransactionID != "" {
		out.ChargeRef = ev.Data.TransactionID
	}
	if ev.Data.ContractID != "" {
		out.Reference = ev.Data.ContractID
	}
	if t, ok := parseDateTime(ev.Data.Datetime); ok {
		out.OccurredAt = &t
	}
	if ev.Data.Amount != "" {
		if amt, err := money.Parse(ev.Data.Amount, money.AOA); err == nil {
			out.Amount = &amt
		}
	}

	switch ev.Type {
	case EventMandateActivated:
		out.Type = payment.EventMandateActivated
	case EventMandateRejected, EventRegisterMandateRejected:
		out.Type = payment.EventMandateRejected
	case EventMandateCanceled:
		out.Type = payment.EventMandateCancelled
	case EventPaymentCollected:
		out.Type = payment.EventChargeSucceeded
		out.Status = payment.StatusApproved
	case EventPaymentRejected:
		out.Type = payment.EventChargeFailed
		out.Status = payment.StatusRejected
	case EventPaymentCanceled, EventPaymentRevoked:
		out.Type = payment.EventChargeCancelled
		out.Status = payment.StatusCancelled
	case EventPaymentReversed:
		out.Type = payment.EventChargeRefunded
		out.Status = payment.StatusRefunded
	}
	return out
}

// BankWillRetry indica que o banco vai reapresentar a cobrança recusada por sua
// conta.
//
// Quando é verdade, não reapresente: seriam duas cobranças pela mesma coisa, e
// o cliente veria o dobro debitado se ambas passassem.
func BankWillRetry(ev Event) bool {
	if ev.Data.Retry != nil && *ev.Data.Retry {
		return true
	}
	return ev.Data.Reason == PayRejectInsufficientRetry
}

// --- auxiliares ---------------------------------------------------------------

// parseTransactionID desmonta o identificador construído em Charge.
func parseTransactionID(s string) (mandateID, paymentID int, ok bool) {
	parts := strings.Split(strings.TrimSpace(s), "-")
	if len(parts) != 3 || parts[0] != "PAY" {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &mandateID); err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(parts[2], "%d", &paymentID); err != nil {
		return 0, 0, false
	}
	return mandateID, paymentID, true
}

func formatDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

func parseDateTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
