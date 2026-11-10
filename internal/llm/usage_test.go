package llm

import (
	"testing"
	"time"
)

// Mensagens reais do CLI (sessões de 2026-10-11 a 2026-10-14) e as variantes de rate limit: todas
// são limite de uso, e o horário do reset sai da própria mensagem.
func TestUsageResetAtReadsTheResetFromTheMessage(t *testing.T) {
	sp, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Skip("sem tzdata: ", err)
	}
	now := time.Date(2026, 10, 14, 12, 29, 0, 0, sp) // quarta, 12:29 em São Paulo
	cases := []struct {
		msg  string
		want time.Time
		ok   bool
	}{
		{"You've hit your weekly limit · resets Oct 16, 7am (America/Sao_Paulo)", time.Date(2026, 10, 16, 7, 0, 0, 0, sp), true},
		{"You've hit your session limit · resets 5:50pm (America/Sao_Paulo)", time.Date(2026, 10, 14, 17, 50, 0, 0, sp), true},
		// Já passou hoje: é amanhã.
		{"You've hit your session limit · resets 8:40am (America/Sao_Paulo)", time.Date(2026, 10, 15, 8, 40, 0, 0, sp), true},
		{"Claude AI usage limit reached|1760500000", time.Unix(1760500000, 0), true},
		// Virada de ano: dezembro visto em janeiro não é o passado, é daqui a pouco.
		{"You've hit your weekly limit · resets Jan 2, 9am (America/Sao_Paulo)", time.Date(2027, 1, 2, 9, 0, 0, 0, sp), true},
		// Rate limit sem horário: é limite, mas o reset fica com o padrão de quem chama.
		{"API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"x\"}}", time.Time{}, false},
	}
	for _, c := range cases {
		if !UsageLimited(c.msg) {
			t.Errorf("%q deveria ser limite de uso", c.msg)
		}
		got, ok := UsageResetAt(c.msg, now)
		if ok != c.ok || !got.Equal(c.want) {
			t.Errorf("%q: reset = %s, %v; quer %s, %v", c.msg, got, ok, c.want, c.ok)
		}
	}
	for _, msg := range []string{"Rate limit reached for requests", "You've hit your limit · resets 3am"} {
		if !UsageLimited(msg) {
			t.Errorf("%q deveria ser limite de uso", msg)
		}
	}
	if UsageLimited("claude terminou com error_during_execution: build falhou") {
		t.Error("erro comum não é limite de uso")
	}
}
