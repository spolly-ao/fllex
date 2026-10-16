package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

var _ payment.StatementReader = (*Provider)(nil)

// O que a sandbox devolveu a 2026-10-16 (ids encurtados): uma cobrança de 120
// USD com 4,20 de comissão, um estorno parcial de 30, uma transferência.
const paginaUm = `{"data":[
 {"id":"txn_1","type":"charge","amount":12000,"fee":420,"net":11580,"currency":"usd","created":1792163227,
  "source":{"id":"ch_1","object":"charge","amount":12000,"currency":"usd","payment_intent":"pi_1","metadata":{}}},
 {"id":"txn_2","type":"refund","amount":-3000,"fee":0,"net":-3000,"currency":"usd","created":1792163245,
  "source":{"id":"re_1","object":"refund","amount":3000,"currency":"usd","payment_intent":"pi_1"}}
],"has_more":true}`

const paginaDois = `{"data":[
 {"id":"txn_3","type":"payment","amount":2000,"fee":90,"net":1910,"currency":"usd","created":1792163300,
  "source":{"id":"py_1","object":"charge","amount":1850,"currency":"eur","payment_intent":"pi_2"}},
 {"id":"txn_4","type":"payout","amount":-11000,"fee":0,"net":-11000,"currency":"usd","created":1792163400,"source":"po_1"},
 {"id":"txn_5","type":"stripe_fee","amount":-150,"fee":0,"net":-150,"currency":"usd","created":1792163500,"source":null},
 {"id":"txn_6","type":"charge","amount":500,"fee":10,"net":490,"currency":"usd","created":1792163600,"source":"ch_9"}
],"has_more":false}`

func servidorDoSaldo(t *testing.T, sessoes map[string]string) (*httptest.Server, *int) {
	t.Helper()
	pedidosDeSessao := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/balance_transactions":
			q := r.URL.Query()
			if q.Get("created[gte]") != "1792108800" || q.Get("created[lt]") != "1792195200" || q.Get("limit") != "100" || q.Get("expand[]") != "data.source" {
				t.Errorf("consulta = %v", q)
			}
			switch q.Get("starting_after") {
			case "":
				fmt.Fprint(w, paginaUm)
			case "txn_2":
				fmt.Fprint(w, paginaDois)
			default:
				t.Errorf("starting_after = %q", q.Get("starting_after"))
			}
		case "/v1/checkout/sessions":
			pedidosDeSessao++
			fmt.Fprint(w, sessoes[r.URL.Query().Get("payment_intent")])
		default:
			t.Errorf("caminho inesperado %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &pedidosDeSessao
}

func TestStatementDoStripe(t *testing.T) {
	srv, pedidos := servidorDoSaldo(t, map[string]string{
		"pi_1": `{"data":[{"id":"cs_1","metadata":{"reference":"pay_A"}}]}`,
		"pi_2": `{"data":[]}`,
	})
	p := New(Config{SecretKey: "sk_test_x", BaseURL: srv.URL})
	de := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	linhas, err := p.Statement(context.Background(), de, de.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(linhas) != 6 {
		t.Fatalf("linhas = %d", len(linhas))
	}
	c := linhas[0]
	if c.Type != payment.StatementCharge || c.ID != "txn_1" || c.SourceRef != "ch_1" || c.ChargeRef != "cs_1" || c.Reference != "pay_A" ||
		!c.Amount.Equal(money.New(12000, money.USD)) || !c.Fee.Equal(money.New(420, money.USD)) || c.OccurredAt.Unix() != 1792163227 {
		t.Fatalf("cobrança = %+v", c)
	}
	e := linhas[1]
	if e.Type != payment.StatementRefund || e.SourceRef != "re_1" || e.ChargeRef != "cs_1" || !e.Amount.Equal(money.New(-3000, money.USD)) {
		t.Fatalf("estorno = %+v", e)
	}
	// Convertida: o valor na moeda do comprador, sem a comissão em dólares.
	x := linhas[2]
	if x.Type != payment.StatementCharge || !x.Amount.Equal(money.New(1850, money.EUR)) || !x.Fee.Equal(money.Zero(money.EUR)) || x.ChargeRef != "" {
		t.Fatalf("convertida = %+v", x)
	}
	if l := linhas[3]; l.Type != payment.StatementPayout || l.SourceRef != "po_1" || !l.Amount.Equal(money.New(-11000, money.USD)) {
		t.Fatalf("transferência = %+v", l)
	}
	if l := linhas[4]; l.Type != payment.StatementAdjustment || !l.Amount.Equal(money.New(-150, money.USD)) || l.SourceRef != "" {
		t.Fatalf("acerto = %+v", l)
	}
	if l := linhas[5]; l.Type != payment.StatementCharge || l.SourceRef != "ch_9" || !l.Amount.Equal(money.New(500, money.USD)) || l.ChargeRef != "" {
		t.Fatalf("sem expansão = %+v", l)
	}
	// Uma pergunta por payment intent, e não uma por linha.
	if *pedidos != 2 {
		t.Fatalf("pedidos de sessão = %d", *pedidos)
	}
}

func TestStatementDoStripeFalhas(t *testing.T) {
	de := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	if _, err := New(Config{}).Statement(context.Background(), de, de); !errors.Is(err, payment.ErrNotConfigured) {
		t.Fatalf("sem chave = %v", err)
	}

	falha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"não"}}`)
	}))
	defer falha.Close()
	if _, err := New(Config{SecretKey: "sk_test_x", BaseURL: falha.URL}).Statement(context.Background(), de, de); err == nil {
		t.Fatal("a listagem falhada não deu erro")
	}

	sessaoFalha := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/checkout/sessions" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"não"}}`)
			return
		}
		fmt.Fprint(w, paginaUm[:len(paginaUm)-len(`"has_more":true}`)]+`"has_more":false}`)
	}))
	defer sessaoFalha.Close()
	if _, err := New(Config{SecretKey: "sk_test_x", BaseURL: sessaoFalha.URL}).Statement(context.Background(), de, de); err == nil {
		t.Fatal("a sessão falhada não deu erro: uma linha sem referência faria um pagamento verdadeiro parecer inventado")
	}

	vazia := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[],"has_more":true}`)
	}))
	defer vazia.Close()
	linhas, err := New(Config{SecretKey: "sk_test_x", BaseURL: vazia.URL}).Statement(context.Background(), de, de)
	if err != nil || len(linhas) != 0 {
		t.Fatalf("página vazia com has_more = %v %v", linhas, err)
	}
}
