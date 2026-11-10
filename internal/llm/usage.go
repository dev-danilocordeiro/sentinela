package llm

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Horário do reset na mensagem de limite do CLI. Formatos vistos nas sessões de 2026-10-11 a
// 2026-10-14 (Claude Code 2.1.29x):
//
//	You've hit your weekly limit · resets Oct 16, 7am (America/Sao_Paulo)
//	You've hit your session limit · resets 5:50pm (America/Sao_Paulo)
//
// e o formato antigo, com o instante em segundos: "Claude AI usage limit reached|1760500000".
var (
	resetRe      = regexp.MustCompile(`(?i)resets\s+(?:([A-Z][a-z]{2})\s+(\d{1,2}),?\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)(?:\s*\(([^)]+)\))?`)
	resetEpochRe = regexp.MustCompile(`(?i)limit reached\|(\d{9,11})`)
)

// UsageResetAt lê na mensagem de limite de uso quando a cota volta. Sem data, o horário é o
// próximo depois de now; sem fuso, vale o de now. ok=false quando a mensagem não diz (rate
// limit da API, por exemplo): quem chama usa um intervalo padrão.
func UsageResetAt(msg string, now time.Time) (time.Time, bool) {
	if m := resetEpochRe.FindStringSubmatch(msg); m != nil {
		if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return time.Unix(sec, 0), true
		}
	}
	m := resetRe.FindStringSubmatch(msg)
	if m == nil {
		return time.Time{}, false
	}
	loc := now.Location()
	if m[6] != "" {
		if l, err := time.LoadLocation(m[6]); err == nil {
			loc = l
		}
	}
	hour, _ := strconv.Atoi(m[3])
	minute, _ := strconv.Atoi(m[4])
	if hour < 1 || hour > 12 || minute > 59 {
		return time.Time{}, false
	}
	hour %= 12
	if strings.EqualFold(m[5], "pm") {
		hour += 12
	}
	local := now.In(loc)
	if m[1] == "" {
		t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	}
	month, err := time.Parse("Jan", m[1])
	if err != nil {
		return time.Time{}, false
	}
	day, _ := strconv.Atoi(m[2])
	t := time.Date(local.Year(), month.Month(), day, hour, minute, 0, 0, loc)
	// "Jan 2" lido em dezembro é o ano que vem; uma semana de folga para relógios adiantados.
	if t.Before(now.AddDate(0, 0, -7)) {
		t = t.AddDate(1, 0, 0)
	}
	return t, true
}
