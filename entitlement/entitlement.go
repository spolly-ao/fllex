// Package entitlement trata do que cada plano dá direito e do que acontece
// quando o cliente passa desse limite.
//
// A parte fácil é dizer não a quem tenta criar mais um recurso do que o plano
// permite. A difícil, e a que os sistemas costumam deixar por fazer, é o que
// acontece a quem já tinha vinte utilizadores e desce para um plano de cinco.
// Apagar quinze pessoas é inaceitável, e deixá-las a funcionar torna o limite
// decorativo.
//
// A resposta deste pacote é a suspensão reversível: os recursos acima do limite
// ficam suspensos, mantêm-se guardados, e voltam sozinhos se o cliente subir de
// plano outra vez. Ver [Enforcer].
package entitlement

import (
	"context"
	"strings"
)

// Unlimited é o valor de um limite sem tecto.
const Unlimited = -1

// Limits são os tectos de um plano, por tipo de recurso.
//
// A chave é o nome do recurso ("members", "projects", "brands"), escolhido por
// quem usa a biblioteca. O valor é o tecto, e [Unlimited] é sem tecto.
//
// Um recurso que não esteja no mapa conta como zero, não como ilimitado. É a
// escolha segura: um limite esquecido na configuração de um plano novo bloqueia
// a criação, que se nota logo, em vez de abrir a porta de par em par, que só se
// nota na factura da infra-estrutura.
type Limits map[string]int

// Limit devolve o tecto de um recurso.
func (l Limits) Limit(resource string) int {
	if l == nil {
		return 0
	}
	v, ok := l[resource]
	if !ok {
		return 0
	}
	return v
}

// IsUnlimited indica se o recurso não tem tecto.
func (l Limits) IsUnlimited(resource string) bool { return l.Limit(resource) == Unlimited }

// Features são as funcionalidades ligadas pelo plano.
type Features map[string]bool

// Has indica se a funcionalidade está ligada.
func (f Features) Has(name string) bool {
	if f == nil {
		return false
	}
	return f[name]
}

// Entitlements é o que um plano dá.
type Entitlements struct {
	// PlanID identifica o plano de onde isto veio.
	PlanID string
	// Limits são os tectos por recurso.
	Limits Limits
	// Features são as funcionalidades ligadas.
	Features Features
}

// Resolver devolve os direitos em vigor de um cliente.
//
// É a porta por onde o plano entra. Quem usa a biblioteca implementa-a lendo a
// subscrição activa e traduzindo o plano para limites, o que normalmente é uma
// consulta a uma tabela de planos.
type Resolver interface {
	For(ctx context.Context, subjectID string) (Entitlements, error)
}

// ResolverFunc adapta uma função a [Resolver].
type ResolverFunc func(ctx context.Context, subjectID string) (Entitlements, error)

// For chama a função.
func (f ResolverFunc) For(ctx context.Context, subjectID string) (Entitlements, error) {
	return f(ctx, subjectID)
}

// Resource é um recurso contável de um cliente.
type Resource struct {
	// ID é o identificador do recurso.
	ID string
	// CreatedAt decide quem fica e quem é suspenso: os mais antigos ficam.
	//
	// É a única ordem defensável perante o cliente. Suspender por ordem
	// alfabética ou pela ordem que a base de dados devolver significa que dois
	// clientes iguais têm resultados diferentes, e que a mesma conta muda de
	// resultado entre execuções.
	CreatedAt int64
	// Suspended diz se está suspenso por limite de plano.
	Suspended bool
	// Disabled diz se foi desligado por decisão de alguém.
	//
	// É separado da suspensão de propósito: quem sobe de plano quer os
	// suspensos de volta, mas não quer ressuscitar o que um administrador
	// desligou de propósito. Confundir as duas coisas num só campo é o erro
	// clássico, e só se descobre quando um utilizador despedido volta a ter
	// acesso.
	Disabled bool
}

// Countable indica se o recurso conta para o limite. Um recurso desligado por
// decisão de alguém não ocupa lugar.
func (r Resource) Countable() bool { return !r.Disabled }

// Store é o acesso aos recursos de um cliente.
type Store interface {
	// List devolve os recursos de um tipo, por ordem de criação crescente.
	List(ctx context.Context, subjectID, resource string) ([]Resource, error)
	// SetSuspended muda o estado de suspensão de vários recursos de uma vez.
	SetSuspended(ctx context.Context, ids []string, suspended bool) error
}

// Usage é a ocupação de um recurso.
type Usage struct {
	// Resource é o tipo.
	Resource string
	// Used é quantos contam para o limite.
	Used int
	// Limit é o tecto ([Unlimited] quando não há).
	Limit int
	// Suspended é quantos estão suspensos por passarem o limite.
	Suspended int
}

// Remaining é quanto ainda cabe. Devolve -1 quando não há tecto.
func (u Usage) Remaining() int {
	if u.Limit == Unlimited {
		return -1
	}
	r := u.Limit - u.Used
	if r < 0 {
		return 0
	}
	return r
}

// Over indica se a ocupação passou o tecto.
func (u Usage) Over() bool { return u.Limit != Unlimited && u.Used > u.Limit }

// Full indica se já não cabe mais nenhum.
func (u Usage) Full() bool { return u.Limit != Unlimited && u.Used >= u.Limit }

// Describe devolve a ocupação em texto ("7 de 10", "7, sem limite").
func (u Usage) Describe() string {
	if u.Limit == Unlimited {
		return itoa(u.Used) + ", sem limite"
	}
	return itoa(u.Used) + " de " + itoa(u.Limit)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
