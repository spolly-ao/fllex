package invoicepdf

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/invoice"
	"github.com/spolly-ao/fllex/money"
)

// Medidas da página, em pontos. A4 porque é o formato de toda a gente fora dos
// Estados Unidos, e uma factura impressa fora do formato do papel local é uma
// factura com margens erradas.
const (
	pageW  = 595.28
	pageH  = 841.89
	margin = 48.0

	contentW = pageW - 2*margin
	contentR = margin + contentW

	// Colunas da tabela, todas alinhadas à direita menos a descrição.
	colDescW  = 250.0
	colQtyR   = margin + 310
	colUnitR  = margin + 410
	colTotalR = contentR

	lineH = 13.0
)

// Options são os parâmetros do documento.
type Options struct {
	// Locale escolhe o idioma. "pt" (por omissão) ou "en".
	Locale string
	// Accent é a cor da marca, em "#RRGGBB". Vazio usa um cinzento escuro,
	// que é o que fica bem impresso a preto e branco.
	Accent string
	// Logo é um PNG do logótipo do emitente. Opcional: sem ele, o nome do
	// emitente é composto em texto, que numa factura funciona igualmente bem.
	Logo []byte
	// LogoHeight limita a altura do logótipo em pontos. Zero usa 34.
	LogoHeight float64
	// PaymentInstructions é o texto que acompanha os dados de pagamento numa
	// proforma (o IBAN, o horário do balcão, o que for).
	PaymentInstructions string
	// Footer é a nota de rodapé, para o texto legal que a empresa precise de
	// pôr em todas as facturas.
	Footer string
}

func (o Options) accent() rgb {
	if o.Accent == "" {
		return rgb{R: 0.11, G: 0.16, B: 0.24}
	}
	return hex(o.Accent)
}

func (o Options) logoHeight() float64 {
	if o.LogoHeight > 0 {
		return o.LogoHeight
	}
	return 34
}

// Paleta do documento. Deliberadamente curta: um documento fiscal com muitas
// cores lê-se pior e imprime-se pior.
var (
	inkColor   = rgb{R: 0.06, G: 0.09, B: 0.16}
	mutedColor = rgb{R: 0.39, G: 0.45, B: 0.55}
	ruleColor  = rgb{R: 0.89, G: 0.91, B: 0.94}
	panelColor = rgb{R: 0.97, G: 0.98, B: 0.99}
	whiteColor = rgb{R: 1, G: 1, B: 1}
	paidColor  = rgb{R: 0.02, G: 0.59, B: 0.41}
	dueColor   = rgb{R: 0.85, G: 0.47, B: 0.02}
	voidColor  = rgb{R: 0.55, G: 0.58, B: 0.64}
)

// Render devolve o PDF de um documento de cobrança.
func Render(inv *invoice.Invoice, opt Options) ([]byte, error) {
	if inv == nil {
		return nil, fmt.Errorf("invoicepdf: documento em falta")
	}

	d := &doc{p: &pdf{}, opt: opt, loc: localeFor(opt.Locale), inv: inv}

	if len(opt.Logo) > 0 {
		lg, err := decodeLogo(opt.Logo)
		if err != nil {
			return nil, err
		}
		d.logo = lg
	}

	d.newPage()
	d.header()
	d.parties()
	d.meta()
	d.payment()
	d.table()
	d.totals()
	d.notes()
	d.footers()

	return d.build()
}

// doc vai desenhando o documento e partindo em páginas quando é preciso.
type doc struct {
	p    *pdf
	opt  Options
	loc  strings_
	inv  *invoice.Invoice
	logo *logo

	pages []*canvas
	c     *canvas
	y     float64
}

func (d *doc) newPage() {
	d.c = &canvas{height: pageH}
	d.pages = append(d.pages, d.c)
	d.y = margin
}

// need garante que há espaço para o que vem a seguir, e abre uma página nova se
// não houver.
func (d *doc) need(h float64) bool {
	if d.y+h <= pageH-margin-30 {
		return false
	}
	d.newPage()
	return true
}

// --- cabeçalho ----------------------------------------------------------------

func (d *doc) header() {
	accent := d.opt.accent()

	// Emitente à esquerda: o logótipo quando existe, o nome quando não.
	if d.logo != nil {
		w, h := d.logo.fit(180, d.opt.logoHeight())
		d.c.image("Logo", margin, d.y, w, h)
	} else {
		name := bold.truncate(orDash(d.inv.Issuer.Name), 17, 260)
		d.c.text(margin, d.y+4, bold, 17, inkColor, name)
	}

	// Tipo de documento à direita, que é a primeira coisa que quem recebe
	// precisa de saber: uma proforma e uma factura não se pagam da mesma
	// maneira.
	title := d.loc.title(d.inv.Kind)
	d.c.textRight(contentR, d.y, bold, 19, accent, title)
	if d.inv.Number != "" {
		d.c.textRight(contentR, d.y+24, regular, 10, mutedColor, d.inv.Number)
	}

	d.y += 52
	d.stamp()
	d.c.line(margin, d.y, contentR, d.y, 1.6, accent)
	d.y += 20
}

