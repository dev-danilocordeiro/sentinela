package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pgvector/pgvector-go"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/signals/code"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// RefreshVulnFinding regenera, com o código atual, o texto de um achado de vulnerabilidade
// (ADR 0033) ainda sem issue, no mesmo commit da rodada que o gravou. Roda só o govulncheck,
// sem modelo. O achado regenerado precisa ter o mesmo fingerprint (mesmas versões alvo); só o
// conteúdo muda, nunca estado, roteamento, severidade nem o veredito do Judge (ADR 0036).
func (r *Runner) RefreshVulnFinding(ctx context.Context, id int64) (before db.Finding, after signals.Finding, err error) {
	before, err = r.Store.Q.GetFinding(ctx, id)
	if err != nil {
		return before, after, fmt.Errorf("achado %d: %w", id, err)
	}
	if before.Category != "security" || !strings.HasPrefix(before.Title, "Atualiza ") {
		return before, after, fmt.Errorf("achado %d não é de vulnerabilidade (govulncheck ou osv-scanner): %s", id, before.Title)
	}
	if before.State != "open" || before.MergedInto.Valid {
		return before, after, fmt.Errorf("achado %d está %s; só um achado aberto é regenerado", id, before.State)
	}
	run, err := r.Store.Q.GetRun(ctx, before.RunID.Int64)
	if err != nil {
		return before, after, fmt.Errorf("rodada %d: %w", before.RunID.Int64, err)
	}
	dir := filepath.Join(r.Git.Root, filepath.FromSlash(before.Repo))
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output() //nolint:gosec // dir do workdir
	if err != nil {
		return before, after, fmt.Errorf("clone de %s em %s: %w", before.Repo, dir, err)
	}
	if head := strings.TrimSpace(string(out)); head != run.HeadSha.String {
		return before, after, fmt.Errorf("o clone está em %s e a rodada %d em %s; regenere só no commit da rodada", head, run.ID, run.HeadSha.String)
	}
	vr := scan.Runner{}
	if r.Scanners != nil {
		vr = *r.Scanners
	}
	// Achado do osv-scanner (ADR 0058) se regenera com ele; os de Go, com o govulncheck.
	tool := scan.Scanner(scan.Govulncheck{})
	if strings.Contains(before.ProposedFix, "osv-scanner") {
		tool = scan.OSVScanner{}
	}
	vr.Scanners = []scan.Scanner{tool}
	sigs, results := vr.Run(ctx, dir, map[string]bool{"go": true})
	for _, res := range results {
		if res.Status != "ok" {
			return before, after, fmt.Errorf("%s %s: %s", tool.Name(), res.Status, res.Detail)
		}
	}
	co, err := codeownersFrom(dir)
	if err != nil {
		return before, after, err
	}
	src := &code.Source{
		Cfg: r.Cfg, Checkout: &repo.Checkout{Repo: before.Repo, Dir: dir, HeadSHA: run.HeadSha.String},
		Resolver: repo.NewResolver(r.Cfg, repo.StaticCodeowners{before.Repo: co}), ScanSignals: sigs,
	}
	found := false
	for _, f := range src.VulnFindings(ctx) {
		if f.Fingerprint == before.Fingerprint {
			after, found = f, true
		}
	}
	if !found {
		return before, after, fmt.Errorf("o %s de agora não gera o mesmo fingerprint (versões alvo mudaram); a próxima rodada grava um achado novo", tool.Name())
	}
	evidence, err := json.Marshal(after.Evidence)
	if err != nil {
		return before, after, err
	}
	sigsJSON, err := json.Marshal(agents.Slice(after.Signals))
	if err != nil {
		return before, after, err
	}
	n, err := r.Store.Q.RefreshFindingText(ctx, db.RefreshFindingTextParams{
		ID: id, Module: after.Module, Title: after.Title, Context: after.Context, Evidence: evidence,
		Signals: sigsJSON, ProposedFix: after.ProposedFix, AcceptanceCriteria: agents.Slice(after.AcceptanceCriteria),
		ScopePaths: agents.Slice(after.ScopePaths),
		Embedding:  pgvector.NewVector(r.Embedder.Embed(after, src.ProblemKeys[after.Fingerprint])),
	})
	if err != nil {
		return before, after, err
	}
	if n == 0 {
		return before, after, fmt.Errorf("achado %d ganhou issue ou mudou de estado no meio; nada gravado", id)
	}
	return before, after, nil
}
