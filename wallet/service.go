package wallet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spolly-ao/fllex/money"
	"github.com/spolly-ao/fllex/payment"
)

// ErrReasonRequired é devolvido num acerto manual sem motivo. O motivo é
// obrigatório porque é a única explicação que uma auditoria vai encontrar para
// dinheiro que apareceu ou desapareceu sem um facto do sistema por trás.
var ErrReasonRequired = errors.New("wallet: um acerto manual exige um motivo")

// Service é a API da carteira.
type Service struct {
	store Store
	// Currency é a moeda das carteiras que este serviço gere.
	Currency money.Currency
	// MinTopup e MaxTopup delimitam um carregamento.
	//
	// O mínimo existe porque os gateways recusam valores muito baixos; o máximo
	// é uma salvaguarda contra o engano de digitação que manda alguém para um
	// pedido de confirmação de dez milhões.
	MinTopup money.Amount
	MaxTopup money.Amount
	// IDs gera identificadores para os movimentos.
	IDs func() string
}

// NewService cria o serviço da carteira.
func NewService(store Store, currency money.Currency) *Service {
	return &Service{store: store, Currency: money.NormalizeCurrency(string(currency))}
}

// Balance devolve o saldo do cliente, criando a carteira na primeira consulta
// para que a interface tenha sempre um número que mostrar.
func (s *Service) Balance(ctx context.Context, customerID string) (*Wallet, error) {
	return s.store.Ensure(ctx, customerID, s.Currency)
}

// Statement devolve o extracto do cliente.
func (s *Service) Statement(ctx context.Context, customerID string, offset, limit int) ([]*Entry, int64, error) {
	w, err := s.store.Ensure(ctx, customerID, s.Currency)
	if err != nil {
		return nil, 0, err
	}
	return s.store.Entries(ctx, w.ID, offset, limit)
}

// ValidateTopup verifica se um carregamento cabe nos limites.
func (s *Service) ValidateTopup(amount money.Amount) error {
	if !amount.IsPositive() {
		return payment.ErrAmountNotPositive
	}
	if s.MinTopup.IsPositive() && amount.LessThan(s.MinTopup) {
		return fmt.Errorf("wallet: o carregamento mínimo é %s", s.MinTopup)
	}
	if s.MaxTopup.IsPositive() && amount.GreaterThan(s.MaxTopup) {
		return fmt.Errorf("wallet: o carregamento máximo é %s", s.MaxTopup)
	}
	return nil
}

// Credit acrescenta saldo.
//
// A referência é o que torna a operação idempotente: um carregamento já
// creditado é ignorado em silêncio, o que importa porque os gateways
// reentregam webhooks e um crédito repetido é dinheiro oferecido.
func (s *Service) Credit(ctx context.Context, customerID string, amount money.Amount, kind Kind, reference, description string) (*Wallet, error) {
	if !amount.IsPositive() {
		return nil, payment.ErrAmountNotPositive
	}
	w, err := s.store.Ensure(ctx, customerID, s.Currency)
	if err != nil {
		return nil, err
	}
	if reference != "" {
		existing, err := s.store.EntryByReference(ctx, w.ID, reference)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return w, nil
		}
	}
	return s.apply(ctx, w, amount, kind, reference, "", description)
}

// Debit desconta saldo. Devolve [payment.ErrInsufficientFunds] quando o saldo
// não chega, sem escrever nada.
func (s *Service) Debit(ctx context.Context, customerID string, amount money.Amount, kind Kind, reference, description string) (*Wallet, error) {
	if !amount.IsPositive() {
		return nil, payment.ErrAmountNotPositive
	}
	w, err := s.store.Ensure(ctx, customerID, s.Currency)
	if err != nil {
		return nil, err
	}
	if reference != "" {
		existing, err := s.store.EntryByReference(ctx, w.ID, reference)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return w, nil
		}
	}
	if w.Balance.LessThan(amount) {
		return w, payment.ErrInsufficientFunds
	}
	return s.apply(ctx, w, amount.Neg(), kind, reference, "", description)
}

