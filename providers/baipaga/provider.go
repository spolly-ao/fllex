package baipaga

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
	"github.com/spolly-ao/fllex/phone"
)

// ErrReferenceRequired diz que a cobrança seguiu sem referência.
//
// O BAI exige uma referência externa única, devolve-a no callback e é por ela
// que a consulta de estado encontra o pagamento. Sem ela não há nada que ligue
// a confirmação à cobrança.
var ErrReferenceRequired = errors.New("baipaga: a cobrança precisa de uma referência")

// ErrPhoneRequired diz que falta o telemóvel do cliente.
//
// É a identificação do pagador: sem número não há a quem pedir autorização.
var ErrPhoneRequired = errors.New("baipaga: o BAI PAGA precisa do telemóvel do cliente")

// Provider implementa [payment.Provider] sobre o BAI PAGA.
type Provider struct {
	client *Client
}

// New cria o provider.
func New(cfg Config) *Provider { return &Provider{client: NewClient(cfg)} }

// NewWithClient cria o provider sobre um cliente já construído.
func NewWithClient(c *Client) *Provider { return &Provider{client: c} }

// Client dá acesso ao cliente, para as operações que não cabem na interface
// (validar um telemóvel, gerar um QR).
func (p *Provider) Client() *Client { return p.client }

// Name devolve "baipaga".
func (p *Provider) Name() string { return "baipaga" }

// Methods: só BAI PAGA.
func (p *Provider) Methods() []payment.Method {
	return []payment.Method{payment.MethodBAIPaga}
}

// SupportsCurrency: só kwanza.
//
// A biblioteca fala AOA e o BAI fala AKZ. A tradução é do pacote e não sobe: o
// que entra e sai daqui é sempre AOA.
func (p *Provider) SupportsCurrency(c money.Currency) bool {
	return money.NormalizeCurrency(string(c)) == money.AOA
}

// Configured indica se há chave de API e chave de HMAC.
func (p *Provider) Configured() bool { return p.client.Configured() }

// Charge pede ao BAI para notificar o cliente de que tem um pagamento à espera.
//
// O desfecho depende do fluxo configurado: com confirmação na app do banco fica
// [payment.KindPending], porque não há nada para mostrar e o cliente age no
// telemóvel dele; com confirmação numa página nossa fica
// [payment.KindRedirect], com a URL que se abre num popup.
//
// Em ambos o estado fica pendente. Uma resposta com sucesso aqui significa que
// o pedido foi aceite, e não que o dinheiro entrou.
func (p *Provider) Charge(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	if !p.Configured() {
		return payment.ChargeResult{}, payment.ErrNotConfigured
	}
	if req.Method != "" && req.Method != payment.MethodBAIPaga {
		return payment.ChargeResult{}, payment.ErrUnsupportedMethod
	}
	if !p.SupportsCurrency(req.Amount.Currency) {
		return payment.ChargeResult{}, payment.ErrUnsupportedCurrency
	}
	if !req.Amount.IsPositive() {
		return payment.ChargeResult{}, payment.ErrAmountNotPositive
	}
	referencia := strings.TrimSpace(req.Reference)
	if referencia == "" {
		return payment.ChargeResult{}, ErrReferenceRequired
	}
	// O telemóvel vai na forma local de nove dígitos, que é a que o BAI usa.
	// Valida-se antes de qualquer chamada de rede: um número mal escrito
	// devolve aqui um erro que se percebe, em vez de um
	// CUSTOMER_NOT_FOUND_FOR_MSISDN vindo do banco.
	msisdn := phone.LocalAO(req.Customer.Phone)
	if msisdn == "" {
		return payment.ChargeResult{}, ErrPhoneRequired
	}

	cfg := p.client.Config()
	descricao := strings.TrimSpace(req.Description)
	if descricao == "" {
		descricao = cfg.Description
	}

	res, err := p.client.CriarPagamento(ctx, pedidoPagamento{
		CustomerMsisdn:    msisdn,
		TotalAmount:       decimalJSON(req.Amount),
		Currency:          moedaBAI,
		ShoppingCart:      carrinhoDe(req),
		ExternalReference: referencia,
		Description:       descricao,
		AcceptancePointID: cfg.AcceptancePointID,
	})
	if err != nil {
		return payment.ChargeResult{}, err
	}

	out := payment.ChargeResult{
		Kind:   payment.KindPending,
		Status: payment.StatusPending,
		// O identificador do BAI e a nossa referência guardam-se os dois: o
		// callback traz a nossa, a consulta aceita qualquer uma, e trocá-las
		// devolve sempre "não encontrado".
		ProviderRef: itoa(res.PaymentID),
		StatusRef:   referencia,
		Reference:   referencia,
		Raw:         map[string]any{"paymentId": res.PaymentID},
	}
	if url := limparURL(res.ConfirmationURL); url != "" {
		out.Kind = payment.KindRedirect
		out.URL = url
	}
	if req.ExpiresAt != nil {
		quando := *req.ExpiresAt
		out.ExpiresAt = &quando
	}
	return out, nil
}

