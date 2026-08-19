package emis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// ErrReferenceRequired diz que a cobrança seguiu sem referência.
//
// A EMIS não gera identificador nenhum que volte no callback: o que volta é o
// que lhe mandámos, dentro de `reference.id`. Uma cobrança sem referência é um
// pagamento que, quando for pago, ninguém consegue ligar a coisa nenhuma.
var ErrReferenceRequired = errors.New("emis: a cobrança precisa de uma referência")

// Os valores que o callback usa. São o vocabulário da EMIS e não o nosso.
const (
	estadoAceite  = "ACCEPTED"
	tipoPagamento = "PAYMENT"
)

// Provider implementa [payment.Provider] sobre o gateway de pagamentos online
// da EMIS.
type Provider struct {
	client *Client
}

// New cria o provider da EMIS.
func New(cfg Config) *Provider { return &Provider{client: NewClient(cfg)} }

// NewWithClient cria o provider sobre um cliente já construído.
func NewWithClient(c *Client) *Provider { return &Provider{client: c} }

// Client dá acesso ao cliente, para quem precise da URL do frame sem passar
// por uma cobrança.
func (p *Provider) Client() *Client { return p.client }

// Name devolve "emis".
func (p *Provider) Name() string { return "emis" }

// Methods: só Multicaixa Express.
//
// O frame também sabe aceitar o cartão Multicaixa quando [Config.Card] está
// ligado, e mesmo assim isso não entra aqui: é o cartão de débito nacional a
// pagar o mesmo pedido dentro do mesmo frame, e não o [payment.MethodCard] da
// biblioteca, que é cartão guardado com cobrança recorrente. Anunciá-lo como
// método punha o checkout a oferecer um botão que promete o que não faz.
func (p *Provider) Methods() []payment.Method {
	return []payment.Method{payment.MethodMCX}
}

// SupportsCurrency: o Multicaixa só processa kwanza.
func (p *Provider) SupportsCurrency(c money.Currency) bool {
	return money.NormalizeCurrency(string(c)) == money.AOA
}

// Configured indica se há token de comerciante.
func (p *Provider) Configured() bool { return p.client.Configured() }

// Charge pede o token do frame e devolve o endereço que o cliente abre para
// confirmar o pagamento no telemóvel.
//
// O desfecho é [payment.KindRedirect] e o estado fica pendente, ao contrário do
// Multicaixa Express do MoMenu, que devolve [payment.KindPaid] porque a chamada
// dele fica à espera da confirmação. Aqui a chamada devolve em menos de um
// segundo e quem espera é o cliente, à frente do frame.
func (p *Provider) Charge(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	if !p.Configured() {
		return payment.ChargeResult{}, payment.ErrNotConfigured
	}
	if req.Method != "" && req.Method != payment.MethodMCX {
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

	token, bruto, err := p.client.FrameToken(ctx, referencia, req.Amount.Decimal())
	if err != nil {
		return payment.ChargeResult{}, err
	}

	out := payment.ChargeResult{
		Kind:   payment.KindRedirect,
		Status: payment.StatusPending,
		// O token do frame é o que a EMIS nos deu para esta operação. Note-se
		// que **não volta no callback**: o que volta é a referência. Quem
		// correlaciona eventos correlaciona por ela.
		ProviderRef: token,
		Reference:   referencia,
		URL:         p.client.FrameURL(token),
		Raw:         bruto,
	}
	// A EMIS não diz quanto tempo o token vive, e inventar um prazo era
	// prometer ao cliente uma janela que ninguém garante. Só se marca validade
	// quando quem chama a definiu.
	if req.ExpiresAt != nil {
		quando := *req.ExpiresAt
		out.ExpiresAt = &quando
	}
	return out, nil
}

// ParseWebhook lê o desfecho que a EMIS envia para a `CallbackURL`.
//
// **Não há assinatura para verificar.** A EMIS envia um POST em claro, e o
// gateway online não tem consulta de estado que sirva de segunda opinião. O que
// este método faz, e é tudo o que se pode fazer, é recusar o que não bate certo
// com a configuração desta conta.
//
// O que não passa devolve [payment.EventNone] em vez de erro: um erro faria o
// gateway reenviar em ciclo, e não há reenvio que corrija um evento que não é
// nosso.
func (p *Provider) ParseWebhook(payload []byte, _ string) (*payment.Event, error) {
	ev := &payment.Event{Type: payment.EventNone, Provider: p.Name(), Status: payment.StatusPending}

	var cb Callback
	if err := json.Unmarshal(payload, &cb); err != nil {
		return ev, nil
	}
	var bruto map[string]any
	_ = json.Unmarshal(payload, &bruto)

	ev.ID = cb.ID
	ev.ChargeRef = cb.ID
	ev.Reference = strings.TrimSpace(cb.Reference.ID.String())
	ev.Method = payment.MethodMCX
	ev.Raw = bruto

	// O valor vai preenchido mesmo quando o evento acaba por ser ignorado.
	// Quem confirma compara-o com o que cobrou, e é essa comparação que apanha
	// um callback com a referência certa e o valor errado.
	if v, err := money.Parse(cb.Amount.String(), money.AOA); err == nil {
		ev.Amount = &v
	}

	// O ponto de venda é o único campo que diz se este evento é desta conta.
	// Sem ele configurado não há filtro nenhum, e um provider que aceita tudo
	// aceita também o que um estranho lhe mandar: por isso, sem ponto de venda,
	// não se aceita nada. O `services/core` recusa guardar a credencial sem
	// este campo, para que este caso não chegue a existir em produção.
	pos := strings.TrimSpace(p.client.Config().POSID)
	if pos == "" || pos != strings.TrimSpace(cb.PointOfSale.ID.String()) {
		return ev, nil
	}
	if !strings.EqualFold(strings.TrimSpace(cb.Currency), string(money.AOA)) {
		return ev, nil
	}
	if !strings.EqualFold(strings.TrimSpace(cb.TransactionType), tipoPagamento) {
		return ev, nil
	}

	// Só o aceite move alguma coisa.
	//
	// Uma recusa **não** marca a cobrança como falhada, e é deliberado: o
	// cliente que erra o PIN ou deixa passar o tempo continua com o mesmo frame
	// aberto e tenta outra vez, e a EMIS envia um callback por tentativa.
	// Fechar a cobrança à primeira recusa era impedir a segunda tentativa de
	// contar. Quem nunca paga é apanhado pela expiração, que é para isso que
	// ela existe.
	if !strings.EqualFold(strings.TrimSpace(cb.Status), estadoAceite) {
		return ev, nil
	}

	ev.Type = payment.EventChargeSucceeded
	ev.Status = payment.StatusApproved
	return ev, nil
}
