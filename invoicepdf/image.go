package invoicepdf

import (
	"bytes"
	"fmt"
	"image/png"
)

// logo é uma imagem já preparada para entrar no PDF.
type logo struct {
	width, height int
	rgb           []byte // três bytes por pixel
	alpha         []byte // um byte por pixel; nil quando a imagem é opaca
}

// decodeLogo lê um PNG e separa a cor da transparência.
//
// O PDF não sabe o que é um PNG: guarda as amostras em bruto e a transparência
// numa segunda imagem em tons de cinzento, a máscara suave. Separá-las é todo o
// trabalho, e é o que faz um logótipo com fundo transparente assentar bem sobre
// o branco da factura em vez de trazer um rectângulo preto atrás.
//
// Aceita qualquer PNG que a biblioteca padrão leia, incluindo os de paleta e os
// de escala de cinzentos, porque a conversão passa pela interface comum de
// imagem e não pelo formato de origem.
func decodeLogo(data []byte) (*logo, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("invoicepdf: ler o logótipo: %w", err)
	}
	// Não há guarda de dimensões aqui: o formato PNG exige largura e altura de
	// pelo menos um pixel, e uma imagem que as não tenha nem chega a ser
	// descodificada. Uma verificação a seguir só daria a impressão de cobrir um
	// caso que não existe.
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()

	out := &logo{width: w, height: h, rgb: make([]byte, 0, w*h*3)}
	alpha := make([]byte, 0, w*h)
	opaque := true

	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			// O modelo de cor do Go devolve as componentes já multiplicadas
			// pela transparência. O PDF quer-nas separadas, por isso é preciso
			// desfazer a multiplicação; sem isso, um logótipo semitransparente
			// escurece em vez de esbater.
			if a > 0 && a < 0xFFFF {
				r = r * 0xFFFF / a
				g = g * 0xFFFF / a
				bl = bl * 0xFFFF / a
			}
			out.rgb = append(out.rgb, clamp8(r), clamp8(g), clamp8(bl))
			alpha = append(alpha, byte(a>>8))
			if a>>8 != 0xFF {
				opaque = false
			}
		}
	}
	if !opaque {
		out.alpha = alpha
	}
	return out, nil
}

func clamp8(v uint32) byte {
	v >>= 8
	if v > 0xFF {
		return 0xFF
	}
	return byte(v)
}

// embed acrescenta a imagem ao documento e devolve o número do objecto.
func (l *logo) embed(p *pdf) int {
	dict := fmt.Sprintf(
		"/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8",
		l.width, l.height)

	if l.alpha != nil {
		mask := p.addStream(fmt.Sprintf(
			"/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8",
			l.width, l.height), l.alpha)
		dict += fmt.Sprintf(" /SMask %d 0 R", mask)
	}
	return p.addStream(dict, l.rgb)
}

// fit devolve as dimensões da imagem dentro de uma caixa, sem a deformar.
func (l *logo) fit(maxW, maxH float64) (w, h float64) {
	iw, ih := float64(l.width), float64(l.height)
	scale := maxW / iw
	if s := maxH / ih; s < scale {
		scale = s
	}
	return iw * scale, ih * scale
}
