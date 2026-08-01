package payment

import (
	"context"
	"time"

	"github.com/spolly-ao/fllex/money"
)

// Customer são os dados do pagador. Nem todos os gateways usam todos os campos:
// o Stripe quer o email, o MoMenu quer o NIF para a factura fiscal, o
// Multicaixa Express quer o telemóvel e o Proxypay DDS quer o nome e o IBAN.
type Customer struct {
	// ID é o identificador do cliente do lado de quem chama.
	ID string
	// Name é o nome ou a designação social.
	Name string
	// Email para recibos e notificações do gateway.
	Email string
	// Phone é o telemóvel. Nos métodos angolanos deve estar normalizado com
	// [phone.NormalizeAO].
	Phone string
	// TaxID é o NIF ou equivalente. O MoMenu só põe o nome na factura quando vem
	// um NIF real; sem ele a factura sai a "Consumidor Final".
	TaxID string
	// ProviderRef é o id do cliente no gateway, quando já existe (customer do
	// Stripe). Reutilizá-lo evita criar clientes duplicados a cada compra.
	ProviderRef string
	// Address e Country alimentam a factura e as regras fiscais.
	Address string
	Country string
	// IBAN é a conta a debitar, no débito directo.
	IBAN string
}

// LineItem é uma linha da factura fiscal. Os gateways angolanos emitem factura
// e exigem pelo menos uma linha: sem ela, o documento sai sem descrição do que
// foi vendido.
type LineItem struct {
	// Description é o que aparece na factura ("Plano Essencial, mensal").
	Description string
	// UnitPrice é o preço unitário.
	UnitPrice money.Amount
	// Quantity é a quantidade (1 na esmagadora maioria dos casos).
	Quantity int
	// TaxRate é a taxa de IVA em percentagem inteira (0 quando isento).
	TaxRate int
}

// Total é o valor da linha.
func (l LineItem) Total() money.Amount {
	q := int64(l.Quantity)
	if q <= 0 {
		q = 1
	}
	return l.UnitPrice.Mul(q)
}

// Mode distingue o que se está a comprar.
type Mode string

const (
	// ModePayment é um pagamento único: uma compra, um destaque, um
	// carregamento de saldo, uma prestação.
	ModePayment Mode = "payment"
	// ModeSubscription é uma subscrição recorrente, em que o provider passa a
	// cobrar sozinho nos ciclos seguintes (só faz sentido com cartão).
	ModeSubscription Mode = "subscription"
)

// ChargeRequest é o pedido de cobrança, comum a todos os providers. Cada
// provider usa o que lhe interessa e ignora o resto.
type ChargeRequest struct {
	// Reference é o identificador da cobrança do lado de quem chama (id da
	// encomenda, do pagamento ou da subscrição). Segue como metadados para o
	// gateway e é por ele que os webhooks se correlacionam de volta.
	Reference string

	// Amount é o valor a cobrar.
	Amount money.Amount

	// Method é o método pedido. Um provider que não o suporte devolve
	// [ErrUnsupportedMethod].
	Method Method

	// Mode distingue pagamento único de subscrição. Vazio lê-se como
	// [ModePayment].
	Mode Mode

	// Interval é a periodicidade, quando Mode é [ModeSubscription].
	Interval string

	// Description é o texto que o cliente vê na factura e no extracto.
	Description string

	// Items são as linhas da factura fiscal. Sem elas, o provider constrói uma
	// linha única a partir de Description e Amount.
	Items []LineItem

	// Customer são os dados do pagador.
	Customer Customer

	// SuccessURL e CancelURL são para onde o gateway devolve o cliente depois de
	// um checkout com redireccionamento.
	SuccessURL string
	CancelURL  string

	// ExpiresAt é o prazo de validade de uma referência. Nas renovações deve ser
	// o fecho da janela, não 24 horas: uma referência que morre no dia seguinte
	// deixa o cliente com um número que não pode pagar durante a tolerância que
	// lhe foi prometida.
	ExpiresAt *time.Time

	// MandateID é o mandato contra o qual apresentar um débito directo.
	MandateID string

	// PeriodStart e PeriodEnd são o período que esta cobrança paga. O débito
	// directo usa o início como data de cobrança.
	PeriodStart *time.Time
	PeriodEnd   *time.Time

	// Metadata são pares livres anexados à cobrança e devolvidos nos webhooks.
	Metadata map[string]string
}

