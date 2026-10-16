package relatorio

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type leitorAvariado struct{}

func (leitorAvariado) Read([]byte) (int, error) { return 0, errors.New("disco") }

func TestLerComBOMEPontoEVirgula(t *testing.T) {
	csv := "\xef\xbb\xbfID; Reference_ID ;amount\r\n1;123;1.234,56\r\n\r\n   \r\n2;124\r\n"
	tab, err := Ler(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if tab.Linhas() != 2 {
		t.Fatalf("linhas = %d", tab.Linhas())
	}
	if v := tab.Valor(0, "amount"); v != "1.234,56" {
		t.Fatalf("amount = %q", v)
	}
	if v := tab.Valor(0, "nada", "reference_id"); v != "123" {
		t.Fatalf("sinónimo = %q", v)
	}
	if v := tab.Valor(1, "amount"); v != "" {
		t.Fatalf("linha curta = %q", v)
	}
	if v := tab.Valor(0, "nada"); v != "" {
		t.Fatalf("coluna inexistente = %q", v)
	}
	if err := tab.Exigir("id", "valor|amount"); err != nil {
		t.Fatal(err)
	}
	err = tab.Exigir("id", "datetime|data", "status")
	if err == nil || !strings.Contains(err.Error(), "datetime, status") {
		t.Fatalf("Exigir = %v", err)
	}
}

func TestLerComVirgula(t *testing.T) {
	tab, err := Ler(strings.NewReader("id,amount\n1,\"10,5\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v := tab.Valor(0, "amount"); v != "10,5" {
		t.Fatalf("amount = %q", v)
	}
}

func TestLerRecusa(t *testing.T) {
	if _, err := Ler(strings.NewReader("")); !errors.Is(err, ErrVazio) {
		t.Fatalf("vazio = %v", err)
	}
	if _, err := Ler(strings.NewReader("  \nid\n")); !errors.Is(err, ErrVazio) {
		t.Fatalf("cabeçalho em branco = %v", err)
	}
	if _, err := Ler(leitorAvariado{}); err == nil {
		t.Fatal("leitor avariado aceite")
	}
	if _, err := Ler(strings.NewReader("id,amount\n1,\"aberto\n")); err == nil {
		t.Fatal("aspas por fechar aceites")
	}
}

func TestErro(t *testing.T) {
	if e := Erro(0, "valor %q", "x").Error(); e != `linha 2 do relatório: valor "x"` {
		t.Fatal(e)
	}
}

func TestData(t *testing.T) {
	casos := map[string]time.Time{
		"2026-10-16T10:00:00Z":      time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC),
		"2026-10-16T10:00:00+01:00": time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
		"2026-10-16 10:00:00":       time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
		"2026-10-16T10:00:00":       time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
		"16/10/2026 10:00:00":       time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
		"16/10/2026 10:00":          time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC),
		"2026-10-16":                time.Date(2026, 10, 15, 23, 0, 0, 0, time.UTC),
		" 16/10/2026 ":              time.Date(2026, 10, 15, 23, 0, 0, 0, time.UTC),
	}
	for s, quer := range casos {
		got, ok := Data(s)
		if !ok || !got.Equal(quer) {
			t.Errorf("Data(%q) = %v %v, quer %v", s, got, ok, quer)
		}
	}
	if _, ok := Data("ontem"); ok {
		t.Fatal("data inválida aceite")
	}
}
