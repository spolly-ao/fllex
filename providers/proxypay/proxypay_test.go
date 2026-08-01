package proxypay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

func TestChargeGeneratesAndBindsReference(t *testing.T) {
	var bound map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Token chave" {
			t.Errorf("autenticação = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/reference_ids":
			fmt.Fprint(w, `"123456789"`)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/references/"):
			_ = json.NewDecoder(r.Body).Decode(&bound)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("pedido inesperado: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Entity: "01234"})
	expires := time.Date(2025, time.August, 5, 23, 59, 59, 0, time.UTC)
	res, err := p.Charge(context.Background(), payment.ChargeRequest{
		Reference: "cobranca-7",
		Amount:    money.FromMajor(5900, money.AOA),
		Method:    payment.MethodReference,
		ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatalf("cobrança falhou: %v", err)
	}
	if res.Reference != "123456789" || res.Entity != "01234" {
		t.Errorf("resultado = %+v", res)
	}
	// O valor vai como decimal com duas casas, que é o formato exigido.
	if bound["amount"] != "5900.00" {
		t.Errorf("valor enviado = %v, queria \"5900.00\"", bound["amount"])
	}
	// A validade tem de ser a que pedimos, e não um valor por omissão: numa
	// renovação é ela que mantém a referência viva durante toda a tolerância.
	if got, _ := bound["end_datetime"].(string); !strings.HasPrefix(got, "2025-08-05") {
		t.Errorf("validade enviada = %v, queria 2025-08-05", bound["end_datetime"])
	}
	// A nossa referência tem de seguir nos campos personalizados, senão a
	// confirmação não sabe a que cobrança pertence.
	fields, _ := bound["custom_fields"].(map[string]any)
	if fields == nil || fields["invoice"] != "cobranca-7" {
		t.Errorf("campos personalizados = %v", bound["custom_fields"])
	}
}

func TestChargeDeletesReferenceWhenBindingFails(t *testing.T) {
	// Uma referência emitida e sem valor associado aparece como inválida no
	// ATM: apaga-se para não deixar um número morto.
	deleted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			fmt.Fprint(w, `"123456789"`)
		case http.MethodPut:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"message":"invalid amount"}`)
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Entity: "01234"})
	_, err := p.Charge(context.Background(), payment.ChargeRequest{
		Amount: money.FromMajor(5900, money.AOA), Method: payment.MethodReference,
	})
	if err == nil {
		t.Fatal("queria o erro do gateway")
	}
	if !deleted {
		t.Error("a referência órfã devia ter sido apagada")
	}
}

func TestVerifyChargeFindsPaymentInQueue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[{"id":42,"reference_id":"123456789","amount":"5900.00",
			"datetime":"2025-08-01T10:00:00Z","custom_fields":{"invoice":"cobranca-7"}}]`)
	}))
	defer srv.Close()

	p := New(Config{APIKey: "chave", BaseURL: srv.URL, Entity: "01234"})
	st, err := p.VerifyCharge(context.Background(), "123456789", "")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Paid {
		t.Error("a referência estava na fila de pagos")
	}
	if st.Amount == nil || st.Amount.Minor != money.FromMajor(5900, money.AOA).Minor {
		t.Errorf("valor recebido = %v", st.Amount)
	}

	st, err = p.VerifyCharge(context.Background(), "outra-referencia", "")
	if err != nil {
		t.Fatal(err)
	}
	if st.Paid {
		t.Error("uma referência que não está na fila não está paga")
	}
}

// --- escoamento da fila --------------------------------------------------------

func TestDrainerAppliesBeforeAcknowledging(t *testing.T) {
	// A ordem é o que evita perder dinheiro: confirmar antes de aplicar deixa o
	// pagamento fora da fila e sem efeito nenhum do nosso lado.
	var mu sync.Mutex
	var order []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `[{"id":42,"reference_id":"123456789","amount":"5900.00",
				"datetime":"2025-08-01T10:00:00Z","custom_fields":{"invoice":"cobranca-7"}}]`)
		case http.MethodDelete:
			order = append(order, "acknowledge")
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	d := &Drainer{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL}),
		Apply: func(_ context.Context, p ConfirmedPayment, reference string, amount money.Amount) error {
			mu.Lock()
			order = append(order, "apply")
			mu.Unlock()
			if reference != "cobranca-7" {
				t.Errorf("referência = %q, queria cobranca-7", reference)
			}
			if amount.Minor != money.FromMajor(5900, money.AOA).Minor {
				t.Errorf("valor = %s", amount)
			}
			return nil
		},
	}
	d.Run(context.Background())

	if len(order) != 2 || order[0] != "apply" || order[1] != "acknowledge" {
		t.Errorf("ordem = %v, queria aplicar antes de confirmar", order)
	}
}

func TestDrainerKeepsPaymentInQueueOnFailure(t *testing.T) {
	acknowledged := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, `[{"id":42,"reference_id":"123456789","amount":"5900.00"}]`)
		case http.MethodDelete:
			acknowledged = true
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	d := &Drainer{
		Client: NewClient(Config{APIKey: "chave", BaseURL: srv.URL}),
		Apply: func(context.Context, ConfirmedPayment, string, money.Amount) error {
			return fmt.Errorf("base de dados em baixo")
		},
	}
	d.Run(context.Background())

	// Fica na fila para a passagem seguinte: mais vale repetir do que retirar
	// um pagamento cujo efeito não aconteceu.
	if acknowledged {
		t.Error("um pagamento que falhou a aplicar não pode sair da fila")
	}
}

func TestConfirmedPaymentField(t *testing.T) {
	p := ConfirmedPayment{CustomFields: map[string]any{
		"invoice": "cobranca-7",
		"numero":  float64(42),
	}}
	if got := p.Field("invoice"); got != "cobranca-7" {
		t.Errorf("campo de texto = %q", got)
	}
	// Os campos personalizados voltam do JSON como números; ler um id
	// numérico como "42" e não "4.2e+01" é o que faz a correlação funcionar.
	if got := p.Field("numero"); got != "42" {
		t.Errorf("campo numérico = %q, queria 42", got)
	}
	if got := p.Field("inexistente"); got != "" {
		t.Errorf("campo em falta = %q, queria vazio", got)
	}
}
