package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/payment"
)

func newClient(url string) *Client {
	c := New("teste", url+"/", 5*time.Second)
	c.RetryWait = time.Millisecond
	return c
}

func TestNewTrimsTrailingSlash(t *testing.T) {
	// Uma barra a mais na configuração dá pedidos para "//api", que alguns
	// servidores tratam como um caminho diferente.
	c := New("teste", "  https://exemplo.ao/  ", time.Second)
	if c.BaseURL != "https://exemplo.ao" {
		t.Errorf("base = %q", c.BaseURL)
	}
	if c.HTTP.Timeout != time.Second {
		t.Errorf("timeout = %v", c.HTTP.Timeout)
	}
}

func TestSetHeaderAppliesToEveryRequest(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	c := newClient(srv.URL).SetHeader("x-api-key", "chave").SetHeader("Accept", "application/json")
	if err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"}); err != nil {
		t.Fatal(err)
	}
	if got.Get("x-api-key") != "chave" || got.Get("Accept") != "application/json" {
		t.Errorf("cabeçalhos = %v", got)
	}

	// Um cliente construído sem cabeçalhos aceita o primeiro sem entrar em
	// pânico.
	bare := &Client{Provider: "x"}
	bare.SetHeader("a", "b")
	if bare.Header.Get("a") != "b" {
		t.Error("SetHeader devia criar o mapa de cabeçalhos")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	type in struct {
		Amount int `json:"amount"`
	}
	type out struct {
		OK bool `json:"ok"`
	}
	var body in
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("tipo de conteúdo = %q", ct)
		}
		_ = decodeJSON(r.Body, &body)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	var res out
	err := newClient(srv.URL).Do(context.Background(), Request{
		Method: http.MethodPost, Path: "/pagar", JSON: in{Amount: 5900}, Out: &res,
	})
	if err != nil {
		t.Fatal(err)
	}
	if body.Amount != 5900 || !res.OK {
		t.Errorf("enviado = %+v, recebido = %+v", body, res)
	}
}

func TestFormEncoding(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("tipo de conteúdo = %q", ct)
		}
		if u, _, _ := r.BasicAuth(); u != "sk_teste" {
			t.Errorf("autenticação = %q", u)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	form := map[string][]string{"amount": {"1500"}}
	err := newClient(srv.URL).Do(context.Background(), Request{
		Method: http.MethodPost, Path: "/x", Form: form, BasicAuth: "sk_teste",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "amount=1500" {
		t.Errorf("corpo = %q", got)
	}
}

func TestRawBodyAndRawOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != "bytes em bruto" {
			t.Errorf("corpo = %q", b)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("tipo de conteúdo = %q", ct)
		}
		_, _ = w.Write([]byte("%PDF-1.4"))
	}))
	defer srv.Close()

	var raw []byte
	err := newClient(srv.URL).Do(context.Background(), Request{
		Method: http.MethodPost, Path: "/x", Body: []byte("bytes em bruto"), RawOut: &raw,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "%PDF-1.4" {
		t.Errorf("resposta em bruto = %q", raw)
	}
}

func TestPerRequestHeaderOverrides(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Accept")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	c := newClient(srv.URL).SetHeader("Accept", "application/json")
	err := c.Do(context.Background(), Request{
		Method: http.MethodGet, Path: "/x",
		Header: http.Header{"Accept": {"application/pdf"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "application/pdf" {
		t.Errorf("o cabeçalho do pedido devia sobrepor-se ao do cliente: %q", got)
	}
}

func TestEmptyResponseBodyIsNotAnError(t *testing.T) {
	// O Proxypay responde 204 sem corpo a várias operações. Tentar ler JSON de
	// um corpo vazio não pode dar erro.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var out map[string]any
	if err := newClient(srv.URL).Do(context.Background(), Request{
		Method: http.MethodPut, Path: "/x", Out: &out,
	}); err != nil {
		t.Fatalf("resposta vazia devolveu erro: %v", err)
	}
}

func TestGatewayErrorShapes(t *testing.T) {
	// Cada gateway devolve o erro com a sua forma. Perder a mensagem deixa
	// quem faz suporte só com um número de estado.
	tests := []struct {
		name     string
		body     string
		wantMsg  string
		wantCode string
	}{
		{"message", `{"message":"amount too small"}`, "amount too small", ""},
		{"detail", `{"detail":"conta inexistente"}`, "conta inexistente", ""},
		{"objecto error", `{"error":{"message":"chave inválida","code":"api_key_invalid"}}`, "chave inválida", "api_key_invalid"},
		{"texto error", `{"error":"em manutenção"}`, "em manutenção", ""},
		{"lista errors", `{"errors":[{"message":"iban inválido","code":"AC04"}]}`, "iban inválido", "AC04"},
		{"texto simples", `avaria geral`, "avaria geral", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			err := newClient(srv.URL).Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"})
			var ge *payment.GatewayError
			if !errors.As(err, &ge) {
				t.Fatalf("erro = %v", err)
			}
			if ge.Message != tt.wantMsg {
				t.Errorf("mensagem = %q, queria %q", ge.Message, tt.wantMsg)
			}
			if ge.Code != tt.wantCode {
				t.Errorf("código = %q, queria %q", ge.Code, tt.wantCode)
			}
			if ge.StatusCode != http.StatusBadRequest || ge.Provider != "teste" {
				t.Errorf("erro = %+v", ge)
			}
		})
	}
}

func TestRetriesOnlyIdempotentRequests(t *testing.T) {
	// Repetir uma cobrança às cegas é cobrar duas vezes. Só se repete o que
	// quem chama disser que é seguro repetir.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"message":"indisponível"}`)
	}))
	defer srv.Close()

	c := newClient(srv.URL)
	c.MaxRetries = 2

	hits = 0
	_ = c.Do(context.Background(), Request{Method: http.MethodPost, Path: "/cobrar"})
	if hits != 1 {
		t.Errorf("uma escrita foi tentada %d vezes, queria 1", hits)
	}

	hits = 0
	_ = c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/estado", Idempotent: true})
	if hits != 3 {
		t.Errorf("uma leitura foi tentada %d vezes, queria 3", hits)
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	// Um 400 dá sempre o mesmo resultado: repetir só gasta tempo.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"pedido inválido"}`)
	}))
	defer srv.Close()

	c := newClient(srv.URL)
	c.MaxRetries = 3
	_ = c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/x", Idempotent: true})
	if hits != 1 {
		t.Errorf("tentativas = %d, queria 1", hits)
	}
}

