package stripe

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/cycle"
	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// Provider implementa [payment.Provider] sobre o Stripe.
type Provider struct {
	client *Client
	// ReferenceKey é o nome do campo de metadados onde vai a nossa referência.
	// É configurável para que uma integração que já escreveu eventos com outro
	// nome ("orgId", "order_id") continue a conseguir lê-los: os eventos
	// antigos ficam no Stripe com o nome que tinham na altura.
	ReferenceKey string
	// PlanKey e IntervalKey são os metadados do plano e da periodicidade.
	PlanKey     string
	IntervalKey string
}

// New cria o provider Stripe.
func New(cfg Config) *Provider {
	return &Provider{
		client:       NewClient(cfg),
		ReferenceKey: "reference",
		PlanKey:      "planId",
		IntervalKey:  "interval",
	}
}

// NewWithClient cria o provider sobre um cliente já construído.
func NewWithClient(c *Client) *Provider {
	return &Provider{client: c, ReferenceKey: "reference", PlanKey: "planId", IntervalKey: "interval"}
}

// Client dá acesso ao cliente da API.
func (p *Provider) Client() *Client { return p.client }

// Name devolve "stripe".
func (p *Provider) Name() string { return "stripe" }

// Methods: o Stripe entra aqui como o método de cartão.
func (p *Provider) Methods() []payment.Method {
	return []payment.Method{payment.MethodCard}
}

// SupportsCurrency: o Stripe processa todas as moedas que usamos, incluindo o
// kwanza. Quem quiser que o kwanza vá por um gateway local resolve-o pela ordem
// de registo, pondo esse gateway antes do Stripe.
func (p *Provider) SupportsCurrency(money.Currency) bool { return true }

// Configured indica se há chave secreta.
func (p *Provider) Configured() bool { return p.client.Configured() }

// Charge cria uma sessão de checkout e devolve a URL para onde encaminhar o
// cliente.
//
// O preço vai em linha (price_data) em vez de um preço pré-criado no Stripe:
// evita ter de sincronizar um catálogo de produtos e preços, e deixa passar o
// preço regional já resolvido do nosso lado.
func (p *Provider) Charge(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	if !p.Configured() {
		return payment.ChargeResult{}, payment.ErrNotConfigured
	}
	if req.Method != "" && req.Method != payment.MethodCard {
		return payment.ChargeResult{}, payment.ErrUnsupportedMethod
	}
	if !req.Amount.IsPositive() {
		return payment.ChargeResult{}, payment.ErrAmountNotPositive
	}

	form := url.Values{}
	form.Set("success_url", req.SuccessURL)
	form.Set("cancel_url", req.CancelURL)
	if req.Reference != "" {
		form.Set("client_reference_id", req.Reference)
		form.Set("metadata["+p.ReferenceKey+"]", req.Reference)
	}
	if req.Customer.ProviderRef != "" {
		form.Set("customer", req.Customer.ProviderRef)
	} else if req.Customer.Email != "" {
		form.Set("customer_email", req.Customer.Email)
	}
	for k, v := range req.Metadata {
		form.Set("metadata["+k+"]", v)
	}

	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", strings.ToLower(req.Amount.Currency.String()))
	form.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(req.Amount.Minor, 10))
	form.Set("line_items[0][price_data][product_data][name]", descriptionOf(req))

	if req.Mode == payment.ModeSubscription {
		unit, count := cycle.ParseInterval(req.Interval).StripeInterval()
		form.Set("mode", "subscription")
		form.Set("line_items[0][price_data][recurring][interval]", unit)
		form.Set("line_items[0][price_data][recurring][interval_count]", strconv.Itoa(count))
		form.Set("metadata["+p.IntervalKey+"]", req.Interval)
		// Os metadados são repetidos na subscrição para que os webhooks
		// seguintes (renovações, falhas de pagamento) continuem a saber a quem
		// dizem respeito. Sem isto, só o primeiro evento é correlacionável.
		if req.Reference != "" {
			form.Set("subscription_data[metadata]["+p.ReferenceKey+"]", req.Reference)
		}
		form.Set("subscription_data[metadata]["+p.IntervalKey+"]", req.Interval)
		for k, v := range req.Metadata {
			form.Set("subscription_data[metadata]["+k+"]", v)
		}
	} else {
		form.Set("mode", "payment")
	}

	sess, err := p.client.CreateCheckoutSession(ctx, form)
	if err != nil {
		return payment.ChargeResult{}, err
	}
	out := payment.ChargeResult{
		Kind:        payment.KindRedirect,
		Status:      payment.StatusPending,
		URL:         sess.URL,
		ProviderRef: sess.ID,
		StatusRef:   sess.ID,
		CustomerRef: sess.Customer,
	}
	if sess.ExpiresAt > 0 {
		t := time.Unix(sess.ExpiresAt, 0).UTC()
		out.ExpiresAt = &t
	}
	return out, nil
}

