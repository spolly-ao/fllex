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

// Methods são os dois métodos que o MoMenu cobra.
//
// Eram três. O eKwanza saiu porque o endpoint de pagamento dele deixou de
// existir na API do MoMenu: anunciá-lo punha o checkout a oferecer um botão que
// responde 404.
func (p *Provider) Methods() []payment.Method {
	return []payment.Method{payment.MethodMCX, payment.MethodReference}
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
		Products:          p.products(req),
		Customer:          p.customer(req),
		ExternalReference: referenciaDe(req),
	}, idempotenciaDe(req))
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
		StatusRef:   res.TransactionID,
		InvoiceURL:  res.InvoiceURL,
		// O `Reference` do resultado é a referência bancária, e no Multicaixa
		// Express não existe nenhuma. A nossa vai no Raw, que é onde quem
		// correlaciona a vai buscar.
		Raw: map[string]any{"externalReference": res.ExternalReference},
	}, nil
}

func (p *Provider) chargeReference(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	res, err := p.client.CreateReference(ctx, ReferenceRequest{
		PaymentInfo:       PaymentInfo{Amount: amountOf(req.Amount)},
		Products:          p.products(req),
		Customer:          p.customer(req),
		ExternalReference: referenciaDe(req),
	}, idempotenciaDe(req))
	if err != nil {
		return payment.ChargeResult{}, err
	}
	out := payment.ChargeResult{
		Kind:   payment.KindReference,
		Status: payment.StatusPending,
		// Os dois guardam o transactionId, e é essa a correcção: a consulta de
		// estado passou a ser por ele, e não pelo operationId. Enquanto o
		// StatusRef levou o operationId, a consulta devolvia "não encontrado" e
		// nenhuma referência paga chegava a fechar.
		ProviderRef: res.TransactionID,
		StatusRef:   res.TransactionID,
		// `Reference` é a referência bancária, a que o cliente escreve no ATM.
		Reference: res.ReferenceNumber,
		Entity:    res.Entity,
		DueDate:   res.DueDate,
		Raw:       map[string]any{"operationId": res.OperationID},
	}
	if ref := httpxFirst(res.ExternalReference, req.Reference); ref != "" {
		out.Raw["externalReference"] = ref
	}
	if t, ok := parseDate(res.DueDate); ok {
		out.ExpiresAt = &t
	}
	return out, nil
}

// VerifyCharge consulta o estado no MoMenu.
//
// É a confirmação de que se pode depender, e passou a ser mais do que isso: a
// consulta é uma só para todos os métodos e é **pelo transactionId**. A forma
// antiga lia o estado de dentro de um objecto `payment` que já não existe, e o
// resultado era pendente para sempre, sem erro nenhum pelo caminho.
func (p *Provider) VerifyCharge(ctx context.Context, statusRef, merchantRef string) (payment.ChargeStatus, error) {
	if !p.Configured() {
		return payment.ChargeStatus{}, payment.ErrNotConfigured
	}
	ref := strings.TrimSpace(statusRef)
	if ref == "" {
		ref = strings.TrimSpace(merchantRef)
	}
	if ref == "" {
		return payment.ChargeStatus{}, nil
	}

	res, err := p.client.Status(ctx, ref)
	if err != nil {
		return payment.ChargeStatus{}, err
	}

	st := payment.ChargeStatus{Status: payment.StatusPending, InvoiceURL: res.InvoiceURL}
	if s := payment.ParseStatus(res.Status); s != "" {
		st.Status = s
	}
	st.Paid = st.Status == payment.StatusApproved
	if res.Amount > 0 {
		v := money.FromFloat(res.Amount, money.AOA)
		st.Amount = &v
	}
	if st.Paid {
		if t, ok := parseDate(res.PaidAt); ok {
			st.PaidAt = &t
		} else {
			agora := time.Now().UTC()
			st.PaidAt = &agora
		}
	}
	return st, nil
}

