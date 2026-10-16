package baipaga

import (
	"io"
	"strings"

	"github.com/spolly-ao/fllex/internal/relatorio"
	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// ImportStatement lê o relatório de pagamentos descarregado do portal do BAI
// PAGA ([payment.StatementImporter]).
//
// A API do BAI PAGA consulta um pagamento de cada vez, pela referência, e não
// lista um período. O relatório do período vem do portal do banco.
//
// As colunas lêem-se pelo nome, que é o dos campos da consulta de estado:
// id, externalId, amount, status e creationDate, obrigatórias; currency,
// lastChangeDate, totalReversed e customerMsisdn, quando o portal as exporta.
// Só entram os pagamentos que o BAI dá por pagos (SUCCESS e COMPENSATED, como
// na consulta): os pendentes e os recusados não são dinheiro. Um pagamento com
// valor revertido dá também uma linha de devolução, negativa, porque o
// dinheiro que o BAI devolveu ao comprador deixou de nos ser devido.
func (p *Provider) ImportStatement(r io.Reader) ([]payment.StatementEntry, error) {
	t, err := relatorio.Ler(r)
	if err != nil {
		return nil, err
	}
	if err := t.Exigir("id", "externalId", "amount", "status", "creationDate"); err != nil {
		return nil, err
	}
	var out []payment.StatementEntry
	for i := range t.Linhas() {
		id := t.Valor(i, "id")
		if id == "" {
			return nil, relatorio.Erro(i, "pagamento sem id")
		}
		if estadoBAI(t.Valor(i, "status")) != payment.StatusApproved {
			continue
		}
		if m := strings.ToUpper(t.Valor(i, "currency")); m != "" && m != moedaBAI && m != string(money.AOA) {
			return nil, relatorio.Erro(i, "moeda %q: o BAI PAGA só cobra kwanzas", m)
		}
		valor, err := money.Parse(t.Valor(i, "amount"), money.AOA)
		if err != nil || !valor.IsPositive() {
			return nil, relatorio.Erro(i, "valor inválido %q", t.Valor(i, "amount"))
		}
		// O dinheiro entra quando o BAI o dá por pago, que é a última mudança
		// de estado; sem ela, a criação.
		data := t.Valor(i, "lastChangeDate")
		if data == "" {
			data = t.Valor(i, "creationDate")
		}
		quando, ok := relatorio.Data(data)
		if !ok {
			return nil, relatorio.Erro(i, "data inválida %q", data)
		}
		ref := t.Valor(i, "externalId")
		linha := payment.StatementEntry{
			ID: id, Type: payment.StatementCharge, SourceRef: id, ChargeRef: id, Reference: ref,
			Phone: t.Valor(i, "customerMsisdn"), Amount: valor, Fee: money.Zero(money.AOA), OccurredAt: quando,
			Raw: map[string]any{"status": t.Valor(i, "status"), "amount": t.Valor(i, "amount")},
		}
		out = append(out, linha)
		if rev := t.Valor(i, "totalReversed"); rev != "" {
			revertido, err := money.Parse(rev, money.AOA)
			if err != nil || revertido.IsNegative() || revertido.GreaterThan(valor) {
				return nil, relatorio.Erro(i, "valor revertido inválido %q", rev)
			}
			if revertido.IsPositive() {
				dev := linha
				dev.ID, dev.Type, dev.Amount = id+":revertido", payment.StatementRefund, revertido.Neg()
				dev.Raw = map[string]any{"totalReversed": rev}
				out = append(out, dev)
			}
		}
	}
	return out, nil
}
