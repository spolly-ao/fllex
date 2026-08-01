package entitlement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
)

var (
	// ErrLimitReached indica que o plano não dá para mais.
	ErrLimitReached = errors.New("entitlement: limite do plano atingido")
	// ErrFeatureLocked indica uma funcionalidade que o plano não inclui.
	ErrFeatureLocked = errors.New("entitlement: funcionalidade não incluída no plano")
)

// LimitError é uma recusa com mensagem para o cliente.
type LimitError struct {
	// Err é o motivo, para o código decidir.
	Err error
	// Resource é o recurso ou a funcionalidade em causa.
	Resource string
	// Usage é a ocupação no momento da recusa.
	Usage Usage
	// Message é o motivo, para a pessoa ler.
	Message string
}

func (e *LimitError) Error() string { return e.Message }
func (e *LimitError) Unwrap() error { return e.Err }

// Enforcer aplica os limites do plano aos recursos que já existem.
type Enforcer struct {
	resolver Resolver
	store    Store
	// Names traduz o nome técnico de um recurso para o que se mostra ao
	// cliente ("members" para "membros"). Sem entrada, usa-se o nome técnico.
	Names map[string]string
	// Log recebe o que corre mal.
	Log *slog.Logger
}

// NewEnforcer cria o aplicador.
func NewEnforcer(resolver Resolver, store Store) *Enforcer {
	return &Enforcer{resolver: resolver, store: store}
}

// Usage devolve a ocupação de um recurso.
func (e *Enforcer) Usage(ctx context.Context, subjectID, resource string) (Usage, error) {
	ent, err := e.resolver.For(ctx, subjectID)
	if err != nil {
		return Usage{}, err
	}
	items, err := e.store.List(ctx, subjectID, resource)
	if err != nil {
		return Usage{}, err
	}
	return usageOf(resource, ent.Limits.Limit(resource), items), nil
}

// UsageAll devolve a ocupação de todos os recursos do plano.
func (e *Enforcer) UsageAll(ctx context.Context, subjectID string) (map[string]Usage, error) {
	ent, err := e.resolver.For(ctx, subjectID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Usage, len(ent.Limits))
	for resource, limit := range ent.Limits {
		items, err := e.store.List(ctx, subjectID, resource)
		if err != nil {
			return nil, err
		}
		out[resource] = usageOf(resource, limit, items)
	}
	return out, nil
}

// Allow verifica se ainda cabem mais n recursos deste tipo.
//
// Chame-o antes de criar, e não depois: o [Enforcer.Apply] arruma o que já
// existe, mas deixar criar para suspender a seguir dá ao cliente um recurso que
// nasce morto e uma explicação difícil.
func (e *Enforcer) Allow(ctx context.Context, subjectID, resource string, n int) error {
	if n <= 0 {
		return nil
	}
	u, err := e.Usage(ctx, subjectID, resource)
	if err != nil {
		return err
	}
	if u.Limit == Unlimited || u.Used+n <= u.Limit {
		return nil
	}
	return &LimitError{
		Err: ErrLimitReached, Resource: resource, Usage: u,
		Message: e.limitMessage(resource, u),
	}
}

// Require verifica se o plano inclui uma funcionalidade.
func (e *Enforcer) Require(ctx context.Context, subjectID, feature string) error {
	ent, err := e.resolver.For(ctx, subjectID)
	if err != nil {
		return err
	}
	if ent.Features.Has(feature) {
		return nil
	}
	return &LimitError{
		Err: ErrFeatureLocked, Resource: feature,
		Message: "O plano actual não inclui " + e.name(feature) + ".",
	}
}

// Result descreve o que uma passagem do [Enforcer.Apply] fez.
type Result struct {
	// Suspended são os recursos que passaram a suspensos.
	Suspended []string
	// Restored são os que voltaram a ficar activos.
	Restored []string
}

// Changed indica se houve alguma alteração.
func (r Result) Changed() bool { return len(r.Suspended) > 0 || len(r.Restored) > 0 }

// Apply põe os recursos de um cliente de acordo com o plano em vigor.
//
// Suspende os que passam do tecto e repõe os que voltaram a caber, e é a mesma
// função que trata dos dois sentidos de propósito: quem desce de plano e volta a
// subir tem de encontrar tudo como estava. Correr isto só na descida deixa os
// recursos suspensos para sempre.
//
// A ordem é sempre a de criação: ficam os mais antigos. É a única regra que dá
// o mesmo resultado em duas execuções seguidas e que se explica ao cliente numa
// frase.
//
// Chame-o sempre que a subscrição mudar de estado ou de plano, e não a cada
// pedido: é uma passagem sobre todos os recursos do cliente.
func (e *Enforcer) Apply(ctx context.Context, subjectID string) (Result, error) {
	ent, err := e.resolver.For(ctx, subjectID)
	if err != nil {
		return Result{}, err
	}
	var out Result
	for resource, limit := range ent.Limits {
		r, err := e.applyOne(ctx, subjectID, resource, limit)
		if err != nil {
			return out, err
		}
		out.Suspended = append(out.Suspended, r.Suspended...)
		out.Restored = append(out.Restored, r.Restored...)
	}
	return out, nil
}