// Kind diz como é que o cliente conclui o pagamento. É a informação que decide
// o que a interface mostra a seguir.
type Kind string

const (
	// KindRedirect: encaminhar o cliente para uma URL (Stripe Checkout).
	KindRedirect Kind = "redirect"
	// KindPaid: o pagamento ficou concluído na própria chamada (Multicaixa
	// Express, débito de carteira). Pode activar de imediato.
	KindPaid Kind = "paid"
	// KindReference: foi gerada uma referência bancária para o cliente pagar
	// depois. Confirma-se por consulta de estado ou webhook.
	KindReference Kind = "reference"
	// KindCode: foi gerado um código ou QR para o cliente confirmar na app
	// (eKwanza).
	KindCode Kind = "code"
	// KindPending: a instrução foi aceite e o desfecho chega mais tarde
	// (débito directo apresentado ao banco, transferência por confirmar).
	KindPending Kind = "pending"
)

// ChargeResult é o desfecho de uma cobrança.
type ChargeResult struct {
	// Kind diz o que fazer a seguir.
	Kind Kind

	// Status é o estado em que a cobrança fica.
	Status Status

	// ProviderRef é o identificador da operação no gateway, o que correlaciona
	// os webhooks de volta a esta cobrança.
	ProviderRef string

	// StatusRef é a referência a usar para consultar o estado, quando difere de
	// ProviderRef. No MoMenu, por exemplo, o webhook fala em transactionId e a
	// consulta de estado em operationId.
	StatusRef string

	// URL é para onde encaminhar o cliente ([KindRedirect]).
	URL string

	// Entity, Reference e DueDate são a referência bancária ([KindReference]).
	Entity    string
	Reference string
	DueDate   string

	// Code e QRCode são o código e a imagem para confirmar na app ([KindCode]).
	Code   string
	QRCode string

	// ExpiresAt é quando a referência ou o código deixam de ser aceites.
	ExpiresAt *time.Time

	// InvoiceURL é a factura fiscal, quando o gateway já a emite (MoMenu).
	InvoiceURL string

	// ExternalID é o identificador sequencial atribuído pelo gateway, quando
	// existe (o payment_id do Proxypay DDS dentro do mandato).
	ExternalID int

	// SubscriptionRef e CustomerRef são os identificadores criados no gateway
	// numa subscrição, para cancelar e para abrir o portal de gestão.
	SubscriptionRef string
	CustomerRef     string

	// Raw guarda a resposta original para diagnóstico e para campos que ainda
	// não têm lugar próprio aqui.
	Raw map[string]any
}

// ChargeStatus é o estado de uma cobrança consultada no gateway.
type ChargeStatus struct {
	// Status é o estado normalizado.
	Status Status
	// Paid é um atalho para Status == [StatusApproved].
	Paid bool
	// PaidAt é quando o dinheiro entrou, se o gateway o disser.
	PaidAt *time.Time
	// InvoiceURL e InvoiceNumber são a factura fiscal emitida pelo gateway.
	InvoiceURL    string
	InvoiceNumber string
	// Amount é o valor efectivamente recebido, quando o gateway o devolve.
	// Compará-lo com o esperado apanha pagamentos parciais.
	Amount *money.Amount
	// Raw guarda a resposta original.
	Raw map[string]any
}

