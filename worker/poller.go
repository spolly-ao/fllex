package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/spolly-ao/fllex/payment"
)

// Poller confirma, junto do gateway, as cobranças pendentes que já foram pagas.
//
// É indispensável nos métodos diferidos: uma referência paga ao balcão ou no
// ATM não produz nenhum aviso de que se possa depender. Onde o webhook não é
// assinado nem reentregue, este processo é a única confirmação a sério; onde é,
// continua a ser a rede que apanha o que se perdeu numa paragem.
type Poller struct {
	// Store é o armazenamento das cobranças.
	Store payment.Store
	// Registry resolve o provider de cada cobrança.
	Registry *payment.Registry
	// Provider limita a consulta a um gateway. Vazio consulta todos.
	Provider string
	// BatchSize é quantas cobranças cada passagem consulta. Zero usa 50.
	//
	// Vale a pena mantê-lo baixo: cada cobrança é uma chamada de rede, e uma
	// passagem que demore mais do que o intervalo acumula atraso.
	BatchSize int
	// OnPaid é chamado quando uma cobrança é confirmada, dentro da passagem.
	// É aqui que se activa a subscrição, se aprovisiona o serviço ou se emite a
	// factura.
	//
	// Devolver erro deixa a cobrança por confirmar, para ser tentada de novo na
	// passagem seguinte. É o comportamento certo: mais vale repetir uma
	// activação idempotente do que dar por confirmada uma cobrança cujo efeito
	// não chegou a acontecer.
	OnPaid func(ctx context.Context, p *payment.Payment, status payment.ChargeStatus) error
	// Log recebe o que corre mal.
	Log *slog.Logger
}

// Run faz uma passagem.
func (p *Poller) Run(ctx context.Context) {
	if p.Registry == nil {
		p.log().Error("expirer: Registry não pode ser nil")
		return
	}

	limit := p.BatchSize
	if limit <= 0 {
		limit = 50
	}
	pending, err := p.Store.PendingVerifiable(ctx, p.Provider, limit)
	if err != nil {
		p.log().Error("fllex: falha a obter cobranças por confirmar", "err", err)
		return
	}
	for _, pay := range pending {
		if err := ctx.Err(); err != nil {
			return
		}
		p.checkOne(ctx, pay)
	}
}

func (p *Poller) checkOne(ctx context.Context, pay *payment.Payment) {
	verifier, ok := p.Registry.Verifier(pay.Provider)
	if !ok {
		return
	}
	status, err := verifier.VerifyCharge(ctx, refOf(pay), pay.ID)
	if err != nil {
		p.log().Warn("fllex: falha a consultar estado da cobrança",
			"err", err, "payment", pay.ID, "provider", pay.Provider)
		return
	}
	if !status.Paid {
		return
	}

	// O efeito acontece antes de a cobrança ser dada por paga. Pela ordem
	// contrária, uma falha a activar o serviço deixava a cobrança marcada como
	// confirmada e o cliente sem nada, e nenhuma passagem seguinte voltaria a
	// tentar.
	if p.OnPaid != nil {
		if err := p.OnPaid(ctx, pay, status); err != nil {
			p.log().Error("fllex: falha a aplicar o efeito do pagamento",
				"err", err, "payment", pay.ID)
			return
		}
	}

	if err := pay.Approve(pay.ProviderRef); err != nil {
		p.log().Warn("fllex: cobrança em estado inesperado ao confirmar",
			"err", err, "payment", pay.ID, "status", pay.Status)
		return
	}
	if status.InvoiceURL != "" {
		pay.InvoiceURL = status.InvoiceURL
	}
	if err := p.Store.Update(ctx, pay); err != nil {
		p.log().Error("fllex: falha a gravar cobrança confirmada", "err", err, "payment", pay.ID)
	}
}

func refOf(pay *payment.Payment) string {
	if pay.StatusRef != "" {
		return pay.StatusRef
	}
	return pay.ProviderRef
}

func (p *Poller) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// Expirer marca como expiradas as cobranças cujo prazo passou e revoga-as no
// gateway.
//
// A revogação é a parte que se esquece e que custa dinheiro: sem ela, a
// referência de uma subscrição já terminada continua viva no ATM, e o cliente
// que a pague fica com um pagamento que ninguém esperava e que alguém tem de
// devolver à mão.
type Expirer struct {
	// Store é o armazenamento das cobranças.
	Store payment.Store
	// Registry resolve o provider de cada cobrança.
	Registry *payment.Registry
	// BatchSize é quantas cobranças cada passagem trata. Zero usa 100.
	BatchSize int
	// OnExpired é chamado depois de a cobrança ser dada por expirada.
	OnExpired func(ctx context.Context, p *payment.Payment)
	// Log recebe o que corre mal.
	Log *slog.Logger
}

// Run faz uma passagem.
func (e *Expirer) Run(ctx context.Context) {
	if e.Registry == nil {
		e.log().Error("expirer: Registry não pode ser nil")
		return
	}

	limit := e.BatchSize
	if limit <= 0 {
		limit = 100
	}
	expired, err := e.Store.ExpiredPending(ctx, time.Now().UTC(), limit)
	if err != nil {
		e.log().Error("fllex: falha a obter cobranças expiradas", "err", err)
		return
	}
	for _, pay := range expired {
		if err := ctx.Err(); err != nil {
			return
		}
		if provider, ok := e.Registry.Get(pay.Provider); ok {
			if canceller, ok := provider.(payment.Canceller); ok {
				result := payment.ChargeResult{
					ProviderRef: pay.ProviderRef,
					Reference:   pay.Reference,
					Entity:      pay.Entity,
					ExternalID:  pay.ExternalID,
				}
				req := pay.ToChargeRequest(payment.Customer{ID: pay.CustomerID})
				if err := canceller.CancelCharge(ctx, req, result); err != nil {
					e.log().Warn("fllex: falha a revogar cobrança expirada", "err", err, "payment", pay.ID)
				}
			}
		}
		if err := pay.Expire(); err != nil {
			continue
		}
		if err := e.Store.Update(ctx, pay); err != nil {
			e.log().Error("fllex: falha a gravar cobrança expirada", "err", err, "payment", pay.ID)
			continue
		}
		if e.OnExpired != nil {
			e.OnExpired(ctx, pay)
		}
	}
}

func (e *Expirer) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}
