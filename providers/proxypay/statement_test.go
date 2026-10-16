package proxypay

import (
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

var _ payment.StatementImporter = (*Provider)(nil)

func TestImportStatementLeOPainelDoProxypay(t *testing.T) {
	p := New(Config{APIKey: "chave", Entity: "11333"})
	csv := "\xef\xbb\xbfid;reference_id;amount;datetime;invoice\r\n" +
		"901;123456789;2500.00;2026-10-16T10:00:00Z;pay_A\r\n" +
		"902;123456790;1.250,50;16/10/2026 11:30:00;\r\n"
	linhas, err := p.ImportStatement(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(linhas) != 2 {
		t.Fatalf("linhas = %d", len(linhas))
	}
	a := linhas[0]
	if a.ID != "901" || a.Type != payment.StatementCharge || a.ChargeRef != "123456789" || a.Reference != "pay_A" ||
		!a.Amount.Equal(money.New(250000, money.AOA)) || !a.Fee.IsZero() || !a.OccurredAt.Equal(time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("primeira = %+v", a)
	}
	if b := linhas[1]; !b.Amount.Equal(money.New(125050, money.AOA)) || b.Reference != "" || b.OccurredAt.Hour() != 10 {
		t.Fatalf("segunda = %+v", b)
	}
}

func TestImportStatementCampoPersonalizadoComPrefixo(t *testing.T) {
	p := &Provider{}
	linhas, err := p.ImportStatement(strings.NewReader("id,reference_id,amount,datetime,custom_fields.invoice\n1,9,10,2026-10-16,pay_B\n"))
	if err != nil {
		t.Fatal(err)
	}
	if linhas[0].Reference != "pay_B" {
		t.Fatalf("referência = %q", linhas[0].Reference)
	}
}

func TestImportStatementRecusa(t *testing.T) {
	p := New(Config{})
	casos := map[string]string{
		"":                 "vazio",
		"id;amount\n1;2\n": "faltam colunas",
		"id;reference_id;amount;datetime\n;1;10;2026-10-16\n":   "sem id",
		"id;reference_id;amount;datetime\n1;1;dez;2026-10-16\n": "valor inválido",
		"id;reference_id;amount;datetime\n1;1;-10;2026-10-16\n": "valor inválido",
		"id;reference_id;amount;datetime\n1;1;10;amanhã\n":      "data inválida",
	}
	for csv, quer := range casos {
		_, err := p.ImportStatement(strings.NewReader(csv))
		if err == nil || !strings.Contains(err.Error(), quer) {
			t.Errorf("%q: erro = %v, quer %q", csv, err, quer)
		}
	}
}
