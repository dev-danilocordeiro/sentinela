package linear

import (
	"testing"
	"time"
)

func TestPickMarked(t *testing.T) {
	cut := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	before, after := cut.Add(-time.Hour), cut.Add(time.Hour)
	in := Install{ID: "a1b2c3d4", LegacyUntil: cut}
	q := MarkerQuery{Base: "sentinela-fingerprint: abc", TeamID: "t1", ProjectID: "p1"}
	mk := func(id, desc string, f func(*MarkedIssue)) MarkedIssue {
		m := MarkedIssue{Description: desc, TeamID: "t1", ProjectID: "p1", CreatedAt: before}
		m.Identifier = id
		if f != nil {
			f(&m)
		}
		return m
	}
	mine := "`sentinela-fingerprint: abc@a1b2c3d4`"
	legacy := "`sentinela-fingerprint: abc`"
	cases := []struct {
		name  string
		in    Install
		cands []MarkedIssue
		want  string // identificador escolhido; vazio = nenhum
	}{
		{"desta instalação", in, []MarkedIssue{mk("A", mine, nil)}, "A"},
		{"marcador antigo antes do corte", in, []MarkedIssue{mk("A", legacy, nil)}, "A"},
		{"marcador antigo depois do corte", in, []MarkedIssue{mk("A", legacy, func(m *MarkedIssue) { m.CreatedAt = after })}, ""},
		{"marcador antigo sem corte (instalação nova)", Install{ID: "a1b2c3d4"}, []MarkedIssue{mk("A", legacy, nil)}, ""},
		{"outra instalação", in, []MarkedIssue{mk("A", "`sentinela-fingerprint: abc@deadbeef`", nil)}, ""},
		{"arquivada", in, []MarkedIssue{mk("A", mine, func(m *MarkedIssue) { m.Archived = true })}, ""},
		{"na lixeira", in, []MarkedIssue{mk("A", mine, func(m *MarkedIssue) { m.Trashed = true })}, ""},
		{"outro time", in, []MarkedIssue{mk("A", mine, func(m *MarkedIssue) { m.TeamID = "t2" })}, ""},
		{"outro projeto", in, []MarkedIssue{mk("A", mine, func(m *MarkedIssue) { m.ProjectID = "p2" })}, ""},
		{"sem projeto quando o roteamento dá um", in, []MarkedIssue{mk("A", mine, func(m *MarkedIssue) { m.ProjectID = "" })}, ""},
		{"fingerprint mais longo não casa", in, []MarkedIssue{mk("A", "`sentinela-fingerprint: abcd@a1b2c3d4`", nil)}, ""},
		{"prefere a desta instalação à antiga", in, []MarkedIssue{mk("A", legacy, nil), mk("B", mine, nil)}, "B"},
		{"antiga quando a nova é de outro projeto", in, []MarkedIssue{mk("A", legacy, nil), mk("B", mine, func(m *MarkedIssue) { m.ProjectID = "p2" })}, "A"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := pickMarked(c.cands, q, c.in)
			id := ""
			if got != nil {
				id = got.Identifier
			}
			if id != c.want {
				t.Errorf("escolhida %q, quer %q", id, c.want)
			}
		})
	}
}

func TestMatchMarkerRelated(t *testing.T) {
	base := relatedMarker("abc", "fin")
	for desc, want := range map[string]markerMatch{
		"`sentinela-related: abc/fin@a1b2c3d4`":        markerMine,
		"`sentinela-related: abc/fin`":                 markerLegacy,
		"`sentinela-related: abc/financeiro@a1b2c3d4`": markerNone,
		"`sentinela-related: abc/fin@00000000`":        markerForeign,
	} {
		if got := matchMarker(desc, base, "a1b2c3d4"); got != want {
			t.Errorf("%s: %v, quer %v", desc, got, want)
		}
	}
}
