package entitlement

import (
	"context"
	"errors"
	"testing"
)

type memStore struct {
	items map[string][]Resource // subject|resource
}

func newStore() *memStore { return &memStore{items: map[string][]Resource{}} }

func (m *memStore) key(subject, resource string) string { return subject + "|" + resource }

func (m *memStore) seed(subject, resource string, n int) {
	list := make([]Resource, 0, n)
	for i := 0; i < n; i++ {
		list = append(list, Resource{ID: resource + itoa(i), CreatedAt: int64(i)})
	}
	m.items[m.key(subject, resource)] = list
}

func (m *memStore) List(_ context.Context, subject, resource string) ([]Resource, error) {
	src := m.items[m.key(subject, resource)]
	out := make([]Resource, len(src))
	copy(out, src)
	return out, nil
}

func (m *memStore) SetSuspended(_ context.Context, ids []string, suspended bool) error {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	for key, list := range m.items {
		for i := range list {
			if want[list[i].ID] {
				list[i].Suspended = suspended
			}
		}
		m.items[key] = list
	}
	return nil
}

func (m *memStore) states(subject, resource string) (active, suspended []string) {
	for _, it := range m.items[m.key(subject, resource)] {
		if it.Suspended {
			suspended = append(suspended, it.ID)
		} else {
			active = append(active, it.ID)
		}
	}
	return
}

func plan(limit int, features ...string) Resolver {
	f := Features{}
	for _, name := range features {
		f[name] = true
	}
	return ResolverFunc(func(context.Context, string) (Entitlements, error) {
		return Entitlements{PlanID: "p", Limits: Limits{"members": limit}, Features: f}, nil
	})
}

func TestLimitsUnknownResourceIsZeroNotUnlimited(t *testing.T) {
	// Um limite esquecido na configuração de um plano novo tem de bloquear, o
	// que se nota logo, e não abrir a porta, que só se nota na factura.
	l := Limits{"members": 5}
	if got := l.Limit("projects"); got != 0 {
		t.Errorf("recurso desconhecido = %d, queria 0", got)
	}
	if l.IsUnlimited("projects") {
		t.Error("um recurso desconhecido não pode ser ilimitado")
	}
	if !(Limits{"members": Unlimited}).IsUnlimited("members") {
		t.Error("-1 é sem limite")
	}
}

func TestApplySuspendsNewestKeepsOldest(t *testing.T) {
	store := newStore()
	store.seed("org", "members", 8)
	e := NewEnforcer(plan(3), store)

	res, err := e.Apply(context.Background(), "org")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suspended) != 5 {
		t.Errorf("suspensos = %d, queria 5", len(res.Suspended))
	}
	active, suspended := store.states("org", "members")
	// Ficam os três mais antigos, e é a única ordem que se explica ao cliente.
	if len(active) != 3 || active[0] != "members0" || active[2] != "members2" {
		t.Errorf("activos = %v, queria os três mais antigos", active)
	}
	if len(suspended) != 5 || suspended[0] != "members3" {
		t.Errorf("suspensos = %v", suspended)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	store := newStore()
	store.seed("org", "members", 8)
	e := NewEnforcer(plan(3), store)
	ctx := context.Background()

	if _, err := e.Apply(ctx, "org"); err != nil {
		t.Fatal(err)
	}
	res, err := e.Apply(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	// A segunda passagem não pode mexer em nada, senão o registo de auditoria
	// enche-se de alterações que nunca aconteceram.
	if res.Changed() {
		t.Errorf("segunda passagem alterou %d suspensos e %d repostos", len(res.Suspended), len(res.Restored))
	}
}

func TestApplyRestoresOnUpgrade(t *testing.T) {
	store := newStore()
	store.seed("org", "members", 8)
	ctx := context.Background()

	// Desce para três.
	if _, err := NewEnforcer(plan(3), store).Apply(ctx, "org"); err != nil {
		t.Fatal(err)
	}
	if _, susp := store.states("org", "members"); len(susp) != 5 {
		t.Fatalf("suspensos = %d", len(susp))
	}

	// Sobe para dez: quem desce e volta a subir tem de encontrar tudo como
	// estava.
	res, err := NewEnforcer(plan(10), store).Apply(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Restored) != 5 {
		t.Errorf("repostos = %d, queria 5", len(res.Restored))
	}
	if _, susp := store.states("org", "members"); len(susp) != 0 {
		t.Errorf("ficaram %d suspensos depois de subir de plano", len(susp))
	}
}

func TestApplyUnlimitedRestoresEverything(t *testing.T) {
	store := newStore()
	store.seed("org", "members", 6)
	ctx := context.Background()
	_, _ = NewEnforcer(plan(2), store).Apply(ctx, "org")

	res, err := NewEnforcer(plan(Unlimited), store).Apply(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Restored) != 4 {
		t.Errorf("repostos = %d, queria 4", len(res.Restored))
	}
}

func TestDisabledResourcesDoNotTakeUpSpace(t *testing.T) {
	// Um recurso desligado por decisão de alguém não ocupa lugar, e não pode
	// voltar sozinho quando o plano sobe: confundir desligado com suspenso é
	// como um utilizador despedido volta a ter acesso.
	store := newStore()
	store.items["org|members"] = []Resource{
		{ID: "a", CreatedAt: 1},
		{ID: "b", CreatedAt: 2, Disabled: true},
		{ID: "c", CreatedAt: 3},
		{ID: "d", CreatedAt: 4},
	}
	e := NewEnforcer(plan(3), store)
	ctx := context.Background()

	u, err := e.Usage(ctx, "org", "members")
	if err != nil {
		t.Fatal(err)
	}
	if u.Used != 3 {
		t.Errorf("ocupação = %d, queria 3 (o desligado não conta)", u.Used)
	}

	res, err := e.Apply(ctx, "org")
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed() {
		t.Errorf("três contáveis num limite de três não devia mexer em nada: %+v", res)
	}
	for _, it := range store.items["org|members"] {
		if it.ID == "b" && it.Suspended {
			t.Error("um recurso desligado não deve ser marcado como suspenso")
		}
	}
}

func TestAllow(t *testing.T) {
	store := newStore()
	store.seed("org", "members", 4)
	e := NewEnforcer(plan(5), store)
	e.Names = map[string]string{"members": "membros"}
	ctx := context.Background()

	if err := e.Allow(ctx, "org", "members", 1); err != nil {
		t.Errorf("ainda cabia um: %v", err)
	}
	err := e.Allow(ctx, "org", "members", 2)
	if err == nil {
		t.Fatal("dois não cabiam")
	}
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("erro sem contexto: %v", err)
	}
	if !errors.Is(err, ErrLimitReached) {
		t.Errorf("motivo = %v", le.Err)
	}
	// A mensagem tem de dizer o limite e o uso, senão o cliente não sabe o que
	// fazer a seguir.
	if le.Message != "O plano actual permite 5 membros e já tem 4." {
		t.Errorf("mensagem = %q", le.Message)
	}
}

