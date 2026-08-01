package money

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Rate é uma taxa de câmbio entre duas moedas, com validade.
//
// A validade não é decorativa. Uma taxa semeada uma vez no arranque e nunca
// mais actualizada fica na base de dados para sempre, e uma desvalorização uns
// meses depois passa a cobrar a mais ou a menos em todas as transacções
// convertidas, sem ninguém dar por isso. Preferir recusar a conversão a
// converter por um valor que já não é verdade.
type Rate struct {
	// From e To são as moedas.
	From Currency
	To   Currency
	// Value é quantas unidades de To valem uma unidade de From.
	Value float64
	// ValidUntil é até quando a taxa serve.
	ValidUntil time.Time
	// Source identifica quem a forneceu, para se saber em que confiar.
	Source string
	// FetchedAt é quando foi obtida.
	FetchedAt time.Time
}

// Valid indica se a taxa ainda serve.
func (r Rate) Valid(now time.Time) bool {
	return r.Value > 0 && (r.ValidUntil.IsZero() || now.Before(r.ValidUntil))
}

// ErrNoRate é devolvido quando não há taxa válida para o par de moedas.
var ErrNoRate = fmt.Errorf("money: sem taxa de câmbio válida")

// RateStore é a fonte das taxas. Implemente-a sobre a sua tabela ou sobre o
// serviço que consulta.
type RateStore interface {
	// Rate devolve a taxa entre duas moedas, ou ErrNoRate.
	Rate(ctx context.Context, from, to Currency) (Rate, error)
	// PutRate guarda uma taxa.
	PutRate(ctx context.Context, r Rate) error
}

// Converter converte montantes entre moedas.
type Converter struct {
	store RateStore
	// Now devolve a hora. Substituível em testes.
	Now func() time.Time
}

// NewConverter cria o conversor.
func NewConverter(store RateStore) *Converter {
	return &Converter{store: store, Now: func() time.Time { return time.Now().UTC() }}
}

// Convert converte um montante para outra moeda.
//
// Devolve [ErrNoRate] quando não há taxa válida, em vez de converter por uma
// taxa expirada. Numa conversão de cobrança, errar por omissão é uma falha
// visível que alguém corrige; converter mal é uma diferença que só se descobre
// na contabilidade.
func (c *Converter) Convert(ctx context.Context, a Amount, to Currency) (Amount, error) {
	from := NormalizeCurrency(string(a.Currency))
	target := NormalizeCurrency(string(to))
	if from == target {
		return a, nil
	}
	rate, err := c.store.Rate(ctx, from, target)
	if err != nil {
		return Amount{}, err
	}
	if !rate.Valid(c.now()) {
		return Amount{}, fmt.Errorf("%w: %s para %s", ErrNoRate, from, target)
	}
	return Apply(a, rate), nil
}

func (c *Converter) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

// Apply converte um montante por uma taxa, ajustando as casas decimais quando
// as duas moedas não as têm iguais.
func Apply(a Amount, r Rate) Amount {
	to := NormalizeCurrency(string(r.To))
	// A conversão passa pela unidade maior porque é aí que a taxa está
	// definida, e volta à menor da moeda de destino, que pode ter outro número
	// de casas (converter kwanzas para ienes não é multiplicar e ficar por
	// aqui).
	major := a.Float() * r.Value
	return FromFloat(major, to)
}

// MemoryRateStore guarda taxas em memória.
//
// Serve para arrancar e para testes. Num serviço com mais do que uma instância,
// use a base de dados: instâncias diferentes com taxas diferentes convertem a
// mesma compra por valores diferentes conforme quem a atender.
type MemoryRateStore struct {
	mu    sync.RWMutex
	rates map[string]Rate
}

// NewMemoryRateStore cria o armazenamento em memória.
func NewMemoryRateStore() *MemoryRateStore {
	return &MemoryRateStore{rates: map[string]Rate{}}
}

// Rate devolve a taxa entre duas moedas.
//
// Sem taxa directa, tenta a inversa: quem guarda o par kwanza para dólar não
// tem de guardar também o dólar para kwanza.
func (s *MemoryRateStore) Rate(_ context.Context, from, to Currency) (Rate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r, ok := s.rates[key(from, to)]; ok {
		return r, nil
	}
	if r, ok := s.rates[key(to, from)]; ok && r.Value > 0 {
		return Rate{
			From: from, To: to, Value: 1 / r.Value,
			ValidUntil: r.ValidUntil, Source: r.Source + " (invertida)", FetchedAt: r.FetchedAt,
		}, nil
	}
	return Rate{}, fmt.Errorf("%w: %s para %s", ErrNoRate, NormalizeCurrency(string(from)), NormalizeCurrency(string(to)))
}

// PutRate guarda uma taxa.
func (s *MemoryRateStore) PutRate(_ context.Context, r Rate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.From = NormalizeCurrency(string(r.From))
	r.To = NormalizeCurrency(string(r.To))
	if r.FetchedAt.IsZero() {
		r.FetchedAt = time.Now().UTC()
	}
	s.rates[key(r.From, r.To)] = r
	return nil
}

func key(from, to Currency) string {
	return NormalizeCurrency(string(from)).String() + ">" + NormalizeCurrency(string(to)).String()
}
