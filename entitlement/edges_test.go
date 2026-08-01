package entitlement

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type failingStore struct {
	*memStore
	onList, onSuspend error
	// suspendFailsFor limita a falha a uma direcção, para se poder testar o
	// que acontece quando a reposição passa e a suspensão falha.
	suspendFailsFor *bool
}

func (f *failingStore) List(ctx context.Context, subject, resource string) ([]Resource, error) {
	if f.onList != nil {
		return nil, f.onList
	}
	return f.memStore.List(ctx, subject, resource)
}

func (f *failingStore) SetSuspended(ctx context.Context, ids []string, suspended bool) error {
	if f.onSuspend != nil && (f.suspendFailsFor == nil || *f.suspendFailsFor == suspended) {
		return f.onSuspend
	}
	return f.memStore.SetSuspended(ctx, ids, suspended)
}

func failingResolver(err error) Resolver {
	return ResolverFunc(func(context.Context, string) (Entitlements, error) {
		return Entitlements{}, err
	})
}

func TestLimitErrorInterface(t *testing.T) {
	e := &LimitError{Err: ErrLimitReached, Resource: "members", Message: "O plano permite 5 membros."}
	if e.Error() != "O plano permite 5 membros." {
		t.Errorf("Error() = %q, queria a mensagem do cliente", e.Error())
	}
	if !errors.Is(e, ErrLimitReached) {
		t.Error("o motivo tem de sobreviver ao embrulho")
	}
}

func TestNilMapsAreSafe(t *testing.T) {
	var l Limits
	if got := l.Limit("members"); got != 0 {
		t.Errorf("mapa nulo = %d, queria 0", got)
	}
	var f Features
	if f.Has("api") {
		t.Error("um mapa nulo não tem funcionalidade nenhuma")
	}
}

func TestResolverErrorsPropagate(t *testing.T) {
	boom := errors.New("subscrição ilegível")
	e := NewEnforcer(failingResolver(boom), newStore())
	e.Log = quiet()
	ctx := context.Background()

	if _, err := e.Usage(ctx, "org", "members"); !errors.Is(err, boom) {
		t.Errorf("ocupação = %v", err)
	}
	if _, err := e.UsageAll(ctx, "org"); !errors.Is(err, boom) {
		t.Errorf("ocupação total = %v", err)
	}
	if err := e.Allow(ctx, "org", "members", 1); !errors.Is(err, boom) {
		t.Errorf("permitir = %v", err)
	}
	if err := e.Require(ctx, "org", "api"); !errors.Is(err, boom) {
		t.Errorf("exigir = %v", err)
	}
	if _, err := e.Apply(ctx, "org"); !errors.Is(err, boom) {
		t.Errorf("aplicar = %v", err)
	}
	if _, err := e.ApplyResource(ctx, "org", "members"); !errors.Is(err, boom) {
		t.Errorf("aplicar a um recurso = %v", err)
	}
}

func TestStoreErrorsPropagate(t *testing.T) {
	boom := errors.New("base de dados em baixo")
	store := &failingStore{memStore: newStore(), onList: boom}
	e := NewEnforcer(plan(3), store)
	e.Log = quiet()
	ctx := context.Background()

	if _, err := e.Usage(ctx, "org", "members"); !errors.Is(err, boom) {
		t.Errorf("ocupação = %v", err)
	}
	if _, err := e.UsageAll(ctx, "org"); !errors.Is(err, boom) {
		t.Errorf("ocupação total = %v", err)
	}
	if _, err := e.Apply(ctx, "org"); !errors.Is(err, boom) {
		t.Errorf("aplicar = %v", err)
	}
	if _, err := e.ReleaseAll(ctx, "org", "members"); !errors.Is(err, boom) {
		t.Errorf("libertar = %v", err)
	}
}

