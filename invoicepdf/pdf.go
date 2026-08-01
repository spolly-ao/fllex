// Package invoicepdf gera o PDF de uma factura ou proforma.
//
// Escreve o formato à mão, com a biblioteca padrão e mais nada. Parece
// exagerado até se olhar para a alternativa: qualquer biblioteca de PDF é uma
// dependência grande, com o seu próprio calendário de versões, imposta a todos
// os projectos que usem esta. Uma factura precisa de texto, linhas, rectângulos
// e uma imagem, e isso são umas centenas de linhas de um formato documentado e
// estável desde 1993.
//
// A tipografia usa as fontes de base do PDF (Helvetica), que todos os leitores
// têm e que por isso não precisam de ser incorporadas. A codificação é
// WinAnsi, que cobre todos os acentos do português: á, ã, ç, ê, ó e o resto
// saem como devem, em qualquer leitor.
package invoicepdf

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strconv"
	"strings"
)

// pdf acumula os objectos de um documento e serializa-o no fim.
type pdf struct {
	objects [][]byte // corpo de cada objecto; o número é o índice mais um
}

// add acrescenta um objecto e devolve o seu número.
func (p *pdf) add(body string) int {
	p.objects = append(p.objects, []byte(body))
	return len(p.objects)
}

// reserve guarda um número de objecto para se preencher depois.
//
// É preciso porque há referências circulares no PDF: a página aponta para o
// nó de páginas e o nó de páginas aponta para as páginas.
func (p *pdf) reserve() int {
	p.objects = append(p.objects, nil)
	return len(p.objects)
}

// set preenche um objecto reservado.
func (p *pdf) set(id int, body string) { p.objects[id-1] = []byte(body) }

// addStream acrescenta um objecto de fluxo, comprimido.
//
// A compressão não é decoração: o conteúdo de uma factura é texto repetitivo, e
// sem ela o ficheiro fica várias vezes maior por nada.
func (p *pdf) addStream(dict string, data []byte) int {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write(data)
	_ = zw.Close()

	var b strings.Builder
	b.WriteString("<< ")
	if dict != "" {
		b.WriteString(dict)
		b.WriteByte(' ')
	}
	b.WriteString("/Filter /FlateDecode /Length ")
	b.WriteString(strconv.Itoa(buf.Len()))
	b.WriteString(" >>\nstream\n")
	b.WriteString(buf.String())
	b.WriteString("\nendstream")
	return p.add(b.String())
}

// bytes serializa o documento, com a tabela de referências cruzadas.
//
// A tabela é a parte do formato que não perdoa: guarda o deslocamento em bytes
// de cada objecto, e um byte a mais em qualquer sítio torna o ficheiro
// ilegível. É por isso que é montada a partir do que foi mesmo escrito, e não
// de contas feitas à parte.
func (p *pdf) bytes(rootID int) []byte {
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	// Um comentário com bytes altos assinala aos programas que o ficheiro é
	// binário e não deve ser transferido como texto.
	out.Write([]byte{'%', 0xE2, 0xE3, 0xCF, 0xD3, '\n'})

	offsets := make([]int, len(p.objects))
	for i, body := range p.objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n", i+1)
		out.Write(body)
		out.WriteString("\nendobj\n")
	}

	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n", len(p.objects)+1)
	out.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(p.objects)+1, rootID, xref)

	return out.Bytes()
}

// --- cores -------------------------------------------------------------------

// rgb é uma cor.
type rgb struct{ R, G, B float64 }

// hex lê uma cor em "#RRGGBB". Uma cor ilegível fica preta, porque um documento
// com uma cor errada ainda se lê e um que não se gera não serve para nada.
func hex(s string) rgb {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return rgb{}
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return rgb{}
	}
	return rgb{
		R: float64((v>>16)&0xFF) / 255,
		G: float64((v>>8)&0xFF) / 255,
		B: float64(v&0xFF) / 255,
	}
}

// --- tela --------------------------------------------------------------------

