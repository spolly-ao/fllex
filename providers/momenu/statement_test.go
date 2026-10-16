package momenu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

var _ payment.StatementReader = (*Provider)(nil)

func comFacturas(t *testing.T, h http.HandlerFunc) *Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(Config{APIKey: "chave", BaseURL: srv.URL, Timeout: 5 * time.Second})
}

func TestStatementPelasFacturas(t *testing.T) {
	detalhes := 0
	p := comFacturas(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/invoices" && r.URL.Query().Get("offset") == "0":
			if r.URL.Query().Get("limit") != "100" {
				t.Errorf("limit = %q", r.URL.Query().Get("limit"))
			}
			fmt.Fprint(w, `{"success":true,"total":3,"invoices":[
				{"invoiceId":"inv-1","invoiceNumber":"FR 2026/1","invoiceType":"FR","total":2500,"createdAt":"2026-10-16T10:00:00Z"},
				{"invoiceId":"inv-0","invoiceNumber":"FR 2026/0","total":99,"createdAt":"2026-10-15T23:59:59Z"}]}`)
		case r.URL.Path == "/api/invoices" && r.URL.Query().Get("offset") == "2":
			fmt.Fprint(w, `{"success":true,"total":3,"invoices":[
				{"invoiceId":"inv-2","invoiceNumber":"FR 2026/2","total":1500.5,"createdAt":"2026-10-17T00:00:00Z"}]}`)
		case strings.HasPrefix(r.URL.Path, "/api/invoices/"):
			detalhes++
			if r.URL.Path != "/api/invoices/FR 2026/1" {
				t.Errorf("detalhe pedido para %q", r.URL.Path)
			}
			fmt.Fprint(w, `{"success":true,"invoice":{"customer":{"phone":"+244 923 000 111"},"paymentMethod":"MCX"}}`)
		default:
			t.Errorf("pedido inesperado %s", r.URL)
		}
	})
	de := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	linhas, err := p.Statement(context.Background(), de, de.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(linhas) != 1 || detalhes != 1 {
		t.Fatalf("linhas = %+v, detalhes = %d", linhas, detalhes)
	}
	l := linhas[0]
	if l.ID != "inv-1" || l.SourceRef != "FR 2026/1" || l.Type != payment.StatementCharge || l.ChargeRef != "" ||
		l.Phone != "+244 923 000 111" || !l.Amount.Equal(money.New(250000, money.AOA)) || l.Raw["paymentMethod"] != "MCX" {
		t.Fatalf("linha = %+v", l)
	}
}

func TestStatementPelasFacturasFalhas(t *testing.T) {
	de := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	ate := de.Add(24 * time.Hour)
	if _, err := New(Config{}).Statement(context.Background(), de, ate); !errors.Is(err, payment.ErrNotConfigured) {
		t.Fatalf("sem chave = %v", err)
	}
	casos := map[string]http.HandlerFunc{
		"403 numa conta sem Pro": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"success":false,"message":"FORBIDDEN"}`)
		},
		"sem sucesso": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"success":false}`) },
		"data ilegível": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"success":true,"total":1,"invoices":[{"invoiceNumber":"X","createdAt":"ontem"}]}`)
		},
		"detalhe falhado": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/invoices" {
				fmt.Fprint(w, `{"success":true,"total":1,"invoices":[{"invoiceNumber":"X","createdAt":"2026-10-16T10:00:00Z"}]}`)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"success":false}`)
		},
		"detalhe sem sucesso": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/invoices" {
				fmt.Fprint(w, `{"success":true,"total":1,"invoices":[{"invoiceNumber":"X","createdAt":"2026-10-16T10:00:00Z"}]}`)
				return
			}
			fmt.Fprint(w, `{"success":false}`)
		},
	}
	for nome, h := range casos {
		if _, err := comFacturas(t, h).Statement(context.Background(), de, ate); err == nil {
			t.Errorf("%s: sem erro", nome)
		}
	}
	vazia := comFacturas(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"success":true,"total":10,"invoices":[]}`)
	})
	if linhas, err := vazia.Statement(context.Background(), de, ate); err != nil || len(linhas) != 0 {
		t.Fatalf("página vazia = %v %v", linhas, err)
	}
}
