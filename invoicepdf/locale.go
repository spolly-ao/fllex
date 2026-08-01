package invoicepdf

import (
	"strings"

	"github.com/spolly-ao/fllex/invoice"
)

// strings_ são os textos do documento num idioma.
//
// O nome tem o sublinhado para não colidir com o pacote strings da biblioteca
// padrão, que este ficheiro usa. É feio e é preferível ao alternativo, que era
// importar o pacote padrão com um nome inventado e obrigar quem lê a lembrar-se
// disso em todos os outros ficheiros.
type strings_ struct {
	invoice   string
	proforma  string
	credit    string
	issuer    string
	billTo    string
	taxID     string
	issued    string
	due       string
	paidOn    string
	period    string
	until     string
	relatedTo string

	howToPay  string
	entity    string
	reference string
	payUntil  string

	description string
	quantity    string
	unitPrice   string
	amount      string
	tax         string

	subtotal string
	discount string
	total    string
	notes    string

	paid      string
	unpaid    string
	cancelled string

	page string

	thousands string
	decimal   string
}

func (s strings_) title(k invoice.Kind) string {
	switch k {
	case invoice.KindProforma:
		return s.proforma
	case invoice.KindCredit:
		return s.credit
	default:
		return s.invoice
	}
}

var localePT = strings_{
	invoice:   "FATURA",
	proforma:  "FATURA PROFORMA",
	credit:    "NOTA DE CRÉDITO",
	issuer:    "Emitente",
	billTo:    "Faturado a",
	taxID:     "NIF",
	issued:    "Data de emissão",
	due:       "Prazo de pagamento",
	paidOn:    "Data de pagamento",
	period:    "Período",
	until:     "Válido até",
	relatedTo: "Documento relacionado",

	howToPay:  "Como pagar",
	entity:    "Entidade",
	reference: "Referência",
	payUntil:  "Pagar até",

	description: "Descrição",
	quantity:    "Qtd.",
	unitPrice:   "Preço unitário",
	amount:      "Valor",
	tax:         "IVA",

	subtotal: "Subtotal",
	discount: "Desconto",
	total:    "Total a pagar",
	notes:    "Observações",

	paid:      "PAGO",
	unpaid:    "POR PAGAR",
	cancelled: "ANULADO",

	page: "Página %d de %d",

	// Em português o milhar separa-se com espaço e a casa decimal com vírgula.
	thousands: " ", // espaço inquebrável, para o número não partir na linha
	decimal:   ",",
}

var localeEN = strings_{
	invoice:   "INVOICE",
	proforma:  "PROFORMA INVOICE",
	credit:    "CREDIT NOTE",
	issuer:    "From",
	billTo:    "Bill to",
	taxID:     "Tax ID",
	issued:    "Issue date",
	due:       "Due date",
	paidOn:    "Paid on",
	period:    "Period",
	until:     "Valid until",
	relatedTo: "Related document",

	howToPay:  "How to pay",
	entity:    "Entity",
	reference: "Reference",
	payUntil:  "Pay by",

	description: "Description",
	quantity:    "Qty",
	unitPrice:   "Unit price",
	amount:      "Amount",
	tax:         "VAT",

	subtotal: "Subtotal",
	discount: "Discount",
	total:    "Total due",
	notes:    "Notes",

	paid:      "PAID",
	unpaid:    "UNPAID",
	cancelled: "VOID",

	page: "Page %d of %d",

	thousands: ",",
	decimal:   ".",
}

// localeFor escolhe o idioma. O que não for reconhecido cai no português, que é
// o idioma dos mercados que esta biblioteca serve.
func localeFor(code string) strings_ {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "en", "en-gb", "en-us":
		return localeEN
	default:
		return localePT
	}
}
