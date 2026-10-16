package baipaga

import (
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

var _ payment.StatementImporter = (*Provider)(nil)

func TestImportStatementLeOPortalDoBAI(t *testing.T) {
	p := New(Config{})
	csv := "id;externalId;amount;currency;status;creationDate;lastChangeDate;totalReversed;customerMsisdn\r\n" +
		"5001;pay_A;1500.00;AKZ;SUCCESS;2026-10-16 09:00:00;2026-10-16 09:01:00;0;923000111\r\n" +
		"5002;pay_B;800;AOA;COMPENSATED;2026-10-16T08:00:00Z;;300,00;\r\n" +
		"5003;pay_C;900;AKZ;REJECTED;2026-10-16 08:00:00;;;\r\n" +
		"5004;pay_D;900;;PROCESSING;2026-10-16 08:00:00;;;\r\n"
	linhas, err := p.ImportStatement(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(linhas) != 3 {
		t.Fatalf("linhas = %+v", linhas)
	}
	a := linhas[0]
	if a.ID != "5001" || a.ChargeRef != "5001" || a.Reference != "pay_A" || a.Type != payment.StatementCharge ||
		a.Phone != "923000111" || !a.Amount.Equal(money.New(150000, money.AOA)) ||
		!a.OccurredAt.Equal(time.Date(2026, 10, 16, 8, 1, 0, 0, time.UTC)) {
		t.Fatalf("primeira = %+v", a)
	}
	if b := linhas[1]; b.Reference != "pay_B" || !b.Amount.Equal(money.New(80000, money.AOA)) || !b.OccurredAt.Equal(time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("segunda = %+v", b)
	}
	if d := linhas[2]; d.ID != "5002:revertido" || d.Type != payment.StatementRefund || d.ChargeRef != "5002" ||
		!d.Amount.Equal(money.New(-30000, money.AOA)) {
		t.Fatalf("devolução = %+v", d)
	}
}

func TestImportStatementDoBAIRecusa(t *testing.T) {
	p := New(Config{})
	cab := "id;externalId;amount;status;creationDate;currency;totalReversed\n"
	casos := map[string]string{
		"":                                     "vazio",
		"id;amount\n1;2\n":                     "faltam colunas",
		cab + ";pay;10;SUCCESS;2026-10-16;;\n": "sem id",
		cab + "1;pay;10;SUCCESS;2026-10-16;USD;\n": "só cobra kwanzas",
		cab + "1;pay;x;SUCCESS;2026-10-16;;\n":     "valor inválido",
		cab + "1;pay;0;SUCCESS;2026-10-16;;\n":     "valor inválido",
		cab + "1;pay;10;SUCCESS;ontem;;\n":         "data inválida",
		cab + "1;pay;10;SUCCESS;2026-10-16;;x\n":   "revertido inválido",
		cab + "1;pay;10;SUCCESS;2026-10-16;;-1\n":  "revertido inválido",
		cab + "1;pay;10;SUCCESS;2026-10-16;;11\n":  "revertido inválido",
	}
	for csv, quer := range casos {
		_, err := p.ImportStatement(strings.NewReader(csv))
		if err == nil || !strings.Contains(err.Error(), quer) {
			t.Errorf("%q: erro = %v, quer %q", csv, err, quer)
		}
	}
}
