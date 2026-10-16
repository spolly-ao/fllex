package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// BalanceTransaction é uma linha do saldo do Stripe: uma cobrança, um
// estorno, uma transferência para o banco, uma comissão.
type BalanceTransaction struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Amount   int64  `json:"amount"`
	Fee      int64  `json:"fee"`
	Net      int64  `json:"net"`
	Currency string `json:"currency"`
	Created  int64  `json:"created"`
	// Source é o objecto que originou a linha: expandido (um objecto) quando
	// se pede expand[]=data.source, e só o id (um texto) quando não.
	Source json.RawMessage `json:"source"`
}

// BalanceTransactionList é uma página de linhas do saldo.
type BalanceTransactionList struct {
	Data    []BalanceTransaction `json:"data"`
	HasMore bool                 `json:"has_more"`
}

// BalanceTransactions devolve uma página das linhas do saldo criadas entre
// from (incluído) e to (excluído), com a origem expandida. startingAfter é o
// id da última linha da página anterior, vazio na primeira.
func (c *Client) BalanceTransactions(ctx context.Context, from, to time.Time, startingAfter string) (*BalanceTransactionList, error) {
	q := url.Values{}
	q.Set("created[gte]", strconv.FormatInt(from.Unix(), 10))
	q.Set("created[lt]", strconv.FormatInt(to.Unix(), 10))
	q.Set("limit", "100")
	q.Add("expand[]", "data.source")
	if startingAfter != "" {
		q.Set("starting_after", startingAfter)
	}
	var out BalanceTransactionList
	if err := c.get(ctx, "/v1/balance_transactions?"+q.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SessionForPaymentIntent devolve a sessão de checkout que criou o payment
// intent, com os metadados, ou nil quando não foi criado por uma sessão.
func (c *Client) SessionForPaymentIntent(ctx context.Context, paymentIntent string) (*SessionWithMetadata, error) {
	var out struct {
		Data []SessionWithMetadata `json:"data"`
	}
	if err := c.get(ctx, "/v1/checkout/sessions?payment_intent="+url.QueryEscape(paymentIntent), &out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, nil
	}
	return &out.Data[0], nil
}

// SessionWithMetadata é uma sessão de checkout com os metadados.
type SessionWithMetadata struct {
	ID       string            `json:"id"`
	Metadata map[string]string `json:"metadata"`
}

// origem é a parte da origem de uma linha do saldo que interessa: a cobrança
// ou o estorno, com o valor na moeda em que o comprador pagou.
type origem struct {
	ID            string `json:"id"`
	Object        string `json:"object"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	PaymentIntent string `json:"payment_intent"`
}

func lerOrigem(raw json.RawMessage) origem {
	var o origem
	if json.Unmarshal(raw, &o) == nil {
		return o
	}
	// Sem expansão, a origem é só o id.
	var id string
	_ = json.Unmarshal(raw, &id)
	return origem{ID: id}
}

// Statement devolve o relatório do período pelas linhas do saldo do Stripe
// ([payment.StatementReader]).
//
// O valor de uma cobrança ou de um estorno é o da origem, na moeda em que o
// comprador pagou, e não o da linha do saldo: o Stripe converte para a moeda
// da conta quando são diferentes, e quem compara com o que cobrou tem de
// comparar na moeda em que cobrou. A comissão só vem quando as duas moedas são
// a mesma; convertida, fica no Raw.
//
// A cobrança do Stripe não guarda os metadados da sessão de checkout, onde está
// a nossa referência: verificado na sandbox a 2026-10-16, a cobrança vem com os
// metadados vazios. Por isso cada payment intent pergunta pela sessão que o
// criou, uma vez por período, e é dela que saem o ChargeRef (o id da sessão,
// que é o [payment.ChargeResult.ProviderRef] deste provider) e a referência.
func (p *Provider) Statement(ctx context.Context, from, to time.Time) ([]payment.StatementEntry, error) {
	if !p.Configured() {
		return nil, payment.ErrNotConfigured
	}
	sessoes := map[string]*SessionWithMetadata{}
	var out []payment.StatementEntry
	depois := ""
	for {
		pagina, err := p.client.BalanceTransactions(ctx, from, to, depois)
		if err != nil {
			return nil, err
		}
		for _, bt := range pagina.Data {
			linha, err := p.linhaDoSaldo(ctx, bt, sessoes)
			if err != nil {
				return nil, err
			}
			out = append(out, linha)
			depois = bt.ID
		}
		if !pagina.HasMore || len(pagina.Data) == 0 {
			return out, nil
		}
	}
}

func (p *Provider) linhaDoSaldo(ctx context.Context, bt BalanceTransaction, sessoes map[string]*SessionWithMetadata) (payment.StatementEntry, error) {
	moeda := money.NormalizeCurrency(strings.ToUpper(bt.Currency))
	o := lerOrigem(bt.Source)
	linha := payment.StatementEntry{
		ID: bt.ID, SourceRef: o.ID,
		Amount: money.New(bt.Amount, moeda), Fee: money.New(bt.Fee, moeda),
		OccurredAt: time.Unix(bt.Created, 0).UTC(),
		Raw:        map[string]any{"type": bt.Type, "amount": bt.Amount, "fee": bt.Fee, "net": bt.Net, "currency": bt.Currency},
	}
	sinal := int64(1)
	switch bt.Type {
	case "charge", "payment":
		linha.Type = payment.StatementCharge
	case "refund", "payment_refund":
		linha.Type, sinal = payment.StatementRefund, -1
	case "payout":
		linha.Type = payment.StatementPayout
		return linha, nil
	default:
		linha.Type = payment.StatementAdjustment
		return linha, nil
	}
	if o.Currency != "" {
		origemMoeda := money.NormalizeCurrency(strings.ToUpper(o.Currency))
		linha.Amount = money.New(sinal*o.Amount, origemMoeda)
		if origemMoeda != moeda {
			linha.Fee = money.Zero(origemMoeda)
		}
	}
	if o.PaymentIntent == "" {
		return linha, nil
	}
	s, visto := sessoes[o.PaymentIntent]
	if !visto {
		var err error
		s, err = p.client.SessionForPaymentIntent(ctx, o.PaymentIntent)
		if err != nil {
			return payment.StatementEntry{}, fmt.Errorf("stripe: a sessão do payment intent %s: %w", o.PaymentIntent, err)
		}
		sessoes[o.PaymentIntent] = s
	}
	if s != nil {
		linha.ChargeRef = s.ID
		linha.Reference = s.Metadata[p.ReferenceKey]
	}
	return linha, nil
}
