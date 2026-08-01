package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

var now = time.Date(2026, time.March, 10, 12, 0, 0, 0, time.UTC)

type memStore struct {
	msgs   map[string]*Message
	order  []string
	purged int
}

func newStore() *memStore { return &memStore{msgs: map[string]*Message{}} }

func (m *memStore) Enqueue(_ context.Context, msgs ...*Message) error {
	for _, msg := range msgs {
		if _, seen := m.msgs[msg.ID]; !seen {
			m.order = append(m.order, msg.ID)
		}
		m.msgs[msg.ID] = msg
	}
	return nil
}

func (m *memStore) Claim(_ context.Context, limit int, at time.Time) ([]*Message, error) {
	var out []*Message
	for _, id := range m.order {
		msg := m.msgs[id]
		if msg.Status != StatusPending && msg.Status != StatusFailed {
			continue
		}
		if msg.NextAttemptAt != nil && at.Before(*msg.NextAttemptAt) {
			continue
		}
		if msg.AvailableAt != nil && at.Before(*msg.AvailableAt) {
			continue
		}
		out = append(out, msg)
		if len(out) >= limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *memStore) MarkDispatched(_ context.Context, id string, at time.Time) error {
	msg, ok := m.msgs[id]
	if !ok {
		return ErrNotFound
	}
	msg.Status = StatusDispatched
	msg.DispatchedAt = &at
	return nil
}

func (m *memStore) MarkFailed(_ context.Context, id string, attempts int, next time.Time, reason string) error {
	msg, ok := m.msgs[id]
	if !ok {
		return ErrNotFound
	}
	msg.Status = StatusFailed
	msg.Attempts = attempts
	msg.NextAttemptAt = &next
	msg.LastError = reason
	return nil
}

func (m *memStore) MarkDead(_ context.Context, id, reason string) error {
	msg, ok := m.msgs[id]
	if !ok {
		return ErrNotFound
	}
	msg.Status = StatusDead
	msg.LastError = reason
	return nil
}

func (m *memStore) Purge(_ context.Context, before time.Time) (int, error) {
	n := 0
	for id, msg := range m.msgs {
		if msg.Status == StatusDispatched && msg.DispatchedAt != nil && msg.DispatchedAt.Before(before) {
			delete(m.msgs, id)
			n++
		}
	}
	m.purged += n
	return n, nil
}

func msg(id, topic, key string, created time.Time) *Message {
	m, _ := New(id, topic, key, map[string]string{"id": id})
	m.CreatedAt = created
	return m
}

func dispatcher(store *memStore, pub Publisher) *Dispatcher {
	return &Dispatcher{
		Store: store, Publisher: pub,
		BaseDelay: time.Minute, MaxDelay: time.Hour, MaxAttempts: 3,
		Now: func() time.Time { return now },
		Log: quiet(),
	}
}

func TestNewAndDecode(t *testing.T) {
	type body struct {
		PaymentID string `json:"payment_id"`
		Amount    int64  `json:"amount"`
	}
	m, err := New("m1", "payment.approved", "pag-1", body{PaymentID: "pag-1", Amount: 5900})
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != StatusPending {
		t.Errorf("estado = %s, queria pendente", m.Status)
	}
	var out body
	if err := m.Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.PaymentID != "pag-1" || out.Amount != 5900 {
		t.Errorf("corpo = %+v", out)
	}
}

func TestDispatchHappyPath(t *testing.T) {
	store := newStore()
	ctx := context.Background()
	_ = store.Enqueue(ctx, msg("m1", "a", "", now), msg("m2", "b", "", now.Add(time.Second)))

	var sent []string
	d := dispatcher(store, PublisherFunc(func(_ context.Context, m *Message) error {
		sent = append(sent, m.ID)
		return nil
	}))

	rep := d.Run(ctx)
	if rep.Dispatched != 2 || rep.Failed != 0 {
		t.Errorf("relatório = %+v", rep)
	}
	if len(sent) != 2 || sent[0] != "m1" {
		t.Errorf("enviadas = %v, queria por ordem de criação", sent)
	}
	if store.msgs["m1"].Status != StatusDispatched || store.msgs["m1"].DispatchedAt == nil {
		t.Errorf("m1 = %+v", store.msgs["m1"])
	}

	// A segunda passagem não repete o que já saiu.
	rep = d.Run(ctx)
	if rep.Claimed != 0 {
		t.Errorf("segunda passagem reservou %d mensagens", rep.Claimed)
	}
}

func TestFailureSchedulesRetryWithBackoff(t *testing.T) {
	store := newStore()
	ctx := context.Background()
	_ = store.Enqueue(ctx, msg("m1", "a", "", now))

	d := dispatcher(store, PublisherFunc(func(context.Context, *Message) error {
		return errors.New("destino em baixo")
	}))

	rep := d.Run(ctx)
	if rep.Failed != 1 {
		t.Errorf("relatório = %+v", rep)
	}
	m := store.msgs["m1"]
	if m.Status != StatusFailed || m.Attempts != 1 {
		t.Errorf("mensagem = %+v", m)
	}
	if m.LastError == "" {
		t.Error("o motivo da falha tem de ficar guardado, senão ninguém sabe porquê")
	}
	if want := now.Add(time.Minute); !m.NextAttemptAt.Equal(want) {
		t.Errorf("próxima tentativa = %v, queria %v", m.NextAttemptAt, want)
	}

	// Antes da hora marcada não é reservada outra vez.
	if rep := d.Run(ctx); rep.Claimed != 0 {
		t.Errorf("repetiu antes da hora: %+v", rep)
	}
}

func TestDeadAfterMaxAttempts(t *testing.T) {
	store := newStore()
	ctx := context.Background()
	_ = store.Enqueue(ctx, msg("m1", "a", "", now))

	dead := 0
	d := dispatcher(store, PublisherFunc(func(context.Context, *Message) error {
		return errors.New("sempre a falhar")
	}))
	d.OnDead = func(context.Context, *Message, string) { dead++ }

	// Três tentativas: as duas primeiras marcam repetição, a terceira desiste.
	for i := 0; i < 3; i++ {
		d.Now = func() time.Time { return now.Add(time.Duration(i) * time.Hour) }
		d.Run(ctx)
	}

	if store.msgs["m1"].Status != StatusDead {
		t.Errorf("estado = %s, queria perdida", store.msgs["m1"].Status)
	}
	// Uma mensagem perdida é um alarme: algo aconteceu e o mundo não soube.
	if dead != 1 {
		t.Errorf("avisos de mensagem perdida = %d, queria 1", dead)
	}
	// E não volta a ser reservada.
	if rep := d.Run(ctx); rep.Claimed != 0 {
		t.Errorf("uma mensagem perdida foi reservada outra vez: %+v", rep)
	}
}

func TestFailureBlocksLaterMessagesOfTheSameKey(t *testing.T) {
	// Sem isto, a segunda alteração de uma subscrição sai antes da primeira
	// sempre que a primeira falhe, e o consumidor vê o estado final antes do
	// intermédio.
	store := newStore()
	ctx := context.Background()
	_ = store.Enqueue(ctx,
		msg("m1", "sub.updated", "sub-1", now),
		msg("m2", "sub.updated", "sub-1", now.Add(time.Second)),
		msg("m3", "sub.updated", "sub-2", now.Add(2*time.Second)),
	)

	var sent []string
	d := dispatcher(store, PublisherFunc(func(_ context.Context, m *Message) error {
		if m.ID == "m1" {
			return errors.New("falha")
		}
		sent = append(sent, m.ID)
		return nil
	}))

	d.Run(ctx)

	for _, id := range sent {
		if id == "m2" {
			t.Error("m2 saiu antes de m1, que é da mesma chave e falhou")
		}
	}
	// Outra chave não é afectada.
	found := false
	for _, id := range sent {
		if id == "m3" {
			found = true
		}
	}
	if !found {
		t.Error("m3 é de outra chave e devia ter saído na mesma")
	}
}

func TestMarkDispatchedFailureLeavesMessageToRepeat(t *testing.T) {
	// A mensagem saiu, a marcação falhou: vai voltar a sair. É o caso que
	// obriga o consumidor a ser idempotente, e tem de ser um comportamento
	// conhecido e não uma surpresa.
	store := &brokenMarkStore{memStore: newStore()}
	ctx := context.Background()
	_ = store.Enqueue(ctx, msg("m1", "a", "", now))

	sent := 0
	d := dispatcher(store.memStore, PublisherFunc(func(context.Context, *Message) error {
		sent++
		return nil
	}))
	d.Store = store

	if rep := d.Run(ctx); rep.Dispatched != 1 {
		t.Errorf("relatório = %+v", rep)
	}
	if store.msgs["m1"].Status != StatusPending {
		t.Errorf("estado = %s, queria continuar pendente", store.msgs["m1"].Status)
	}
	d.Run(ctx)
	if sent != 2 {
		t.Errorf("publicações = %d, queria 2 (a repetição é esperada)", sent)
	}
}

type brokenMarkStore struct{ *memStore }

func (b *brokenMarkStore) MarkDispatched(context.Context, string, time.Time) error {
	return errors.New("base de dados em baixo")
}

func TestAvailableAtDelaysFirstAttempt(t *testing.T) {
	store := newStore()
	ctx := context.Background()
	m := msg("m1", "a", "", now)
	later := now.Add(2 * time.Hour)
	m.AvailableAt = &later
	_ = store.Enqueue(ctx, m)

	d := dispatcher(store, PublisherFunc(func(context.Context, *Message) error { return nil }))
	if rep := d.Run(ctx); rep.Claimed != 0 {
		t.Errorf("saiu antes da hora: %+v", rep)
	}
	d.Now = func() time.Time { return later.Add(time.Minute) }
	if rep := d.Run(ctx); rep.Dispatched != 1 {
		t.Errorf("não saiu depois da hora: %+v", rep)
	}
}

func TestBackoff(t *testing.T) {
	base, max := time.Minute, 10*time.Minute
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, max, max}
	for i, w := range want {
		if got := Backoff(i+1, base, max); got != w {
			t.Errorf("tentativa %d = %v, queria %v", i+1, got, w)
		}
	}
	if got := Backoff(0, base, max); got != base {
		t.Errorf("tentativa 0 = %v, queria a base", got)
	}
}

