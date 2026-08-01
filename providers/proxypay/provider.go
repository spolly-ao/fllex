package proxypay

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// defaultReferenceTTL é a validade de uma referência avulsa.
const defaultReferenceTTL = 24 * time.Hour

// Provider implementa [payment.Provider] sobre o Proxypay.
type Provider struct {
	client *Client
	// ReferenceField é o nome do campo personalizado onde vai a nossa
	// referência. É por ele que a confirmação encontra a cobrança, tanto pelo
	// webhook como pela fila de pagamentos.
	ReferenceField string
	// TTL é a validade das referências que não trazem prazo definido.
	TTL time.Duration
}

// New cria o provider Proxypay.
func New(cfg Config) *Provider {
	return &Provider{client: NewClient(cfg), ReferenceField: "invoice", TTL: defaultReferenceTTL}
}

// NewWithClient cria o provider sobre um cliente já construído.
func NewWithClient(c *Client) *Provider {
	return &Provider{client: c, ReferenceField: "invoice", TTL: defaultReferenceTTL}
}

// Client dá acesso ao cliente da API, para a fila de pagamentos confirmados.
func (p *Provider) Client() *Client { return p.client }

// Name devolve "proxypay".
func (p *Provider) Name() string { return "proxypay" }

// Methods: o Proxypay cobra por referência.
func (p *Provider) Methods() []payment.Method {
	return []payment.Method{payment.MethodReference}
}

// SupportsCurrency: só kwanza.
func (p *Provider) SupportsCurrency(c money.Currency) bool {
	return money.NormalizeCurrency(string(c)) == money.AOA
}

// Configured indica se há chave de API.
func (p *Provider) Configured() bool { return p.client.Configured() }

// Charge gera uma referência ATM e associa-lhe o valor e a validade.
func (p *Provider) Charge(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	if !p.Configured() {
		return payment.ChargeResult{}, payment.ErrNotConfigured
	}
	if req.Method != "" && req.Method != payment.MethodReference {
		return payment.ChargeResult{}, payment.ErrUnsupportedMethod
	}
	if !p.SupportsCurrency(req.Amount.Currency) {
		return payment.ChargeResult{}, payment.ErrUnsupportedCurrency
	}
	if !req.Amount.IsPositive() {
		return payment.ChargeResult{}, payment.ErrAmountNotPositive
	}

	expires := time.Now().UTC().Add(p.ttl())
	if req.ExpiresAt != nil {
		expires = *req.ExpiresAt
	}

	ref, err := p.client.GenerateReference(ctx)
	if err != nil {
		return payment.ChargeResult{}, err
	}

	fields := map[string]string{}
	for k, v := range req.Metadata {
		fields[k] = v
	}
	if req.Reference != "" {
		fields[p.ReferenceField] = req.Reference
	}

	if err := p.client.UpdateReference(ctx, ref, req.Amount.Decimal(), expires, fields); err != nil {
		// A referência foi emitida mas ficou sem valor associado: no ATM
		// apareceria como inválida. Apaga-se para não deixar um número morto no
		// sistema do Proxypay.
		_ = p.client.DeleteReference(context.WithoutCancel(ctx), ref)
		return payment.ChargeResult{}, err
	}

	return payment.ChargeResult{
		Kind:        payment.KindReference,
		Status:      payment.StatusPending,
		ProviderRef: ref,
		StatusRef:   ref,
		Entity:      p.client.Entity(),
		Reference:   ref,
		DueDate:     expires.Format("2006-01-02"),
		ExpiresAt:   &expires,
	}, nil
}

// CancelCharge invalida a referência.
func (p *Provider) CancelCharge(ctx context.Context, _ payment.ChargeRequest, res payment.ChargeResult) error {
	if !p.Configured() {
		return payment.ErrNotConfigured
	}
	ref := strings.TrimSpace(res.Reference)
	if ref == "" {
		ref = strings.TrimSpace(res.ProviderRef)
	}
	if ref == "" {
		return nil
	}
	return p.client.DeleteReference(ctx, ref)
}

// VerifyCharge procura a referência na fila de pagamentos confirmados.
//
// O Proxypay não expõe a consulta do estado de uma referência: o que expõe é a
// fila de pagamentos por confirmar. Procurar aqui é, na prática, a mesma coisa,
// com a diferença de que encontrar não retira o pagamento da fila. Quem o
// processar deve chamar [Client.AcknowledgePayment] depois de gravar, e é por
// isso que o caminho recomendado é o [Reconciler], que faz as duas coisas pela
// ordem certa.
func (p *Provider) VerifyCharge(ctx context.Context, statusRef, _ string) (payment.ChargeStatus, error) {
	if !p.Configured() {
		return payment.ChargeStatus{}, payment.ErrNotConfigured
	}
	if strings.TrimSpace(statusRef) == "" {
		return payment.ChargeStatus{}, nil
	}
	payments, err := p.client.ListConfirmedPayments(ctx)
	if err != nil {
		return payment.ChargeStatus{}, err
	}
	for _, pay := range payments {
		if pay.ReferenceID != statusRef {
			continue
		}
		st := payment.ChargeStatus{Status: payment.StatusApproved, Paid: true}
		if t, ok := pay.PaidAt(); ok {
			st.PaidAt = &t
		}
		if amt, err := money.Parse(pay.Amount, money.AOA); err == nil {
			st.Amount = &amt
		}
		return st, nil
	}
	return payment.ChargeStatus{Status: payment.StatusPending}, nil
}

// ParseWebhook lê a confirmação que o Proxypay envia para a URL de callback.
//
// O corpo não vem assinado: valide o pedido pela URL secreta ou por um segredo
// partilhado antes de chegar aqui, e trate o evento como um aviso de que vale a
// pena ir à fila de pagamentos confirmar.
func (p *Provider) ParseWebhook(payload []byte, _ string) (*payment.Event, error) {
	var body struct {
		ID           int64          `json:"id"`
		ReferenceID  string         `json:"reference_id"`
		Amount       string         `json:"amount"`
		CustomFields map[string]any `json:"custom_fields"`
		Datetime     string         `json:"datetime"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return &payment.Event{Type: payment.EventNone, Provider: p.Name()}, nil
	}
	if body.ReferenceID == "" {
		return &payment.Event{Type: payment.EventNone, Provider: p.Name()}, nil
	}
	cp := ConfirmedPayment{
		ID: body.ID, ReferenceID: body.ReferenceID, Amount: body.Amount,
		CustomFields: body.CustomFields, Datetime: body.Datetime,
	}
	ev := &payment.Event{
		Provider:  p.Name(),
		Type:      payment.EventChargeSucceeded,
		Status:    payment.StatusApproved,
		Method:    payment.MethodReference,
		ChargeRef: body.ReferenceID,
		Reference: cp.Field(p.ReferenceField),
	}
	if t, ok := cp.PaidAt(); ok {
		ev.OccurredAt = &t
	}
	if amt, err := money.Parse(body.Amount, money.AOA); err == nil {
		ev.Amount = &amt
	}
	return ev, nil
}

func (p *Provider) ttl() time.Duration {
	if p.TTL > 0 {
		return p.TTL
	}
	return defaultReferenceTTL
}