func TestSuspendFailureKeepsWhatWasRestored(t *testing.T) {
	// A reposição acontece primeiro. Se a suspensão falhar a seguir, o
	// resultado tem de dizer o que já foi reposto, senão quem chama não sabe
	// que parte do trabalho ficou feita.
	boom := errors.New("base de dados em baixo")
	store := newStore()
	store.seed("org", "members", 6)
	ctx := context.Background()

	// Suspende tudo acima de dois.
	if _, err := NewEnforcer(plan(2), store).Apply(ctx, "org"); err != nil {
		t.Fatal(err)
	}

	suspending := true
	failing := &failingStore{memStore: store, onSuspend: boom, suspendFailsFor: &suspending}
	e := NewEnforcer(plan(4), failing) // sobe para quatro: repõe dois, e nada a suspender
	e.Log = quiet()
	if _, err := e.Apply(ctx, "org"); err != nil {
		t.Fatalf("só a suspensão devia falhar: %v", err)
	}

	// Agora desce, para haver o que suspender e a falha aparecer.
	e = NewEnforcer(plan(1), failing)
	e.Log = quiet()
	res, err := e.Apply(ctx, "org")
	if !errors.Is(err, boom) {
		t.Fatalf("erro = %v", err)
	}
	if !strings.Contains(err.Error(), "suspender") {
		t.Errorf("o erro devia dizer em que passo falhou: %v", err)
	}
	_ = res
}

func TestRestoreFailureIsReported(t *testing.T) {
	boom := errors.New("base de dados em baixo")
	store := newStore()
	store.seed("org", "members", 6)
	ctx := context.Background()
	if _, err := NewEnforcer(plan(2), store).Apply(ctx, "org"); err != nil {
		t.Fatal(err)
	}

	restoring := false
	failing := &failingStore{memStore: store, onSuspend: boom, suspendFailsFor: &restoring}
	e := NewEnforcer(plan(6), failing)
	e.Log = quiet()
	_, err := e.Apply(ctx, "org")
	if !errors.Is(err, boom) {
		t.Fatalf("erro = %v", err)
	}
	if !strings.Contains(err.Error(), "repor") {
		t.Errorf("o erro devia dizer em que passo falhou: %v", err)
	}
}

func TestApplyResource(t *testing.T) {
	store := newStore()
	store.seed("org", "members", 5)
	e := NewEnforcer(plan(2), store)
	e.Log = quiet()

	res, err := e.ApplyResource(context.Background(), "org", "members")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suspended) != 3 {
		t.Errorf("suspensos = %d, queria 3", len(res.Suspended))
	}
	// Um recurso que o plano não conhece tem limite zero: suspende tudo.
	store.seed("org", "projects", 2)
	res, err = e.ApplyResource(context.Background(), "org", "projects")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suspended) != 2 {
		t.Errorf("recurso desconhecido: suspensos = %d, queria 2", len(res.Suspended))
	}
}

func TestNegativeLimitOtherThanUnlimitedSuspendsEverything(t *testing.T) {
	// Um limite negativo que não seja o de "sem limite" é configuração errada.
	// Tratá-lo como zero é o comportamento seguro.
	store := newStore()
	store.seed("org", "members", 3)
	e := NewEnforcer(ResolverFunc(func(context.Context, string) (Entitlements, error) {
		return Entitlements{Limits: Limits{"members": -7}}, nil
	}), store)
	e.Log = quiet()

	res, err := e.Apply(context.Background(), "org")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suspended) != 3 {
		t.Errorf("suspensos = %d, queria 3", len(res.Suspended))
	}
}

func TestApplyOrdersByIDWhenCreatedAtTies(t *testing.T) {
	// Duas passagens seguidas têm de suspender as mesmas pessoas. Com datas
	// iguais, o desempate é o identificador; sem ele, a ordem do mapa mandava e
	// o resultado mudava a cada execução.
	store := newStore()
	store.items["org|members"] = []Resource{
		{ID: "c", CreatedAt: 1},
		{ID: "a", CreatedAt: 1},
		{ID: "b", CreatedAt: 1},
	}
	e := NewEnforcer(plan(1), store)
	e.Log = quiet()

	res, err := e.Apply(context.Background(), "org")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suspended) != 2 {
		t.Fatalf("suspensos = %v", res.Suspended)
	}
	active, _ := store.states("org", "members")
	if len(active) != 1 || active[0] != "a" {
		t.Errorf("activo = %v, queria o primeiro por identificador", active)
	}
}