func TestPurgeRemovesOnlyDispatched(t *testing.T) {
	store := newStore()
	ctx := context.Background()
	_ = store.Enqueue(ctx, msg("m1", "a", "", now), msg("m2", "b", "", now))

	d := dispatcher(store, PublisherFunc(func(_ context.Context, m *Message) error {
		if m.ID == "m2" {
			return errors.New("falha")
		}
		return nil
	}))
	d.Run(ctx)

	d.Now = func() time.Time { return now.Add(48 * time.Hour) }
	n, err := d.Purge(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("apagadas = %d, queria 1", n)
	}
	if _, still := store.msgs["m2"]; !still {
		t.Error("uma mensagem por entregar não pode ser apagada")
	}
}

func TestRunWithoutDependencies(t *testing.T) {
	d := &Dispatcher{Log: quiet()}
	if rep := d.Run(context.Background()); rep.Claimed != 0 {
		t.Errorf("sem dependências devia não fazer nada: %+v", rep)
	}
}

// --- restantes caminhos ---------------------------------------------------------

func TestNewRejectsUnserialisablePayload(t *testing.T) {
	if _, err := New("m1", "t", "", make(chan int)); err == nil {
		t.Error("um canal não se serializa e devia dar erro")
	}
}

func TestHeaders(t *testing.T) {
	m, _ := New("m1", "t", "", nil)
	if got := m.Header("x"); got != "" {
		t.Errorf("mensagem sem cabeçalhos = %q", got)
	}
	m.WithHeader("tenant", "acme").WithHeader("origem", "renovacao")
	if m.Header("tenant") != "acme" || m.Header("origem") != "renovacao" {
		t.Errorf("cabeçalhos = %v", m.Headers)
	}
}

