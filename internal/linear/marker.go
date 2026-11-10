package linear

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Marcadores no corpo das issues: tornam a publicação idempotente. Se o processo cair depois de
// criar a issue e antes de gravar no Postgres, a próxima rodada reencontra a issue pelo marcador
// em vez de duplicá-la.
//
// O marcador leva o id da instalação (tabela installation, migration 00018):
//
//	sentinela-fingerprint: <fp>@<instalação>
//	sentinela-related: <fp>/<time>@<instalação>
//
// Sem o id, uma instalação reaproveitava a issue de outra com o mesmo fingerprint (um achado
// ficou ligado a uma issue arquivada de uma instalação de avaliação). O marcador antigo, sem
// "@<instalação>", só é reconhecido em issue criada até installation.legacy_markers_until.
func primaryMarker(fp string) string       { return "sentinela-fingerprint: " + fp }
func relatedMarker(fp, team string) string { return "sentinela-related: " + fp + "/" + team }

// installMarker é o marcador gravado nas issues novas: o marcador base com o id da instalação.
func installMarker(base, install string) string { return base + "@" + install }

// Install é a identidade desta instalação.
type Install struct {
	ID string
	// LegacyUntil: issues criadas até aqui ainda valem com o marcador antigo; zero = nenhuma.
	LegacyUntil time.Time
}

// LoadInstall lê a identidade da instalação no banco.
func LoadInstall(ctx context.Context, q *db.Queries) (Install, error) {
	row, err := q.Installation(ctx)
	if err != nil {
		return Install{}, fmt.Errorf("identidade da instalação (migration 00018): %w", err)
	}
	in := Install{ID: row.ID}
	if row.LegacyMarkersUntil.Valid {
		in.LegacyUntil = row.LegacyMarkersUntil.Time
	}
	return in, nil
}

// MarkedIssue é uma issue candidata da busca por marcador, com o que é preciso para conferir se
// ela é desta instalação e do lugar certo.
type MarkedIssue struct {
	Issue
	Description string
	TeamID      string
	ProjectID   string // vazio: sem projeto
	CreatedAt   time.Time
	Archived    bool
	Trashed     bool
}

// MarkerQuery é o que a issue reaproveitada precisa ter: o marcador base (sem instalação), o
// time e o projeto que o roteamento daria agora.
type MarkerQuery struct {
	Base      string
	TeamID    string
	ProjectID string // vazio: a issue não pode ter projeto
}

// markerMatch diz como o marcador base aparece na descrição: com o id desta instalação, sem id
// (formato antigo) ou só com o id de outra instalação.
type markerMatch int

const (
	markerNone markerMatch = iota
	markerForeign
	markerLegacy
	markerMine
)

func matchMarker(desc, base, install string) markerMatch {
	best := markerNone
	for rest := desc; ; {
		i := strings.Index(rest, base)
		if i < 0 {
			return best
		}
		rest = rest[i+len(base):]
		var m markerMatch
		switch {
		case strings.HasPrefix(rest, "@"):
			id := rest[1:]
			if j := strings.IndexFunc(id, func(r rune) bool { return !isHex(r) }); j >= 0 {
				id = id[:j]
			}
			m = markerForeign
			if id == install {
				m = markerMine
			}
		case rest == "" || !isMarkerChar(rune(rest[0])):
			m = markerLegacy
		default:
			// O base é prefixo de outro marcador (ex.: time "fin" dentro de "financeiro").
			m = markerNone
		}
		best = max(best, m)
	}
}

func isHex(r rune) bool { return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' }

func isMarkerChar(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_' || r == '/' || r == '.'
}

// pickMarked escolhe a issue a reaproveitar entre os candidatos, ou nenhuma. Descarta (e diz por
// quê em skipped) issue arquivada ou na lixeira, de outro time ou projeto, de outra instalação e
// a do marcador antigo criada depois de LegacyUntil. Prefere o marcador desta instalação.
func pickMarked(cands []MarkedIssue, q MarkerQuery, in Install) (found *Issue, skipped []string) {
	var legacy *Issue
	for i := range cands {
		c := &cands[i]
		why := ""
		switch m := matchMarker(c.Description, q.Base, in.ID); {
		case m == markerNone:
			why = "sem o marcador"
		case c.Archived || c.Trashed:
			why = "arquivada ou na lixeira"
		case c.TeamID != "" && c.TeamID != q.TeamID:
			why = "de outro time"
		case c.ProjectID != q.ProjectID:
			why = "de outro projeto"
		case m == markerForeign:
			why = "de outra instalação"
		case m == markerLegacy && (in.LegacyUntil.IsZero() || c.CreatedAt.IsZero() || c.CreatedAt.After(in.LegacyUntil)):
			why = "marcador antigo em issue que não é desta instalação"
		case m == markerMine:
			return &c.Issue, skipped
		default:
			if legacy == nil {
				legacy = &c.Issue
			}
			continue
		}
		skipped = append(skipped, c.Identifier+": "+why)
	}
	return legacy, skipped
}