// ReuseOrCharge devolve a sessão anterior quando ela ainda está aberta, e só
// cria outra quando não está.
//
// Evita a sessão duplicada de quem carrega duas vezes no botão de pagar: sem
// isto ficam duas sessões abertas para a mesma encomenda, uma delas nunca é
// reconciliada, e o painel do Stripe enche-se de checkouts abandonados que na
// verdade foram pagos noutro sítio.
func (p *Provider) ReuseOrCharge(ctx context.Context, existingSessionID string, req payment.ChargeRequest) (payment.ChargeResult, error) {
	if strings.TrimSpace(existingSessionID) != "" {
		if sess, err := p.client.GetCheckoutSession(ctx, existingSessionID); err == nil {
			if sess.Paid() {
				return payment.ChargeResult{
					Kind:            payment.KindPaid,
					Status:          payment.StatusApproved,
					ProviderRef:     sess.ID,
					CustomerRef:     sess.Customer,
					SubscriptionRef: sess.Subscription,
				}, nil
			}
			if sess.Reusable(time.Now()) {
				return payment.ChargeResult{
					Kind:        payment.KindRedirect,
					Status:      payment.StatusPending,
					URL:         sess.URL,
					ProviderRef: sess.ID,
					StatusRef:   sess.ID,
					CustomerRef: sess.Customer,
				}, nil
			}
		}
	}
	return p.Charge(ctx, req)
}

// VerifyCharge consulta o estado de uma sessão de checkout.
//
// O Stripe assina e reentrega os webhooks, por isso a consulta não é o caminho
// normal de confirmação. Serve para o cliente que volta da página de pagamento
// antes de o webhook chegar, e para reconciliar depois de uma paragem.
func (p *Provider) VerifyCharge(ctx context.Context, statusRef, _ string) (payment.ChargeStatus, error) {
	if !p.Configured() {
		return payment.ChargeStatus{}, payment.ErrNotConfigured
	}
	if strings.TrimSpace(statusRef) == "" {
		return payment.ChargeStatus{}, nil
	}
	sess, err := p.client.GetCheckoutSession(ctx, statusRef)
	if err != nil {
		return payment.ChargeStatus{}, err
	}
	st := payment.ChargeStatus{Status: payment.StatusPending}
	switch {
	case sess.Paid():
		st.Status = payment.StatusApproved
		st.Paid = true
		now := time.Now().UTC()
		st.PaidAt = &now
	case sess.Status == "expired":
		st.Status = payment.StatusExpired
	}
	return st, nil
}

// CancelSubscription cancela a subscrição no Stripe.
func (p *Provider) CancelSubscription(ctx context.Context, subscriptionRef string, atPeriodEnd bool) error {
	if !p.Configured() {
		return payment.ErrNotConfigured
	}
	return p.client.CancelSubscription(ctx, subscriptionRef, atPeriodEnd)
}

// PortalURL devolve o portal de facturação do cliente.
func (p *Provider) PortalURL(ctx context.Context, customerRef, returnURL string) (string, error) {
	if !p.Configured() {
		return "", payment.ErrNotConfigured
	}
	return p.client.BillingPortalURL(ctx, customerRef, returnURL)
}

// Refund devolve dinheiro de uma cobrança.
func (p *Provider) Refund(ctx context.Context, r payment.Refund) (payment.RefundResult, error) {
	if !p.Configured() {
		return payment.RefundResult{}, payment.ErrNotConfigured
	}
	res, err := p.client.RefundPaymentIntent(ctx, r.ChargeRef, r.Amount.Minor, r.Reason)
	if err != nil {
		return payment.RefundResult{}, err
	}
	status := payment.ParseStatus(res.Status)
	if status == "" {
		status = payment.StatusPending
	}
	return payment.RefundResult{
		RefundRef: res.ID,
		Status:    status,
		Amount:    money.New(res.Amount, money.NormalizeCurrency(res.Currency)),
	}, nil
}

