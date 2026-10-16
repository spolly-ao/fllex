package proxypay

import (
	"io"

	"github.com/spolly-ao/fllex/internal/relatorio"
	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// ImportStatement lê o relatório de pagamentos descarregado do painel do
// Proxypay ([payment.StatementImporter]).
//
// A API do Proxypay não tem relatório por período: GET /payments é a fila dos
// pagamentos por confirmar, e esvazia-se a cada confirmação ([Drainer]). O
// período inteiro só existe no painel, e é de lá que vem o ficheiro.
//
// As colunas lêem-se pelo nome, que é o dos campos do pagamento na API: id,
// reference_id, amount e datetime, obrigatórias; e o campo personalizado da
// nossa referência ([Provider.ReferenceField], por omissão "invoice", aceite
// também como "custom_fields.invoice"), quando o painel o exporta. O
// ChargeRef é o número da referência, que é o [payment.ChargeResult.ProviderRef]
// das referências deste provider.
func (p *Provider) ImportStatement(r io.Reader) ([]payment.StatementEntry, error) {
	t, err := relatorio.Ler(r)
	if err != nil {
		return nil, err
	}
	if err := t.Exigir("id", "reference_id", "amount", "datetime"); err != nil {
		return nil, err
	}
	campo := p.ReferenceField
	if campo == "" {
		campo = "invoice"
	}
	out := make([]payment.StatementEntry, 0, t.Linhas())
	for i := range t.Linhas() {
		id := t.Valor(i, "id")
		if id == "" {
			return nil, relatorio.Erro(i, "pagamento sem id")
		}
		valor, err := money.Parse(t.Valor(i, "amount"), money.AOA)
		if err != nil || !valor.IsPositive() {
			return nil, relatorio.Erro(i, "valor inválido %q", t.Valor(i, "amount"))
		}
		quando, ok := relatorio.Data(t.Valor(i, "datetime"))
		if !ok {
			return nil, relatorio.Erro(i, "data inválida %q", t.Valor(i, "datetime"))
		}
		ref := t.Valor(i, "reference_id")
		out = append(out, payment.StatementEntry{
			ID: id, Type: payment.StatementCharge, SourceRef: id, ChargeRef: ref,
			Reference:  t.Valor(i, campo, "custom_fields."+campo),
			Amount:     valor,
			Fee:        money.Zero(money.AOA),
			OccurredAt: quando,
			Raw:        map[string]any{"reference_id": ref, "amount": t.Valor(i, "amount"), "datetime": t.Valor(i, "datetime")},
		})
	}
	return out, nil
}
