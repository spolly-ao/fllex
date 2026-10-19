// Package ensaio é o gateway que não fala com rede nenhuma.
//
// **Existe para o modo de teste servir para alguma coisa.** Sem ele, uma conta
// acabada de abrir não consegue cobrar por referência nem por Multicaixa
// Express, porque esses métodos exigem credenciais de uma rede a sério, e essas
// só existem depois de haver contrato. Quem se registou para experimentar ficava
// parado antes da primeira cobrança.
//
// Produz os mesmos artefactos que a rede produziria: uma entidade e uma
// referência com o formato certo, um pedido de confirmação no telemóvel, um
// prazo. O que muda é que ninguém do outro lado é chamado, e nenhum dinheiro se
// move.
//
// **Não tem caminho para produção.** Quem o monta em modo real está a dizer que
// aprova cobranças que ninguém pagou, e é por isso que o `services/core` só o
// liga em contas de teste.
package ensaio

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// Provider implementa [payment.Provider] sem falar com ninguém.
type Provider struct {
	// Entidade é o número de cinco dígitos que aparece nas referências.
	// 00123 é reservado para ensaio e não é atribuído a ninguém.
	Entidade string
	// TTL é o prazo de uma referência. Vinte e quatro horas, como as verdadeiras.
	TTL time.Duration
}

// New cria o gateway de ensaio.
func New() *Provider {
	return &Provider{Entidade: "00123", TTL: 24 * time.Hour}
}

// Name devolve "ensaio". Aparece no painel e nos eventos, e é deliberado: quem
// olhar para uma cobrança tem de saber que não foi a sério.
func (p *Provider) Name() string { return "ensaio" }

// Methods: todos os que a fllex sabe cobrar.
//
// É o que permite escrever a integração de qualquer método antes de existir
// contrato com a rede que o opera.
func (p *Provider) Methods() []payment.Method {
	return []payment.Method{
		payment.MethodReference,
		payment.MethodMCX,
		payment.MethodBAIPaga,
		payment.MethodCard,
		payment.MethodPix,
		payment.MethodWallet,
		payment.MethodDirectDebit,
		payment.MethodBankTransfer,
		payment.MethodExternal,
		payment.MethodManual,
	}
}

// SupportsCurrency aceita todas: um ensaio não tem razão para recusar moedas.
func (p *Provider) SupportsCurrency(money.Currency) bool { return true }

// Configured é sempre verdade: não há credenciais para configurar, e é esse o
// ponto.
func (p *Provider) Configured() bool { return true }

// Charge devolve o que a rede devolveria.
func (p *Provider) Charge(_ context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	if !req.Amount.IsPositive() {
		return payment.ChargeResult{}, payment.ErrAmountNotPositive
	}

	prazo := req.ExpiresAt
	if prazo == nil && p.TTL > 0 {
		t := time.Now().UTC().Add(p.TTL)
		prazo = &t
	}

	switch req.Method {
	case payment.MethodReference:
		return payment.ChargeResult{
			Kind:        payment.KindReference,
			Status:      payment.StatusPending,
			ProviderRef: req.Reference,
			Entity:      p.Entidade,
			Reference:   referenciaDe(req.Reference),
			// A data da referência vai só com o dia: é o que aparece no talão do
			// ATM, e a hora não cabe lá.
			DueDate:   diaDe(prazo),
			ExpiresAt: prazo,
		}, nil

	case payment.MethodMCX, payment.MethodBAIPaga, payment.MethodPix, payment.MethodWallet, payment.MethodDirectDebit:
		// Fica à espera da confirmação de quem paga, como a rede a sério (no
		// BAI PAGA, a autorização com o OTP no telemóvel do cliente). Quem
		// integra tem de escrever o ecrã de espera na mesma, e é isso que se
		// quer: o código que trata a espera escreve-se antes da primeira espera
		// real, e não depois.
		return payment.ChargeResult{
			Kind:        payment.KindPending,
			Status:      payment.StatusPending,
			ProviderRef: req.Reference,
			ExpiresAt:   prazo,
		}, nil

	case payment.MethodCard:
		return payment.ChargeResult{
			Kind:        payment.KindPaid,
			Status:      payment.StatusApproved,
			ProviderRef: req.Reference,
		}, nil

	case payment.MethodManual:
		return payment.ChargeResult{
			Kind:        payment.KindPaid,
			Status:      payment.StatusApproved,
			ProviderRef: req.Reference,
		}, nil

	case payment.MethodExternal, payment.MethodBankTransfer:
		return payment.ChargeResult{
			Kind:        payment.KindPending,
			Status:      payment.StatusPending,
			ProviderRef: req.Reference,
			ExpiresAt:   prazo,
		}, nil
	}
	return payment.ChargeResult{}, payment.ErrUnsupportedMethod
}

