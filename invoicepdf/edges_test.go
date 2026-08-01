package invoicepdf

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spolly-ao/fllex/invoice"
)

func TestCharWidthOutsideTheTable(t *testing.T) {
	// Um byte de controlo não tem largura na tabela; devolver uma largura
	// média é preferível a um alinhamento com um número negativo.
	if got := regular.charWidth(0x01); got != 556 {
		t.Errorf("byte de controlo = %d", got)
	}
	if got := bold.charWidth(0xFE); got != 556 {
		t.Errorf("byte sem entrada = %d", got)
	}
	// Os símbolos com largura própria não caem no valor médio.
	if got := regular.charWidth(0xBA); got != 365 {
		t.Errorf("º = %d, queria 365", got)
	}
}

func TestTruncateToNothing(t *testing.T) {
	// Uma caixa demasiado estreita para uma única letra devolve vazio em vez
	// de transbordar.
	if got := regular.truncate("qualquer coisa", 10, 1); got != "" {
		t.Errorf("= %q", got)
	}
}

func TestDecodeLogoRejectsBrokenPNG(t *testing.T) {
	// Um PNG truncado a meio: o cabeçalho passa, o resto não.
	full := pngFixture(t, 20, 20, false)
	if _, err := decodeLogo(full[:len(full)/2]); err == nil {
		t.Error("um PNG truncado devia dar erro")
	}
	// E um que não é PNG de todo.
	if _, err := decodeLogo([]byte("nem por sombras")); err == nil {
		t.Error("bytes que não são PNG deviam dar erro")
	}
}

func TestClamp8Saturates(t *testing.T) {
	if got := clamp8(0xFFFFF); got != 0xFF {
		t.Errorf("= %d, queria saturar em 255", got)
	}
	if got := clamp8(0x8000); got != 0x80 {
		t.Errorf("= %d", got)
	}
}

func TestHexRejectsBadInput(t *testing.T) {
	// Uma cor com o comprimento certo mas com lixo lá dentro cai no preto.
	if got := hex("#GGGGGG"); got != (rgb{}) {
		t.Errorf("= %+v", got)
	}
}

func TestEmptyTextIsSkipped(t *testing.T) {
	c := &canvas{height: pageH}
	c.text(10, 10, regular, 10, inkColor, "")
	if c.buf.Len() != 0 {
		t.Errorf("um texto vazio não devia escrever nada: %q", c.buf.String())
	}
}

func TestNumFormatsZeroAndNegatives(t *testing.T) {
	tests := map[float64]string{
		0: "0", 0.001: "0", -0.001: "0", 1.5: "1.5", 100: "100", -12.25: "-12.25", -1.5: "-1.5",
	}
	for in, want := range tests {
		if got := num(in); got != want {
			t.Errorf("num(%v) = %q, queria %q", in, got, want)
		}
	}
}

func TestEscapeControlCharacters(t *testing.T) {
	// Uma quebra de linha dentro de uma cadeia parte o ficheiro se não for
	// escapada. Uma morada em duas linhas é o caso normal.
	got := escape("Rua da Missão\r\n42")
	if !strings.Contains(got, `\r`) || !strings.Contains(got, `\n`) {
		t.Errorf("= %q", got)
	}
}