func TestReleaseAllWithNothingSuspended(t *testing.T) {
	store := newStore()
	store.seed("org", "members", 3)
	e := NewEnforcer(plan(10), store)
	e.Log = quiet()

	res, err := e.ReleaseAll(context.Background(), "org", "members")
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed() {
		t.Errorf("não havia nada suspenso: %+v", res)
	}
}

func TestReleaseAllPropagatesSuspendError(t *testing.T) {
	boom := errors.New("base de dados em baixo")
	store := newStore()
	store.seed("org", "members", 4)
	ctx := context.Background()
	if _, err := NewEnforcer(plan(1), store).Apply(ctx, "org"); err != nil {
		t.Fatal(err)
	}

	failing := &failingStore{memStore: store, onSuspend: boom}
	e := NewEnforcer(plan(1), failing)
	e.Log = quiet()
	if _, err := e.ReleaseAll(ctx, "org", "members"); !errors.Is(err, boom) {
		t.Errorf("erro = %v", err)
	}
}

func TestAllowWithNonPositiveAmount(t *testing.T) {
	e := NewEnforcer(plan(1), newStore())
	if err := e.Allow(context.Background(), "org", "members", 0); err != nil {
		t.Errorf("pedir zero = %v", err)
	}
	if err := e.Allow(context.Background(), "org", "members", -3); err != nil {
		t.Errorf("pedir negativo = %v", err)
	}
}

func TestAllowOnUnlimitedResource(t *testing.T) {
	store := newStore()
	store.seed("org", "members", 100)
	e := NewEnforcer(plan(Unlimited), store)
	if err := e.Allow(context.Background(), "org", "members", 50); err != nil {
		t.Errorf("sem limite = %v", err)
	}
}

func TestUsageCountsSuspended(t *testing.T) {
	store := newStore()
	store.seed("org", "members", 5)
	ctx := context.Background()
	e := NewEnforcer(plan(2), store)
	e.Log = quiet()
	if _, err := e.Apply(ctx, "org"); err != nil {
		t.Fatal(err)
	}

	u, err := e.Usage(ctx, "org", "members")
	if err != nil {
		t.Fatal(err)
	}
	if u.Suspended != 3 {
		t.Errorf("suspensos = %d, queria 3", u.Suspended)
	}
	if u.Used != 5 {
		t.Errorf("ocupação = %d: um suspenso continua a ocupar lugar até ser apagado", u.Used)
	}
}

func TestDefaultLoggerIsNotNil(t *testing.T) {
	e := NewEnforcer(plan(1), newStore())
	if e.log() == nil {
		t.Error("sem registador configurado devia usar o por omissão")
	}
}

func TestResourceNameFallback(t *testing.T) {
	// Sem tradução configurada, usa-se o nome técnico em minúsculas.
	e := NewEnforcer(plan(0), newStore())
	err := e.Allow(context.Background(), "org", "  Members  ", 1)
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatal(err)
	}
	if !strings.Contains(le.Message, "members") {
		t.Errorf("mensagem = %q", le.Message)
	}
	// Uma tradução vazia também cai no nome técnico.
	e.Names = map[string]string{"members": ""}
	err = e.Allow(context.Background(), "org", "members", 1)
	if !errors.As(err, &le) || !strings.Contains(le.Message, "members") {
		t.Errorf("mensagem = %v", err)
	}
}

func TestItoaNegative(t *testing.T) {
	if got := itoa(-5); got != "-5" {
		t.Errorf("= %q", got)
	}
	if got := itoa(0); got != "0" {
		t.Errorf("= %q", got)
	}
}
