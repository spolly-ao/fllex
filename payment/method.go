package payment

import "strings"

// Method é o método pelo qual o dinheiro entra. É o vocabulário partilhado por
// todos os providers: o mesmo código significa a mesma coisa venha ele do
// MoMenu, do Proxypay ou do Stripe.
type Method string

const (
	// MethodCard é o cartão, com os dados guardados no provider e cobrança
	// recorrente automática (Stripe).
	MethodCard Method = "card"

	// MethodMCX é o Multicaixa Express: push imediato no telemóvel do cliente.
	// O pedido é síncrono e uma resposta com sucesso significa pago.
	MethodMCX Method = "mcx"

	// MethodEKwanza é o eKwanza: devolve um código e um QR, confirma-se depois
	// (webhook ou consulta de estado).
	MethodEKwanza Method = "ekwanza"

	// MethodReference é a referência bancária diferida (entidade + referência +
	// validade), paga ao multibanco, no ATM ou na app do banco.
	MethodReference Method = "reference"

	// MethodDirectDebit é o débito directo em conta, cobrado contra um mandato
	// previamente autorizado pelo titular (Proxypay DDS).
	MethodDirectDebit Method = "direct_debit"

	// MethodWallet é o saldo pré-pago do próprio cliente. Onde não há cobrança
	// recorrente fiável, o cliente carrega a carteira e o saldo é descontado a
	// cada renovação.
	MethodWallet Method = "wallet"

	// MethodExternal é o depósito ou a transferência bancária, liquidado fora do
	// sistema. Não passa por gateway nenhum: não há referência para gerar nem
	// banco a quem apresentar a cobrança. O dinheiro entra na conta e é um
	// operador que o confirma.
	//
	// É exclusivo do backoffice: não há nada que um cliente possa fazer sozinho
	// com ele, porque quem o marca como pago é quem vê o extracto bancário.
	MethodExternal Method = "external"

	// MethodManual é a atribuição sem cobrança (cortesia, migração de contrato
	// em papel, cupão de 100%). Fica registada como pagamento de valor zero para
	// a subscrição ter um ciclo de onde renovar.
	MethodManual Method = "manual"
)

// AllMethods são todos os métodos conhecidos, na ordem em que fazem sentido
// para um cliente escolher.
var AllMethods = []Method{
	MethodMCX, MethodReference, MethodEKwanza, MethodCard,
	MethodDirectDebit, MethodWallet, MethodExternal, MethodManual,
}

// ParseMethod normaliza a escrita de um método. Aceita as variantes que
// costumam aparecer em esquemas antigos ("REFERENCE", "multicaixa", "dd",
// "transferencia") e devolve o código canónico; o que não reconhece devolve
// string vazia, para quem chama decidir o que fazer (tipicamente cair no
// método por omissão).
func ParseMethod(s string) Method {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "card", "cartao", "cartão", "stripe", "credit_card":
		return MethodCard
	case "mcx", "multicaixa", "multicaixa_express", "express":
		return MethodMCX
	case "ekwanza", "e-kwanza", "ekz":
		return MethodEKwanza
	case "reference", "referencia", "referência", "ref", "atm", "proxypay":
		return MethodReference
	case "direct_debit", "directdebit", "debito_directo", "débito_directo", "dd", "dds":
		return MethodDirectDebit
	case "wallet", "carteira", "saldo", "balance":
		return MethodWallet
	case "external", "externo", "deposit", "deposito", "depósito", "transfer", "transferencia", "transferência":
		return MethodExternal
	case "manual", "grant", "cortesia", "free":
		return MethodManual
	default:
		return ""
	}
}

// MethodOrDefault devolve m quando é um método conhecido e, caso contrário, o
// fallback. Use nos pontos onde o método é opcional na entrada.
func MethodOrDefault(m Method, fallback Method) Method {
	if m.Valid() {
		return m
	}
	return fallback
}

// Valid indica se o método é um dos conhecidos.
func (m Method) Valid() bool {
	for _, v := range AllMethods {
		if v == m {
			return true
		}
	}
	return false
}

// String devolve o código do método.
func (m Method) String() string { return string(m) }

// Recurring indica se o método cobra sozinho no ciclo seguinte, sem o cliente
// ter de agir. Só o cartão e o débito directo o fazem; tudo o resto exige uma
// nova cobrança em cada ciclo, e é essa diferença que decide se a renovação
// precisa de emitir proforma e link de pagamento.
func (m Method) Recurring() bool {
	return m == MethodCard || m == MethodDirectDebit
}

// Deferred indica se o pagamento é concluído mais tarde pelo cliente (não é
// síncrono nem automático). Os métodos diferidos precisam de confirmação por
// consulta de estado ou webhook, e de um prazo de validade.
func (m Method) Deferred() bool {
	return m == MethodReference || m == MethodEKwanza || m == MethodExternal
}

// Instant indica se o desfecho do pagamento é conhecido na própria chamada.
// O Multicaixa Express é o caso: o pedido espera a confirmação no telemóvel e
// uma resposta com sucesso significa que o dinheiro já entrou.
func (m Method) Instant() bool { return m == MethodMCX || m == MethodWallet }

// SelfService indica se o método pode ser oferecido a um cliente sem
// intervenção de um operador. O externo e o manual não podem.
func (m Method) SelfService() bool {
	return m != MethodExternal && m != MethodManual
}
