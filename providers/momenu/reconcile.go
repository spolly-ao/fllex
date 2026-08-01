package momenu

import (
	"context"
	"log/slog"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/phone"
)

// Attempt é o registo de uma tentativa de Multicaixa Express que ficou sem
// resposta.
//
// Escreva-o antes de chamar [Client.InitMCX] e feche-o depois. É o único rasto
// que fica quando a resposta se perde, e sem ele não há como saber que houve
// sequer um pagamento a acontecer.
type Attempt struct {
	// ID é o identificador do registo.
	ID string
	// Reference é a cobrança do lado de quem chama.
	Reference string
	// Phone é o telemóvel para onde foi o pedido de confirmação.
	Phone string
	// Amount é o valor pedido.
	Amount money.Amount
	// StartedAt é quando o pedido foi feito.
	StartedAt time.Time
}

// Match é uma correspondência encontrada entre uma tentativa e uma factura.
type Match struct {
	// Attempt é a tentativa em causa.
	Attempt Attempt
	// Invoice é a factura que a comprova.
	Invoice InvoiceDetail
}

// Reconciler recupera os pagamentos por Multicaixa Express cuja resposta se
// perdeu.
//
// O problema que resolve é concreto e caro: o Multicaixa Express não tem
// webhook, e a resposta HTTP é o único sinal de que o pagamento passou. Se essa
// resposta se perde (o processo morre, um proxy corta ao fim de 60 segundos, o
// cliente fecha o separador e a ligação cai), o MoMenu cobrou o dinheiro e
// emitiu a factura, e o nosso lado não sabe de nada. Sem reconciliação, o
// cliente é cobrado e não recebe nada, e a única forma de o descobrir é ele
// reclamar.
//
// Como funciona: para cada tentativa por fechar, procura entre as facturas
// recentes do MoMenu uma que corresponda ao telemóvel, ao valor e à janela de
// tempo. Uma correspondência única promove o pagamento; nenhuma tenta outra
// vez mais tarde; mais do que uma é registada para resolução manual, porque
// promover a errada é pior do que não promover nenhuma.
//
// A correlação é aproximada porque não há alternativa: o Multicaixa Express não
// aceita uma chave de idempotência nossa, e a factura não traz nenhuma
// referência que controlemos. Os únicos dados partilhados entre os dois lados
// são o valor, o telemóvel e a hora. Fica robusta exigindo valor exacto,
// comparando só os dígitos do telemóvel e apertando bem a janela de tempo.
type Reconciler struct {
	// Client fala com o MoMenu.
	Client *Client

	// Pending devolve as tentativas por fechar com mais do que a idade mínima.
	Pending func(ctx context.Context, olderThan time.Duration, limit int) ([]Attempt, error)

	// Confirm promove um pagamento reconciliado. Tem de ser idempotente: pode
	// correr ao mesmo tempo que um operador promove a mesma cobrança à mão.
	Confirm func(ctx context.Context, m Match) error

	// Abandon é chamado quando uma tentativa passa a idade máxima sem
	// correspondência. A partir daqui assume-se que o pagamento não aconteceu;
	// o registo fica como rasto para uma recuperação manual, se mais tarde
	// aparecer no painel do MoMenu.
	Abandon func(ctx context.Context, a Attempt) error

	// MinAge é a idade mínima de uma tentativa para ser considerada. Zero usa 4
	// minutos.
	//
	// Serve para não correr contra a resposta HTTP normal, que pode ainda estar
	// a caminho: promover um pagamento que a chamada normal está prestes a
	// confirmar duplica o efeito.
	MinAge time.Duration

	// MaxAge é quanto tempo se insiste antes de desistir. Zero usa 24 horas.
	MaxAge time.Duration

	// Window é a folga de tempo, para cada lado, em que se procura a factura.
	// Zero usa 15 minutos.
	//
	// O MoMenu emite a factura logo a seguir à confirmação, tipicamente em
	// segundos; a folga cobre a diferença de relógios e um atraso interno.
	Window time.Duration

	// BatchSize limita quantas tentativas cada passagem trata. Zero usa 10.
	BatchSize int

	// PageSize é quantas facturas se pedem por página. Zero usa 50, o máximo do
	// MoMenu.
	PageSize int

	// Log recebe o que corre mal.
	Log *slog.Logger
}

