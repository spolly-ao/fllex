package payment

import (
	"context"
	"strings"
	"sync"

	"github.com/spolly-ao/fllex/money"
)

// Registry guarda os providers disponíveis e resolve qual usar para cada
// cobrança.
//
// A ordem de registo é a ordem de preferência: perante um pedido que mais do
// que um provider consegue satisfazer, ganha o primeiro registado. É assim que
// se diz "o kwanza vai pelo MoMenu, tudo o resto pelo Stripe" sem escrever um
// único if no código de negócio.
//
// Todos os métodos são seguros para uso concorrente.
type Registry struct {
	mu        sync.RWMutex
	order     []string
	providers map[string]Provider
}

// NewRegistry cria um registo vazio.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register adiciona providers, pela ordem de preferência. Registar de novo o
// mesmo nome substitui-o sem alterar a sua posição na ordem.
func (r *Registry) Register(ps ...Provider) *Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range ps {
		if p == nil {
			continue
		}
		name := normalizeName(p.Name())
		if _, exists := r.providers[name]; !exists {
			r.order = append(r.order, name)
		}
		r.providers[name] = p
	}
	return r
}

// Get devolve um provider pelo nome.
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[normalizeName(name)]
	return p, ok
}

// MustGet devolve um provider pelo nome, ou nil.
func (r *Registry) MustGet(name string) Provider {
	p, _ := r.Get(name)
	return p
}

// Names devolve os nomes registados, por ordem de preferência.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// All devolve os providers por ordem de preferência.
func (r *Registry) All() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.providers[n])
	}
	return out
}

// For resolve o provider que cobra este método nesta moeda. É a forma normal de
// escolher: o método e a moeda são o que o cliente e o preço determinam, e o
// provider é uma consequência.
//
// Só considera providers configurados: um Stripe sem chave está registado mas
// não pode ser escolhido, e nesse caso um provider mais abaixo na ordem apanha
// o pedido em vez de a compra falhar.
func (r *Registry) For(method Method, currency money.Currency) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, name := range r.order {
		p := r.providers[name]
		if !p.Configured() || !p.SupportsCurrency(currency) {
			continue
		}
		if supportsMethod(p, method) {
			return p, true
		}
	}
	return nil, false
}

// ForCurrency devolve o primeiro provider configurado que processa esta moeda,
// sem exigir um método concreto.
func (r *Registry) ForCurrency(currency money.Currency) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, name := range r.order {
		if p := r.providers[name]; p.Configured() && p.SupportsCurrency(currency) {
			return p, true
		}
	}
	return nil, false
}

// MethodsFor devolve os métodos que se podem oferecer a um cliente nesta moeda,
// sem repetições e por ordem de preferência dos providers.
//
// É o que a página de pagamento deve mostrar: só os métodos que alguém
// configurado consegue mesmo cobrar. Os métodos que não são de auto-serviço
// (transferência, atribuição manual) ficam de fora, porque não há nada que o
// cliente possa fazer com eles sozinho; use [Registry.AdminMethodsFor] no
// backoffice.
func (r *Registry) MethodsFor(currency money.Currency) []Method {
	return r.methodsFor(currency, false)
}

// AdminMethodsFor é como [Registry.MethodsFor] mas inclui os métodos que só um
// operador pode usar.
func (r *Registry) AdminMethodsFor(currency money.Currency) []Method {
	return r.methodsFor(currency, true)
}

func (r *Registry) methodsFor(currency money.Currency, includeAdmin bool) []Method {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[Method]bool{}
	var out []Method
	for _, name := range r.order {
		p := r.providers[name]
		if !p.Configured() || !p.SupportsCurrency(currency) {
			continue
		}
		for _, m := range p.Methods() {
			if seen[m] || (!includeAdmin && !m.SelfService()) {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// Charge resolve o provider e cobra numa só chamada. Devolve [ErrNoProvider]
// quando nenhum provider registado cobre o pedido, e o nome de quem cobrou.
func (r *Registry) Charge(ctx context.Context, req ChargeRequest) (ChargeResult, string, error) {
	p, ok := r.For(req.Method, req.Amount.Currency)
	if !ok {
		return ChargeResult{}, "", ErrNoProvider
	}
	res, err := p.Charge(ctx, req)
	return res, p.Name(), err
}

// Verifier devolve a capacidade de consulta de estado de um provider, se a
// tiver.
func (r *Registry) Verifier(name string) (Verifier, bool) {
	p, ok := r.Get(name)
	if !ok {
		return nil, false
	}
	v, ok := p.(Verifier)
	return v, ok
}

// WebhookParser devolve a capacidade de leitura de webhooks de um provider.
func (r *Registry) WebhookParser(name string) (WebhookParser, bool) {
	p, ok := r.Get(name)
	if !ok {
		return nil, false
	}
	w, ok := p.(WebhookParser)
	return w, ok
}

// Subscriber devolve a capacidade de gestão de subscrições de um provider.
func (r *Registry) Subscriber(name string) (Subscriber, bool) {
	p, ok := r.Get(name)
	if !ok {
		return nil, false
	}
	s, ok := p.(Subscriber)
	return s, ok
}

// Refunder devolve a capacidade de estorno de um provider.
func (r *Registry) Refunder(name string) (Refunder, bool) {
	p, ok := r.Get(name)
	if !ok {
		return nil, false
	}
	f, ok := p.(Refunder)
	return f, ok
}

func supportsMethod(p Provider, m Method) bool {
	if m == "" {
		return true
	}
	for _, v := range p.Methods() {
		if v == m {
			return true
		}
	}
	return false
}

func normalizeName(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