// Refund devolve o que a rede devolveria a um estorno aceite: um identificador
// e o estado aprovado, sem dinheiro nenhum a mexer-se.
//
// Existe para quem integra escrever o caminho do estorno contra o modo de teste
// antes de ter contrato com uma rede que estorne. O ensaio não guarda as
// cobranças, e por isso não sabe quanto foi cobrado: um estorno tem de dizer o
// valor (o zero, que noutros gateways devolve tudo, aqui é recusado), e não se
// recusa um estorno acima do cobrado. Essa conta é de quem pede, que é quem
// sabe quanto já devolveu. A mesma chave de idempotência dá sempre o mesmo
// identificador, como num gateway que a aceite.
func (p *Provider) Refund(_ context.Context, r payment.Refund) (payment.RefundResult, error) {
	if strings.TrimSpace(r.ChargeRef) == "" {
		return payment.RefundResult{}, fmt.Errorf("ensaio: o estorno exige a cobrança: %w", payment.ErrInvalidTransition)
	}
	if r.Amount.Minor == 0 {
		return payment.RefundResult{}, fmt.Errorf("ensaio: o estorno total exige o valor, porque o ensaio não guarda as cobranças: %w", payment.ErrUnsupported)
	}
	if !r.Amount.IsPositive() {
		return payment.RefundResult{}, payment.ErrAmountNotPositive
	}
	semente := r.IdempotencyKey
	if semente == "" {
		semente = fmt.Sprintf("%s|%d|%s|%d", r.ChargeRef, r.Amount.Minor, r.Amount.Currency, time.Now().UnixNano())
	}
	soma := sha256.Sum256([]byte(r.ChargeRef + "|" + semente))
	return payment.RefundResult{
		RefundRef: fmt.Sprintf("re_ensaio_%x", soma[:12]),
		Status:    payment.StatusApproved,
		Amount:    r.Amount,
	}, nil
}

// HonorsExpiry: o ensaio devolve o prazo pedido tal como veio, em todos os
// métodos que esperam por quem paga. Quem integra vê no ensaio o mesmo prazo
// que verá num gateway que o aplique.
func (p *Provider) HonorsExpiry(payment.Method) bool { return true }

// CancelCharge não tem nada para revogar do lado de fora.
func (p *Provider) CancelCharge(context.Context, payment.ChargeRequest, payment.ChargeResult) error {
	return nil
}

// diaDe escreve a data da referência. Vazio quando não há prazo.
func diaDe(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

/*
referenciaDe devolve nove dígitos a partir do identificador da cobrança.

**Determinista de propósito.** A mesma cobrança consultada duas vezes tem de dar
a mesma referência: quem integra guarda-a, mostra-a ao cliente, e uma referência
que mudasse entre chamadas era um cliente a pagar um número que já não existe.

Não são aleatórios nem sequenciais porque nenhum dos dois serve aqui: o
aleatório perde a propriedade acima, e o sequencial obrigava a guardar estado num
gateway que existe para não guardar nenhum.
*/
func referenciaDe(id string) string {
	soma := sha256.Sum256([]byte(id))
	n := binary.BigEndian.Uint64(soma[:8]) % 1000000000
	return fmt.Sprintf("%09d", n)
}
