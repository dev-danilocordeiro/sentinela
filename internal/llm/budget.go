package llm

import (
	"errors"
	"fmt"
	"sync"
)

// ErrBudgetExceeded indica que a rodada estourou o orçamento e deve ser abortada.
var ErrBudgetExceeded = errors.New("orçamento da rodada estourado")

// ErrCallLimitExceeded é o estouro do limite de chamadas por rodada (provider claude_code).
// Embrulha ErrBudgetExceeded: quem trata orçamento estourado trata os dois.
var ErrCallLimitExceeded = fmt.Errorf("%w: limite de chamadas da rodada", ErrBudgetExceeded)

// Budget acumula o gasto de uma rodada e recusa novas chamadas quando o limite é atingido.
// É seguro para uso concorrente.
type Budget struct {
	mu       sync.Mutex
	limit    NanoUSD
	spent    NanoUSD
	maxCalls int // 0 = sem limite de chamadas
	calls    int
}

func NewBudget(limit NanoUSD) *Budget { return &Budget{limit: limit} }

// NewCallBudget limita a rodada por número de chamadas, sem teto em dólar
// (providers sem custo por token, como a assinatura do Claude Code).
func NewCallBudget(maxCalls int) *Budget {
	return &Budget{limit: NanoUSD(1<<62 - 1), maxCalls: maxCalls}
}

// ReserveCall conta uma chamada antes de fazê-la; falha se o limite já foi atingido.
func (b *Budget) ReserveCall() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.maxCalls > 0 && b.calls >= b.maxCalls {
		return fmt.Errorf("%w (%d)", ErrCallLimitExceeded, b.maxCalls)
	}
	b.calls++
	return nil
}

// Calls devolve quantas chamadas foram reservadas.
func (b *Budget) Calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// MaxCalls devolve o limite de chamadas (0 = sem limite).
func (b *Budget) MaxCalls() int { return b.maxCalls }

// Check falha se o gasto já atingiu o limite; chamado antes de cada requisição.
func (b *Budget) Check() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.spent >= b.limit {
		return fmt.Errorf("%w: gasto %s de %s", ErrBudgetExceeded, b.spent, b.limit)
	}
	return nil
}

// Charge registra um gasto já incorrido. O custo sempre é contabilizado; o erro
// avisa o chamador que a rodada passou do limite e precisa parar.
func (b *Budget) Charge(c NanoUSD) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spent += c
	if b.spent > b.limit {
		return fmt.Errorf("%w: gasto %s de %s", ErrBudgetExceeded, b.spent, b.limit)
	}
	return nil
}

func (b *Budget) Spent() NanoUSD {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

func (b *Budget) Remaining() NanoUSD {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit - b.spent
}
