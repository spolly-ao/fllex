// Package offline cobre os métodos que não passam por gateway nenhum: a
// transferência ou depósito bancário, confirmados por um operador que vê o
// extracto, e a atribuição sem cobrança (cortesia, migração de um contrato em
// papel, cupão de 100%).
//
// Parece supérfluo ter um provider para o que não é processado por ninguém, e
// não é: sem ele, estes dois casos vivem em ramos especiais espalhados pelo
// código de negócio, cada um com o seu esquecimento. Com ele, uma atribuição
// manual passa pelo mesmo caminho de todas as outras, deixa o mesmo rasto e dá
// à subscrição o mesmo ciclo de onde renovar.
package offline

import (
	"context"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// Provider implementa [payment.Provider] para os métodos liquidados fora do
// sistema.
type Provider struct {
	// Currencies limita as moedas aceites. Vazio aceita todas, que é o normal:
	// uma transferência bancária não tem restrição de moeda do nosso lado.
	Currencies []money.Currency
	// TTL é o prazo dado a uma transferência antes de a cobrança expirar. Zero
	// deixa-a sem prazo.
	TTL time.Duration
}

// New cria o provider.
func New() *Provider { return &Provider{TTL: 7 * 24 * time.Hour} }

// Name devolve "offline".
func (p *Provider) Name() string { return "offline" }

// Methods: transferência e atribuição manual.
func (p *Provider) Methods() []payment.Method {
	return []payment.Method{payment.MethodExternal, payment.MethodManual}
}

// SupportsCurrency aceita qualquer moeda, salvo restrição explícita.
func (p *Provider) SupportsCurrency(c money.Currency) bool {
	if len(p.Currencies) == 0 {
		return true
	}
	want := money.NormalizeCurrency(string(c))
	for _, v := range p.Currencies {
		if money.NormalizeCurrency(string(v)) == want {
			return true
		}
	}
	return false
}

// Configured é sempre verdade: não há nada para configurar.
func (p *Provider) Configured() bool { return true }

// Charge regista a cobrança sem a apresentar a ninguém.
//
// A transferência fica pendente até um operador a confirmar com
// [Provider.Confirm]. A atribuição manual nasce já aprovada, porque não há
// dinheiro nenhum a esperar.
func (p *Provider) Charge(_ context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	if !p.SupportsCurrency(req.Amount.Currency) {
		return payment.ChargeResult{}, payment.ErrUnsupportedCurrency
	}
	switch req.Method {
	case payment.MethodManual:
		return payment.ChargeResult{
			Kind:        payment.KindPaid,
			Status:      payment.StatusApproved,
			ProviderRef: req.Reference,
		}, nil

	case payment.MethodExternal:
		if !req.Amount.IsPositive() {
			return payment.ChargeResult{}, payment.ErrAmountNotPositive
		}
		out := payment.ChargeResult{
			Kind:        payment.KindPending,
			Status:      payment.StatusPending,
			ProviderRef: req.Reference,
		}
		switch {
		case req.ExpiresAt != nil:
			out.ExpiresAt = req.ExpiresAt
		case p.TTL > 0:
			exp := time.Now().UTC().Add(p.TTL)
			out.ExpiresAt = &exp
		}
		return out, nil

	default:
		return payment.ChargeResult{}, payment.ErrUnsupportedMethod
	}
}

// Confirm marca uma cobrança externa como recebida.
//
// Só um operador o pode fazer, e por uma razão que não é de permissões mas de
// factos: quem confirma que o dinheiro entrou é quem vê o extracto bancário.
// operatorID e note ficam registados porque este é o único pagamento cuja prova
// não existe em lado nenhum senão no nosso registo.
func (p *Provider) Confirm(pay *payment.Payment, operatorID, note string) error {
	if pay == nil {
		return payment.ErrNotFound
	}
	if pay.Method != payment.MethodExternal {
		return payment.ErrUnsupportedMethod
	}
	if err := pay.Approve(pay.ProviderRef); err != nil {
		return err
	}
	if pay.Metadata == nil {
		pay.Metadata = map[string]string{}
	}
	pay.Metadata["confirmed_by"] = operatorID
	if note != "" {
		pay.Metadata["confirmation_note"] = note
	}
	pay.Provider = p.Name()
	return nil
}

// CancelCharge não tem nada para revogar do lado de fora.
func (p *Provider) CancelCharge(context.Context, payment.ChargeRequest, payment.ChargeResult) error {
	return nil
}
