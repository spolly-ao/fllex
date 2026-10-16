package payment

import (
	"context"
	"io"
	"time"

	"github.com/spolly-ao/fllex/money"
)

// StatementType diz o que uma linha do relatório do gateway representa.
type StatementType string

const (
	// StatementCharge é dinheiro cobrado a um comprador, que o gateway passou
	// a dever-nos.
	StatementCharge StatementType = "charge"
	// StatementRefund é dinheiro devolvido a um comprador pelo gateway. O
	// valor vem negativo.
	StatementRefund StatementType = "refund"
	// StatementPayout é dinheiro que o gateway transferiu para a nossa conta
	// bancária (a liquidação). O valor vem negativo: deixa de ser dívida do
	// gateway.
	StatementPayout StatementType = "payout"
	// StatementAdjustment é tudo o resto que mexe no saldo do gateway
	// (comissões cobradas à parte, contestações, acertos). Não se esconde: é
	// precisamente o que uma reconciliação tem de mostrar.
	StatementAdjustment StatementType = "adjustment"
)

// StatementEntry é uma linha do que o gateway diz que aconteceu ao dinheiro.
//
// É a matéria-prima da reconciliação: comparar o que o gateway diz que cobrou,
// devolveu e liquidou com o que o nosso lado registou é a única forma de dar
// por um pagamento perdido ou duplicado antes de o comprador se queixar.
type StatementEntry struct {
	// ID identifica a linha no gateway, e é estável: ler o mesmo período duas
	// vezes devolve os mesmos IDs, e é isso que deixa guardar as linhas sem as
	// duplicar.
	ID string
	// Type é o que a linha representa.
	Type StatementType
	// SourceRef é o objecto do gateway que deu origem à linha (a cobrança, o
	// estorno, a transferência, a factura, o pagamento da fila).
	SourceRef string
	// ChargeRef é a cobrança a que a linha diz respeito, no mesmo formato do
	// [ChargeResult.ProviderRef] que o [Provider.Charge] devolveu. Vazio
	// quando o gateway não o diz.
	ChargeRef string
	// Reference é a referência de quem cobrou ([ChargeRequest.Reference]),
	// quando o gateway a guarda e a devolve. É a correspondência exacta; sem
	// ela, só sobra comparar valor, hora e telemóvel.
	Reference string
	// Phone é o telemóvel do pagador, quando o gateway o diz (o MoMenu, nas
	// facturas). Serve para a correspondência quando não há referência.
	Phone string
	// Amount é o valor, na moeda da cobrança, com sinal: positivo entra na
	// dívida do gateway para connosco, negativo sai dela.
	Amount money.Amount
	// Fee é a comissão que o gateway reteve nesta linha, positiva. Zero quando
	// o gateway não a diz ou não a cobra aqui.
	Fee money.Amount
	// OccurredAt é quando o gateway registou a linha.
	OccurredAt time.Time
	// Raw guarda o que veio do gateway, para diagnóstico.
	Raw map[string]any
}

// StatementReader é a capacidade de pedir ao gateway, pela API, o relatório
// de um período: o que cobrou, devolveu e liquidou entre from (incluído) e to
// (excluído).
//
// Uma leitura que não consegue o período inteiro devolve erro, e nunca uma
// lista parcial: uma linha em falta faria um pagamento verdadeiro parecer
// inventado do nosso lado.
type StatementReader interface {
	Statement(ctx context.Context, from, to time.Time) ([]StatementEntry, error)
}

// StatementImporter é a capacidade de ler o relatório que o gateway deixa
// descarregar do painel dele, para os gateways que não o dão pela API. O
// ficheiro é o que a equipa descarrega e entrega, tal como veio.
//
// Uma linha ilegível devolve erro com o número da linha, e nunca se salta:
// saltá-la em silêncio era o mesmo que o gateway não a ter dito.
type StatementImporter interface {
	ImportStatement(r io.Reader) ([]StatementEntry, error)
}