func TestAllowOnZeroLimit(t *testing.T) {
	store := newStore()
	e := NewEnforcer(plan(0), store)
	e.Names = map[string]string{"members": "membros"}
	err := e.Allow(context.Background(), "org", "members", 1)
	var le *LimitError
	if !errors.As(err, &le) || le.Message != "O plano actual não inclui membros." {
		t.Errorf("mensagem = %v", err)
	}
}

func TestRequireFeature(t *testing.T) {
	e := NewEnforcer(plan(5, "api"), newStore())
	e.Names = map[string]string{"api": "acesso à API"}
	ctx := context.Background()

	if err := e.Require(ctx, "org", "api"); err != nil {
		t.Errorf("a funcionalidade estava ligada: %v", err)
	}
	err := e.Require(ctx, "org", "sso")
	if !errors.Is(err, ErrFeatureLocked) {
		t.Errorf("motivo = %v", err)
	}
}

func TestUsageHelpers(t *testing.T) {
	u := Usage{Resource: "members", Used: 7, Limit: 10}
	if u.Remaining() != 3 || u.Over() || u.Full() {
		t.Errorf("ocupação = %+v", u)
	}
	if u.Describe() != "7 de 10" {
		t.Errorf("descrição = %q", u.Describe())
	}

	u = Usage{Used: 12, Limit: 10}
	if !u.Over() || !u.Full() || u.Remaining() != 0 {
		t.Errorf("acima do limite = %+v", u)
	}

	u = Usage{Used: 7, Limit: Unlimited}
	if u.Remaining() != -1 || u.Over() || u.Full() {
		t.Errorf("sem limite = %+v", u)
	}
	if u.Describe() != "7, sem limite" {
		t.Errorf("descrição = %q", u.Describe())
	}
}

func TestReleaseAll(t *testing.T) {
	store := newStore()
	store.seed("org", "members", 5)
	ctx := context.Background()
	_, _ = NewEnforcer(plan(2), store).Apply(ctx, "org")

	res, err := NewEnforcer(plan(2), store).ReleaseAll(ctx, "org", "members")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Restored) != 3 {
		t.Errorf("repostos = %d, queria 3", len(res.Restored))
	}
	if _, susp := store.states("org", "members"); len(susp) != 0 {
		t.Errorf("ficaram %d suspensos", len(susp))
	}
}

func TestUsageAll(t *testing.T) {
	store := newStore()
	store.seed("org", "members", 4)
	store.seed("org", "projects", 2)
	e := NewEnforcer(ResolverFunc(func(context.Context, string) (Entitlements, error) {
		return Entitlements{Limits: Limits{"members": 3, "projects": Unlimited}}, nil
	}), store)

	all, err := e.UsageAll(context.Background(), "org")
	if err != nil {
		t.Fatal(err)
	}
	if all["members"].Used != 4 || !all["members"].Over() {
		t.Errorf("membros = %+v", all["members"])
	}
	if all["projects"].Over() {
		t.Errorf("projectos sem limite não pode estar acima: %+v", all["projects"])
	}
}
