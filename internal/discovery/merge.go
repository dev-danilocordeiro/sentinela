package discovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// MergeChoice é quem absorve quem entre dois achados equivalentes.
type MergeChoice struct {
	Absorber, Absorbed string // fingerprints
	Why                string
}

type mergeSide struct {
	fp        string
	findingID int64 // 0 = candidato ainda não gravado (o mais novo de todos)
	issueSeq  int64 // 0 = sem issue publicada
	issue     string
}

func (s mergeSide) label() string {
	if s.issue != "" {
		return s.issue
	}
	return s.fp
}

// MergeDirection torna determinística a mesclagem entre dois achados equivalentes (ADR 0020):
// quem já foi publicado absorve quem não foi; se os dois foram publicados, a issue mais antiga
// absorve a mais nova; se nenhum foi, o achado mais antigo absorve o mais novo. Um candidato que
// ainda não foi gravado é sempre o mais novo. Assim, dois rejulgamentos que se mesclam um no
// outro (EX-6 e EX-8) dão a mesma resposta.
func (r *Runner) MergeDirection(ctx context.Context, a, b string) (MergeChoice, error) {
	sa, err := r.mergeSide(ctx, a)
	if err != nil {
		return MergeChoice{}, err
	}
	sb, err := r.mergeSide(ctx, b)
	if err != nil {
		return MergeChoice{}, err
	}
	first, second, why := sa, sb, ""
	switch {
	case sa.issueSeq > 0 && sb.issueSeq == 0:
		why = fmt.Sprintf("%s já foi publicado e %s não", sa.label(), sb.label())
	case sb.issueSeq > 0 && sa.issueSeq == 0:
		first, second, why = sb, sa, fmt.Sprintf("%s já foi publicado e %s não", sb.label(), sa.label())
	case sa.issueSeq > 0 && sb.issueSeq > 0:
		if sb.issueSeq < sa.issueSeq {
			first, second = sb, sa
		}
		why = fmt.Sprintf("os dois foram publicados; %s é a issue mais antiga", first.label())
	default:
		older := func(x, y mergeSide) bool { return x.findingID != 0 && (y.findingID == 0 || x.findingID < y.findingID) }
		if older(sb, sa) {
			first, second = sb, sa
		}
		why = fmt.Sprintf("nenhum foi publicado; %s é o achado mais antigo", first.label())
	}
	return MergeChoice{Absorber: first.fp, Absorbed: second.fp, Why: why}, nil
}

func (r *Runner) mergeSide(ctx context.Context, fp string) (mergeSide, error) {
	row, err := r.Store.Q.FindingPublication(ctx, fp)
	if errors.Is(err, pgx.ErrNoRows) {
		return mergeSide{fp: fp}, nil
	}
	if err != nil {
		return mergeSide{}, err
	}
	return mergeSide{fp: fp, findingID: row.ID, issueSeq: row.IssueSeq, issue: row.Identifier}, nil
}
