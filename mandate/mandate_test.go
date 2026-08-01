package mandate

import (
	"context"
	"errors"
	"testing"
	"time"
)

var now = time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC)

type memStore struct {
	items map[string]*Mandate
	fail  error
}

func newStore(ms ...*Mandate) *memStore {
	s := &memStore{items: map[string]*Mandate{}}
	for _, m := range ms {
		s.items[m.ID] = m
	}
	return s
}

func (s *memStore) Create(_ context.Context, m *Mandate) error {
	if s.fail != nil {
		return s.fail
	}
	s.items[m.ID] = m
	return nil
}

func (s *memStore) Update(_ context.Context, m *Mandate) error {
	if s.fail != nil {
		return s.fail
	}
	s.items[m.ID] = m
	return nil
}

func (s *memStore) ByID(_ context.Context, id string) (*Mandate, error) {
	if s.fail != nil {
		return nil, s.fail
	}
	return s.items[id], nil
}

func (s *memStore) ByExternalID(_ context.Context, provider string, externalID int) (*Mandate, error) {
	for _, m := range s.items {
		if m.Provider == provider && m.ExternalID == externalID {
			return m, nil
		}
	}
	return nil, nil
}

func (s *memStore) ActiveForSubject(_ context.Context, subjectID string) (*Mandate, error) {
	for _, m := range s.items {
		if m.SubjectID == subjectID && m.Active() {
			return m, nil
		}
	}
	return nil, nil
}

func (s *memStore) PendingActivation(_ context.Context, limit int) ([]*Mandate, error) {
	var out []*Mandate
	for _, m := range s.items {
		if m.Status == StatusSubmitted {
			out = append(out, m)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func newMandate(id string) *Mandate {
	return &Mandate{
		ID: id, SubjectID: "sub-1", CustomerID: "cli-1",
		Provider: "proxypay-dds", Type: TypeSelfActivated,
		Status: StatusPending, CreatedAt: now,
	}
}

func TestLifecycle(t *testing.T) {
	m := newMandate("m1")
	if m.Active() {
		t.Error("um mandato pendente não pode receber cobranças")
	}

	m.Submit(42)
	if m.Status != StatusSubmitted || m.ExternalID != 42 {
		t.Errorf("depois de submeter = %+v", m)
	}
	if m.Active() {
		t.Error("submetido ainda não é activo: falta o titular ir ao banco")
	}

	m.Activate(now)
	if !m.Active() || m.ActivatedAt == nil || !m.ActivatedAt.Equal(now) {
		t.Errorf("depois de activar = %+v", m)
	}

	m.Cancel("CUST", now)
	if m.Status != StatusCancelled || m.Reason != "CUST" || m.CancelledAt == nil {
		t.Errorf("depois de cancelar = %+v", m)
	}
	if m.Active() {
		t.Error("um mandato cancelado não pode receber cobranças")
	}
}

func TestRejectAndExpire(t *testing.T) {
	m := newMandate("m1")
	m.Submit(1)
	m.Reject("AC04")
	if m.Status != StatusRejected || m.Reason != "AC04" {
		t.Errorf("recusado = %+v", m)
	}

	other := newMandate("m2")
	other.Submit(2)
	other.Expire()
	if other.Status != StatusExpired {
		t.Errorf("expirado = %s", other.Status)
	}
}

func TestTerminalStates(t *testing.T) {
	tests := map[Status]bool{
		StatusPending:   false,
		StatusSubmitted: false,
		StatusActive:    false,
		StatusRejected:  true,
		StatusCancelled: true,
		StatusExpired:   true,
	}
	for s, want := range tests {
		if got := s.Terminal(); got != want {
			t.Errorf("%s: terminal = %v, queria %v", s, got, want)
		}
	}
}

func TestExpiredOnlyAppliesWhileWaiting(t *testing.T) {
	// O prazo é para a activação. Um mandato já activo não expira por o
	// titular ter demorado, e um sem prazo nunca expira.
	past := now.Add(-time.Hour)

	m := newMandate("m1")
	m.Submit(1)
	m.ExpiresAt = &past
	if !m.Expired(now) {
		t.Error("submetido com prazo passado devia estar expirado")
	}

	m.Activate(now)
	if m.Expired(now) {
		t.Error("um mandato activo não expira à espera de activação")
	}

	noDeadline := newMandate("m2")
	noDeadline.Submit(2)
	if noDeadline.Expired(now) {
		t.Error("sem prazo não expira")
	}

	var nilMandate *Mandate
	if nilMandate.Expired(now) || nilMandate.Active() {
		t.Error("um mandato inexistente não é activo nem expirado")
	}
}

func TestStoreResolver(t *testing.T) {
	active := newMandate("m1")
	active.Submit(42)
	active.Activate(now)

	waiting := newMandate("m2")
	waiting.Submit(43)

	r := NewStoreResolver(newStore(active, waiting))
	ctx := context.Background()

	id, ok, err := r.Resolve(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 || !ok {
		t.Errorf("mandato activo = (%d, %v), queria (42, true)", id, ok)
	}

	// Um mandato por activar resolve o número mas diz que não está activo: é o
	// que permite ao provider distinguir "o titular ainda não foi ao banco" de
	// "não há mandato nenhum".
	id, ok, err = r.Resolve(ctx, "m2")
	if err != nil {
		t.Fatal(err)
	}
	if id != 43 || ok {
		t.Errorf("mandato por activar = (%d, %v), queria (43, false)", id, ok)
	}

	// Um identificador que não existe devolve zero, e não um erro: a ausência é
	// um resultado normal.
	id, ok, err = r.Resolve(ctx, "não existe")
	if err != nil || id != 0 || ok {
		t.Errorf("mandato inexistente = (%d, %v, %v)", id, ok, err)
	}
}

func TestStoreResolverPropagatesErrors(t *testing.T) {
	boom := errors.New("base de dados em baixo")
	r := NewStoreResolver(&memStore{items: map[string]*Mandate{}, fail: boom})
	if _, _, err := r.Resolve(context.Background(), "m1"); !errors.Is(err, boom) {
		t.Errorf("erro = %v, queria o do armazenamento", err)
	}
}

func TestStoreQueries(t *testing.T) {
	active := newMandate("m1")
	active.Submit(42)
	active.Activate(now)
	waiting := newMandate("m2")
	waiting.Submit(43)

	s := newStore(active, waiting)
	ctx := context.Background()

	got, err := s.ByExternalID(ctx, "proxypay-dds", 42)
	if err != nil || got == nil || got.ID != "m1" {
		t.Errorf("por identificador do gateway = %v, %v", got, err)
	}
	if got, _ := s.ByExternalID(ctx, "outro", 42); got != nil {
		t.Error("o provider faz parte da chave")
	}
	if got, _ := s.ActiveForSubject(ctx, "sub-1"); got == nil || got.ID != "m1" {
		t.Errorf("mandato activo do assunto = %v", got)
	}
	pending, _ := s.PendingActivation(ctx, 10)
	if len(pending) != 1 || pending[0].ID != "m2" {
		t.Errorf("à espera de activação = %v", pending)
	}
	if err := s.Create(ctx, newMandate("m3")); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, active); err != nil {
		t.Fatal(err)
	}
}
