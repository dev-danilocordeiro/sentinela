package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Candidatos por localização (ADR 0025): um problema recusado volta com outras palavras, mas
// aponta para o mesmo código (EX-9: as quatro versões do problema citavam o mesmo service e o
// mesmo docker-compose.yml). O código acha, no mesmo módulo, os achados existentes que
// compartilham arquivo, símbolo ou linhas próximas e os mostra ao Judge, que decide. O código
// não descarta nem mescla por localização: mesmo lugar não é mesmo problema (ADR 0016).

// Pontos de um par de evidências (achado novo × candidato); vale o maior tipo.
const (
	scoreSymbolSameFile = 4
	scoreNearbyLines    = 3
	scoreSymbolModule   = 2
	scoreSameFile       = 1
	// minSnippetRelocate é o tamanho mínimo do trecho para relocar a linha por ele: trechos
	// curtos ("}", "return err") aparecem em todo lugar.
	minSnippetRelocate = 8
)

// genericSymbols não contam como mesmo símbolo entre arquivos diferentes.
var genericSymbols = map[string]bool{
	"main": true, "init": true, "run": true, "Run": true, "get": true, "set": true, "handle": true,
	"execute": true, "toString": true, "equals": true, "hashCode": true, "String": true, "Error": true,
}

// codeLoc é onde uma evidência aponta. Line 0 é o arquivo inteiro.
type codeLoc struct {
	File   string
	Line   int
	Symbol string
}

// sourceIndex lê os arquivos do checkout, uma vez cada, e acha o símbolo de cada linha. Sem
// checkout (dir vazio ou arquivo ausente), vale só arquivo e linha.
type sourceIndex struct {
	dir   string
	files map[string][]string
}

func newSourceIndex(dir string) *sourceIndex {
	return &sourceIndex{dir: dir, files: map[string][]string{}}
}

func (s *sourceIndex) lines(file string) []string {
	if s.dir == "" || file == "" || file == "." || strings.HasPrefix(file, "../") || file == ".." {
		return nil
	}
	if l, ok := s.files[file]; ok {
		return l
	}
	data, err := os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(file))) //nolint:gosec // caminho relativo ao checkout, sem ".."
	var l []string
	if err == nil {
		l = strings.Split(string(data), "\n")
	}
	s.files[file] = l
	return l
}

// locate resolve a evidência no checkout. Linha 0 ou 1 é o arquivo inteiro (o Analyst usa a
// linha 1 para evidência que fala do arquivo, não de um trecho).
func (s *sourceIndex) locate(e signals.Evidence) codeLoc {
	l := codeLoc{File: path.Clean(strings.TrimPrefix(e.File, "/")), Line: e.Line}
	if l.Line <= 1 {
		l.Line = 0
		return l
	}
	lines := s.lines(l.File)
	if lines == nil {
		return l
	}
	l.Line = relocate(lines, l.Line, e.Snippet)
	l.Symbol = enclosingSymbol(lines, l.Line)
	return l
}

// relocate devolve a linha do trecho mais próxima da original, quando o arquivo mudou desde a
// evidência e o trecho ainda está nele; senão, a original.
func relocate(lines []string, line int, snippet string) int {
	want := ""
	for _, s := range strings.Split(snippet, "\n") {
		if t := strings.TrimSpace(s); t != "" {
			want = t
			break
		}
	}
	if len(want) < minSnippetRelocate {
		return line
	}
	if line <= len(lines) && strings.Contains(lines[line-1], want) {
		return line
	}
	best := line
	for i, l := range lines {
		if strings.Contains(l, want) && (best == line || abs(i+1-line) < abs(best-line)) {
			best = i + 1
		}
	}
	return best
}