// ParseWebhook existe para cumprir o [payment.WebhookParser] e **recusa sempre**.
//
// A assinatura do MoMenu está partida por dois cabeçalhos, e aqui só chega um
// deles. Sem o carimbo não se calcula o HMAC, e sem HMAC não há como distinguir
// um evento do MoMenu de um POST que qualquer pessoa consiga fazer ao nosso
// endereço. Devolver um evento "pendente" como se fosse inofensivo foi o que
// esta integração fez durante meses: use o
// [Provider.ParseWebhookWithHeaders].
func (p *Provider) ParseWebhook(_ []byte, _ string) (*payment.Event, error) {
	return nil, payment.ErrBadSignature
}

// ParseWebhookWithHeaders valida a assinatura e traduz o evento.
//
// **A assinatura verifica-se primeiro, e nada do corpo é olhado antes disso.**
// É a referência que torna isto crítico: é o webhook que a marca como paga, por
// isso um webhook aceite sem verificação é uma forma de marcar pagamentos como
// pagos.
//
// O desfecho sai do `operationStatus` e nunca do `event`, porque o MoMenu envia
// os estados que não são de sucesso **sem o campo `event`**. Uma condição que
// exigisse os dois deixava passar em silêncio todos os cancelamentos e todas as
// recusas, que é o que acontecia.
func (p *Provider) ParseWebhookWithHeaders(payload []byte, headers map[string]string) (*payment.Event, error) {
	cfg := p.client.Config()
	if !assinaturaValida(
		cfg.WebhookSecret, payload,
		cabecalho(headers, cabecalhoAssinatura),
		cabecalho(headers, cabecalhoCarimbo),
		cfg.WebhookTolerance, p.client.agora(),
	) {
		return nil, payment.ErrBadSignature
	}

	var ev WebhookEvent
	if err := unmarshal(payload, &ev); err != nil {
		return &payment.Event{Type: payment.EventNone, Provider: p.Name()}, nil
	}

	out := &payment.Event{
		Provider:   p.Name(),
		ChargeRef:  httpxFirst(ev.MerchantTransactionID, ev.EkwanzaTransactionID),
		Reference:  httpxFirst(ev.ExternalReference, ev.MerchantTransactionID),
		InvoiceURL: ev.InvoiceURL,
		Raw:        map[string]any{"operationStatus": ev.OperationStatus},
	}
	if ev.Event != "" {
		out.Raw["event"] = ev.Event
	}

	switch strings.TrimSpace(ev.OperationStatus) {
	case "1":
		out.Type, out.Status = payment.EventChargeSucceeded, payment.StatusApproved
	case "3":
		// Cancelado ou expirado. Era ignorado, e a cobrança ficava a ocupar a
		// fila de consultas até a expiração a apanhar.
		out.Type, out.Status = payment.EventChargeExpired, payment.StatusExpired
	case "4", "5":
		out.Type, out.Status = payment.EventChargeFailed, payment.StatusRejected
	default:
		out.Type, out.Status = payment.EventNone, payment.StatusPending
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

// referenciaDe devolve a nossa referência, cortada ao que o MoMenu aceita.
//
// O campo deles tem um limite de 200 caracteres. Passar mais não dá um erro
// claro, dá um INVALID_PARAMETERS no meio de uma cobrança.
func referenciaDe(req payment.ChargeRequest) string {
	ref := strings.TrimSpace(req.Reference)
	if len(ref) > 200 {
		return ref[:200]
	}
	return ref
}

// idempotenciaDe devolve a chave de idempotência a enviar.
//
// Usa-se a nossa referência, que é única por cobrança: é exactamente o que uma
// chave de idempotência tem de ser, e evita inventar um segundo identificador
// para dizer o mesmo. Sem referência não vai chave, e aí repetir emite outra
// cobrança, que é o comportamento antigo.
func idempotenciaDe(req payment.ChargeRequest) string {
	return referenciaDe(req)
}
