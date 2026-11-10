package llm

import (
	"fmt"
	"math"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// NanoUSD é dinheiro em nanodólares (1e-9 USD). Inteiro para que somas de muitas
// chamadas baratas não acumulem erro de ponto flutuante.
type NanoUSD int64

// USD converte um valor em dólares para NanoUSD, arredondando.
func USD(v float64) NanoUSD { return NanoUSD(math.Round(v * 1e9)) }

func (n NanoUSD) Dollars() float64 { return float64(n) / 1e9 }

func (n NanoUSD) String() string { return fmt.Sprintf("$%.6f", n.Dollars()) }

// Usage é o consumo de tokens de uma chamada, como reportado pela API.
type Usage struct {
	InputTokens        int64
	OutputTokens       int64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	CacheReadTokens    int64
}

// PromptTokens soma todos os tokens de entrada, usados para escolher a faixa de preço.
func (u Usage) PromptTokens() int64 {
	return u.InputTokens + u.CacheWrite5mTokens + u.CacheWrite1hTokens + u.CacheReadTokens
}

// PriceTable calcula custo a partir da tabela configurável do sentinela.yaml.
type PriceTable struct {
	p config.Pricing
}

func NewPriceTable(p config.Pricing) *PriceTable { return &PriceTable{p: p} }

// Cost calcula o custo de uma chamada. Batch aplica pricing.batch_discount sobre o total.
func (t *PriceTable) Cost(model string, u Usage, batch bool) (NanoUSD, error) {
	mp, ok := t.p.Models[model]
	if !ok || mp == nil {
		return 0, fmt.Errorf("modelo %q sem preço em pricing.models", model)
	}
	in, out, read := mp.Input, mp.Output, mp.CacheRead
	// A faixa vale para o prompt inteiro (ex.: Haiku 5.5 acima de 100K tokens).
	for _, tier := range mp.Tiers {
		if u.PromptTokens() > tier.AboveInputTokens {
			in, out, read = tier.Input, tier.Output, tier.CacheRead
		}
	}
	if read == 0 {
		read = in * 0.1
	}
	perM := float64(u.InputTokens)*in +
		float64(u.OutputTokens)*out +
		float64(u.CacheWrite5mTokens)*in*t.p.CacheWrite5mMultiplier +
		float64(u.CacheWrite1hTokens)*in*t.p.CacheWrite1hMultiplier +
		float64(u.CacheReadTokens)*read
	usd := perM / 1e6
	if batch {
		usd *= t.p.BatchDiscount
	}
	return USD(usd), nil
}