// enclosingSymbol acha a função ou o método que envolve a linha: a declaração mais próxima acima
// com indentação menor que a da linha (ou a própria linha, se for uma declaração).
func enclosingSymbol(lines []string, line int) string {
	idx := line - 1
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	if name := declName(lines[idx]); name != "" {
		return name
	}
	limit := indent(lines[idx])
	if strings.TrimSpace(lines[idx]) == "" {
		limit = 1 << 30
	}
	for i := idx - 1; i >= 0 && limit > 0; i-- {
		t := lines[i]
		if strings.TrimSpace(t) == "" {
			continue
		}
		ind := indent(t)
		if ind >= limit {
			continue
		}
		if name := declName(t); name != "" {
			return name
		}
		limit = ind
	}
	return ""
}

func indent(s string) int {
	n := 0
	for _, r := range s {
		switch r {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

var (
	declPatterns = []*regexp.Regexp{
		regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*[\[(]`),                                       // Go
		regexp.MustCompile(`^(?:async\s+)?def\s+([A-Za-z_]\w*)\s*\(`),                                               // Python
		regexp.MustCompile(`^(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)\s*[<(]`), // JS/TS
		regexp.MustCompile(`^(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*(?::[^=]+)?=>`),
		regexp.MustCompile(`^(?:(?:public|private|protected|internal|open|override|suspend|inline|static|final|abstract)\s+)*fun\s+(?:<[^>]*>\s*)?(?:[\w.]+\.)?([A-Za-z_]\w*)\s*\(`), // Kotlin
		// Kotlin: val ou var com lambda (o tipo, se houver, é uma função), construtor secundário e
		// construtor primário (class X(...)).
		regexp.MustCompile(`^(?:(?:public|private|protected|internal|override|open)\s+)*va[lr]\s+([A-Za-z_]\w*)\s*(?::[^=]*->[^=]*)?=\s*\{`),
		regexp.MustCompile(`^(?:(?:public|private|protected|internal)\s+)*(constructor)\s*\(`),
		regexp.MustCompile(`^(?:(?:public|private|protected|internal|open|abstract|sealed|data|inline|value|enum|annotation|final|inner)\s+)*class\s+([A-Za-z_]\w*)\s*(?:<[^>]*>\s*)?(?:(?:public|private|protected|internal)?\s*constructor\s*)?\(`),
		// TS/JS: propriedade de classe com arrow (handle = async (req) => {).
		regexp.MustCompile(`^(?:(?:public|private|protected|static|readonly|override)\s+)*([A-Za-z_$][\w$]*)\s*(?::[^=]+)?=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*(?::[^=]+)?=>`),
		// Método de classe TS/JS sem tipo nem modificador: precisa abrir o corpo na mesma linha.
		regexp.MustCompile(`^(?:(?:public|private|protected|static|readonly|async|override)\s+)*([A-Za-z_$][\w$]*)\s*\([^;]*\)\s*(?::\s*[^{;]+)?\{\s*$`),
	}
	// Java, C# e afins: exige modificador ou tipo antes do nome, para não confundir com a
	// chamada que continua na linha seguinte.
	methodDecl = regexp.MustCompile(`^((?:(?:public|private|protected|internal|static|final|abstract|synchronized|native|override|virtual|async|default|sealed|partial|extern|unsafe)\s+)*)(?:<[^>]+>\s+)?([\w<>\[\],.?]+\s+)?([A-Za-z_$][\w$]*)\s*\([^;]*$`)
	notDecl    = map[string]bool{
		"if": true, "for": true, "while": true, "switch": true, "catch": true, "return": true, "new": true,
		"else": true, "do": true, "try": true, "synchronized": true, "foreach": true, "using": true,
		"lock": true, "throw": true, "await": true, "typeof": true, "sizeof": true, "super": true, "this": true,
		"case": true, "yield": true, "defer": true, "go": true, "select": true, "with": true, "elif": true,
	}
)

// declName devolve o nome declarado na linha, se ela declara função ou método.
func declName(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "#") {
		return ""
	}
	first := t
	if i := strings.IndexAny(t, " \t("); i >= 0 {
		first = t[:i]
	}
	if notDecl[first] {
		return ""
	}
	for _, re := range declPatterns {
		if m := re.FindStringSubmatch(t); m != nil && !notDecl[m[1]] {
			return m[1]
		}
	}
	if m := methodDecl.FindStringSubmatch(t); m != nil && (m[1] != "" || m[2] != "") && !notDecl[m[3]] {
		if typ := strings.TrimSpace(m[2]); notDecl[typ] {
			return ""
		}
		return m[3]
	}
	return ""
}

// locCandidate é um achado existente que pode ser o mesmo problema de um novo.
type locCandidate struct {
	sim      judge.Similar
	module   string
	evidence []signals.Evidence
	rank     int // ordem de desempate pelo estado
	locs     []codeLoc
	located  bool
}

// locationHit é um candidato escolhido, gravado em run_items para auditoria.
type locationHit struct {
	Fingerprint string   `json:"fingerprint"`
	State       string   `json:"state"`
	Score       int      `json:"score"`
	Reasons     []string `json:"reasons"`
}

// locationPool é o universo de candidatos de um repo numa rodada.
type locationPool struct {
	idx    *sourceIndex
	cands  []*locCandidate
	merges mergeGraph
	window int
	max    int
}

// Desempate: o que humanos ou o Judge já recusaram vem antes do que está aberto.
var stateRank = map[string]int{"rejected": 0, "suppressed": 1, "discarded": 2, "merged": 3, "open": 4, "executing": 4}

// locationPool monta os candidatos do repo: achados abertos, em execução, recusados, suprimidos
// e duplicatas humanas (findings), e descartes e mesclas do Judge (run_items).
func (r *Runner) locationPool(ctx context.Context, repoName, dir string, prior priorDecisions, merges mergeGraph) (*locationPool, error) {
	maxN := 0
	if r.Cfg.Discovery.LocationCandidates != nil {
		maxN = *r.Cfg.Discovery.LocationCandidates
	}
	if maxN <= 0 {
		return nil, nil
	}
	rows, err := r.Store.Q.LocationCandidates(ctx, repoName)
	if err != nil {
		return nil, err
	}
	return buildLocationPool(newSourceIndex(dir), r.Cfg.Discovery.LocationLineWindow, maxN, rows, prior, merges)
}

func buildLocationPool(idx *sourceIndex, window, maxN int, rows []db.LocationCandidatesRow, prior priorDecisions, merges mergeGraph) (*locationPool, error) {
	p := &locationPool{idx: idx, window: window, max: maxN, merges: merges}
	seen := map[string]bool{}
	for _, row := range rows {
		var ev []signals.Evidence
		if len(row.Evidence) > 0 {
			if err := json.Unmarshal(row.Evidence, &ev); err != nil {
				return nil, fmt.Errorf("achado %s: evidência inválida: %w", row.Fingerprint, err)
			}
		}
		state := row.State
		switch {
		case row.Merged:
			state = "merged"
		case row.Suppressed && state == "rejected":
			state = "suppressed"
		}
		p.cands = append(p.cands, &locCandidate{
			sim: judge.Similar{
				Fingerprint: row.Fingerprint, Title: row.Title, Category: row.Category, State: state,
				Issue: row.Identifier, AcceptanceCriteria: row.AcceptanceCriteria, Evidence: evidenceRefs(ev),
			},
			module: row.Module, evidence: ev, rank: stateRank[state],
		})
		seen[row.Fingerprint] = true
	}
	for _, d := range prior.list {
		if seen[d.Fingerprint] {
			continue
		}
		state := "discarded"
		if d.Decision == judge.Merge {
			state = "merged"
		}
		p.cands = append(p.cands, &locCandidate{
			sim:    judge.Similar{Fingerprint: d.Fingerprint, Title: d.Title, Category: d.Category, State: state, Evidence: evidenceRefs(d.Evidence)},
			module: d.Module, evidence: d.Evidence, rank: stateRank[state],
		})
	}
	return p, nil
}

func evidenceRefs(ev []signals.Evidence) []string {
	out := make([]string, 0, len(ev))
	for _, e := range ev {
		out = append(out, fmt.Sprintf("%s:%d", e.File, e.Line))
	}
	return out
}

// attach acrescenta aos similares do Judge os candidatos por localização do achado: os que já
// estão na lista ganham Location; os outros entram com similaridade 0.
func (p *locationPool) attach(f signals.Finding, similar []judge.Similar) ([]judge.Similar, []locationHit) {
	if p == nil || p.max <= 0 || len(f.Evidence) == 0 {
		return similar, nil
	}
	mine := make([]codeLoc, 0, len(f.Evidence))
	for _, e := range f.Evidence {
		mine = append(mine, p.idx.locate(e))
	}
	type scored struct {
		c       *locCandidate
		score   int
		reasons []string
	}
	var all []scored
	for _, c := range p.cands {
		// O que foi mesclado no próprio achado é parte dele, não outro achado (veja partOf).
		if c.module != f.Module || c.sim.Fingerprint == f.Fingerprint || p.merges.partOf(c.sim.Fingerprint, f.Fingerprint) {
			continue
		}
		if !c.located {
			for _, e := range c.evidence {
				c.locs = append(c.locs, p.idx.locate(e))
			}
			c.located = true
		}
		if score, reasons := matchLocations(mine, c.locs, p.window); score > 0 {
			all = append(all, scored{c, score, reasons})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		if all[i].c.rank != all[j].c.rank {
			return all[i].c.rank < all[j].c.rank
		}
		return all[i].c.sim.Fingerprint < all[j].c.sim.Fingerprint
	})
	if len(all) > p.max {
		all = all[:p.max]
	}
	pos := map[string]int{}
	for i, s := range similar {
		pos[s.Fingerprint] = i
	}
	var hits []locationHit
	for _, s := range all {
		hits = append(hits, locationHit{Fingerprint: s.c.sim.Fingerprint, State: s.c.sim.State, Score: s.score, Reasons: s.reasons})
		if i, ok := pos[s.c.sim.Fingerprint]; ok {
			similar[i].Location = s.reasons
			continue
		}
		sim := s.c.sim
		sim.Location = s.reasons
		similar = append(similar, sim)
	}
	return similar, hits
}

// matchLocations soma, por evidência do achado novo, o melhor par com as do candidato.
func matchLocations(mine, theirs []codeLoc, window int) (int, []string) {
	total := 0
	var reasons []string
	seen := map[string]bool{}
	for _, n := range mine {
		best, why := 0, ""
		for _, x := range theirs {
			if s, w := pairScore(n, x, window); s > best {
				best, why = s, w
			}
		}
		total += best
		if best > 0 && !seen[why] {
			seen[why] = true
			reasons = append(reasons, why)
		}
	}
	return total, reasons
}

func pairScore(n, x codeLoc, window int) (int, string) {
	sameFile := n.File == x.File
	switch {
	case sameFile && n.Symbol != "" && n.Symbol == x.Symbol:
		return scoreSymbolSameFile, fmt.Sprintf("mesmo símbolo %s em %s", n.Symbol, n.File)
	case sameFile && n.Line > 0 && x.Line > 0 && abs(n.Line-x.Line) <= window:
		return scoreNearbyLines, fmt.Sprintf("linhas próximas em %s (%d ≈ %d)", n.File, n.Line, x.Line)
	case !sameFile && n.Symbol != "" && n.Symbol == x.Symbol && !genericSymbols[n.Symbol]:
		return scoreSymbolModule, fmt.Sprintf("mesmo símbolo %s (%s ≈ %s)", n.Symbol, n.File, x.File)
	case sameFile:
		return scoreSameFile, "mesmo arquivo " + n.File
	}
	return 0, ""
}

// checkoutDir é o clone do repo em discovery.workdir, se existir (para o rejudge, que não
// sincroniza o repo).
func (r *Runner) checkoutDir(repoName string) string {
	if r.Git == nil || r.Git.Root == "" {
		return ""
	}
	dir := filepath.Join(r.Git.Root, filepath.FromSlash(repoName))
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ""
	}
	return dir
}
