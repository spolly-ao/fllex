package invoicepdf

import (
	"bytes"
	"compress/zlib"
	"image"
	"image/color"
	"image/png"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/invoice"
	"github.com/spolly-ao/fllex/money"
)

func aoa(v int64) money.Amount { return money.FromMajor(v, money.AOA) }

func sample() *invoice.Invoice {
	issued := time.Date(2026, time.March, 10, 9, 0, 0, 0, time.UTC)
	start := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
	return &invoice.Invoice{
		ID:     "doc-1",
		Number: "FT-000123",
		Kind:   invoice.KindInvoice,
		Status: invoice.StatusPaid,
		Issuer: invoice.Party{
			Name: "Companhia de Serviços, Lda.", TaxID: "5417000000",
			Address: "Rua da Missão, 42\nIngombota", Country: "Angola",
			Email: "faturacao@exemplo.ao", Website: "exemplo.ao",
		},
		BillTo: invoice.Party{
			Name: "Associação São João", TaxID: "5401234567",
			Address: "Avenida 4 de Fevereiro, 10", Email: "geral@saojoao.ao",
		},
		Lines: []invoice.Line{
			{Description: "Plano Essencial, subscrição mensal", Quantity: 1, UnitPrice: aoa(5900)},
			{Description: "Utilizadores adicionais", Quantity: 3, UnitPrice: aoa(800)},
		},
		Subtotal:        aoa(8300),
		DiscountPercent: 10,
		Total:           aoa(7470),
		PeriodStart:     &start,
		PeriodEnd:       &end,
		IssuedAt:        issued,
		PaidAt:          &issued,
		Notes:           "Documento processado por computador. Isento de IVA nos termos da alínea a).",
	}
}

// content devolve o conteúdo dos fluxos do PDF, já descomprimido, para os
// testes poderem afirmar coisas sobre o que foi mesmo desenhado.
func content(t *testing.T, doc []byte) string {
	t.Helper()
	var out strings.Builder
	re := regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	for _, m := range re.FindAllSubmatch(doc, -1) {
		zr, err := zlib.NewReader(bytes.NewReader(m[1]))
		if err != nil {
			continue // fluxo de imagem ou outro que não interessa aqui
		}
		data, err := io.ReadAll(zr)
		_ = zr.Close()
		if err != nil {
			continue
		}
		out.Write(data)
		out.WriteByte('\n')
	}
	return out.String()
}

