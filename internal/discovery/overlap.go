package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

// Sobreposição parcial (ADR 0016): um achado novo que repete parte de um achado aberto (visto
// nas EX-5 e EX-7, em que o critério 2 da EX-7 era a EX-5 inteira). O código detecta a
// sobreposição de forma léxica e a mostra ao Judge, que reduz o escopo do novo; depois do Judge,
// o código remove os critérios que ainda repetem um achado aberto e, se não sobrar nenhum,
// trata o achado como duplicado (merge).

const (
	// overlapLineWindow é a distância máxima, em linhas, entre evidências no mesmo arquivo para
	// contarem como o mesmo trecho de código.
	overlapLineWindow = 3
	// Um critério está coberto quando ao menos minSharedStems radicais dele aparecem no achado
	// existente (título e critérios) e eles são ao menos minSharedShare dos radicais do critério.
	minSharedStems = 3
	minSharedShare = 0.5
	// Para o código remover um critério, a coincidência precisa ser forte. Calibrado nas
	// propostas reais do app-java-angular (rodadas 3 a 10): o critério 2 da EX-7 tem 8 de 11 radicais
	// na EX-5; as 12 coincidências fortes eram variantes das EX-5 a EX-9, e as fracas que não
	// eram o mesmo problema ("excluir uma URL remove seus eventos de clique") ficaram abaixo de 0,7.
	strongSharedStems = 3
	strongSharedShare = 0.7
	shortCriterion    = 5
	stemRunes         = 5
)

// openFinding é um achado aberto ou em execução do repo.
type openFinding struct {
	Fingerprint, Title, Category, State, Issue string
	Criteria                                   []string
	Evidence                                   []signals.Evidence
	stems                                      map[string]bool
}

func newOpenFinding(fp, title, category, state, issue string, criteria []string, evidence []signals.Evidence) openFinding {
	o := openFinding{Fingerprint: fp, Title: title, Category: category, State: state, Issue: issue, Criteria: criteria, Evidence: evidence, stems: map[string]bool{}}
	for _, s := range stems(title + " " + strings.Join(criteria, " ")) {
		o.stems[s] = true
	}
	return o
}

func (r *Runner) openFindings(ctx context.Context, repoName string) (map[string]openFinding, error) {
	rows, err := r.Store.Q.OpenFindingsForOverlap(ctx, repoName)
	if err != nil {
		return nil, err
	}
	out := make(map[string]openFinding, len(rows))
	for _, row := range rows {
		var ev []signals.Evidence
		if len(row.Evidence) > 0 {
			if err := json.Unmarshal(row.Evidence, &ev); err != nil {
				return nil, fmt.Errorf("achado %d: evidência inválida: %w", row.ID, err)
			}
		}
		out[row.Fingerprint] = newOpenFinding(row.Fingerprint, row.Title, row.Category, row.State, row.Identifier, row.AcceptanceCriteria, ev)
	}
	return out, nil
}

// covers diz se o critério parece já coberto pelo achado aberto (pista para o Judge).
func (o openFinding) covers(criterion string) bool {
	return o.coversWith(criterion, minSharedStems, minSharedShare)
}

// strongCovers é a coincidência forte, que o código aplica mesmo sem o Judge. Critério curto
// (menos de shortCriterion radicais) precisa ter todos os radicais no aberto: "Delete com falha
// devolve erro" não é "Save com falha devolve erro", embora 3 de 4 radicais coincidam.
func (o openFinding) strongCovers(criterion string) bool {
	if n := len(stems(criterion)); n > 0 && n < shortCriterion {
		return o.coversWith(criterion, n, 1)
	}
	return o.coversWith(criterion, strongSharedStems, strongSharedShare)
}

func (o openFinding) coversWith(criterion string, minShared int, minShare float64) bool {
	st := stems(criterion)
	if len(st) == 0 {
		return false
	}
	shared := 0
	for _, s := range st {
		if o.stems[s] {
			shared++
		}
	}
	return shared >= minShared && float64(shared) >= minShare*float64(len(st))
}

// overlapWith lista o que o achado novo tem em comum com o aberto.
func overlapWith(f signals.Finding, o openFinding) judge.Overlap {
	var ov judge.Overlap
	for _, c := range f.AcceptanceCriteria {
		if o.covers(c) {
			ov.Criteria = append(ov.Criteria, c)
		}
	}
	for _, e := range f.Evidence {
		for _, x := range o.Evidence {
			if e.File == x.File && abs(e.Line-x.Line) <= overlapLineWindow {
				ov.Evidence = append(ov.Evidence, fmt.Sprintf("%s:%d ≈ %s:%d", e.File, e.Line, x.File, x.Line))
				break
			}
		}
	}
	return ov
}

