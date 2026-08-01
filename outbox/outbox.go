// Package outbox garante que um evento sai sempre que a alteração que o
// justifica ficou gravada, e nunca quando ela não ficou.
//
// O problema que resolve aparece assim que há uma base de dados e uma fila de
// mensagens: gravar o pagamento e publicar o evento são duas operações em dois
// sistemas, e não há transacção que abranja os dois. Publicar primeiro e gravar
// depois anuncia coisas que não aconteceram; gravar primeiro e publicar depois
// perde o evento se o processo morrer no meio, e ninguém dá por isso.
//
// A saída é escrever a mensagem na mesma transacção da alteração, numa tabela
// nossa, e ter um processo à parte a entregá-la. A transacção garante que a
// mensagem existe exactamente quando a alteração existe; o processo garante que
// acaba por sair.
//
// O preço é a entrega poder repetir-se. Ver [Dispatcher] para o que isso obriga
// do lado de quem consome.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Status é o estado de uma mensagem.
type Status string

const (
	// StatusPending: por entregar.
	StatusPending Status = "pending"
	// StatusDispatched: entregue.
	StatusDispatched Status = "dispatched"
	// StatusFailed: a última tentativa falhou e há outra marcada.
	StatusFailed Status = "failed"
	// StatusDead: esgotou as tentativas e ninguém lhe volta a tocar sem
	// alguém decidir.
	//
	// Uma mensagem morta é um alarme, não um fim de linha silencioso: alguma
	// coisa aconteceu no sistema e o resto do mundo não soube.
	StatusDead Status = "dead"
)

// ErrNotFound indica uma mensagem inexistente.
var ErrNotFound = errors.New("outbox: mensagem não encontrada")

// Message é um evento à espera de sair.
type Message struct {
	// ID é o identificador da mensagem, e é ele que os consumidores usam para
	// deduplicar.
	ID string

	// Topic é o assunto ("payment.approved", "subscription.renewed").
	Topic string

	// Key agrupa as mensagens que têm de sair pela ordem em que entraram.
	//
	// Duas mensagens da mesma subscrição com a mesma chave saem por ordem; sem
	// chave, não há ordem nenhuma garantida. Use o identificador do que mudou,
	// não o do evento.
	Key string

	// Payload é o corpo, normalmente JSON.
	Payload []byte

	// Headers são metadados de encaminhamento.
	Headers map[string]string

	// Status, Attempts, NextAttemptAt e LastError descrevem a entrega.
	Status        Status
	Attempts      int
	NextAttemptAt *time.Time
	LastError     string

	// AvailableAt permite adiar a primeira tentativa, para eventos que só
	// fazem sentido daqui a algum tempo.
	AvailableAt *time.Time

	CreatedAt    time.Time
	DispatchedAt *time.Time
}

// New cria uma mensagem pendente com o corpo serializado em JSON.
func New(id, topic, key string, payload any) (*Message, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Message{
		ID:        id,
		Topic:     topic,
		Key:       key,
		Payload:   body,
		Status:    StatusPending,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// Decode lê o corpo da mensagem para uma estrutura.
func (m *Message) Decode(v any) error { return json.Unmarshal(m.Payload, v) }

// Header devolve um metadado.
func (m *Message) Header(k string) string {
	if m.Headers == nil {
		return ""
	}
	return m.Headers[k]
}

// WithHeader acrescenta um metadado.
func (m *Message) WithHeader(k, v string) *Message {
	if m.Headers == nil {
		m.Headers = map[string]string{}
	}
	m.Headers[k] = v
	return m
}

// Store é o armazenamento das mensagens.
//
// A implementação tem de garantir duas coisas que a biblioteca não pode
// garantir sozinha:
//
//   - [Store.Enqueue] participa na transacção de quem chama. É a razão de ser
//     de todo o mecanismo: se a gravação for por fora, o problema volta.
//   - [Store.Claim] entrega cada mensagem a um só processo. Sem isso, duas
//     instâncias publicam a mesma coisa em paralelo.
type Store interface {
	// Enqueue grava mensagens, dentro da transacção em curso.
	Enqueue(ctx context.Context, msgs ...*Message) error

	// Claim reserva até limit mensagens prontas a sair e devolve-as.
	//
	// Reservar é diferente de ler: a implementação deve travar as linhas (um
	// SELECT ... FOR UPDATE SKIP LOCKED, ou uma marca de posse com prazo) para
	// que duas instâncias não apanhem as mesmas. Sem isso, cada evento sai
	// tantas vezes quantos processos estiverem a correr.
	//
	// Devolve por ordem de criação, e nunca duas mensagens da mesma [Message.Key]
	// no mesmo lote se a ordem por chave importar.
	Claim(ctx context.Context, limit int, now time.Time) ([]*Message, error)

	// MarkDispatched dá a mensagem por entregue.
	MarkDispatched(ctx context.Context, id string, at time.Time) error

	// MarkFailed guarda o erro e marca a próxima tentativa.
	MarkFailed(ctx context.Context, id string, attempts int, next time.Time, reason string) error

	// MarkDead desiste da mensagem.
	MarkDead(ctx context.Context, id string, reason string) error

	// Purge apaga as mensagens já entregues há mais tempo do que o indicado.
	//
	// Sem isto a tabela cresce para sempre, e uma tabela de outbox grande é
	// exactamente onde não se quer uma consulta lenta: no caminho de
	// escrita de todas as transacções.
	Purge(ctx context.Context, before time.Time) (int, error)
}

// Publisher entrega a mensagem ao mundo.
//
// Devolver erro faz a mensagem voltar à fila com espera. Devolver nil dá-a por
// entregue, e a partir daí não há maneira de a recuperar: só devolva nil quando
// o destino tiver mesmo aceitado.
type Publisher interface {
	Publish(ctx context.Context, m *Message) error
}

// PublisherFunc adapta uma função a [Publisher].
type PublisherFunc func(ctx context.Context, m *Message) error

// Publish chama a função.
func (f PublisherFunc) Publish(ctx context.Context, m *Message) error { return f(ctx, m) }
