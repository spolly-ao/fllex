package tokens

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type memStore struct {
	byHash map[string]*Token
	seq    int
}

func newMemStore() *memStore { return &memStore{byHash: map[string]*Token{}} }

func (m *memStore) Create(_ context.Context, t *Token) error {
	m.byHash[t.Hash] = t
	return nil
}
func (m *memStore) ByHash(_ context.Context, hash string) (*Token, error) {
	return m.byHash[hash], nil
}
func (m *memStore) MarkUsed(_ context.Context, id string, at time.Time) error {
	for _, t := range m.byHash {
		if t.ID == id {
			t.UsedAt = &at
		}
	}
	return nil
}
func (m *memStore) DeleteExpired(_ context.Context, before time.Time) (int, error) {
	n := 0
	for h, t := range m.byHash {
		if t.ExpiresAt.Before(before) {
			delete(m.byHash, h)
			n++
		}
	}
	return n, nil
}

func newIssuer() (*Issuer, *memStore) {
	store := newMemStore()
	i := NewIssuer(store)
	n := 0
	i.IDs = func() string { n++; return fmt.Sprintf("tok-%d", n) }
	return i, store
}

func TestIssueAndResolve(t *testing.T) {
	i, store := newIssuer()
	ctx := context.Background()

	raw, err := i.Issue(ctx, "sub-1", "renovacao", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" {
		t.Fatal("o token em claro devia ter sido devolvido")
	}
	// O valor em claro não pode estar guardado: uma fuga da base de dados não
	// deve entregar links funcionais.
	for _, tok := range store.byHash {
		if strings.Contains(tok.Hash, raw) || tok.Hash == raw {
			t.Error("o token está guardado em claro")
		}
	}

	got, err := i.Resolve(ctx, raw, "renovacao")
	if err != nil {
		t.Fatalf("resolver falhou: %v", err)
	}
	if got.SubjectID != "sub-1" {
		t.Errorf("assunto = %q", got.SubjectID)
	}
	// Resolver não consome: recarregar a página não pode invalidar o link.
	if got.UsedAt != nil {
		t.Error("resolver não devia consumir o token")
	}
	if _, err := i.Resolve(ctx, raw, "renovacao"); err != nil {
		t.Errorf("segunda resolução falhou: %v", err)
	}
}

func TestConsumeIsSingleUse(t *testing.T) {
	i, _ := newIssuer()
	ctx := context.Background()
	raw, _ := i.Issue(ctx, "sub-1", "renovacao", time.Now().Add(time.Hour))

	if _, err := i.Consume(ctx, raw, "renovacao"); err != nil {
		t.Fatalf("primeira utilização falhou: %v", err)
	}
	if _, err := i.Consume(ctx, raw, "renovacao"); !errors.Is(err, ErrInvalid) {
		t.Errorf("segunda utilização devolveu %v, queria inválido", err)
	}
}

func TestResolveRejectsExpiredAndWrongPurpose(t *testing.T) {
	i, _ := newIssuer()
	ctx := context.Background()

	expired, _ := i.Issue(ctx, "sub-1", "renovacao", time.Now().Add(-time.Minute))
	if _, err := i.Resolve(ctx, expired, "renovacao"); !errors.Is(err, ErrInvalid) {
		t.Errorf("token expirado devolveu %v", err)
	}

	valid, _ := i.Issue(ctx, "sub-1", "renovacao", time.Now().Add(time.Hour))
	// Um link de renovação não pode servir de link de outra coisa.
	if _, err := i.Resolve(ctx, valid, "reembolso"); !errors.Is(err, ErrInvalid) {
		t.Errorf("finalidade errada devolveu %v", err)
	}
	if _, err := i.Resolve(ctx, "token-inventado", "renovacao"); !errors.Is(err, ErrInvalid) {
		t.Errorf("token inexistente devolveu %v", err)
	}
	if _, err := i.Resolve(ctx, "", "renovacao"); !errors.Is(err, ErrInvalid) {
		t.Errorf("token vazio devolveu %v", err)
	}
}

func TestGenerateIsUnpredictable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		v, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if len(v) < 40 {
			t.Fatalf("token demasiado curto: %d caracteres", len(v))
		}
		if seen[v] {
			t.Fatal("token repetido")
		}
		seen[v] = true
	}
}

