package momenu

import (
	"context"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// Provider implementa [payment.Provider] sobre o MoMenu.
type Provider struct {
	client *Client
	// DefaultDescription é o texto da linha de factura quando a cobrança não
	// traz descrição.
	DefaultDescription string
}

// New cria o provider MoMenu.
func New(cfg Config) *Provider {
	return &Provider{client: NewClient(cfg), DefaultDescription: "Pagamento"}
}

// NewWithClient cria o provider sobre um cliente já construído. Útil para
// partilhar o cliente com a reconciliação de facturas ou para o substituir em
// testes.
func NewWithClient(c *Client) *Provider {
	return &Provider{client: c, DefaultDescription: "Pagamento"}
}

// Client dá acesso ao cliente da API, para as operações que não cabem na
// interface de provider (listagem de facturas, reconciliação).
func (p *Provider) Client() *Client { return p.client }

// Name devolve "momenu".
func (p *Provider) Name() string { return "momenu" }

// Methods são os três métodos que o MoMenu cobra.
func (p *Provider) Methods() []payment.Method {
	return []payment.Method{payment.MethodMCX, payment.MethodReference, payment.MethodEKwanza}
}

// SupportsCurrency: o MoMenu só processa kwanza.
func (p *Provider) SupportsCurrency(c money.Currency) bool {
	return money.NormalizeCurrency(string(c)) == money.AOA
}

// Configured indica se há chave de API.
func (p *Provider) Configured() bool { return p.client.Configured() }

// Charge inicia a cobrança pelo método pedido.
func (p *Provider) Charge(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	if !p.Configured() {
		return payment.ChargeResult{}, payment.ErrNotConfigured
	}
	if !p.SupportsCurrency(req.Amount.Currency) {
		return payment.ChargeResult{}, payment.ErrUnsupportedCurrency
	}
	if !req.Amount.IsPositive() {
		return payment.ChargeResult{}, payment.ErrAmountNotPositive
	}
	switch req.Method {
	case payment.MethodMCX:
		return p.chargeMCX(ctx, req)
	case payment.MethodEKwanza:
		return p.chargeEKwanza(ctx, req)
	case payment.MethodReference, "":
		return p.chargeReference(ctx, req)
	default:
		return payment.ChargeResult{}, payment.ErrUnsupportedMethod
	}
}

func (p *Provider) chargeMCX(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	res, err := p.client.InitMCX(ctx, MCXRequest{
		PaymentInfo: PaymentInfo{
			Amount:      amountOf(req.Amount),
			PhoneNumber: req.Customer.Phone,
		},
		Products: p.products(req),
		Customer: p.customer(req),
	})
	if err != nil {
		return payment.ChargeResult{}, err
	}
	// O Multicaixa Express é síncrono: chegar aqui com sucesso significa que o
	// dinheiro entrou e a factura fiscal já existe. Não há polling nem webhook a
	// esperar.
	return payment.ChargeResult{
		Kind:        payment.KindPaid,
		Status:      payment.StatusApproved,
		ProviderRef: res.TransactionID,
		InvoiceURL:  res.InvoiceURL,
	}, nil
}

func (p *Provider) chargeEKwanza(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	res, err := p.client.InitEKwanza(ctx, EKwanzaRequest{
		PaymentInfo: PaymentInfo{
			Amount:      amountOf(req.Amount),
			PhoneNumber: req.Customer.Phone,
		},
		Products: p.products(req),
		Customer: p.customer(req),
	})
	if err != nil {
		return payment.ChargeResult{}, err
	}
	out := payment.ChargeResult{
		Kind:        payment.KindCode,
		Status:      payment.StatusPending,
		ProviderRef: res.MerchantTransactionID,
		// A consulta de estado do eKwanza é feita pelo código, não pelo id da
		// transacção.
		StatusRef: res.Code,
		Code:      res.Code,
		QRCode:    res.QRCode,
	}
	if res.PaymentTimeout > 0 {
		exp := time.Now().UTC().Add(time.Duration(res.PaymentTimeout) * time.Second)
		out.ExpiresAt = &exp
	} else if t, ok := parseDate(res.ExpirationDate); ok {
		out.ExpiresAt = &t
	}
	return out, nil
}

func (p *Provider) chargeReference(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	res, err := p.client.CreateReference(ctx, ReferenceRequest{
		PaymentInfo: PaymentInfo{Amount: amountOf(req.Amount)},
		Products:    p.products(req),
		Customer:    p.customer(req),
	})
	if err != nil {
		return payment.ChargeResult{}, err
	}
	out := payment.ChargeResult{
		Kind:   payment.KindReference,
		Status: payment.StatusPending,
		// O webhook correlaciona pelo id da transacção e a consulta de estado
		// usa o id da operação. Guardar os dois separados evita a troca que
		// devolve sempre "não encontrado".
		ProviderRef: httpxFirst(res.TransactionID, res.OperationID),
		StatusRef:   httpxFirst(res.OperationID, res.TransactionID),
		Entity:      res.Entity,
		Reference:   res.ReferenceNumber,
		DueDate:     res.DueDate,
	}
	if t, ok := parseDate(res.DueDate); ok {
		out.ExpiresAt = &t
	}
	return out, nil
}

// VerifyCharge consulta o estado de uma referência ou de um eKwanza.
//
// É a confirmação de que se pode depender: o webhook do MoMenu não é assinado
// nem reentregue, por isso mesmo quando ele chega o estado é reconfirmado por
// aqui antes de se dar seja o que for por pago.
func (p *Provider) VerifyCharge(ctx context.Context, statusRef, merchantRef string) (payment.ChargeStatus, error) {
	if !p.Configured() {
		return payment.ChargeStatus{}, payment.ErrNotConfigured
	}
	if strings.TrimSpace(statusRef) == "" {
		return payment.ChargeStatus{}, nil
	}
	res, err := p.client.ReferenceStatus(ctx, statusRef, merchantRef)
	if err != nil {
		return payment.ChargeStatus{}, err
	}
	st := payment.ChargeStatus{Status: payment.StatusPending, InvoiceURL: res.InvoiceURL}
	if res.Payment != nil {
		if s := payment.ParseStatus(res.Payment.Status); s != "" {
			st.Status = s
		}
	}
	st.Paid = st.Status == payment.StatusApproved
	if st.Paid {
		now := time.Now().UTC()
		st.PaidAt = &now
	}
	return st, nil
}

// ParseWebhook lê o corpo que o MoMenu envia.
//
// A entrega não é assinada, por isso o evento devolvido serve apenas para
// acordar a confirmação: trate-o como "vale a pena ir verificar" e nunca como
// prova de pagamento. É por isso que o estado devolvido é sempre pendente.
func (p *Provider) ParseWebhook(payload []byte, _ string) (*payment.Event, error) {
	var ev WebhookEvent
	if err := unmarshal(payload, &ev); err != nil {
		return &payment.Event{Type: payment.EventNone, Provider: p.Name()}, nil
	}
	out := &payment.Event{
		Provider:   p.Name(),
		ChargeRef:  httpxFirst(ev.MerchantTransactionID, ev.EkwanzaTransactionID),
		Reference:  ev.MerchantTransactionID,
		InvoiceURL: ev.InvoiceURL,
		Status:     payment.StatusPending,
	}
	if ev.Event == "payment.confirmed" && ev.OperationStatus == "1" {
		out.Type = payment.EventChargeSucceeded
	} else {
		out.Type = payment.EventNone
	}
	return out, nil
}

// --- construção dos blocos da factura ---------------------------------------

// products devolve as linhas da factura fiscal. Sem linhas explícitas, monta
// uma a partir da descrição e do valor.
func (p *Provider) products(req payment.ChargeRequest) []Product {
	if len(req.Items) > 0 {
		out := make([]Product, 0, len(req.Items))
		for _, it := range req.Items {
			q := it.Quantity
			if q <= 0 {
				q = 1
			}
			out = append(out, Product{
				ProductName:     it.Description,
				ProductPrice:    amountOf(it.UnitPrice),
				ProductQuantity: q,
				IVA:             it.TaxRate,
			})
		}
		return out
	}
	name := strings.TrimSpace(req.Description)
	if name == "" {
		name = p.DefaultDescription
	}
	return []Product{{
		ProductName:     name,
		ProductPrice:    amountOf(req.Amount),
		ProductQuantity: 1,
		IVA:             0,
	}}
}

// customer devolve o bloco do pagador. O nome só segue acompanhado de NIF: sem
// ele o MoMenu recusa o pedido, e a factura sairia na mesma a "Consumidor
// Final".
func (p *Provider) customer(req payment.ChargeRequest) *Customer {
	c := &Customer{}
	if req.Customer.TaxID != "" {
		c.NIF = req.Customer.TaxID
		c.Name = req.Customer.Name
	}
	if req.Customer.Phone != "" {
		c.Phone = req.Customer.Phone
	}
	if c.NIF == "" && c.Phone == "" {
		return nil
	}
	return c
}

// amountOf converte para kwanzas inteiros, que é o que o MoMenu espera no campo
// amount. O kwanza tem subunidade na nossa representação, mas o gateway
// trabalha em unidades inteiras.
func amountOf(a money.Amount) float64 {
	return float64(a.Major())
}

// parseDate lê as formas de data que o MoMenu devolve.
func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