// overlaps compara o achado novo com todos os abertos e devolve os sobrepostos.
func overlaps(f signals.Finding, open map[string]openFinding) map[string]judge.Overlap {
	out := map[string]judge.Overlap{}
	for fp, o := range open {
		if fp == f.Fingerprint {
			continue
		}
		if ov := overlapWith(f, o); !ov.Empty() {
			out[fp] = ov
		}
	}
	return out
}

// similarFrom monta o similar do Judge com critérios, evidências e sobreposição do aberto.
func similarFrom(o openFinding, similarity float64, ov judge.Overlap) judge.Similar {
	s := judge.Similar{
		Fingerprint: o.Fingerprint, Title: o.Title, Category: o.Category, State: o.State, Similarity: similarity,
		Issue: o.Issue, AcceptanceCriteria: o.Criteria, Overlap: ov,
	}
	for _, e := range o.Evidence {
		s.Evidence = append(s.Evidence, fmt.Sprintf("%s:%d", e.File, e.Line))
	}
	return s
}

// scopeResult é o que a trava de sobreposição fez com um achado aprovado.
type scopeResult struct {
	Narrowed  bool     `json:"narrowed"`             // o Judge reduziu o escopo
	RelatedTo []string `json:"related_to"`           // achados abertos a linkar como relacionados
	Removed   []string `json:"removed,omitempty"`    // critérios removidos pelo código
	MergeWith string   `json:"merge_with,omitempty"` // não sobrou nada: duplicado deste
}

// applyScope aplica a redução de escopo do Judge e a trava em código. Depois da redução, um
// critério só sai do achado se a coincidência com um aberto (apontado pelo Judge ou detectado) é
// forte; a fraca é só pista, para não desfazer o julgamento com uma regra léxica. O aberto de
// cada critério removido entra em related_to. Se não sobrar critério, devolve MergeWith.
func applyScope(f *signals.Finding, v judge.Verdict, detected map[string]judge.Overlap, open map[string]openFinding) scopeResult {
	var res scopeResult
	if v.Narrowed.Applied() {
		res.Narrowed = true
		f.AcceptanceCriteria = v.Narrowed.AcceptanceCriteria
		if t := strings.TrimSpace(v.Narrowed.Title); t != "" {
			f.Title = t
		}
		if c := strings.TrimSpace(v.Narrowed.Context); c != "" {
			f.Context = c
		}
		if p := strings.TrimSpace(v.Narrowed.ProposedFix); p != "" {
			f.ProposedFix = p
		}
	}
	related := map[string]bool{}
	for _, fp := range v.RelatedTo {
		if _, ok := open[fp]; ok && fp != f.Fingerprint {
			related[fp] = true
		}
	}
	candidates := sortedKeys(related)
	for _, fp := range sortedKeys(detected) {
		if !related[fp] {
			candidates = append(candidates, fp)
		}
	}
	var kept []string
	for _, c := range f.AcceptanceCriteria {
		by := ""
		for _, fp := range candidates {
			if open[fp].strongCovers(c) {
				by = fp
				break
			}
		}
		if by == "" {
			kept = append(kept, c)
			continue
		}
		res.Removed = append(res.Removed, c)
		related[by] = true
	}
	f.AcceptanceCriteria = kept
	res.RelatedTo = sortedKeys(related)
	if len(kept) == 0 && len(res.Removed) > 0 {
		res.MergeWith = res.RelatedTo[0]
	}
	return res
}

var stopwords = map[string]bool{
	"que": true, "com": true, "para": true, "por": true, "uma": true, "não": true, "nao": true,
	"dos": true, "das": true, "após": true, "apos": true, "teste": true, "testes": true, "deve": true,
	"ser": true, "sem": true, "quando": true, "the": true, "and": true, "está": true, "esta": true,
	"são": true, "nos": true, "nas": true, "pelo": true, "pela": true, "cada": true, "mesmo": true,
}

// stems quebra o texto em palavras (inclusive camelCase de identificadores), descarta palavras
// curtas e vazias, e corta cada uma nas primeiras letras: um radical grosseiro que aproxima
// "configurado" de "configurável" e "clickEvents" de "ClickEvent".
func stems(text string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}
	prev := rune(0)
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if unicode.IsUpper(r) && unicode.IsLower(prev) {
				flush()
			}
			cur = append(cur, unicode.ToLower(r))
		default:
			flush()
		}
		prev = r
	}
	flush()
	seen := map[string]bool{}
	var out []string
	for _, w := range words {
		if len([]rune(w)) < 3 || stopwords[w] {
			continue
		}
		rs := []rune(w)
		if len(rs) > stemRunes {
			rs = rs[:stemRunes]
		}
		s := string(rs)
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