// stamp desenha o estado do documento, quando vale a pena dizê-lo.
//
// Uma factura paga não precisa de aviso nenhum; uma proforma por pagar e um
// documento cancelado precisam, e é a informação que decide o que a pessoa faz
// com o papel que tem na mão.
func (d *doc) stamp() {
	var label string
	var col rgb
	switch {
	case d.inv.Status == invoice.StatusCancelled:
		label, col = d.loc.cancelled, voidColor
	case d.inv.Kind == invoice.KindProforma && d.inv.Status == invoice.StatusPending:
		label, col = d.loc.unpaid, dueColor
	case d.inv.Status == invoice.StatusPaid && d.inv.Kind == invoice.KindInvoice:
		label, col = d.loc.paid, paidColor
	default:
		return
	}
	w := bold.width(label, 8) + 16
	d.c.rect(contentR-w, d.y-14, w, 16, col)
	d.c.textCenter(contentR-w/2, d.y-11, bold, 8, whiteColor, label)
}

// --- partes --------------------------------------------------------------------

func (d *doc) parties() {
	colW := (contentW - 24) / 2
	rightX := margin + colW + 24

	top := d.y
	d.c.text(margin, top, bold, 7.5, mutedColor, strings.ToUpper(d.loc.issuer))
	d.c.text(rightX, top, bold, 7.5, mutedColor, strings.ToUpper(d.loc.billTo))
	top += 14

	leftLines := partyLines(d.inv.Issuer, d.loc)
	rightLines := partyLines(d.inv.BillTo, d.loc)

	d.party(margin, top, colW, leftLines)
	d.party(rightX, top, colW, rightLines)

	n := len(leftLines)
	if len(rightLines) > n {
		n = len(rightLines)
	}
	d.y = top + float64(n)*lineH + 18
}

func (d *doc) party(x, y, w float64, lines []string) {
	for i, line := range lines {
		f, size, col := regular, 9.5, mutedColor
		if i == 0 {
			f, size, col = bold, 10.5, inkColor
		}
		d.c.text(x, y+float64(i)*lineH, f, size, col, f.truncate(line, size, w))
	}
}

func partyLines(p invoice.Party, loc strings_) []string {
	lines := []string{orDash(p.Name)}
	if p.TaxID != "" {
		lines = append(lines, loc.taxID+": "+p.TaxID)
	}
	for _, v := range strings.Split(p.Address, "\n") {
		if v = strings.TrimSpace(v); v != "" {
			lines = append(lines, v)
		}
	}
	if p.Country != "" {
		lines = append(lines, p.Country)
	}
	if p.Email != "" {
		lines = append(lines, p.Email)
	}
	if p.Phone != "" {
		lines = append(lines, p.Phone)
	}
	if p.Website != "" {
		lines = append(lines, p.Website)
	}
	return lines
}

// --- datas ---------------------------------------------------------------------

func (d *doc) meta() {
	type item struct{ label, value string }
	items := []item{{d.loc.issued, formatDate(d.inv.IssuedAt)}}

	if d.inv.PaidAt != nil {
		items = append(items, item{d.loc.paidOn, formatDate(*d.inv.PaidAt)})
	} else if d.inv.DueDate != "" {
		items = append(items, item{d.loc.due, d.inv.DueDate})
	}
	if d.inv.PeriodStart != nil && d.inv.PeriodEnd != nil {
		items = append(items, item{d.loc.period,
			formatDate(*d.inv.PeriodStart) + " - " + formatDate(*d.inv.PeriodEnd)})
	} else if d.inv.PeriodEnd != nil {
		items = append(items, item{d.loc.until, formatDate(*d.inv.PeriodEnd)})
	}
	if d.inv.RelatedTo != "" {
		items = append(items, item{d.loc.relatedTo, d.inv.RelatedTo})
	}

	step := contentW / float64(len(items))
	for i, it := range items {
		x := margin + float64(i)*step
		d.c.text(x, d.y, bold, 7.5, mutedColor, strings.ToUpper(it.label))
		d.c.text(x, d.y+12, regular, 9.5, inkColor, regular.truncate(it.value, 9.5, step-12))
	}
	d.y += 38
}