// Provider é o contrato mínimo de um gateway de pagamentos. Tudo o que um
// gateway saiba fazer para além disto declara-se pelas capacidades opcionais
// abaixo, e descobre-se com uma asserção de tipo.
//
// O mínimo é deliberadamente pequeno: um gateway que só emita referências não
// deve ser obrigado a ter métodos de subscrição que devolvem "não suportado".
type Provider interface {
	// Name é o identificador estável do provider ("stripe", "momenu").
	Name() string

	// Methods são os métodos de pagamento que este provider sabe cobrar.
	Methods() []Method

	// SupportsCurrency indica se o provider processa esta moeda.
	SupportsCurrency(c money.Currency) bool

	// Configured indica se o provider tem a configuração necessária para
	// funcionar. Um provider por configurar continua registado (a aplicação
	// arranca), mas devolve [ErrNotConfigured] a quem tentar cobrar.
	Configured() bool

	// Charge inicia uma cobrança.
	Charge(ctx context.Context, req ChargeRequest) (ChargeResult, error)
}

// --- capacidades opcionais --------------------------------------------------

// Verifier é a capacidade de confirmar do lado do servidor se uma cobrança foi
// paga.
//
// É indispensável nos gateways cujo webhook não é assinado nem reentregue: aí a
// única confirmação de que se pode depender é a consulta de estado, e o webhook
// passa a ser apenas um acelerador (e mesmo assim volta a ser verificado).
//
// statusRef é a referência de consulta e merchantRef o identificador da ordem
// do nosso lado, que alguns gateways exigem para a identificar.
type Verifier interface {
	VerifyCharge(ctx context.Context, statusRef, merchantRef string) (ChargeStatus, error)
}

// Canceller é a capacidade de revogar uma cobrança ainda por pagar, para que
// deixe de poder ser paga. Sem isto, uma referência de uma subscrição já
// expirada continua viva no ATM e o cliente paga por algo que já não existe.
type Canceller interface {
	CancelCharge(ctx context.Context, req ChargeRequest, result ChargeResult) error
}

// Subscriber é a capacidade de gerir subscrições recorrentes no gateway (só o
// cartão a tem, na prática).
type Subscriber interface {
	// CancelSubscription cancela no gateway; com atPeriodEnd, deixa terminar no
	// fim do período já pago em vez de cortar de imediato.
	CancelSubscription(ctx context.Context, subscriptionRef string, atPeriodEnd bool) error
	// PortalURL devolve uma página onde o cliente gere a sua subscrição.
	PortalURL(ctx context.Context, customerRef, returnURL string) (string, error)
}

// WebhookParser é a capacidade de validar e normalizar os eventos que o gateway
// envia.
type WebhookParser interface {
	// ParseWebhook valida a assinatura e traduz o evento para o vocabulário
	// comum. Um evento que não interesse devolve Type [EventNone] em vez de erro:
	// os gateways enviam dezenas de tipos e ignorar os que não nos dizem
	// respeito é o comportamento normal, não uma falha.
	ParseWebhook(payload []byte, signature string) (*Event, error)
}

// Refund é o pedido de devolução de dinheiro já cobrado.
type Refund struct {
	// ChargeRef é a cobrança a estornar.
	ChargeRef string
	// Amount é quanto devolver. Zero devolve tudo.
	Amount money.Amount
	// Reason é o motivo, registado no gateway para consulta posterior.
	Reason string
}

// RefundResult é o desfecho de um estorno.
type RefundResult struct {
	// RefundRef é o identificador do estorno no gateway.
	RefundRef string
	// Status é o estado do estorno. "pending" é normal em vários métodos e não
	// exige repetição: o gateway avisa por webhook quando liquidar.
	Status Status
	// Amount é quanto foi efectivamente devolvido.
	Amount money.Amount
}

// Refunder é a capacidade de devolver dinheiro.
type Refunder interface {
	Refund(ctx context.Context, r Refund) (RefundResult, error)
}

// BalanceEntry é o saldo do comerciante numa moeda.
type BalanceEntry struct {
	Currency  money.Currency
	Available money.Amount
	Pending   money.Amount
}

// Reporter é a capacidade de ler dados financeiros do gateway, para painéis de
// tesouraria e reconciliação.
type Reporter interface {
	Balance(ctx context.Context) ([]BalanceEntry, error)
}