func TestClaimErrorStopsThePass(t *testing.T) {
	store := &brokenClaimStore{memStore: newStore()}
	d := dispatcher(store.memStore, PublisherFunc(func(context.Context, *Message) error { return nil }))
	d.Store = store
	if rep := d.Run(context.Background()); rep.Claimed != 0 {
		t.Errorf("relatório = %+v", rep)
	}
}

type brokenClaimStore struct{ *memStore }

func (b *brokenClaimStore) Claim(context.Context, int, time.Time) ([]*Message, error) {
	return nil, errors.New("base de dados em baixo")
}

func TestMarkFailureIsLoggedNotFatal(t *testing.T) {
	// Se nem a marcação da falha grava, a mensagem sai outra vez na passagem
	// seguinte. É melhor do que parar a fila inteira.
	store := &brokenMarkFailStore{memStore: newStore()}
	ctx := context.Background()
	_ = store.Enqueue(ctx, msg("m1", "a", "", now))

	d := dispatcher(store.memStore, PublisherFunc(func(context.Context, *Message) error {
		return errors.New("destino em baixo")
	}))
	d.Store = store
	if rep := d.Run(ctx); rep.Failed != 1 {
		t.Errorf("relatório = %+v", rep)
	}
}

type brokenMarkFailStore struct{ *memStore }

