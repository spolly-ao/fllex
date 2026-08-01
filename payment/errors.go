package payment

import (
	"errors"
	"fmt"
)

var (
	// ErrNotConfigured: o provider existe e está registado mas falta-lhe
	// configuração (tipicamente a chave de API). Registá-lo sem chave é
	// deliberado: a aplicação arranca, e só quem tentar cobrar é que falha.
	ErrNotConfigured = errors.New("fllex: provider não configurado")

	// ErrUnsupportedMethod: o provider não sabe cobrar por este método.
	ErrUnsupportedMethod = errors.New("fllex: método de pagamento não suportado pelo provider")

	// ErrUnsupportedCurrency: o provider não processa esta moeda.
	ErrUnsupportedCurrency = errors.New("fllex: moeda não suportada pelo provider")

	// ErrNoProvider: nenhum provider registado cobre o pedido.
	ErrNoProvider = errors.New("fllex: nenhum método de pagamento disponível")

	// ErrUnsupported: a operação não existe neste provider (ex.: pedir um portal
	// de gestão a um gateway de pagamento único).
	ErrUnsupported = errors.New("fllex: operação não suportada pelo provider")

	// ErrBadSignature: a assinatura do webhook não confere. Nunca trate o
	// conteúdo de um webhook com esta falha: qualquer pessoa pode enviar um POST.
	ErrBadSignature = errors.New("fllex: assinatura de webhook inválida")

	// ErrInvalidTransition: a mudança de estado pedida não é legítima (ex.:
	// aprovar um pagamento já aprovado).
	ErrInvalidTransition = errors.New("fllex: transição de estado inválida")

	// ErrMandateRequired: o débito directo precisa de um mandato.
	ErrMandateRequired = errors.New("fllex: débito directo exige um mandato")

	// ErrMandateNotActive: o mandato existe mas ainda não foi activado pelo
	// titular no seu banco. Não é uma falha do sistema nem do gateway: é o
	// cliente que ainda não fez a sua parte, por isso não deve gastar
	// tentativas de retentativa nem alarmar ninguém.
	ErrMandateNotActive = errors.New("fllex: mandato ainda não activado pelo titular")

	// ErrInsufficientFunds: a carteira não cobre a cobrança.
	ErrInsufficientFunds = errors.New("fllex: saldo insuficiente")

	// ErrAmountNotPositive: tentou-se cobrar zero ou menos.
	ErrAmountNotPositive = errors.New("fllex: o valor a cobrar tem de ser positivo")

	// ErrNotFound: o registo pedido não existe.
	ErrNotFound = errors.New("fllex: não encontrado")
)

// GatewayError é a falha devolvida por um gateway, com o estado HTTP e o corpo
// preservados. Preservá-los importa: a mensagem do gateway é muitas vezes a
// única explicação de por que é que uma cobrança foi recusada, e é o que um
// operador precisa de ver no suporte.
type GatewayError struct {
	// Provider é o nome do gateway ("stripe", "momenu", "proxypay").
	Provider string
	// StatusCode é o estado HTTP da resposta.
	StatusCode int
	// Code é o código de erro do gateway, quando o traz.
	Code string
	// Message é a explicação legível.
	Message string
	// Body é o corpo em bruto, para diagnóstico.
	Body string
}

func (e *GatewayError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = e.Body
	}
	if e.Code != "" {
		return fmt.Sprintf("%s: %d %s (%s)", e.Provider, e.StatusCode, msg, e.Code)
	}
	return fmt.Sprintf("%s: %d %s", e.Provider, e.StatusCode, msg)
}

// Retryable indica se vale a pena repetir o pedido. Erros do lado do servidor
// (5xx) e o 429 são passageiros; um 4xx é um pedido mal formado ou uma recusa,
// e repeti-lo dá o mesmo resultado.
func (e *GatewayError) Retryable() bool {
	return e.StatusCode >= 500 || e.StatusCode == 429 || e.StatusCode == 0
}

// IsRetryable indica se um erro qualquer vale a pena repetir. Erros que não
// sejam de gateway contam como passageiros (falha de rede, timeout), porque não
// há informação que diga o contrário.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var ge *GatewayError
	if errors.As(err, &ge) {
		return ge.Retryable()
	}
	// Uma configuração em falta ou um método não suportado não melhoram com
	// repetição.
	switch {
	case errors.Is(err, ErrNotConfigured),
		errors.Is(err, ErrUnsupportedMethod),
		errors.Is(err, ErrUnsupportedCurrency),
		errors.Is(err, ErrUnsupported),
		errors.Is(err, ErrNoProvider),
		errors.Is(err, ErrBadSignature),
		errors.Is(err, ErrInvalidTransition),
		errors.Is(err, ErrMandateRequired),
		errors.Is(err, ErrMandateNotActive),
		errors.Is(err, ErrAmountNotPositive),
		errors.Is(err, ErrInsufficientFunds):
		return false
	}
	return true
}
