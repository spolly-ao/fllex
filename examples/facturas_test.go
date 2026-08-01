package examples_test

import (
	"context"
	"fmt"

	"github.com/spolly-ao/fllex/invoice"
	"github.com/spolly-ao/fllex/invoicepdf"
	"github.com/spolly-ao/fllex/money"
)

// A proforma cobra-se antes de haver pagamento; a factura emite-se depois de o
// dinheiro entrar. Ambas funcionam com qualquer método: os pacotes `invoice` e
// `invoicepdf` não conhecem gateway nenhum.
func Example_facturas() {
	ctx := context.Background()
	emissor := invoice.NewIssuer(&documentosEmMemoria{}, &numerador{}, invoice.Party{
		Name: "Clínica Sagrada Esperança, Lda.", TaxID: "5417000000",
	})
	emissor.IDs = func() string { return "doc-1" }

	pedido := invoice.Request{
		CustomerID:  "cliente-1",
		BillTo:      invoice.Party{Name: "Ana Silva", TaxID: "5417999999"},
		Description: "Plano Essencial, mensal",
		Amount:      money.FromMajor(5900, money.AOA),
	}

	pro, err := emissor.Proforma(ctx, pedido)
	if err != nil {
		fmt.Println("erro:", err)
		return
	}
	fmt.Println(pro.Kind, pro.Number, pro.Status, pro.Total)

	// Pago. A factura leva a referência do gateway, e é ela que impede a
	// emissão em duplicado quando o webhook é reentregue.
	pedido.Provider, pedido.ProviderRef = "momenu", "tx-1"
	fac, err := emissor.Invoice(ctx, pedido)
	if err != nil {
		fmt.Println("erro:", err)
		return
	}
	fmt.Println(fac.Kind, fac.Number, fac.Status, fac.Total)

	repetida, _ := emissor.Invoice(ctx, pedido)
	fmt.Println("segunda emissão da mesma cobrança:", repetida)

	// Output:
	// proforma PF 2026/1 pending 5900.00 AOA
	// invoice FT 2026/2 paid 5900.00 AOA
	// segunda emissão da mesma cobrança: <nil>
}

// O PDF é escrito à mão sobre a biblioteca padrão, sem dependências. Os acentos
// vão em WinAnsi: escrever UTF-8 num fluxo que o leitor interpreta como WinAnsi
// transforma "Subscrição" em lixo.
func Example_facturaEmPDF() {
	doc := &invoice.Invoice{
		Kind: invoice.KindInvoice, Number: "FT 2026/1", Status: invoice.StatusPaid,
		Issuer: invoice.Party{Name: "Clínica Sagrada Esperança, Lda.", TaxID: "5417000000"},
		BillTo: invoice.Party{Name: "Ana Silva"},
		Lines: []invoice.Line{
			{Description: "Subscrição do Plano Essencial", Quantity: 1, UnitPrice: money.FromMajor(5900, money.AOA)},
		},
		Subtotal: money.FromMajor(5900, money.AOA),
		Total:    money.FromMajor(5900, money.AOA),
	}

	pdf, err := invoicepdf.Render(doc, invoicepdf.Options{
		Accent: "#2563EB",
		Footer: "Clínica Sagrada Esperança, Lda. · NIF 5417000000",
	})
	if err != nil {
		fmt.Println("erro:", err)
		return
	}
	fmt.Printf("%s, com conteúdo: %v\n", pdf[:8], len(pdf) > 1000)

	// Output:
	// %PDF-1.4, com conteúdo: true
}

// --- o mínimo para o emissor funcionar --------------------------------------
//
// No seu projecto, uma tabela e uma sequência. O que interessa é o contrato, e
// não estas implementações.

type documentosEmMemoria struct{ itens []*invoice.Invoice }

func (d *documentosEmMemoria) Create(_ context.Context, inv *invoice.Invoice) error {
	d.itens = append(d.itens, inv)
	return nil
}
func (d *documentosEmMemoria) Update(context.Context, *invoice.Invoice) error { return nil }
func (d *documentosEmMemoria) ByID(context.Context, string) (*invoice.Invoice, error) {
	return nil, nil
}

// É esta consulta que impede a factura em duplicado.
func (d *documentosEmMemoria) ExistsByProviderRef(_ context.Context, kind invoice.Kind, ref string) (bool, error) {
	for _, v := range d.itens {
		if v.Kind == kind && v.ProviderRef == ref {
			return true, nil
		}
	}
	return false, nil
}

func (d *documentosEmMemoria) PendingProformas(context.Context, string) ([]*invoice.Invoice, error) {
	return nil, nil
}
func (d *documentosEmMemoria) ListForCustomer(context.Context, string, int, int) ([]*invoice.Invoice, int64, error) {
	return nil, 0, nil
}

// numerador dá o número seguinte. Nunca se repete, nem quando um documento é
// cancelado: uma sequência com buracos explica-se, uma com repetições não.
type numerador struct{ n int }

func (s *numerador) Next(_ context.Context, kind invoice.Kind) (string, error) {
	s.n++
	prefixo := "FT"
	if kind == invoice.KindProforma {
		prefixo = "PF"
	}
	return fmt.Sprintf("%s 2026/%d", prefixo, s.n), nil
}