func (b *brokenMarkFailStore) MarkFailed(context.Context, string, int, time.Time, string) error {
	return errors.New("base de dados em baixo")
}
func (b *brokenMarkFailStore) MarkDead(context.Context, string, string) error {
	return errors.New("base de dados em baixo")
}

func TestMarkDeadFailureIsLoggedNotFatal(t *testing.T) {
	store := &brokenMarkFailStore{memStore: newStore()}
	ctx := context.Background()
	_ = store.Enqueue(ctx, msg("m1", "a", "", now))

	d := dispatcher(store.memStore, PublisherFunc(func(context.Context, *Message) error {
		return errors.New("sempre a falhar")
	}))
	d.Store = store
	d.MaxAttempts = 1 // desiste à primeira
	if rep := d.Run(ctx); rep.Dead != 1 {
		t.Errorf("relatório = %+v", rep)
	}
}

func TestDeadMessageBlocksLaterOnesOfTheSameKey(t *testing.T) {
	store := newStore()
	ctx := context.Background()
	_ = store.Enqueue(ctx,
		msg("m1", "sub.updated", "sub-1", now),
		msg("m2", "sub.updated", "sub-1", now.Add(time.Second)),
	)
	d := dispatcher(store, PublisherFunc(func(_ context.Context, m *Message) error {
		if m.ID == "m1" {
			return errors.New("falha")
		}
		return nil
	}))
	d.MaxAttempts = 1 // a primeira falha mata a mensagem

	rep := d.Run(ctx)
	if rep.Dead != 1 {
		t.Errorf("relatório = %+v", rep)
	}
	if store.msgs["m2"].Status == StatusDispatched {
		t.Error("m2 saiu apesar de m1, da mesma chave, ter morrido")
	}
}

func TestCancelledContextStopsMidBatch(t *testing.T) {
	store := newStore()
	ctx, cancel := context.WithCancel(context.Background())
	_ = store.Enqueue(ctx, msg("m1", "a", "", now), msg("m2", "a", "", now.Add(time.Second)))

	sent := 0
	d := dispatcher(store, PublisherFunc(func(context.Context, *Message) error {
		sent++
		cancel() // cancela depois da primeira
		return nil
	}))
	d.Run(ctx)
	if sent != 1 {
		t.Errorf("publicações = %d, queria parar depois da primeira", sent)
	}
}

func TestDispatcherDefaults(t *testing.T) {
	// Sem configuração nenhuma, os valores por omissão têm de ser usáveis.
	store := newStore()
	ctx := context.Background()
	_ = store.Enqueue(ctx, msg("m1", "a", "", time.Now()))

	d := &Dispatcher{
		Store:     store,
		Publisher: PublisherFunc(func(context.Context, *Message) error { return nil }),
		Log:       quiet(),
	}
	if rep := d.Run(ctx); rep.Dispatched != 1 {
		t.Errorf("relatório = %+v", rep)
	}
	if got := d.batchSize(); got != 100 {
		t.Errorf("lote por omissão = %d", got)
	}
	if got := d.maxAttempts(); got != 8 {
		t.Errorf("tentativas por omissão = %d", got)
	}
	if got := d.baseDelay(); got != 5*time.Second {
		t.Errorf("espera base por omissão = %v", got)
	}
	if got := d.maxDelay(); got != 30*time.Minute {
		t.Errorf("tecto de espera por omissão = %v", got)
	}
	if d.now().IsZero() {
		t.Error("o relógio por omissão devia ser o do sistema")
	}
	if d.Log = nil; d.log() == nil {
		t.Error("o registador por omissão não pode ser nil")
	}
}

func TestBatchSizeLimitsThePass(t *testing.T) {
	// Um lote grande demais prende a passagem e atrasa tudo o resto; o limite
	// tem de ser respeitado.
	store := newStore()
	ctx := context.Background()
	_ = store.Enqueue(ctx,
		msg("m1", "a", "", now),
		msg("m2", "a", "", now.Add(time.Second)),
		msg("m3", "a", "", now.Add(2*time.Second)),
	)
	d := dispatcher(store, PublisherFunc(func(context.Context, *Message) error { return nil }))
	d.BatchSize = 2

	if rep := d.Run(ctx); rep.Claimed != 2 || rep.Dispatched != 2 {
		t.Errorf("relatório = %+v, queria duas por passagem", rep)
	}
	if store.msgs["m3"].Status != StatusPending {
		t.Errorf("a terceira devia ficar para a passagem seguinte: %s", store.msgs["m3"].Status)
	}
	if rep := d.Run(ctx); rep.Dispatched != 1 {
		t.Errorf("segunda passagem = %+v", rep)
	}
}