// Balance devolve o saldo do comerciante.
func (p *Provider) Balance(ctx context.Context) ([]payment.BalanceEntry, error) {
	if !p.Configured() {
		return nil, payment.ErrNotConfigured
	}
	b, err := p.client.Balance(ctx)
	if err != nil {
		return nil, err
	}
	byCurrency := map[money.Currency]*payment.BalanceEntry{}
	entry := func(cur string) *payment.BalanceEntry {
		c := money.NormalizeCurrency(cur)
		if byCurrency[c] == nil {
			byCurrency[c] = &payment.BalanceEntry{
				Currency:  c,
				Available: money.Zero(c),
				Pending:   money.Zero(c),
			}
		}
		return byCurrency[c]
	}
	for _, a := range b.Available {
		e := entry(a.Currency)
		e.Available = money.New(e.Available.Minor+a.Amount, e.Currency)
	}
	for _, a := range b.Pending {
		e := entry(a.Currency)
		e.Pending = money.New(e.Pending.Minor+a.Amount, e.Currency)
	}
	out := make([]payment.BalanceEntry, 0, len(byCurrency))
	for _, e := range byCurrency {
		out = append(out, *e)
	}
	return out, nil
}

// ParseWebhook valida a assinatura e traduz o evento.
func (p *Provider) ParseWebhook(payload []byte, signature string) (*payment.Event, error) {
	if err := p.client.VerifySignature(payload, signature); err != nil {
		return nil, err
	}
	var raw struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
		Created int64 `json:"created"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, err
	}

	ev := &payment.Event{ID: raw.ID, Provider: p.Name(), Type: payment.EventNone}
	if raw.Created > 0 {
		t := time.Unix(raw.Created, 0).UTC()
		ev.OccurredAt = &t
	}

	switch raw.Type {
	case "checkout.session.completed":
		var o struct {
			ID                string            `json:"id"`
			Customer          string            `json:"customer"`
			Subscription      string            `json:"subscription"`
			PaymentIntent     string            `json:"payment_intent"`
			ClientReferenceID string            `json:"client_reference_id"`
			Mode              string            `json:"mode"`
			Metadata          map[string]string `json:"metadata"`
		}
		_ = json.Unmarshal(raw.Data.Object, &o)
		ev.Metadata = o.Metadata
		ev.CustomerRef = o.Customer
		ev.SubscriptionRef = o.Subscription
		ev.ChargeRef = firstNonEmpty(o.PaymentIntent, o.ID)
		ev.Reference = firstNonEmpty(o.Metadata[p.ReferenceKey], o.ClientReferenceID)
		ev.PlanRef = o.Metadata[p.PlanKey]
		ev.Interval = o.Metadata[p.IntervalKey]
		ev.Status = payment.StatusApproved
		ev.Method = payment.MethodCard
		if o.Mode == "subscription" {
			ev.Type = payment.EventSubscriptionActive
		} else {
			ev.Type = payment.EventChargeSucceeded
		}

	case "checkout.session.expired":
		var o struct {
			ID                string            `json:"id"`
			ClientReferenceID string            `json:"client_reference_id"`
			Metadata          map[string]string `json:"metadata"`
		}
		_ = json.Unmarshal(raw.Data.Object, &o)
		ev.Type = payment.EventChargeExpired
		ev.ChargeRef = o.ID
		ev.Metadata = o.Metadata
		ev.Reference = firstNonEmpty(o.Metadata[p.ReferenceKey], o.ClientReferenceID)
		ev.Status = payment.StatusExpired

	case "customer.subscription.created", "customer.subscription.updated":
		ev.Type = payment.EventSubscriptionUpdated
		p.applySubscription(raw.Data.Object, ev)

	case "customer.subscription.deleted":
		ev.Type = payment.EventSubscriptionCancelled
		p.applySubscription(raw.Data.Object, ev)
		ev.Status = payment.StatusCancelled

	case "invoice.payment_failed":
		var o struct {
			Customer     string `json:"customer"`
			Subscription string `json:"subscription"`
			ID           string `json:"id"`
		}
		_ = json.Unmarshal(raw.Data.Object, &o)
		ev.Type = payment.EventChargeFailed
		ev.CustomerRef = o.Customer
		ev.SubscriptionRef = o.Subscription
		ev.InvoiceRef = o.ID
		ev.Status = payment.StatusRejected

	case "invoice.paid", "invoice.payment_succeeded":
		var o struct {
			ID            string            `json:"id"`
			Customer      string            `json:"customer"`
			Subscription  string            `json:"subscription"`
			AmountPaid    int64             `json:"amount_paid"`
			Currency      string            `json:"currency"`
			BillingReason string            `json:"billing_reason"`
			Metadata      map[string]string `json:"metadata"`
			Lines         struct {
				Data []struct {
					Period struct {
						Start int64 `json:"start"`
						End   int64 `json:"end"`
					} `json:"period"`
				} `json:"data"`
			} `json:"lines"`
		}
		_ = json.Unmarshal(raw.Data.Object, &o)
		ev.Type = payment.EventInvoicePaid
		ev.CustomerRef = o.Customer
		ev.SubscriptionRef = o.Subscription
		ev.InvoiceRef = o.ID
		ev.BillingReason = o.BillingReason
		ev.Metadata = o.Metadata
		ev.Reference = o.Metadata[p.ReferenceKey]
		ev.Status = payment.StatusApproved
		amt := money.New(o.AmountPaid, money.NormalizeCurrency(o.Currency))
		ev.Amount = &amt
		if len(o.Lines.Data) > 0 {
			if s := o.Lines.Data[0].Period.Start; s > 0 {
				t := time.Unix(s, 0).UTC()
				ev.PeriodStart = &t
			}
			if e := o.Lines.Data[0].Period.End; e > 0 {
				t := time.Unix(e, 0).UTC()
				ev.PeriodEnd = &t
			}
		}

	case "charge.refunded":
		var o struct {
			ID            string            `json:"id"`
			PaymentIntent string            `json:"payment_intent"`
			Amount        int64             `json:"amount_refunded"`
			Currency      string            `json:"currency"`
			Metadata      map[string]string `json:"metadata"`
		}
		_ = json.Unmarshal(raw.Data.Object, &o)
		ev.Type = payment.EventChargeRefunded
		ev.ChargeRef = firstNonEmpty(o.PaymentIntent, o.ID)
		ev.Metadata = o.Metadata
		ev.Reference = o.Metadata[p.ReferenceKey]
		ev.Status = payment.StatusRefunded
		amt := money.New(o.Amount, money.NormalizeCurrency(o.Currency))
		ev.Amount = &amt
	}

	return ev, nil
}

func (p *Provider) applySubscription(rawObj json.RawMessage, ev *payment.Event) {
	var o struct {
		ID                string            `json:"id"`
		Customer          string            `json:"customer"`
		Status            string            `json:"status"`
		CurrentPeriodEnd  int64             `json:"current_period_end"`
		CancelAtPeriodEnd bool              `json:"cancel_at_period_end"`
		Metadata          map[string]string `json:"metadata"`
	}
	_ = json.Unmarshal(rawObj, &o)
	ev.SubscriptionRef = o.ID
	ev.CustomerRef = o.Customer
	ev.CancelAtPeriodEnd = o.CancelAtPeriodEnd
	ev.Metadata = o.Metadata
	ev.Reference = firstNonEmpty(ev.Reference, o.Metadata[p.ReferenceKey])
	ev.PlanRef = firstNonEmpty(ev.PlanRef, o.Metadata[p.PlanKey])
	ev.Interval = firstNonEmpty(ev.Interval, o.Metadata[p.IntervalKey])
	if ev.Status == "" {
		ev.Status = normalizeSubscriptionStatus(o.Status)
	}
	if o.CurrentPeriodEnd > 0 {
		t := time.Unix(o.CurrentPeriodEnd, 0).UTC()
		ev.CurrentPeriodEnd = &t
	}
}

// normalizeSubscriptionStatus traduz os estados de subscrição do Stripe.
// "trialing" conta como activa porque o cliente tem acesso; "unpaid" conta como
// falha de pagamento porque o Stripe já desistiu de cobrar.
func normalizeSubscriptionStatus(s string) payment.Status {
	switch s {
	case "active", "trialing":
		return payment.StatusApproved
	case "past_due", "unpaid":
		return payment.StatusRejected
	case "canceled", "incomplete_expired":
		return payment.StatusCancelled
	default:
		return payment.StatusPending
	}
}

func descriptionOf(req payment.ChargeRequest) string {
	if d := strings.TrimSpace(req.Description); d != "" {
		return d
	}
	if len(req.Items) > 0 && req.Items[0].Description != "" {
		return req.Items[0].Description
	}
	return "Pagamento"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