func TestRetrySucceedsAfterFailure(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := newClient(srv.URL)
	c.MaxRetries = 3
	if err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/x", Idempotent: true}); err != nil {
		t.Fatalf("devia ter passado à terceira: %v", err)
	}
}

func TestRetryWaitDefaultsWhenUnset(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if hits == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	c := New("teste", srv.URL, 5*time.Second)
	c.RetryWait = 0 // sem espera configurada
	c.MaxRetries = 1
	if err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/x", Idempotent: true}); err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Errorf("tentativas = %d", hits)
	}
}

func TestContextCancelledDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New("teste", srv.URL, 5*time.Second)
	c.MaxRetries = 5
	c.RetryWait = time.Hour // longa de propósito, para o contexto ganhar

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: "/x", Idempotent: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("erro = %v, queria o do contexto", err)
	}
}

func TestNetworkFailureIsRetryable(t *testing.T) {
	// Sem estado HTTP não há informação que diga que não vale a pena repetir.
	c := newClient("http://127.0.0.1:1")
	err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"})
	var ge *payment.GatewayError
	if !errors.As(err, &ge) {
		t.Fatalf("erro = %v", err)
	}
	if ge.StatusCode != 0 || !ge.Retryable() {
		t.Errorf("falha de rede = %+v", ge)
	}
}

func TestUnserializableBody(t *testing.T) {
	// Um canal não se serializa. O erro tem de vir com o nome do provider, para
	// se saber de onde veio.
	err := newClient("http://exemplo").Do(context.Background(), Request{
		Method: http.MethodPost, Path: "/x", JSON: make(chan int),
	})
	if err == nil || !strings.Contains(err.Error(), "serializar pedido") {
		t.Errorf("erro = %v", err)
	}
}

func TestInvalidMethodOrURL(t *testing.T) {
	err := newClient("http://exemplo").Do(context.Background(), Request{
		Method: "MÉTODO INVÁLIDO", Path: "/x",
	})
	if err == nil || !strings.Contains(err.Error(), "construir pedido") {
		t.Errorf("erro = %v", err)
	}
}

func TestUnreadableResponseBody(t *testing.T) {
	// O servidor promete mais bytes do que envia e corta a ligação a meio.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("curto"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
	}))
	defer srv.Close()

	err := newClient(srv.URL).Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"})
	if err == nil {
		t.Fatal("queria erro de leitura")
	}
	var ge *payment.GatewayError
	if !errors.As(err, &ge) {
		t.Errorf("erro = %v", err)
	}
}

func TestUnparseableResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `isto não é json`)
	}))
	defer srv.Close()

	var out map[string]any
	err := newClient(srv.URL).Do(context.Background(), Request{
		Method: http.MethodGet, Path: "/x", Out: &out,
	})
	if err == nil || !strings.Contains(err.Error(), "ler resposta") {
		t.Errorf("erro = %v", err)
	}
	// O corpo entra na mensagem, senão não há como perceber o que o gateway
	// devolveu.
	if !strings.Contains(err.Error(), "isto não é json") {
		t.Errorf("o erro devia trazer o corpo: %v", err)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("curto", 10); got != "curto" {
		t.Errorf("= %q", got)
	}
	if got := truncate("uma frase bem comprida", 5); got != "uma f..." {
		t.Errorf("= %q", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("", "  ", "algo", "outro"); got != "algo" {
		t.Errorf("= %q", got)
	}
	if got := FirstNonEmpty("", "   "); got != "" {
		t.Errorf("= %q", got)
	}
	if got := FirstNonEmpty(); got != "" {
		t.Errorf("= %q", got)
	}
}

func TestLongErrorBodyIsTruncated(t *testing.T) {
	// Um gateway que devolva uma página de HTML de erro não pode encher os
	// registos com dezenas de milhares de bytes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, strings.Repeat("x", 10000))
	}))
	defer srv.Close()

	c := newClient(srv.URL)
	c.MaxRetries = 0
	err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/x"})
	var ge *payment.GatewayError
	if !errors.As(err, &ge) {
		t.Fatal(err)
	}
	if len(ge.Body) > 2100 {
		t.Errorf("corpo do erro com %d bytes, devia ser cortado", len(ge.Body))
	}
	if len(ge.Message) > 300 {
		t.Errorf("mensagem com %d bytes, devia ser cortada", len(ge.Message))
	}
}

func decodeJSON(r io.Reader, v any) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