func TestCleanup(t *testing.T) {
	i, store := newIssuer()
	ctx := context.Background()
	_, _ = i.Issue(ctx, "sub-1", "renovacao", time.Now().Add(-48*time.Hour))
	_, _ = i.Issue(ctx, "sub-2", "renovacao", time.Now().Add(time.Hour))

	n, err := i.Cleanup(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("apagados = %d, queria 1", n)
	}
	if len(store.byHash) != 1 {
		t.Errorf("ficaram %d tokens, queria 1", len(store.byHash))
	}
}

// --- caminhos de erro -----------------------------------------------------------

type failingStore struct {
	*memStore
	onCreate, onByHash, onMarkUsed error
}

func (f *failingStore) Create(ctx context.Context, t *Token) error {
	if f.onCreate != nil {
		return f.onCreate
	}
	return f.memStore.Create(ctx, t)
}

func (f *failingStore) ByHash(ctx context.Context, hash string) (*Token, error) {
	if f.onByHash != nil {
		return nil, f.onByHash
	}
	return f.memStore.ByHash(ctx, hash)
}

func (f *failingStore) MarkUsed(ctx context.Context, id string, at time.Time) error {
	if f.onMarkUsed != nil {
		return f.onMarkUsed
	}
	return f.memStore.MarkUsed(ctx, id, at)
}

func TestIssuePropagatesStoreErrors(t *testing.T) {
	boom := errors.New("base de dados em baixo")
	i := NewIssuer(&failingStore{memStore: newMemStore(), onCreate: boom})
	if _, err := i.Issue(context.Background(), "sub-1", "renovacao", time.Now().Add(time.Hour)); !errors.Is(err, boom) {
		t.Errorf("erro = %v", err)
	}
}

func TestIssueFailsWhenRandomnessFails(t *testing.T) {
	// Um token previsível é um link que se adivinha: mais vale não emitir.
	boom := errors.New("sem entropia")
	original := randRead
	randRead = func([]byte) (int, error) { return 0, boom }
	defer func() { randRead = original }()

	i := NewIssuer(newMemStore())
	if _, err := i.Issue(context.Background(), "sub-1", "renovacao", time.Now().Add(time.Hour)); !errors.Is(err, boom) {
		t.Errorf("erro = %v", err)
	}
	if _, err := Generate(); !errors.Is(err, boom) {
		t.Errorf("Generate = %v", err)
	}
}

func TestResolveAndConsumePropagateStoreErrors(t *testing.T) {
	boom := errors.New("base de dados em baixo")
	ctx := context.Background()

	i := NewIssuer(&failingStore{memStore: newMemStore(), onByHash: boom})
	if _, err := i.Resolve(ctx, "algum-token", ""); !errors.Is(err, boom) {
		t.Errorf("Resolve = %v", err)
	}
	if _, err := i.Consume(ctx, "algum-token", ""); !errors.Is(err, boom) {
		t.Errorf("Consume = %v", err)
	}

	// Falha só ao marcar como usado: o token continua válido, e é preferível a
	// dá-lo por consumido sem o ter sido.
	store := &failingStore{memStore: newMemStore(), onMarkUsed: boom}
	i = NewIssuer(store)
	raw, err := i.Issue(ctx, "sub-1", "renovacao", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Consume(ctx, raw, "renovacao"); !errors.Is(err, boom) {
		t.Errorf("Consume = %v", err)
	}
}

func TestIssuerDefaults(t *testing.T) {
	// Sem relógio nem gerador de identificadores configurados, continua a
	// funcionar: são costuras de teste, não requisitos.
	i := NewIssuer(newMemStore())
	i.Now = nil
	i.IDs = nil
	ctx := context.Background()
	raw, err := i.Issue(ctx, "sub-1", "renovacao", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := i.Resolve(ctx, raw, "renovacao")
	if err != nil {
		t.Fatal(err)
	}
	if tok.ID != "" {
		t.Errorf("sem gerador de identificadores, o campo fica vazio: %q", tok.ID)
	}
	if tok.CreatedAt.IsZero() {
		t.Error("a hora de criação devia vir do relógio do sistema")
	}
}
