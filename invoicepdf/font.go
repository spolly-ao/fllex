package invoicepdf

import "strings"

// font é uma das fontes de base do PDF.
//
// Só duas, e chegam: uma factura precisa de distinguir o que é rótulo do que é
// valor, e mais variantes só dariam a um documento fiscal um ar de brochura.
type font int

const (
	regular font = iota
	bold
)

func (f font) resource() string {
	if f == bold {
		return "F2"
	}
	return "F1"
}

// fontObject é a definição da fonte dentro do PDF.
func fontObject(f font) string {
	return "<< /Type /Font /Subtype /Type1 /BaseFont /" + f.baseFont() +
		" /Encoding /WinAnsiEncoding >>"
}

func (f font) baseFont() string {
	if f == bold {
		return "Helvetica-Bold"
	}
	return "Helvetica"
}

// width devolve a largura de um texto em pontos, para um dado corpo.
//
// É esta medida que permite alinhar valores à direita, centrar títulos e
// quebrar descrições longas. Sem ela, um gerador de PDF só consegue alinhar à
// esquerda, e uma factura com a coluna dos totais desalinhada não se lê.
func (f font) width(s string, size float64) float64 {
	total := 0
	for _, b := range toWinAnsi(s) {
		total += f.charWidth(b)
	}
	return float64(total) * size / 1000
}

func (f font) charWidth(b byte) int {
	table := helvetica
	if f == bold {
		table = helveticaBold
	}
	// Nas fontes de base do PDF, uma letra acentuada tem exactamente a largura
	// da letra sem acento: o acento é composto por cima e não ocupa avanço. É o
	// que permite medir português com uma tabela de ASCII.
	if base, ok := latin1Base[b]; ok {
		b = base
	}
	if w, ok := extraWidths[b]; ok {
		return w
	}
	if b < 32 || int(b-32) >= len(table) {
		return 556
	}
	return table[b-32]
}

// wrap parte um texto pelas palavras para caber numa largura.
//
// Uma descrição de linha que transborde para a coluna dos valores é o defeito
// mais visível de uma factura mal composta.
func (f font) wrap(s string, size, maxWidth float64) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := words[0]
	for _, w := range words[1:] {
		candidate := line + " " + w
		if f.width(candidate, size) <= maxWidth {
			line = candidate
			continue
		}
		lines = append(lines, line)
		line = w
	}
	return append(lines, line)
}

// truncate corta um texto que não caiba, acrescentando reticências.
//
// Serve os campos onde partir em várias linhas estragaria o alinhamento, como o
// nome de uma empresa no cabeçalho.
func (f font) truncate(s string, size, maxWidth float64) string {
	if f.width(s, size) <= maxWidth {
		return s
	}
	runes := []rune(s)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		candidate := strings.TrimRight(string(runes), " ") + "…"
		if f.width(candidate, size) <= maxWidth {
			return candidate
		}
	}
	return ""
}

// toWinAnsi converte texto para a codificação WinAnsi, que é a que as fontes de
// base do PDF usam.
//
// Cobre todo o português: os acentos vivem no intervalo Latin-1 e passam
// directos. Os caracteres tipográficos que o Windows pôs no intervalo 0x80 a
// 0x9F (aspas curvas, travessões, o símbolo do euro) têm de ser traduzidos um a
// um, porque em Unicode estão noutro sítio.
//
// O que não existir na codificação vira ponto de interrogação. Perder um
// caractere raro é mau; gerar um PDF ilegível por causa dele é pior.
func toWinAnsi(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r < 0x80:
			out = append(out, byte(r))
		case r >= 0xA0 && r <= 0xFF:
			out = append(out, byte(r))
		default:
			if b, ok := winAnsiSpecial[r]; ok {
				out = append(out, b)
				continue
			}
			out = append(out, '?')
		}
	}
	return out
}

// winAnsiSpecial são os caracteres que o WinAnsi põe entre 0x80 e 0x9F.
var winAnsiSpecial = map[rune]byte{
	'€': 0x80, // euro
	'‚': 0x82,
	'ƒ': 0x83,
	'„': 0x84,
	'…': 0x85, // reticências
	'†': 0x86,
	'‡': 0x87,
	'ˆ': 0x88,
	'‰': 0x89,
	'Š': 0x8A,
	'‹': 0x8B,
	'Œ': 0x8C,
	'Ž': 0x8E,
	'‘': 0x91, // aspas curvas
	'’': 0x92,
	'“': 0x93,
	'”': 0x94,
	'•': 0x95, // marca de lista
	'–': 0x96, // travessão curto
	'—': 0x97, // travessão longo
	'˜': 0x98,
	'™': 0x99, // marca registada
	'š': 0x9A,
	'›': 0x9B,
	'œ': 0x9C,
	'ž': 0x9E,
	'Ÿ': 0x9F,
}

// latin1Base diz qual é a letra sem acento de cada letra acentuada, para a
// medição de larguras.
var latin1Base = map[byte]byte{
	0xC0: 'A', 0xC1: 'A', 0xC2: 'A', 0xC3: 'A', 0xC4: 'A', 0xC5: 'A',
	0xC7: 'C',
	0xC8: 'E', 0xC9: 'E', 0xCA: 'E', 0xCB: 'E',
	0xCC: 'I', 0xCD: 'I', 0xCE: 'I', 0xCF: 'I',
	0xD1: 'N',
	0xD2: 'O', 0xD3: 'O', 0xD4: 'O', 0xD5: 'O', 0xD6: 'O',
	0xD9: 'U', 0xDA: 'U', 0xDB: 'U', 0xDC: 'U',
	0xDD: 'Y',
	0xE0: 'a', 0xE1: 'a', 0xE2: 'a', 0xE3: 'a', 0xE4: 'a', 0xE5: 'a',
	0xE7: 'c',
	0xE8: 'e', 0xE9: 'e', 0xEA: 'e', 0xEB: 'e',
	0xEC: 'i', 0xED: 'i', 0xEE: 'i', 0xEF: 'i',
	0xF1: 'n',
	0xF2: 'o', 0xF3: 'o', 0xF4: 'o', 0xF5: 'o', 0xF6: 'o',
	0xF9: 'u', 0xFA: 'u', 0xFB: 'u', 0xFC: 'u',
	0xFD: 'y', 0xFF: 'y',
}

// extraWidths são as larguras dos símbolos fora do ASCII que aparecem em
// documentos em português.
var extraWidths = map[byte]int{
	0x80: 556,            // euro
	0x85: 1000,           // reticências
	0x91: 222, 0x92: 222, // aspas curvas simples
	0x93: 333, 0x94: 333, // aspas curvas duplas
	0x95: 350,            // marca de lista
	0x96: 556,            // travessão curto
	0x97: 1000,           // travessão longo
	0xA0: 278,            // espaço inquebrável
	0xAA: 370,            // ª
	0xB0: 400,            // grau
	0xBA: 365,            // º
	0xAB: 556, 0xBB: 556, // aspas angulares
	0xB7: 278, // ponto medial
}

// helvetica são as larguras da Helvetica, do caractere 32 ao 126, em milésimos
// do corpo. Vêm da especificação das fontes de base do PDF.
var helvetica = [...]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, // 32-47
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, // 48-63
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778, // 64-79
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556, // 80-95
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556, // 96-111
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584, // 112-126
}

// helveticaBold são as larguras da Helvetica-Bold, no mesmo intervalo.
var helveticaBold = [...]int{
	278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278, // 32-47
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611, // 48-63
	975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778, // 64-79
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556, // 80-95
	333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611, // 96-111
	611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584, // 112-126
}