// --- dados de pagamento ---------------------------------------------------------

// payment desenha a caixa com a entidade e a referência.
//
// Fica em destaque e antes das linhas de propósito: numa proforma, é a única
// informação que a pessoa precisa de ler para pagar, e enterrá-la no fim do
// documento é a diferença entre ser paga hoje ou daqui a duas semanas.
func (d *doc) payment() {
	if d.inv.Entity == "" && d.inv.Reference == "" && d.opt.PaymentInstructions == "" {
		return
	}

	h := 46.0
	if d.opt.PaymentInstructions != "" {
		h += 14
	}
	d.need(h + 20)

	d.c.rect(margin, d.y, contentW, h, panelColor)
	d.c.rect(margin, d.y, 3, h, d.opt.accent())

	x := margin + 16
	d.c.text(x, d.y+9, bold, 7.5, mutedColor, strings.ToUpper(d.loc.howToPay))

	type field struct{ label, value string }
	var fields []field
	if d.inv.Entity != "" {
		fields = append(fields, field{d.loc.entity, d.inv.Entity})
	}
	if d.inv.Reference != "" {
		fields = append(fields, field{d.loc.reference, d.inv.Reference})
	}
	if d.inv.DueDate != "" {
		fields = append(fields, field{d.loc.payUntil, d.inv.DueDate})
	}
	for i, f := range fields {
		fx := x + float64(i)*150
		d.c.text(fx, d.y+24, regular, 8, mutedColor, f.label)
		d.c.text(fx, d.y+33, bold, 11, inkColor, f.value)
	}
	if d.opt.PaymentInstructions != "" {
		d.c.text(x, d.y+h-13, regular, 8.5, mutedColor,
			regular.truncate(d.opt.PaymentInstructions, 8.5, contentW-32))
	}
	d.y += h + 22
}

// --- tabela ---------------------------------------------------------------------

func (d *doc) table() {
	d.tableHead()
	lines := d.inv.Lines
	if len(lines) == 0 {
		lines = []invoice.Line{{Description: "-", Quantity: 1, UnitPrice: d.inv.Total}}
	}
	for _, line := range lines {
		d.tableRow(line)
	}
	d.c.line(margin, d.y, contentR, d.y, 0.8, ruleColor)
	d.y += 4
}

func (d *doc) tableHead() {
	d.c.rect(margin, d.y, contentW, 20, panelColor)
	d.c.text(margin+10, d.y+6, bold, 7.5, mutedColor, strings.ToUpper(d.loc.description))
	d.c.textRight(colQtyR, d.y+6, bold, 7.5, mutedColor, strings.ToUpper(d.loc.quantity))
	d.c.textRight(colUnitR, d.y+6, bold, 7.5, mutedColor, strings.ToUpper(d.loc.unitPrice))
	d.c.textRight(colTotalR-10, d.y+6, bold, 7.5, mutedColor, strings.ToUpper(d.loc.amount))
	d.y += 26
}

func (d *doc) tableRow(line invoice.Line) {
	desc := line.Description
	if desc == "" {
		desc = "-"
	}
	wrapped := regular.wrap(desc, 9.5, colDescW)
	h := float64(len(wrapped))*lineH + 8

	if d.need(h) {
		d.tableHead()
	}

	qty := line.Quantity
	if qty <= 0 {
		qty = 1
	}

	for i, w := range wrapped {
		d.c.text(margin+10, d.y+float64(i)*lineH, regular, 9.5, inkColor, w)
	}
	d.c.textRight(colQtyR, d.y, regular, 9.5, mutedColor, strconv.Itoa(qty))
	d.c.textRight(colUnitR, d.y, regular, 9.5, mutedColor, d.money(line.UnitPrice))
	d.c.textRight(colTotalR-10, d.y, regular, 9.5, inkColor, d.money(line.Total()))

	if line.TaxRate > 0 {
		d.c.text(margin+10, d.y+float64(len(wrapped))*lineH-2, regular, 7.5, mutedColor,
			fmt.Sprintf("%s %d%%", d.loc.tax, line.TaxRate))
		h += 8
	}

	d.y += h
	d.c.line(margin, d.y-4, contentR, d.y-4, 0.5, ruleColor)
}

// --- totais ---------------------------------------------------------------------