// VerifyCharge consulta o estado no BAI.
//
// É a fonte da verdade desta integração, e não um extra. A especificação diz
// que o callback é "best effort" e que quem integra deve ter um modelo de
// recurso por consulta: sem isto, um callback perdido deixa dinheiro cobrado e
// nunca confirmado.
func (p *Provider) VerifyCharge(ctx context.Context, statusRef, merchantRef string) (payment.ChargeStatus, error) {
	if !p.Configured() {
		return payment.ChargeStatus{}, payment.ErrNotConfigured
	}
	referencia := strings.TrimSpace(statusRef)
	if referencia == "" {
		referencia = strings.TrimSpace(merchantRef)
	}
	if referencia == "" {
		return payment.ChargeStatus{}, nil
	}

	e, err := p.client.Estado(ctx, referencia)
	if err != nil {
		return payment.ChargeStatus{}, err
	}

	st := payment.ChargeStatus{Status: estadoBAI(e.Status)}
	st.Paid = st.Status == payment.StatusApproved
	if v, ok := montanteDe(e.Amount); ok {
		st.Amount = &v
	}
	if st.Paid {
		if t, ok := lerData(e.LastChangeDate); ok {
			st.PaidAt = &t
		} else {
			agora := time.Now().UTC()
			st.PaidAt = &agora
		}
	}
	return st, nil
}

// ParseWebhook lê o callback XML e valida a assinatura.
//
// **A assinatura vem dentro do corpo**, no elemento `<signature>`, e não num
// cabeçalho como nos outros gateways. Por isso o argumento `signature` é
// ignorado: o que o `services/core` lê dos cabeçalhos não existe aqui.
//
// Uma assinatura que não bate devolve [payment.ErrBadSignature], e não um
// evento ignorado: aqui há chave partilhada e há como provar, ao contrário do
// callback da EMIS. Um corpo assinado que não confere é um corpo forjado ou
// alterado em trânsito, e as duas coisas merecem um erro.
func (p *Provider) ParseWebhook(payload []byte, _ string) (*payment.Event, error) {
	var cb Callback
	if err := xml.Unmarshal(payload, &cb); err != nil {
		return &payment.Event{Type: payment.EventNone, Provider: p.Name()}, nil
	}
	if !assinaturaValida(p.client.Config().HMACKey, cb) {
		return nil, payment.ErrBadSignature
	}

	ev := &payment.Event{
		Provider: p.Name(),
		// O identificador do BAI deduplica reentregas.
		ID:        strings.TrimSpace(cb.ID),
		ChargeRef: strings.TrimSpace(cb.ID),
		Reference: strings.TrimSpace(cb.ExternalReference),
		Method:    payment.MethodBAIPaga,
		Status:    estadoBAI(cb.Status),
		Raw:       map[string]any{"nonce": cb.Nonce, "merchant": cb.Merchant.ExternalID},
	}
	if v, ok := montanteDe(json.RawMessage(cb.Amount)); ok {
		ev.Amount = &v
	}
	if t, ok := lerData(cb.LastChangeDate); ok {
		ev.OccurredAt = &t
	}

	switch ev.Status {
	case payment.StatusApproved:
		ev.Type = payment.EventChargeSucceeded
	case payment.StatusRejected:
		ev.Type = payment.EventChargeFailed
	case payment.StatusExpired:
		ev.Type = payment.EventChargeExpired
	default:
		// Estados intermédios não movem nada. A especificação diz que o
		// callback só é enviado em estados finais, e um que chegue fora disso
		// é para ignorar e não para agir.
		ev.Type = payment.EventNone
	}
	return ev, nil
}