// canvas escreve as instruções de desenho de uma página.
//
// Trabalha em coordenadas de cima para baixo, ao contrário do PDF, que conta do
// canto inferior esquerdo. A conversão está num sítio só, e assim o código do
// desenho lê-se como se lê uma página: de cima para baixo.
type canvas struct {
	buf    bytes.Buffer
	height float64
}

func (c *canvas) y(v float64) float64 { return c.height - v }

func (c *canvas) fill(col rgb) {
	fmt.Fprintf(&c.buf, "%s %s %s rg\n", num(col.R), num(col.G), num(col.B))
}

func (c *canvas) stroke(col rgb) {
	fmt.Fprintf(&c.buf, "%s %s %s RG\n", num(col.R), num(col.G), num(col.B))
}

// rect desenha um rectângulo cheio.
func (c *canvas) rect(x, y, w, h float64, col rgb) {
	c.fill(col)
	fmt.Fprintf(&c.buf, "%s %s %s %s re f\n", num(x), num(c.y(y+h)), num(w), num(h))
}

// line desenha uma linha horizontal ou qualquer outra.
func (c *canvas) line(x1, y1, x2, y2, width float64, col rgb) {
	c.stroke(col)
	fmt.Fprintf(&c.buf, "%s w %s %s m %s %s l S\n",
		num(width), num(x1), num(c.y(y1)), num(x2), num(c.y(y2)))
}

// text escreve uma linha de texto com o canto superior esquerdo em (x, y).
//
// O y recebido é o topo da linha e não a base, que é o que o PDF quer: é a
// medida com que se pensa uma página, e poupa a quem escreve o desenho ter de
// somar alturas de linha à mão em todo o lado.
func (c *canvas) text(x, y float64, f font, size float64, col rgb, s string) {
	if s == "" {
		return
	}
	c.fill(col)
	baseline := c.y(y + size*0.8)
	fmt.Fprintf(&c.buf, "BT /%s %s Tf %s %s Td (%s) Tj ET\n",
		f.resource(), num(size), num(x), num(baseline), escape(s))
}

// textRight escreve o texto alinhado à direita de x.
//
// É indispensável numa factura: uma coluna de valores desalinhada é ilegível, e
// alinhar à direita exige medir o texto, que é o que a tabela de larguras das
// fontes serve para fazer.
func (c *canvas) textRight(x, y float64, f font, size float64, col rgb, s string) {
	c.text(x-f.width(s, size), y, f, size, col, s)
}

// textCenter escreve o texto centrado em x.
func (c *canvas) textCenter(x, y float64, f font, size float64, col rgb, s string) {
	c.text(x-f.width(s, size)/2, y, f, size, col, s)
}

// image desenha uma imagem já incorporada.
func (c *canvas) image(name string, x, y, w, h float64) {
	fmt.Fprintf(&c.buf, "q %s 0 0 %s %s %s cm /%s Do Q\n",
		num(w), num(h), num(x), num(c.y(y+h)), name)
}

// num formata um número sem notação científica e sem zeros à direita.
//
// Com duas casas de precisão há sempre um dígito antes do ponto, por isso o
// resultado nunca fica vazio. O único caso a corrigir é o zero com sinal: um
// valor negativo muito pequeno sai "-0.00" e fica "-0" depois de aparado. Não é
// inválido no formato, mas suja o ficheiro e faz duas gerações do mesmo
// documento diferirem por causa de um arredondamento.
func num(v float64) string {
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "-0" {
		return "0"
	}
	return s
}

// escape prepara uma string para uma cadeia literal de PDF.
//
// Os parênteses delimitam as cadeias, por isso um parêntese no nome de uma
// empresa parte o ficheiro se não for escapado. É o género de erro que só
// aparece no cliente que tem "(Angola)" na designação social.
func escape(s string) string {
	b := toWinAnsi(s)
	var out bytes.Buffer
	out.Grow(len(b) + 8)
	for _, ch := range b {
		switch ch {
		case '\\', '(', ')':
			out.WriteByte('\\')
			out.WriteByte(ch)
		case '\r':
			out.WriteString("\\r")
		case '\n':
			out.WriteString("\\n")
		default:
			out.WriteByte(ch)
		}
	}
	return out.String()
}
