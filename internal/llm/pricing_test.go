package llm

import (
	"errors"
	"testing"
)

func TestPriceTable(t *testing.T) {
	prices := realPricing(t)
	cases := []struct {
		name  string
		model string
		usage Usage
		batch bool
		want  NanoUSD
	}{
		{
			name: "Haiku até 100K tokens usa a faixa base", model: "claude-haiku-5-5",
			usage: Usage{InputTokens: 100_000, OutputTokens: 1_000},
			want:  USD((100_000*0.10 + 1_000*0.50) / 1e6),
		},
		{
			name: "Haiku acima de 100K tokens muda de faixa no prompt inteiro", model: "claude-haiku-5-5",
			usage: Usage{InputTokens: 90_000, CacheReadTokens: 20_000, OutputTokens: 1_000},
			want:  USD((90_000*0.50 + 20_000*0.05 + 1_000*2.50) / 1e6),
		},
		{
			name: "Sonnet com escrita de cache de 1h e leitura de cache", model: "claude-sonnet-5-5",
			usage: Usage{InputTokens: 1_000, CacheWrite1hTokens: 10_000, CacheReadTokens: 50_000, OutputTokens: 2_000},
			want:  USD((1_000*2.0 + 10_000*2.0*2.0 + 50_000*0.20 + 2_000*10.0) / 1e6),
		},
		{
			name: "Opus em batch paga metade", model: "claude-opus-5-5",
			usage: Usage{InputTokens: 10_000, OutputTokens: 1_000}, batch: true,
			want: USD((10_000*4.0 + 1_000*20.0) / 1e6 * 0.5),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := prices.Cost(tc.model, tc.usage, tc.batch)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("custo = %s (%d), quer %s (%d)", got, got, tc.want, tc.want)
			}
		})
	}
}

func TestPriceTableRejectsUnknownModel(t *testing.T) {
	if _, err := realPricing(t).Cost("claude-desconhecido", Usage{InputTokens: 1}, false); err == nil {
		t.Fatal("modelo sem preço deveria falhar")
	}
}

func TestBudgetChargesPastLimitAndRefusesNextCall(t *testing.T) {
	b := NewBudget(USD(1))
	if err := b.Charge(USD(0.6)); err != nil {
		t.Fatal(err)
	}
	if err := b.Check(); err != nil {
		t.Fatalf("ainda há saldo: %v", err)
	}
	if err := b.Charge(USD(0.6)); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("esperava estouro, veio %v", err)
	}
	if b.Spent() != USD(1.2) {
		t.Errorf("gasto = %s, o custo incorrido deve ser contabilizado mesmo acima do limite", b.Spent())
	}
	if err := b.Check(); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("próxima chamada deveria ser recusada, veio %v", err)
	}
}