// --- traduções ---------------------------------------------------------------

// estadoBAI traduz os nove estados do BAI PAGA.
//
// O COMPENSATED é o que obriga a pensar: "compensação financeira realizada" é
// mais adiantado do que o sucesso, nunca menos, e por isso conta como pago. Um
// gateway que já compensou e aqui ficasse pendente era dinheiro recebido e não
// creditado.
func estadoBAI(s string) payment.Status {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "SUCCESS", "COMPENSATED":
		return payment.StatusApproved
	case "ERROR", "REJECTED":
		return payment.StatusRejected
	case "TIMEOUT", "EXPIRED":
		return payment.StatusExpired
	case "PROCESSING", "PENDING_CHALLENGE_RESPONSE", "PENDING_SIGNATURE":
		return payment.StatusPending
	default:
		return payment.StatusPending
	}
}

// decimalJSON escreve o montante como número JSON com as casas da moeda.
//
// Vai como número e não como texto porque é assim que os exemplos da
// especificação o mostram, e passa pelo [money.Amount.Decimal] em vez de por
// float64 para não ganhar casas que ninguém pediu.
func decimalJSON(a money.Amount) json.RawMessage {
	return json.RawMessage(a.Decimal())
}

// montanteDe lê um montante devolvido pelo BAI, sempre em kwanzas.
func montanteDe(raw json.RawMessage) (money.Amount, bool) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return money.Amount{}, false
	}
	v, err := money.Parse(s, money.AOA)
	if err != nil {
		return money.Amount{}, false
	}
	return v, true
}

// lerData lê as formas de data que o BAI devolve.
func lerData(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// limparURL tira as aspas que a resposta do BAI traz à volta da URL de
// confirmação.
//
// Não é paranóia: o exemplo da especificação mostra
// `"confirmationUrl": "\"https://...\""`, ou seja, o valor é uma string que
// contém aspas. Passá-la assim ao browser dá um endereço que não abre.
func limparURL(s string) string {
	return strings.Trim(strings.TrimSpace(s), `"`)
}

// itoa evita importar strconv só para isto.
func itoa(n int64) string {
	if n == 0 {
		return ""
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
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

// carrinhoDe monta o detalhe do que está a ser pago.
//
// É opcional para o BAI e manda-se na mesma: é o que o cliente vê antes de
// autorizar, e uma autorização sem saber do quê é uma autorização que se
// recusa. Sem linhas explícitas, monta-se uma a partir da descrição e do valor.
func carrinhoDe(req payment.ChargeRequest) *carrinho {
	if len(req.Items) > 0 {
		itens := make([]linha, 0, len(req.Items))
		for _, it := range req.Items {
			q := it.Quantity
			if q <= 0 {
				q = 1
			}
			itens = append(itens, linha{
				AmountPerItem: decimalJSON(it.UnitPrice),
				Description:   it.Description,
				Count:         q,
				Discount:      0,
				TotalAmount:   decimalJSON(it.Total()),
			})
		}
		return &carrinho{Items: itens}
	}
	nome := strings.TrimSpace(req.Description)
	if nome == "" {
		nome = "Pagamento"
	}
	return &carrinho{Items: []linha{{
		AmountPerItem: decimalJSON(req.Amount),
		Description:   nome,
		Count:         1,
		Discount:      0,
		TotalAmount:   decimalJSON(req.Amount),
	}}}
}
