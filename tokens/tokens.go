// Package tokens gera os links de pagamento de uso único que acompanham uma
// cobrança de renovação.
//
// O link é o que permite ao cliente pagar e trocar de método sem precisar de
// sessão iniciada, o que importa quando quem recebe o email de cobrança é o
// departamento financeiro e não quem tem a conta. Por isso mesmo, o token é
// tratado como uma credencial:
//
//   - Aleatório de 256 bits, indistinguível de ruído.
//   - Guardado apenas em resumo criptográfico, para que uma fuga da base de
//     dados não entregue links funcionais.
//   - Com prazo e de uso único.
package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// ErrInvalid é devolvido quando o token não existe, já foi usado ou expirou.
//
// É deliberadamente um erro só para os três casos: distinguir "não existe" de
// "expirou" diz a quem tenta adivinhar tokens quais é que já foram válidos.
var ErrInvalid = errors.New("tokens: link inválido ou expirado")

// Token é um link de pagamento emitido.
type Token struct {
	// ID é o identificador do registo.
	ID string
	// SubjectID é o que o token dá a pagar (a subscrição, a factura).
	SubjectID string
	// Hash é o resumo do valor entregue ao cliente. O valor em claro não é
	// guardado em lado nenhum.
	Hash string
	// Purpose distingue para que serve o token, para que um link de renovação
	// não sirva de link de outra coisa qualquer.
	Purpose string
	// ExpiresAt é o prazo de validade.
	ExpiresAt time.Time
	// UsedAt é quando foi consumido.
	UsedAt    *time.Time
	CreatedAt time.Time
}

// Valid indica se o token ainda pode ser usado.
func (t *Token) Valid(now time.Time) bool {
	return t != nil && t.UsedAt == nil && now.Before(t.ExpiresAt)
}

// Store é o armazenamento dos tokens.
type Store interface {
	// Create persiste um token.
	Create(ctx context.Context, t *Token) error
	// ByHash devolve um token pelo resumo, ou (nil, nil).
	ByHash(ctx context.Context, hash string) (*Token, error)
	// MarkUsed marca o token como consumido.
	MarkUsed(ctx context.Context, id string, at time.Time) error
	// DeleteExpired apaga os tokens que já não servem. Correr de tempos a
	// tempos evita que a tabela cresça para sempre.
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
}

// Issuer emite e resolve tokens.
type Issuer struct {
	store Store
	// IDs gera identificadores para os tokens.
	IDs func() string
	// Now devolve a hora. Substituível em testes.
	Now func() time.Time
}

// NewIssuer cria o emissor.
func NewIssuer(store Store) *Issuer {
	return &Issuer{store: store, Now: func() time.Time { return time.Now().UTC() }}
}

// Issue cria um token para um assunto, válido até ao prazo dado, e devolve o
// valor em claro para pôr no link.
//
// Este é o único momento em que o valor em claro existe: guarde-o no link e não
// noutro sítio, porque não há como o recuperar depois.
func (i *Issuer) Issue(ctx context.Context, subjectID, purpose string, expiresAt time.Time) (string, error) {
	raw, err := Generate()
	if err != nil {
		return "", err
	}
	now := i.now()
	t := &Token{
		ID:        i.newID(),
		SubjectID: subjectID,
		Hash:      Hash(raw),
		Purpose:   purpose,
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}
	if err := i.store.Create(ctx, t); err != nil {
		return "", err
	}
	return raw, nil
}

// Resolve valida um token e devolve-o sem o consumir.
//
// É o que a página de pagamento usa para se mostrar: consumir aqui faria com
// que recarregar a página invalidasse o link.
func (i *Issuer) Resolve(ctx context.Context, raw, purpose string) (*Token, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrInvalid
	}
	t, err := i.store.ByHash(ctx, Hash(raw))
	if err != nil {
		return nil, err
	}
	if t == nil || !t.Valid(i.now()) {
		return nil, ErrInvalid
	}
	if purpose != "" && t.Purpose != purpose {
		return nil, ErrInvalid
	}
	return t, nil
}

// Consume valida e marca o token como usado, numa só operação.
//
// Chame-o quando o token tiver mesmo cumprido a sua função (o pagamento
// passou), e não quando a página abre: um link consumido ao abrir deixa o
// cliente sem forma de voltar se algo correr mal a meio.
func (i *Issuer) Consume(ctx context.Context, raw, purpose string) (*Token, error) {
	t, err := i.Resolve(ctx, raw, purpose)
	if err != nil {
		return nil, err
	}
	now := i.now()
	if err := i.store.MarkUsed(ctx, t.ID, now); err != nil {
		return nil, err
	}
	t.UsedAt = &now
	return t, nil
}

// Cleanup apaga os tokens expirados há mais do que o período de retenção.
func (i *Issuer) Cleanup(ctx context.Context, retention time.Duration) (int, error) {
	return i.store.DeleteExpired(ctx, i.now().Add(-retention))
}

func (i *Issuer) now() time.Time {
	if i.Now != nil {
		return i.Now()
	}
	return time.Now().UTC()
}

func (i *Issuer) newID() string {
	if i.IDs != nil {
		return i.IDs()
	}
	return ""
}

// randRead é a fonte de aleatoriedade, indirecta para os testes poderem
// forçar a falha.
//
// A falha do gerador do sistema não acontece na prática, mas o caminho existe e
// tem de estar certo: devolver um token previsível porque a leitura falhou seria
// entregar links que se adivinham.
var randRead = rand.Read

// Generate cria um valor aleatório de 256 bits, seguro para URL.
func Generate() (string, error) {
	buf := make([]byte, 32)
	if _, err := randRead(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Hash devolve o resumo SHA-256 de um token, em hexadecimal.
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}