func TestRenderProducesAValidPDF(t *testing.T) {
	doc, err := Render(sample(), Options{Accent: "#2563EB"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(doc, []byte("%PDF-1.4")) {
		t.Error("falta o cabeçalho do formato")
	}
	if !bytes.Contains(doc, []byte("%%EOF")) {
		t.Error("falta a marca de fim")
	}
	if !bytes.Contains(doc, []byte("/Type /Catalog")) {
		t.Error("falta o catálogo")
	}
	if len(doc) < 1000 {
		t.Errorf("documento com %d bytes, pequeno de mais para ter conteúdo", len(doc))
	}
}

func TestXrefOffsetsPointAtTheObjects(t *testing.T) {
	// A tabela de referências cruzadas é a parte do formato que não perdoa: um
	// byte a mais em qualquer sítio e o ficheiro deixa de abrir. Aqui
	// verifica-se que cada deslocamento aponta mesmo para o início do objecto.
	doc, err := Render(sample(), Options{})
	if err != nil {
		t.Fatal(err)
	}

	i := bytes.LastIndex(doc, []byte("startxref"))
	if i < 0 {
		t.Fatal("sem startxref")
	}
	var start int
	if _, err := strconv.Atoi(strings.Fields(string(doc[i+len("startxref"):]))[0]); err != nil {
		t.Fatalf("startxref ilegível: %v", err)
	} else {
		start, _ = strconv.Atoi(strings.Fields(string(doc[i+len("startxref"):]))[0])
	}
	if start <= 0 || start >= len(doc) {
		t.Fatalf("startxref = %d, fora do ficheiro de %d bytes", start, len(doc))
	}
	if !bytes.HasPrefix(doc[start:], []byte("xref")) {
		t.Fatalf("o startxref não aponta para a tabela")
	}

	lines := strings.Split(string(doc[start:]), "\n")
	// lines[0] = "xref", lines[1] = "0 N", lines[2] = objecto livre
	count, err := strconv.Atoi(strings.Fields(lines[1])[1])
	if err != nil {
		t.Fatalf("cabeçalho da tabela ilegível: %v", err)
	}
	for n := 1; n < count; n++ {
		fields := strings.Fields(lines[2+n])
		off, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatalf("objecto %d: deslocamento ilegível", n)
		}
		want := []byte(strconv.Itoa(n) + " 0 obj")
		if !bytes.HasPrefix(doc[off:], want) {
			t.Errorf("objecto %d: o deslocamento %d aponta para %q, queria %q",
				n, off, doc[off:off+12], want)
		}
	}
}

func TestPortugueseAccentsSurvive(t *testing.T) {
	// É o teste que mais importa neste pacote. Um gerador de PDF que escreva
	// "Subscrio" em vez de "Subscrição" é inútil num mercado de língua
	// portuguesa, e é exactamente o que acontece quando se escreve UTF-8 num
	// fluxo que o leitor interpreta como WinAnsi.
	inv := sample()
	inv.Lines = []invoice.Line{{
		Description: "Subscrição anual, cobrança à cabeça, coração e não",
		Quantity:    1, UnitPrice: aoa(1000),
	}}
	doc, err := Render(inv, Options{})
	if err != nil {
		t.Fatal(err)
	}

	body := content(t, doc)
	for _, want := range []string{
		"Subscri\xe7\xe3o", // ção
		"cobran\xe7a",      // ça
		"\xe0 cabe\xe7a",   // à cabeça
		"cora\xe7\xe3o",    // coração
		"n\xe3o",           // não
	} {
		if !strings.Contains(body, want) {
			t.Errorf("acento perdido: faltava %q no conteúdo", want)
		}
	}
	// E os rótulos do próprio documento, que vão em maiúsculas. As versões
	// maiúsculas dos acentos vivem noutros bytes, e são um segundo sítio onde a
	// codificação se pode partir sem o primeiro dar sinal.
	if !strings.Contains(body, "DESCRI\xc7\xc3O") {
		t.Error("o rótulo DESCRIÇÃO saiu mal codificado")
	}
	if !strings.Contains(body, "PER\xcdODO") {
		t.Error("o rótulo PERÍODO saiu mal codificado")
	}
}

func TestWinAnsiEncoding(t *testing.T) {
	// fllex:em-dash-ok, o travessão faz parte do que se está a testar.
	got := toWinAnsi("ação €5 — “aspas”")
	want := []byte{'a', 0xE7, 0xE3, 'o', ' ', 0x80, '5', ' ', 0x97, ' ', 0x93, 'a', 's', 'p', 'a', 's', 0x94}
	if !bytes.Equal(got, want) {
		t.Errorf("codificação = % x\nqueria      = % x", got, want)
	}
	// O que não existe na codificação não pode partir o ficheiro.
	if got := toWinAnsi("olá 世界"); !bytes.Equal(got, []byte{'o', 'l', 0xE1, ' ', '?', '?'}) {
		t.Errorf("caracteres fora da codificação = % x", got)
	}
}

func TestParenthesesInNamesAreEscaped(t *testing.T) {
	// O parêntese delimita as cadeias no PDF. Sem escape, o cliente que tenha
	// "(Angola)" na designação social parte o ficheiro.
	inv := sample()
	inv.BillTo.Name = `Empresa (Angola) \ Lda`
	doc, err := Render(inv, Options{})
	if err != nil {
		t.Fatal(err)
	}
	body := content(t, doc)
	if !strings.Contains(body, `Empresa \(Angola\) \\ Lda`) {
		t.Error("os parênteses e a barra não foram escapados")
	}
}

func TestMoneyFormatting(t *testing.T) {
	tests := []struct {
		amount money.Amount
		loc    strings_
		want   string
	}{
		{aoa(5900), localePT, "5 900,00 AOA"},
		{aoa(1234567), localePT, "1 234 567,00 AOA"},
		{money.New(50, money.AOA), localePT, "0,50 AOA"},
		{money.New(-123456, money.AOA), localePT, "-1 234,56 AOA"},
		{aoa(5900), localeEN, "5,900.00 AOA"},
		{money.FromMajor(1000, "JPY"), localePT, "1 000 JPY"},
	}
	for _, tt := range tests {
		if got := formatMoney(tt.amount, tt.loc); got != tt.want {
			t.Errorf("formato = %q, queria %q", got, tt.want)
		}
	}
}

func TestProformaShowsPaymentDetails(t *testing.T) {
	inv := sample()
	inv.Kind = invoice.KindProforma
	inv.Status = invoice.StatusPending
	inv.Number = "PF-000045"
	inv.PaidAt = nil
	inv.Entity = "01234"
	inv.Reference = "987 654 321"
	inv.DueDate = "05/04/2026"

	doc, err := Render(inv, Options{PaymentInstructions: "Multicaixa, ATM ou app do banco."})
	if err != nil {
		t.Fatal(err)
	}
	body := content(t, doc)

	for _, want := range []string{"01234", "987 654 321", "POR PAGAR", "PF-000045", "Multicaixa"} {
		if !strings.Contains(body, want) {
			t.Errorf("faltava %q numa proforma por pagar", want)
		}
	}
	if !strings.Contains(body, "FATURA PROFORMA") {
		t.Error("o tipo de documento tem de estar visível: uma proforma não se paga como uma factura")
	}
}

func TestInvoiceShowsPaidStamp(t *testing.T) {
	doc, err := Render(sample(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	body := content(t, doc)
	if !strings.Contains(body, "PAGO") {
		t.Error("uma factura paga devia dizê-lo")
	}
	if strings.Contains(body, "POR PAGAR") {
		t.Error("uma factura paga não pode dizer que está por pagar")
	}
}

func TestCancelledStamp(t *testing.T) {
	inv := sample()
	inv.Status = invoice.StatusCancelled
	doc, _ := Render(inv, Options{})
	if !strings.Contains(content(t, doc), "ANULADO") {
		t.Error("um documento anulado tem de o dizer")
	}
}

func TestCreditNoteTitle(t *testing.T) {
	inv := sample()
	inv.Kind = invoice.KindCredit
	inv.RelatedTo = "FT-000123"
	doc, _ := Render(inv, Options{})
	body := content(t, doc)
	if !strings.Contains(body, "NOTA DE CR\xc9DITO") {
		t.Error("falta o título da nota de crédito")
	}
	if !strings.Contains(body, "FT-000123") {
		t.Error("uma nota de crédito tem de dizer que documento corrige")
	}
}

func TestDiscountLineOnlyWhenThereIsOne(t *testing.T) {
	doc, _ := Render(sample(), Options{})
	if !strings.Contains(content(t, doc), "Desconto \\(10%\\)") {
		t.Error("faltava a linha de desconto com a percentagem")
	}

	inv := sample()
	inv.DiscountPercent = 0
	inv.Total = inv.Subtotal
	doc, _ = Render(inv, Options{})
	if strings.Contains(content(t, doc), "Desconto") {
		t.Error("sem desconto não deve aparecer linha de desconto")
	}
}

func TestManyLinesBreakIntoPages(t *testing.T) {
	inv := sample()
	inv.Lines = nil
	for i := 0; i < 60; i++ {
		inv.Lines = append(inv.Lines, invoice.Line{
			Description: "Linha de serviço número " + strconv.Itoa(i+1) + ", com descrição suficientemente longa para partir em duas linhas de texto",
			Quantity:    1, UnitPrice: aoa(100),
		})
	}
	doc, err := Render(inv, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(doc, []byte("/Type /Page\n")) + bytes.Count(doc, []byte("/Type /Page ")); n < 2 {
		t.Errorf("páginas = %d, queria mais do que uma", n)
	}
	body := content(t, doc)
	// A numeração só aparece quando há mais do que uma página, e tem de contar
	// o total certo.
	if !strings.Contains(body, "gina 1 de ") {
		t.Error("falta a numeração de páginas")
	}
	// O cabeçalho da tabela repete-se em cada página, senão a segunda folha é
	// uma lista de números sem dizer do que são.
	if n := strings.Count(body, "DESCRI"); n < 2 {
		t.Errorf("cabeçalho da tabela repetido %d vezes, queria uma por página", n)
	}
}

func TestLocaleEnglish(t *testing.T) {
	doc, _ := Render(sample(), Options{Locale: "en"})
	body := content(t, doc)
	if !strings.Contains(body, "INVOICE") || !strings.Contains(body, "BILL TO") {
		t.Error("faltavam os textos em inglês")
	}
	if !strings.Contains(body, "Total due") && !strings.Contains(body, "TOTAL DUE") {
		t.Error("faltava o total em inglês")
	}
	if strings.Contains(body, "FATURA") {
		t.Error("ficaram textos em português num documento em inglês")
	}
}

func TestLogoIsEmbedded(t *testing.T) {
	doc, err := Render(sample(), Options{Logo: pngFixture(t, 60, 20, true)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(doc, []byte("/Subtype /Image")) {
		t.Error("a imagem não foi incorporada")
	}
	// Com transparência tem de haver máscara suave, senão o logótipo traz um
	// rectângulo atrás.
	if !bytes.Contains(doc, []byte("/SMask")) {
		t.Error("falta a máscara de transparência")
	}
	if !bytes.Contains(doc, []byte("/XObject << /Logo")) {
		t.Error("a imagem não foi ligada à página")
	}
}

func TestOpaqueLogoHasNoMask(t *testing.T) {
	doc, err := Render(sample(), Options{Logo: pngFixture(t, 40, 40, false)})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(doc, []byte("/SMask")) {
		t.Error("uma imagem opaca não precisa de máscara e não deve levar uma")
	}
}

func TestBrokenLogoIsAnError(t *testing.T) {
	if _, err := Render(sample(), Options{Logo: []byte("isto não é um png")}); err == nil {
		t.Error("um logótipo ilegível devia dar erro em vez de sair em branco")
	}
}

func TestLogoFitKeepsAspect(t *testing.T) {
	l := &logo{width: 200, height: 50}
	w, h := l.fit(100, 100)
	if w != 100 || h != 25 {
		t.Errorf("dimensões = %v x %v, queria 100 x 25", w, h)
	}
	w, h = l.fit(400, 20)
	if w != 80 || h != 20 {
		t.Errorf("dimensões = %v x %v, queria 80 x 20", w, h)
	}
}

func TestNilInvoice(t *testing.T) {
	if _, err := Render(nil, Options{}); err == nil {
		t.Error("um documento em falta devia dar erro")
	}
}

func TestEmptyInvoiceStillRenders(t *testing.T) {
	// Um documento sem nada preenchido não pode entrar em pânico: acontece em
	// migrações e em dados antigos, e uma factura feia é melhor do que um
	// serviço em baixo.
	doc, err := Render(&invoice.Invoice{Kind: invoice.KindInvoice}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc) < 500 {
		t.Errorf("documento vazio com %d bytes", len(doc))
	}
}

func TestTextMeasurement(t *testing.T) {
	// A largura é o que permite alinhar à direita. Um erro aqui não parte o
	// ficheiro: desalinha a coluna dos valores, que é pior porque passa.
	if w := regular.width("iii", 10); w >= regular.width("mmm", 10) {
		t.Error("três is não podem ser mais largos do que três emes")
	}
	// A letra acentuada mede o mesmo que a letra base.
	if regular.width("a", 10) != regular.width("á", 10) {
		t.Error("o acento não ocupa avanço nas fontes de base")
	}
	if got, want := regular.width("A", 1000), 667.0; got != want {
		t.Errorf("largura do A = %v, queria %v", got, want)
	}
	if got, want := bold.width("A", 1000), 722.0; got != want {
		t.Errorf("largura do A a negrito = %v, queria %v", got, want)
	}
}

func TestWrapAndTruncate(t *testing.T) {
	lines := regular.wrap("uma descrição bastante longa que não cabe numa linha só", 9.5, 100)
	if len(lines) < 2 {
		t.Errorf("linhas = %d, queria partir", len(lines))
	}
	for _, l := range lines {
		if w := regular.width(l, 9.5); w > 100 {
			t.Errorf("a linha %q mede %v e o limite era 100", l, w)
		}
	}
	if got := regular.wrap("", 10, 100); got != nil {
		t.Errorf("texto vazio = %v", got)
	}

	short := regular.truncate("curto", 10, 500)
	if short != "curto" {
		t.Errorf("um texto que cabe não deve ser cortado: %q", short)
	}
	long := regular.truncate("um texto bem mais longo do que a caixa permite", 10, 60)
	if !strings.HasSuffix(long, "…") {
		t.Errorf("um texto cortado devia acabar em reticências: %q", long)
	}
	if w := regular.width(long, 10); w > 60 {
		t.Errorf("o texto cortado mede %v e o limite era 60", w)
	}
}

func TestHexColour(t *testing.T) {
	c := hex("#2563EB")
	if int(c.R*255+0.5) != 0x25 || int(c.G*255+0.5) != 0x63 || int(c.B*255+0.5) != 0xEB {
		t.Errorf("cor = %+v", c)
	}
	// Uma cor ilegível fica preta: o documento sai na mesma.
	if got := hex("nao é uma cor"); got != (rgb{}) {
		t.Errorf("cor inválida = %+v", got)
	}
}

// pngFixture cria um PNG pequeno para os testes.
func pngFixture(t *testing.T, w, h int, alpha bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if alpha && x < w/2 {
				a = 128
			}
			img.SetNRGBA(x, y, color.NRGBA{R: 0x25, G: 0x63, B: 0xEB, A: a})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