// Run faz uma passagem de reconciliação.
func (r *Reconciler) Run(ctx context.Context) {
	if r.Client == nil || r.Pending == nil || r.Confirm == nil {
		r.log().Warn("fllex: reconciliação do Multicaixa Express sem dependências, não corre")
		return
	}

	attempts, err := r.Pending(ctx, r.minAge(), r.batchSize())
	if err != nil {
		r.log().Error("fllex: falha a obter tentativas por fechar", "err", err)
		return
	}
	if len(attempts) == 0 {
		return
	}

	invoices, err := r.Client.ListInvoices(ctx, r.pageSize(), 0)
	if err != nil {
		r.log().Error("fllex: falha a listar facturas do MoMenu", "err", err)
		return
	}
	if invoices == nil || len(invoices.Invoices) == 0 {
		return
	}

	for _, a := range attempts {
		if err := ctx.Err(); err != nil {
			return
		}
		r.reconcileOne(ctx, a, invoices.Invoices)
	}
}

func (r *Reconciler) reconcileOne(ctx context.Context, a Attempt, invoices []InvoiceListItem) {
	if time.Since(a.StartedAt) > r.maxAge() {
		r.log().Warn("fllex: tentativa de Multicaixa Express abandonada, recuperação manual se aparecer",
			"reference", a.Reference, "started_at", a.StartedAt)
		if r.Abandon != nil {
			if err := r.Abandon(ctx, a); err != nil {
				r.log().Error("fllex: falha a abandonar tentativa", "err", err, "reference", a.Reference)
			}
		}
		return
	}

	// Primeiro filtra-se pelo valor e pela janela de tempo, que é barato, e só
	// depois se pede o detalhe de cada candidata, que custa uma chamada por
	// factura e é onde está o telemóvel.
	from := a.StartedAt.Add(-r.window())
	to := a.StartedAt.Add(r.window())
	var shortlist []InvoiceListItem
	for _, inv := range invoices {
		if !amountMatches(inv.Total, a.Amount) {
			continue
		}
		ts, ok := parseDate(inv.CreatedAt)
		if !ok || ts.Before(from) || ts.After(to) {
			continue
		}
		shortlist = append(shortlist, inv)
	}
	if len(shortlist) == 0 {
		return
	}

	var matches []InvoiceDetail
	for _, item := range shortlist {
		detail, err := r.Client.GetInvoice(ctx, item.InvoiceID)
		if err != nil {
			r.log().Warn("fllex: falha a obter detalhe de factura", "err", err, "invoice", item.InvoiceID)
			continue
		}
		if detail == nil || !detail.Success {
			continue
		}
		if !phone.SameAO(detail.Invoice.Customer.Phone, a.Phone) {
			continue
		}
		matches = append(matches, detail.Invoice)
	}

	switch len(matches) {
	case 0:
		return // tenta outra vez na passagem seguinte
	case 1:
		r.log().Warn("fllex: pagamento por Multicaixa Express recuperado por reconciliação",
			"reference", a.Reference, "invoice", matches[0].InvoiceNumber, "amount", a.Amount.String())
		if err := r.Confirm(ctx, Match{Attempt: a, Invoice: matches[0]}); err != nil {
			r.log().Error("fllex: correspondência encontrada mas a confirmação falhou, repete na próxima passagem",
				"err", err, "reference", a.Reference, "invoice", matches[0].InvoiceID)
		}
	default:
		// Promover a errada cobra a encomenda de outra pessoa. Fica para
		// resolução manual, que é o que uma ambiguidade merece.
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.InvoiceID
		}
		r.log().Warn("fllex: reconciliação ambígua, várias facturas correspondem, deixada para resolução manual",
			"reference", a.Reference, "invoices", ids)
	}
}

// amountMatches compara o total da factura com o valor pedido.
//
// O MoMenu devolve os totais em kwanzas inteiros e a nossa representação tem
// subunidade, por isso a comparação é feita na unidade maior, com uma tolerância
// de uma unidade menor para absorver a formatação em vírgula flutuante.
func amountMatches(invoiceTotal float64, want money.Amount) bool {
	ours := want.Float()
	diff := invoiceTotal - ours
	if diff < 0 {
		diff = -diff
	}
	return diff <= 1.0/float64(want.Currency.Factor())
}

func (r *Reconciler) minAge() time.Duration {
	if r.MinAge > 0 {
		return r.MinAge
	}
	return 4 * time.Minute
}

func (r *Reconciler) maxAge() time.Duration {
	if r.MaxAge > 0 {
		return r.MaxAge
	}
	return 24 * time.Hour
}

func (r *Reconciler) window() time.Duration {
	if r.Window > 0 {
		return r.Window
	}
	return 15 * time.Minute
}

func (r *Reconciler) batchSize() int {
	if r.BatchSize > 0 {
		return r.BatchSize
	}
	return 10
}

func (r *Reconciler) pageSize() int {
	if r.PageSize > 0 {
		return r.PageSize
	}
	return 50
}

func (r *Reconciler) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}