// Adjust é o acerto manual de um operador. Positivo credita, negativo debita.
// Ao contrário do débito normal, pode deixar o saldo negativo: um operador que
// corrige um crédito indevido tem de o poder fazer mesmo que o cliente já tenha
// gasto o dinheiro.
func (s *Service) Adjust(ctx context.Context, customerID, operatorID string, amount money.Amount, reason string) (*Wallet, error) {
	if amount.IsZero() {
		return nil, payment.ErrAmountNotPositive
	}
	if reason == "" {
		return nil, ErrReasonRequired
	}
	w, err := s.store.Ensure(ctx, customerID, s.Currency)
	if err != nil {
		return nil, err
	}
	return s.apply(ctx, w, amount, KindAdjustment, "", operatorID, reason)
}

// Refund devolve à carteira o valor de uma cobrança.
func (s *Service) Refund(ctx context.Context, customerID string, amount money.Amount, reference, description string) (*Wallet, error) {
	return s.Credit(ctx, customerID, amount, KindRefund, reference, description)
}

func (s *Service) apply(ctx context.Context, w *Wallet, signed money.Amount, kind Kind, reference, operatorID, description string) (*Wallet, error) {
	balance, err := w.Balance.Add(signed)
	if err != nil {
		return nil, err
	}
	entry := &Entry{
		ID:           s.newID(),
		WalletID:     w.ID,
		Amount:       signed,
		BalanceAfter: balance,
		Kind:         kind,
		Reference:    reference,
		OperatorID:   operatorID,
		Description:  description,
		CreatedAt:    time.Now().UTC(),
	}
	w.Balance = balance
	w.UpdatedAt = entry.CreatedAt
	if err := s.store.Apply(ctx, w, entry); err != nil {
		return nil, err
	}
	return w, nil
}

func (s *Service) newID() string {
	if s.IDs != nil {
		return s.IDs()
	}
	return ""
}

// --- carteira como método de pagamento ---------------------------------------

// Provider expõe a carteira como um [payment.Provider], para que cobrar do
// saldo passe pelo mesmo caminho que cobrar por qualquer outro método.
type Provider struct{ svc *Service }

// AsProvider devolve a carteira como provider de pagamento.
func (s *Service) AsProvider() *Provider { return &Provider{svc: s} }

// Name devolve "wallet".
func (p *Provider) Name() string { return "wallet" }

// Methods: só o saldo.
func (p *Provider) Methods() []payment.Method { return []payment.Method{payment.MethodWallet} }

// SupportsCurrency: a moeda das carteiras que o serviço gere.
func (p *Provider) SupportsCurrency(c money.Currency) bool {
	return money.NormalizeCurrency(string(c)) == p.svc.Currency
}

// Configured indica se há armazenamento ligado.
func (p *Provider) Configured() bool { return p.svc != nil && p.svc.store != nil }

// Charge desconta o valor do saldo do cliente.
//
// O desfecho é imediato e definitivo: ou havia saldo e a cobrança fica paga, ou
// não havia e devolve [payment.ErrInsufficientFunds]. Não há estado pendente
// nem confirmação a esperar.
func (p *Provider) Charge(ctx context.Context, req payment.ChargeRequest) (payment.ChargeResult, error) {
	if !p.Configured() {
		return payment.ChargeResult{}, payment.ErrNotConfigured
	}
	if req.Method != "" && req.Method != payment.MethodWallet {
		return payment.ChargeResult{}, payment.ErrUnsupportedMethod
	}
	if !p.SupportsCurrency(req.Amount.Currency) {
		return payment.ChargeResult{}, payment.ErrUnsupportedCurrency
	}
	if req.Customer.ID == "" {
		return payment.ChargeResult{}, fmt.Errorf("wallet: a cobrança exige um cliente")
	}
	w, err := p.svc.Debit(ctx, req.Customer.ID, req.Amount, KindCharge, req.Reference, req.Description)
	if err != nil {
		return payment.ChargeResult{}, err
	}
	return payment.ChargeResult{
		Kind:        payment.KindPaid,
		Status:      payment.StatusApproved,
		ProviderRef: req.Reference,
		Raw:         map[string]any{"balance_after": w.Balance.Minor},
	}, nil
}

// O provider não implementa [payment.Refunder] de propósito: devolver dinheiro
// à carteira precisa de saber a quem, e a referência de estorno sozinha não o
// diz. Use [Service.Refund], que recebe o cliente.
