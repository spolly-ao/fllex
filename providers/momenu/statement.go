package momenu

import (
	"context"
	"fmt"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// paginaDeFacturas é quantas facturas se pedem de cada vez: o máximo do MoMenu.
const paginaDeFacturas = 100

// Statement devolve o relatório do período pelas facturas do MoMenu
// ([payment.StatementReader]): o MoMenu emite uma factura por cada pagamento
// que recebe, e a listagem das facturas é o único relatório que a API dele dá.
// Como a reconciliação de [Reconciler], **só funciona em contas Pro**; nas
// outras a listagem responde 403, e isso chega a quem chama como erro.
//
// A factura não traz a nossa referência nem o transactionId da cobrança: os
// dados que os dois lados partilham são o valor, a hora e o telemóvel. Por isso
// cada factura do período pede o detalhe, que é onde está o telemóvel, e a
// linha sai sem ChargeRef nem Reference. Quem reconcilia compara pelo valor
// exacto, pela hora com folga e pelos dígitos do telemóvel, como o
// [Reconciler] faz.
//
// A listagem lê-se inteira, página a página, e filtra-se pela data de cada
// factura: a documentação não promete a ordem, e parar na primeira factura
// fora do período podia deixar de fora uma que lá estava.
func (p *Provider) Statement(ctx context.Context, from, to time.Time) ([]payment.StatementEntry, error) {
	if !p.Configured() {
		return nil, payment.ErrNotConfigured
	}
	var out []payment.StatementEntry
	offset := 0
	for {
		pagina, err := p.client.ListInvoices(ctx, paginaDeFacturas, offset)
		if err != nil {
			return nil, err
		}
		if !pagina.Success {
			return nil, fmt.Errorf("momenu: a listagem das facturas não teve sucesso (offset %d)", offset)
		}
		for _, f := range pagina.Invoices {
			quando, ok := parseDate(f.CreatedAt)
			if !ok {
				return nil, fmt.Errorf("momenu: a factura %s tem a data ilegível %q", f.InvoiceNumber, f.CreatedAt)
			}
			if quando.Before(from) || !quando.Before(to) {
				continue
			}
			d, err := p.client.GetInvoice(ctx, f.InvoiceNumber)
			if err != nil {
				return nil, fmt.Errorf("momenu: o detalhe da factura %s: %w", f.InvoiceNumber, err)
			}
			if !d.Success {
				return nil, fmt.Errorf("momenu: o detalhe da factura %s não teve sucesso", f.InvoiceNumber)
			}
			out = append(out, payment.StatementEntry{
				ID: f.InvoiceID, Type: payment.StatementCharge, SourceRef: f.InvoiceNumber,
				Phone:  d.Invoice.Customer.Phone,
				Amount: money.FromFloat(f.Total, money.AOA), Fee: money.Zero(money.AOA),
				OccurredAt: quando,
				Raw: map[string]any{"invoiceNumber": f.InvoiceNumber, "invoiceType": f.InvoiceType,
					"paymentMethod": d.Invoice.PaymentMethod, "total": f.Total, "invoiceUrl": f.InvoiceURL},
			})
		}
		offset += len(pagina.Invoices)
		if len(pagina.Invoices) == 0 || offset >= pagina.Total {
			return out, nil
		}
	}
}