// ApplyResource faz o mesmo para um só tipo de recurso.
func (e *Enforcer) ApplyResource(ctx context.Context, subjectID, resource string) (Result, error) {
	ent, err := e.resolver.For(ctx, subjectID)
	if err != nil {
		return Result{}, err
	}
	return e.applyOne(ctx, subjectID, resource, ent.Limits.Limit(resource))
}

func (e *Enforcer) applyOne(ctx context.Context, subjectID, resource string, limit int) (Result, error) {
	items, err := e.store.List(ctx, subjectID, resource)
	if err != nil {
		return Result{}, err
	}

	// A ordem de criação decide, e não a ordem em que a base de dados
	// devolveu: sem isto, duas passagens seguidas podem suspender pessoas
	// diferentes.
	countable := make([]Resource, 0, len(items))
	for _, it := range items {
		if it.Countable() {
			countable = append(countable, it)
		}
	}
	sort.SliceStable(countable, func(i, j int) bool {
		if countable[i].CreatedAt != countable[j].CreatedAt {
			return countable[i].CreatedAt < countable[j].CreatedAt
		}
		return countable[i].ID < countable[j].ID
	})

	keep := len(countable)
	if limit != Unlimited {
		keep = limit
		if keep < 0 {
			keep = 0
		}
		if keep > len(countable) {
			keep = len(countable)
		}
	}

	var toRestore, toSuspend []string
	for i, it := range countable {
		if i < keep {
			if it.Suspended {
				toRestore = append(toRestore, it.ID)
			}
			continue
		}
		if !it.Suspended {
			toSuspend = append(toSuspend, it.ID)
		}
	}

	// Repor primeiro e suspender depois. Pela ordem contrária, uma falha a
	// meio deixa o cliente com menos recursos activos do que o plano lhe dá,
	// que é o pior dos dois enganos.
	if len(toRestore) > 0 {
		if err := e.store.SetSuspended(ctx, toRestore, false); err != nil {
			return Result{}, fmt.Errorf("entitlement: repor %s: %w", resource, err)
		}
	}
	if len(toSuspend) > 0 {
		if err := e.store.SetSuspended(ctx, toSuspend, true); err != nil {
			return Result{Restored: toRestore}, fmt.Errorf("entitlement: suspender %s: %w", resource, err)
		}
	}

	if len(toSuspend) > 0 || len(toRestore) > 0 {
		e.log().Info("entitlement: limites aplicados",
			"subject", subjectID, "resource", resource, "limit", limit,
			"suspended", len(toSuspend), "restored", len(toRestore))
	}
	return Result{Suspended: toSuspend, Restored: toRestore}, nil
}

// ReleaseAll repõe todos os recursos suspensos de um cliente, sem olhar a
// limites.
//
// Serve o encerramento do serviço e as migrações: quando se deixa de cobrar por
// um recurso, ninguém deve ficar com ele suspenso à espera de um plano que já
// não existe.
func (e *Enforcer) ReleaseAll(ctx context.Context, subjectID, resource string) (Result, error) {
	items, err := e.store.List(ctx, subjectID, resource)
	if err != nil {
		return Result{}, err
	}
	var ids []string
	for _, it := range items {
		if it.Suspended {
			ids = append(ids, it.ID)
		}
	}
	if len(ids) == 0 {
		return Result{}, nil
	}
	if err := e.store.SetSuspended(ctx, ids, false); err != nil {
		return Result{}, err
	}
	return Result{Restored: ids}, nil
}

func (e *Enforcer) limitMessage(resource string, u Usage) string {
	name := e.name(resource)
	if u.Limit <= 0 {
		return "O plano actual não inclui " + name + "."
	}
	return "O plano actual permite " + itoa(u.Limit) + " " + name +
		" e já tem " + itoa(u.Used) + "."
}

func (e *Enforcer) name(resource string) string {
	if e.Names != nil {
		if v, ok := e.Names[resource]; ok && v != "" {
			return v
		}
	}
	return normalize(resource)
}

func (e *Enforcer) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func usageOf(resource string, limit int, items []Resource) Usage {
	u := Usage{Resource: resource, Limit: limit}
	for _, it := range items {
		if !it.Countable() {
			continue
		}
		u.Used++
		if it.Suspended {
			u.Suspended++
		}
	}
	return u
}
