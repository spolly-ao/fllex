package stripe

import (
	"context"
	"encoding/json"
	"fmt"
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

// Methods: o cartão, e o Pix em reais.
func (p *Provider) Methods() []payment.Method {
	return []payment.Method{payment.MethodCard, payment.MethodPix}
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
	switch req.Method {
	case "", payment.MethodCard:
	case payment.MethodPix:
		// O Pix só existe em reais, e não cobra sozinho a renovação de uma
		// subscrição: o Stripe recusaria a sessão, e recusa-se antes de a
		// pedir, com o erro que diz porquê.
		if req.Amount.Currency != money.BRL {
			return payment.ChargeResult{}, payment.ErrUnsupportedCurrency
		}
		if req.Mode == payment.ModeSubscription {
			return payment.ChargeResult{}, payment.ErrUnsupportedMethod
		}
	default:
		return payment.ChargeResult{}, payment.ErrUnsupportedMethod
	}
	if !req.Amount.IsPositive() {
		return payment.ChargeResult{}, payment.ErrAmountNotPositive
	}

	form := url.Values{}
	form.Set("success_url", req.SuccessURL)
	if req.Method != "" {
		// Com o método pedido, a página do Stripe mostra só esse: sem isto
		// mostra os que o painel da conta tiver ligados, e um pagamento por
		// outro método chegava aqui com o método errado.
		form.Set("payment_method_types[0]", string(req.Method))
	}
	form.Set("cancel_url", req.CancelURL)
	if req.Reference != "" {
		form.Set("client_reference_id", req.Reference)
		form.Set("metadata["+p.ReferenceKey+"]", req.Reference)
	}
	customer := req.Customer.ProviderRef
	if customer == "" && req.Mode == payment.ModeSubscription && p.client.cfg.TestClock != "" {
		if p.client.Live() {
			return payment.ChargeResult{}, fmt.Errorf("stripe: o relógio de ensaio só existe em contas de teste, e a chave é de produção: %w", payment.ErrNotConfigured)
		}
		id, err := p.client.CreateCustomer(ctx, req.Customer.Email, req.Customer.Name, p.client.cfg.TestClock)
		if err != nil {
			return payment.ChargeResult{}, err
		}
		customer = id
	}
	if customer != "" {
		form.Set("customer", customer)
	} else if req.Customer.Email != "" {
		form.Set("customer_email", req.Customer.Email)
	}
	for k, v := range req.Metadata {
		form.Set("metadata["+k+"]", v)
	}

	// O preço adaptativo do Stripe mostra e cobra o preço convertido para a
	// moeda do cliente. Quem chama regista a cobrança na moeda que pediu, e uma
	// subscrição convertida renovava noutra moeda: as facturas dos ciclos
	// deixavam de bater com o preço contratado. Cobra-se na moeda pedida, e a
	// conversão, quando a houver, é decisão de quem chama.
	form.Set("adaptive_pricing[enabled]", "false")
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
		CustomerRef: firstNonEmpty(sess.Customer, customer),
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

// VerifyCharge consulta o estado de uma sessão de checkout (cs_...) ou de uma
// factura (in_...).
//
// O Stripe assina e reentrega os webhooks, por isso a consulta não é o caminho
// normal de confirmação. Serve para o cliente que volta da página de pagamento
// antes de o webhook chegar, para reconciliar depois de uma paragem, e para
// quem não aceita um aviso como última palavra.
//
// A factura é a cobrança das renovações: o Stripe cobra sozinho cada ciclo de
// uma subscrição, sem sessão de checkout, e o que fica para consultar é a
// factura que o aviso invoice.paid trouxe.
func (p *Provider) VerifyCharge(ctx context.Context, statusRef, _ string) (payment.ChargeStatus, error) {
	if !p.Configured() {
		return payment.ChargeStatus{}, payment.ErrNotConfigured
	}
	if strings.TrimSpace(statusRef) == "" {
		return payment.ChargeStatus{}, nil
	}
	if strings.HasPrefix(statusRef, "in_") {
		return p.verifyInvoice(ctx, statusRef)
	}
	sess, err := p.client.GetCheckoutSession(ctx, statusRef)
	if err != nil {
		return payment.ChargeStatus{}, err
	}
	st := payment.ChargeStatus{Status: payment.StatusPending, SubscriptionRef: sess.Subscription, CustomerRef: sess.Customer}
	switch {
	case sess.Paid():
		st.Status = payment.StatusApproved
		st.Paid = true
		now := time.Now().UTC()
		st.PaidAt = &now
		if sess.Currency != "" {
			// O valor que o Stripe cobrou, para quem confirma o comparar com o
			// que pediu: uma sessão paga noutra moeda ou com outro valor não é
			// a cobrança que se fez.
			amt := money.New(sess.AmountTotal, money.NormalizeCurrency(sess.Currency))
			st.Amount = &amt
		}
	case sess.Status == "expired":
		st.Status = payment.StatusExpired
	case sess.Status == "complete" && sess.PaymentIntent != "":
		// Um método assíncrono (o Pix) acaba a sessão sem pagamento: o
		// cliente tem o código e ainda não pagou, ou já não vai pagar. A
		// sessão não o distingue, e o pagamento por trás dela sim.
		pi, err := p.client.GetPaymentIntent(ctx, sess.PaymentIntent)
		if err != nil {
			return payment.ChargeStatus{}, err
		}
		switch pi.Status {
		case "requires_payment_method":
			// O Pix expirou sem ser pago, ou o banco recusou-o.
			st.Status = payment.StatusRejected
		case "canceled":
			st.Status = payment.StatusCancelled
		}
	}
	return st, nil
}

// verifyInvoice consulta uma factura. Paga traz o valor que entrou, para quem
// confirma o comparar com o esperado; aberta continua pendente, porque o
// Stripe volta a tentar cobrá-la sozinho; anulada (void) e incobrável
// (uncollectible) já não se pagam.
func (p *Provider) verifyInvoice(ctx context.Context, id string) (payment.ChargeStatus, error) {
	inv, err := p.client.GetInvoice(ctx, id)
	if err != nil {
		return payment.ChargeStatus{}, err
	}
	st := payment.ChargeStatus{Status: payment.StatusPending, SubscriptionRef: inv.SubscriptionID(), CustomerRef: inv.Customer}
	switch inv.Status {
	case "paid":
		st.Status = payment.StatusApproved
		st.Paid = true
		paidAt := time.Now().UTC()
		if inv.StatusTransitions.PaidAt > 0 {
			paidAt = time.Unix(inv.StatusTransitions.PaidAt, 0).UTC()
		}
		st.PaidAt = &paidAt
		amt := money.New(inv.AmountPaid, money.NormalizeCurrency(inv.Currency))
		st.Amount = &amt
	case "void":
		st.Status = payment.StatusCancelled
	case "uncollectible":
		st.Status = payment.StatusRejected
	}
	return st, nil
}

// VerifySubscription consulta uma subscrição.
func (p *Provider) VerifySubscription(ctx context.Context, subscriptionRef string) (payment.SubscriptionStatus, error) {
	if !p.Configured() {
		return payment.SubscriptionStatus{}, payment.ErrNotConfigured
	}
	if strings.TrimSpace(subscriptionRef) == "" {
		return payment.SubscriptionStatus{}, fmt.Errorf("stripe: a consulta exige a subscrição: %w", payment.ErrUnsupported)
	}
	sub, err := p.client.GetSubscription(ctx, subscriptionRef)
	if err != nil {
		return payment.SubscriptionStatus{}, err
	}
	out := payment.SubscriptionStatus{
		Status:            normalizeSubscriptionStatus(sub.Status),
		CustomerRef:       sub.Customer,
		CancelAtPeriodEnd: sub.CancelAtPeriodEnd,
	}
	if end := sub.PeriodEnd(); end > 0 {
		t := time.Unix(end, 0).UTC()
		out.CurrentPeriodEnd = &t
	}
	return out, nil
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
//
// O ChargeRef pode ser o payment intent (pi_...) ou a referência que o
// [Provider.Charge] devolveu, que é a sessão de checkout (cs_...): o Stripe só
// estorna payment intents, e por isso a sessão lê-se primeiro para saber qual
// foi o dela. Uma sessão que ainda não tem payment intent (não foi paga) não
// tem nada para estornar. Com [payment.Refund.IdempotencyKey], um pedido
// repetido devolve o mesmo estorno, e as falhas passageiras repetem-se.
func (p *Provider) Refund(ctx context.Context, r payment.Refund) (payment.RefundResult, error) {
	if !p.Configured() {
		return payment.RefundResult{}, payment.ErrNotConfigured
	}
	intent := strings.TrimSpace(r.ChargeRef)
	if strings.HasPrefix(intent, "cs_") {
		sess, err := p.client.GetCheckoutSession(ctx, intent)
		if err != nil {
			return payment.RefundResult{}, err
		}
		if sess.PaymentIntent == "" {
			return payment.RefundResult{}, fmt.Errorf("stripe: a sessão %s não tem pagamento para estornar: %w", intent, payment.ErrInvalidTransition)
		}
		intent = sess.PaymentIntent
	}
	res, err := p.client.RefundPaymentIntentOnce(ctx, intent, r.Amount.Minor, r.Reason, r.IdempotencyKey)
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
	case "checkout.session.completed", "checkout.session.async_payment_succeeded", "checkout.session.async_payment_failed":
		var o struct {
			ID                 string            `json:"id"`
			Customer           string            `json:"customer"`
			Subscription       string            `json:"subscription"`
			PaymentIntent      string            `json:"payment_intent"`
			ClientReferenceID  string            `json:"client_reference_id"`
			Mode               string            `json:"mode"`
			PaymentStatus      string            `json:"payment_status"`
			PaymentMethodTypes []string          `json:"payment_method_types"`
			Metadata           map[string]string `json:"metadata"`
		}
		_ = json.Unmarshal(raw.Data.Object, &o)
		ev.Metadata = o.Metadata
		ev.CustomerRef = o.Customer
		ev.SubscriptionRef = o.Subscription
		ev.ChargeRef = firstNonEmpty(o.PaymentIntent, o.ID)
		ev.Reference = firstNonEmpty(o.Metadata[p.ReferenceKey], o.ClientReferenceID)
		ev.PlanRef = o.Metadata[p.PlanKey]
		ev.Interval = o.Metadata[p.IntervalKey]
		ev.Method = methodOf(o.PaymentMethodTypes)
		switch {
		case raw.Type == "checkout.session.async_payment_failed":
			// O Pix que expirou sem ser pago, ou que o banco recusou.
			ev.Type = payment.EventChargeFailed
			ev.Status = payment.StatusRejected
		case o.PaymentStatus == "unpaid":
			// Um método assíncrono (o Pix) acaba a sessão antes de o dinheiro
			// entrar: o cliente tem o código, e ainda não pagou. O pagamento
			// chega depois, em checkout.session.async_payment_succeeded, e
			// até lá não há nada a fazer.
			ev.Status = payment.StatusPending
		default:
			ev.Status = payment.StatusApproved
			if o.Mode == "subscription" {
				ev.Type = payment.EventSubscriptionActive
			} else {
				ev.Type = payment.EventChargeSucceeded
			}
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
		o := p.readInvoice(raw.Data.Object, ev)
		ev.Type = payment.EventChargeFailed
		ev.Status = payment.StatusRejected
		amt := money.New(o.AmountDue, money.NormalizeCurrency(o.Currency))
		ev.Amount = &amt

	case "invoice.paid", "invoice.payment_succeeded":
		o := p.readInvoice(raw.Data.Object, ev)
		ev.Type = payment.EventInvoicePaid
		ev.Status = payment.StatusApproved
		amt := money.New(o.AmountPaid, money.NormalizeCurrency(o.Currency))
		ev.Amount = &amt

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

// invoiceObject é a factura tal como vem num aviso. A subscrição e os
// metadados dela mudaram de sítio na versão 2025-03-31 da API (de
// subscription e subscription_details para parent.subscription_details), e os
// avisos chegam na versão da conta: lêem-se as duas formas, para uma conta que
// suba de versão não deixar de correlacionar as renovações.
type invoiceObject struct {
	ID                  string            `json:"id"`
	Customer            string            `json:"customer"`
	Subscription        string            `json:"subscription"`
	PaymentIntent       string            `json:"payment_intent"`
	AmountPaid          int64             `json:"amount_paid"`
	AmountDue           int64             `json:"amount_due"`
	Currency            string            `json:"currency"`
	BillingReason       string            `json:"billing_reason"`
	HostedURL           string            `json:"hosted_invoice_url"`
	Metadata            map[string]string `json:"metadata"`
	SubscriptionDetails struct {
		Metadata map[string]string `json:"metadata"`
	} `json:"subscription_details"`
	Parent struct {
		SubscriptionDetails struct {
			Subscription string            `json:"subscription"`
			Metadata     map[string]string `json:"metadata"`
		} `json:"subscription_details"`
	} `json:"parent"`
	Lines struct {
		Data []struct {
			Period struct {
				Start int64 `json:"start"`
				End   int64 `json:"end"`
			} `json:"period"`
		} `json:"data"`
	} `json:"lines"`
}

// readInvoice preenche o evento com o que é comum às facturas pagas e às que
// falharam. A nossa referência não vem na factura (os metadados dela nascem
// vazios nas renovações): vem nos da subscrição, onde o Charge a pôs.
func (p *Provider) readInvoice(rawObj json.RawMessage, ev *payment.Event) invoiceObject {
	var o invoiceObject
	_ = json.Unmarshal(rawObj, &o)
	meta := o.Metadata
	for _, m := range []map[string]string{o.SubscriptionDetails.Metadata, o.Parent.SubscriptionDetails.Metadata} {
		if len(meta) == 0 {
			meta = m
		}
	}
	ev.CustomerRef = o.Customer
	ev.SubscriptionRef = firstNonEmpty(o.Subscription, o.Parent.SubscriptionDetails.Subscription)
	ev.InvoiceRef = o.ID
	ev.InvoiceURL = o.HostedURL
	ev.ChargeRef = o.PaymentIntent
	ev.BillingReason = o.BillingReason
	ev.Metadata = meta
	ev.Reference = meta[p.ReferenceKey]
	ev.PlanRef = meta[p.PlanKey]
	ev.Interval = meta[p.IntervalKey]
	ev.Method = payment.MethodCard
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
	return o
}

func (p *Provider) applySubscription(rawObj json.RawMessage, ev *payment.Event) {
	var o Subscription
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
	if end := o.PeriodEnd(); end > 0 {
		t := time.Unix(end, 0).UTC()
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

// methodOf diz o método de uma sessão pelos tipos que ela aceitava: uma
// sessão só de Pix foi paga por Pix, e tudo o resto conta como cartão, como
// antes de haver Pix.
func methodOf(types []string) payment.Method {
	if len(types) == 1 && types[0] == string(payment.MethodPix) {
		return payment.MethodPix
	}
	return payment.MethodCard
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