func (d *doc) totals() {
	discount, _ := d.inv.Subtotal.Sub(d.inv.Total)
	rows := 1
	if discount.IsPositive() {
		rows = 2
	}
	d.need(float64(rows)*16 + 44)
	d.y += 8

	labelR := colTotalR - 120
	for i, row := range d.totalRows(discount) {
		y := d.y + float64(i)*16
		d.c.textRight(labelR, y, regular, 9.5, mutedColor, row.label)
		d.c.textRight(colTotalR-10, y, regular, 9.5, inkColor, row.value)
	}
	d.y += float64(rows)*16 + 6

	// O total leva uma faixa cheia. Numa folha com muitos números, é o único
	// que a pessoa procura, e tem de se encontrar sem ler o resto.
	h := 30.0
	x := colTotalR - 240
	d.c.rect(x, d.y, 240, h, d.opt.accent())
	d.c.text(x+14, d.y+11, bold, 9, whiteColor, strings.ToUpper(d.loc.total))
	d.c.textRight(colTotalR-14, d.y+9, bold, 13, whiteColor, d.money(d.inv.Total))
	d.y += h + 18
}

type totalRow struct{ label, value string }

func (d *doc) totalRows(discount money.Amount) []totalRow {
	rows := []totalRow{{d.loc.subtotal, d.money(d.inv.Subtotal)}}
	if discount.IsPositive() {
		label := d.loc.discount
		if d.inv.DiscountPercent > 0 {
			label += " (" + strconv.Itoa(d.inv.DiscountPercent) + "%)"
		}
		rows = append(rows, totalRow{label, "-" + d.money(discount)})
	}
	return rows
}

// --- notas e rodapé -------------------------------------------------------------

func (d *doc) notes() {
	if d.inv.Notes == "" {
		return
	}
	lines := regular.wrap(d.inv.Notes, 8.5, contentW)
	d.need(float64(len(lines))*11 + 20)
	d.c.text(margin, d.y, bold, 7.5, mutedColor, strings.ToUpper(d.loc.notes))
	d.y += 12
	for _, line := range lines {
		d.c.text(margin, d.y, regular, 8.5, mutedColor, line)
		d.y += 11
	}
	d.y += 10
}

// footers escreve o rodapé em todas as páginas, com a numeração.
//
// Corre no fim, quando já se sabe quantas páginas há: escrever "página 1 de 3"
// antes de saber que são três é impossível, e é a razão de o rodapé não ser
// desenhado à medida que as páginas nascem.
func (d *doc) footers() {
	total := len(d.pages)
	for i, c := range d.pages {
		y := pageH - margin + 6
		c.line(margin, y-10, contentR, y-10, 0.5, ruleColor)
		if d.opt.Footer != "" {
			c.text(margin, y, regular, 7.5, mutedColor,
				regular.truncate(d.opt.Footer, 7.5, contentW-90))
		}
		if total > 1 {
			c.textRight(contentR, y, regular, 7.5, mutedColor,
				fmt.Sprintf(d.loc.page, i+1, total))
		}
	}
}

// --- montagem -------------------------------------------------------------------

func (d *doc) build() ([]byte, error) {
	pagesID := d.p.reserve()

	f1 := d.p.add(fontObject(regular))
	f2 := d.p.add(fontObject(bold))

	xobject := ""
	if d.logo != nil {
		xobject = fmt.Sprintf(" /XObject << /Logo %d 0 R >>", d.logo.embed(d.p))
	}

	kids := make([]string, 0, len(d.pages))
	for _, c := range d.pages {
		content := d.p.addStream("", c.buf.Bytes())
		page := d.p.add(fmt.Sprintf(
			"<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %s %s] "+
				"/Resources << /Font << /F1 %d 0 R /F2 %d 0 R >>%s >> /Contents %d 0 R >>",
			pagesID, num(pageW), num(pageH), f1, f2, xobject, content))
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
	}

	d.p.set(pagesID, fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>",
		len(kids), strings.Join(kids, " ")))
	root := d.p.add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesID))

	return d.p.bytes(root), nil
}

// --- formatação ------------------------------------------------------------------

func (d *doc) money(a money.Amount) string { return formatMoney(a, d.loc) }

// formatMoney escreve um valor com os separadores do idioma.
//
// Em português o separador de milhares é o espaço e o decimal é a vírgula, e
// escrever "5,900.00" a um cliente angolano ou português é escrever outro
// número.
func formatMoney(a money.Amount, loc strings_) string {
	s := a.Decimal()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	whole, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole, frac = s[:i], s[i+1:]
	}

	var b strings.Builder
	for i, ch := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteString(loc.thousands)
		}
		b.WriteRune(ch)
	}
	out := b.String()
	if frac != "" {
		out += loc.decimal + frac
	}
	if neg {
		out = "-" + out
	}
	cur := a.Currency.String()
	if cur == "" {
		return out
	}
	return out + " " + cur
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("02/01/2006")
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