func TestLogoHeightOption(t *testing.T) {
	doc, err := Render(sample(), Options{Logo: pngFixture(t, 100, 50, false), LogoHeight: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(doc, []byte("/Subtype /Image")) {
		t.Error("a imagem não foi incorporada")
	}
}

func TestBillToWithMoreLinesThanIssuer(t *testing.T) {
	// A altura do bloco tem de acompanhar a coluna mais alta, senão a tabela
	// entra por cima dos dados do cliente.
	inv := sample()
	inv.Issuer = invoice.Party{Name: "Empresa"}
	inv.BillTo = invoice.Party{
		Name: "Cliente", TaxID: "5401234567",
		Address: "Linha 1\nLinha 2\nLinha 3", Country: "Angola",
		Email: "a@b.ao", Phone: "+244 923 456 789", Website: "cliente.ao",
	}
	doc, err := Render(inv, Options{})
	if err != nil {
		t.Fatal(err)
	}
	body := content(t, doc)
	if !strings.Contains(body, "+244 923 456 789") {
		t.Error("o telefone devia aparecer")
	}
	if !strings.Contains(body, "cliente.ao") {
		t.Error("o sítio devia aparecer")
	}
}

func TestPeriodEndWithoutStart(t *testing.T) {
	// Um documento com fim de período mas sem início mostra "válido até", que
	// é a leitura certa de uma referência com prazo.
	inv := sample()
	inv.PeriodStart = nil
	end := time.Date(2026, time.April, 30, 0, 0, 0, 0, time.UTC)
	inv.PeriodEnd = &end
	doc, err := Render(inv, Options{})
	if err != nil {
		t.Fatal(err)
	}
	body := content(t, doc)
	if !strings.Contains(body, string(toWinAnsi(strings.ToUpper(localePT.until)))) {
		t.Errorf("faltava o rótulo de validade")
	}
	if !strings.Contains(body, "30/04/2026") {
		t.Error("faltava a data")
	}
}

func TestLineWithoutDescriptionOrQuantity(t *testing.T) {
	inv := sample()
	inv.Lines = []invoice.Line{{UnitPrice: aoa(100)}} // sem descrição nem quantidade
	doc, err := Render(inv, Options{})
	if err != nil {
		t.Fatal(err)
	}
	body := content(t, doc)
	// Sem descrição fica um traço, para a linha não desaparecer da folha.
	if !strings.Contains(body, "(-)") {
		t.Errorf("faltava o marcador de descrição vazia")
	}
	// Quantidade em falta lê-se como uma.
	if !strings.Contains(body, "(1)") {
		t.Error("faltava a quantidade")
	}
}

func TestTaxRateLine(t *testing.T) {
	inv := sample()
	inv.Lines = []invoice.Line{{Description: "Serviço", Quantity: 1, UnitPrice: aoa(1000), TaxRate: 14}}
	doc, err := Render(inv, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content(t, doc), "IVA 14%") {
		t.Error("faltava a taxa na linha")
	}
}

func TestFooterAppearsOnEveryPage(t *testing.T) {
	inv := sample()
	for i := 0; i < 60; i++ {
		inv.Lines = append(inv.Lines, invoice.Line{
			Description: "Linha", Quantity: 1, UnitPrice: aoa(10),
		})
	}
	doc, err := Render(inv, Options{Footer: "Empresa, Lda. NIF 5417000000"})
	if err != nil {
		t.Fatal(err)
	}
	body := content(t, doc)
	if n := strings.Count(body, "NIF 5417000000"); n < 2 {
		t.Errorf("rodapé em %d páginas, queria uma por página", n)
	}
}

func TestDateFormattingOfZeroTime(t *testing.T) {
	if got := formatDate(time.Time{}); got != "-" {
		t.Errorf("= %q", got)
	}
}

func TestOrDash(t *testing.T) {
	if got := orDash("   "); got != "-" {
		t.Errorf("= %q", got)
	}
	if got := orDash("nome"); got != "nome" {
		t.Errorf("= %q", got)
	}
}

func TestLocaleFallback(t *testing.T) {
	// Um idioma desconhecido cai no português, que é o dos mercados que a
	// biblioteca serve.
	for _, code := range []string{"", "fr", "  DE  "} {
		if got := localeFor(code); got.invoice != localePT.invoice {
			t.Errorf("localeFor(%q) não caiu no português", code)
		}
	}
	for _, code := range []string{"en", "EN-GB", " en-us "} {
		if got := localeFor(code); got.invoice != localeEN.invoice {
			t.Errorf("localeFor(%q) não escolheu o inglês", code)
		}
	}
}

func TestFontBaseNames(t *testing.T) {
	if got := regular.baseFont(); got != "Helvetica" {
		t.Errorf("= %q", got)
	}
	if got := bold.baseFont(); got != "Helvetica-Bold" {
		t.Errorf("= %q", got)
	}
	if !strings.Contains(fontObject(bold), "Helvetica-Bold") {
		t.Errorf("= %q", fontObject(bold))
	}
	if !strings.Contains(fontObject(regular), "WinAnsiEncoding") {
		t.Error("a codificação tem de estar declarada, senão os acentos saem errados")
	}
}

func TestTitleForEachKind(t *testing.T) {
	if got := localePT.title(invoice.KindProforma); got != localePT.proforma {
		t.Errorf("= %q", got)
	}
	if got := localePT.title(invoice.KindCredit); got != localePT.credit {
		t.Errorf("= %q", got)
	}
	if got := localePT.title(invoice.Kind("inventado")); got != localePT.invoice {
		t.Errorf("= %q", got)
	}
}
