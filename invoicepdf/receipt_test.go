package invoicepdf

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/invoice"
)

func receiptSample() *invoice.Invoice {
	paid := time.Date(2026, time.October, 16, 14, 5, 0, 0, time.UTC)
	return &invoice.Invoice{
		Number:    "RC-000007",
		Kind:      invoice.KindReceipt,
		Status:    invoice.StatusPaid,
		PaymentID: "pay_0123456789abcdef",
		Issuer:    invoice.Party{Name: "Padaria Conceição, Lda.", TaxID: "5417000000"},
		BillTo:    invoice.Party{Name: "João Ângelo", Email: "joao@exemplo.ao"},
		Lines:     []invoice.Line{{Description: "Pão de ló", Quantity: 2, UnitPrice: aoa(2500)}},
		Subtotal:  aoa(5000),
		Total:     aoa(5000),
		IssuedAt:  paid,
		PaidAt:    &paid,
	}
}

func TestReceiptSaysWhatItIs(t *testing.T) {
	doc, err := Render(receiptSample(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	body := content(t, doc)
	for _, want := range []string{
		escape("RECIBO DE PAGAMENTO"),
		escape("RECEBIDO POR"),
		escape("PAGO POR"),
		escape("TOTAL PAGO"),
		escape("PAGAMENTO"),
		"pay_0123456789abcdef",
		"RC-000007",
		// O aviso de que não é uma factura, com os acentos em WinAnsi.
		escape("Não é uma fatura nem tem valor fiscal"),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("faltava %q no recibo", want)
		}
	}
	// O selo de pago entra também no recibo.
	if !strings.Contains(body, "(PAGO)") {
		t.Error("faltava o selo de pago")
	}
	for _, nope := range []string{"(FATURA)", escape("FATURADO A"), escape("TOTAL A PAGAR")} {
		if strings.Contains(body, nope) {
			t.Errorf("o recibo não pode dizer %q", nope)
		}
	}
}

func TestReceiptInEnglish(t *testing.T) {
	doc, err := Render(receiptSample(), Options{Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	body := content(t, doc)
	for _, want := range []string{"PAYMENT RECEIPT", "RECEIVED BY", "PAID BY", "TOTAL PAID", "It is not an invoice"} {
		if !strings.Contains(body, want) {
			t.Errorf("faltava %q no recibo em inglês", want)
		}
	}
}

func TestReceiptWithoutPaymentIDHasNoPaymentField(t *testing.T) {
	inv := receiptSample()
	inv.PaymentID = ""
	doc, err := Render(inv, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content(t, doc), "(PAGAMENTO)") {
		t.Error("sem PaymentID não há campo de pagamento")
	}
}

func TestOtherKindsHaveNoReceiptNotice(t *testing.T) {
	doc, err := Render(sample(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content(t, doc), "valor fiscal") {
		t.Error("o aviso do recibo só entra no recibo")
	}
}

func TestReceiptTitle(t *testing.T) {
	if got := localePT.title(invoice.KindReceipt); got != "RECIBO DE PAGAMENTO" {
		t.Errorf("= %q", got)
	}
	if got := localeEN.title(invoice.KindReceipt); got != "PAYMENT RECEIPT" {
		t.Errorf("= %q", got)
	}
}

func TestJPEGLogoIsEmbedded(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	doc, err := Render(receiptSample(), Options{Logo: buf.Bytes()})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(doc, []byte("/XObject << /Logo")) {
		t.Error("o logótipo em JPEG não foi ligado à página")
	}
	// Um JPEG não tem transparência: não leva máscara.
	if bytes.Contains(doc, []byte("/SMask")) {
		t.Error("um JPEG não tem transparência e não deve levar máscara")
	}
}

// Um id de pagamento que não cabe na sua coluna encolhe, e nunca se corta: com
// reticências, deixava de servir a quem o procura no extrato do gateway.
func TestLongPaymentIDIsNeverCut(t *testing.T) {
	inv := receiptSample()
	inv.PaymentID = "pay_WWWWWWWWWWWWWWWWWWWWWWWWWWWW"
	inv.RelatedTo = "FT-2026-WWWWWWWWWWWWWWWWWWWW"
	start := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.October, 31, 0, 0, 0, 0, time.UTC)
	inv.PeriodStart, inv.PeriodEnd = &start, &end
	doc, err := Render(inv, Options{})
	if err != nil {
		t.Fatal(err)
	}
	body := content(t, doc)
	for _, want := range []string{escape(inv.PaymentID), escape(inv.RelatedTo)} {
		if !strings.Contains(body, want) {
			t.Errorf("o identificador %q não está inteiro no recibo", want)
		}
	}
	if strings.Contains(body, escape("pay_WWWW…")) {
		t.Error("o id do pagamento foi cortado com reticências")
	}
}

func TestFitShrinksDownToTheMinimum(t *testing.T) {
	const id = "pay_WWWWWWWWWWWWWWWWWWWWWWWWWWWW"
	if got := regular.fit("pay_1", 9.5, 200, minIDSize); got != 9.5 {
		t.Errorf("um texto que cabe muda de tamanho: %v", got)
	}
	wide := regular.width(id, 9.5)
	got := regular.fit(id, 9.5, wide*0.8, minIDSize)
	if got >= 9.5 || got < minIDSize || regular.width(id, got) > wide*0.8 {
		t.Errorf("devia encolher até caber: %v", got)
	}
	if got := regular.fit(id, 9.5, 1, minIDSize); got != minIDSize {
		t.Errorf("abaixo do mínimo deixava de se ler: %v", got)
	}
	if got := regular.fit(id, 9.5, 1, 6.2); got != 6.2 {
		t.Errorf("o mínimo entre dois passos não se respeita: %v", got)
	}
}
