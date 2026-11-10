package executor

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

// CommandsDocs são os arquivos do repo-alvo de onde os agentes tiram os comandos de setup,
// build, teste e lint, na ordem de preferência. Sem nenhum deles, o job falha.
var CommandsDocs = repo.CommandsDocs

// ErrNoCommandsDoc é a falha de um repo sem AGENTS.md nem CLAUDE.md no escopo do achado nem na raiz.
type ErrNoCommandsDoc struct{ Repo string }

func (e ErrNoCommandsDoc) Error() string {
	return fmt.Sprintf("o repo %s não tem AGENTS.md nem CLAUDE.md no escopo do achado nem na raiz: os agentes não carregam nada específico de repo, "+
		"então precisam desse arquivo com os comandos de setup, build, teste e lint (num monorepo, na raiz ou no workspace)", e.Repo)
}

// CommandsDoc devolve os arquivos de comandos do achado no worktree (repo.CommandsDocsFor): o
// principal, o mais próximo do escopo, e todos, com o da raiz por último.
func CommandsDoc(worktree, repoName string, scope []string) (string, []string, error) {
	if docs := repo.CommandsDocsFor(worktree, scope); len(docs) > 0 {
		return docs[0], docs, nil
	}
	return "", nil, ErrNoCommandsDoc{Repo: repoName}
}

// Workspace é uma unidade de build do escopo do achado: diretório, tipo (go, gradle, maven, npm,
// yarn, pnpm) e, num pacote JS, o name do package.json (o filtro do gerenciador).
type Workspace struct {
	Dir  string `json:"dir"`
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
}

// WorkspacesFor lê as unidades de build do escopo no worktree.
func WorkspacesFor(worktree string, scope []string) []Workspace {
	var out []Workspace
	for _, u := range repo.UnitsFor(worktree, scope) {
		out = append(out, Workspace{Dir: u.Dir, Kind: u.Kind, Name: repo.PackageName(worktree, u)})
	}
	return out
}

// JobSpec é o JOB/job.json que a skill e os agentes leem.
type JobSpec struct {
	ExecutionID   int64  `json:"execution_id"`
	Kind          string `json:"kind"`
	Issue         string `json:"issue"`
	IssueURL      string `json:"issue_url"`
	Title         string `json:"title"`
	Repo          string `json:"repo"`
	DefaultBranch string `json:"default_branch"`
	Branch        string `json:"branch"`
	BaseSHA       string `json:"base_sha"`
	Worktree      string `json:"worktree"`
	// CommandsDoc é o AGENTS.md (ou CLAUDE.md) mais próximo do escopo do achado; CommandsDocs, todos
	// os que valem para ele, com o da raiz (setup comum) por último.
	CommandsDoc  string   `json:"commands_doc"`
	CommandsDocs []string `json:"commands_docs,omitempty"`
	// Workspaces são as unidades de build do escopo (repo.UnitsFor): num monorepo de front e BFF, os
	// pacotes cujos testes o job roda.
	Workspaces   []Workspace    `json:"workspaces,omitempty"`
	Category     string         `json:"category"`
	Severity     string         `json:"severity"`
	ScopePaths   []string       `json:"scope_paths"`
	BlockedPaths []string       `json:"blocked_paths"`
	Routing      any            `json:"routing"`
	Policy       config.Policy  `json:"policy"`
	MaxRounds    int            `json:"max_rounds"`
	ReworkNote   string         `json:"rework_note"`
	Previous     map[string]any `json:"previous,omitempty"` // artefatos da execução anterior (rework)
	// Instructions são os comentários humanos da issue que entram na spec (ADR 0026), dentro de
	// InstructionWindow.
	Instructions      []Instruction     `json:"instructions,omitempty"`
	InstructionWindow InstructionWindow `json:"instruction_window"`
	// IgnoredAuthors são os autores de comentários que não entraram por fix_trigger (ADR 0042).
	IgnoredAuthors []string `json:"ignored_authors,omitempty"`
}

// WriteJob cria o diretório do job com job.json, issue.md e, quando a issue veio do Judge com
// critérios de aceite, spec.md (o spec-writer é pulado).
func WriteJob(dir string, spec JobSpec, f signals.Finding, issueTitle, issueBody string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	spec.MaxRounds = config.MaxRounds
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "job.json"), data, 0o600); err != nil {
		return err
	}
	finding, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	issue := fmt.Sprintf("# %s · %s\n\n%s\n\n%s%s\n\n## Achado do Sentinela (JSON)\n\n```json\n%s\n```\n",
		spec.Issue, issueTitle, spec.IssueURL, prefixed(instructionsSection(spec.Instructions, spec.InstructionWindow, spec.IgnoredAuthors)), strings.TrimSpace(issueBody), finding)
	if err := os.WriteFile(filepath.Join(dir, "issue.md"), []byte(issue), 0o600); err != nil {
		return err
	}
	if len(f.AcceptanceCriteria) == 0 {
		return nil
	}
	return os.WriteFile(filepath.Join(dir, "spec.md"), []byte(SpecFromFinding(spec, f)), 0o600)
}

// SpecFromFinding escreve a spec direto do achado julgado: contexto, critérios do Judge,
// escopo, fora de escopo e premissas. É código, não agente: a issue já veio com critérios.
func SpecFromFinding(spec JobSpec, f signals.Finding) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("SPEC: READY\n\n# %s\n\nIssue: %s · fingerprint `%s` · %s/%s\n\n", f.Title, spec.Issue, f.Fingerprint, f.Category, f.Severity)
	w("## Contexto\n\n%s\n\nEvidência:\n", strings.TrimSpace(f.Context))
	for _, e := range f.Evidence {
		w("- `%s:%d`: `%s`\n", e.File, e.Line, strings.TrimSpace(e.Snippet))
	}
	if s := instructionsSection(spec.Instructions, spec.InstructionWindow, spec.IgnoredAuthors); s != "" {
		w("\n%s", s)
		w("\n## Requisitos\n\n_Correção proposta pelo achado; as Instruções da equipe acima prevalecem._\n\n%s\n", strings.TrimSpace(f.ProposedFix))
	} else {
		w("\n## Requisitos\n\n%s\n", strings.TrimSpace(f.ProposedFix))
	}
	w("\n## Critérios de aceite\n\n")
	for _, c := range f.AcceptanceCriteria {
		w("- [ ] %s\n", c)
	}
	w("\n## Fora de escopo\n\n")
	if len(f.ScopePaths) > 0 {
		w("- Qualquer arquivo fora de: `%s`\n", strings.Join(f.ScopePaths, "`, `"))
	}
	for _, p := range spec.BlockedPaths {
		w("- Path bloqueado: `%s`\n", p)
	}
	w("- Mudanças de comportamento além dos critérios acima.\n")
	w("\n## Premissas\n\n")
	if i := strings.Index(f.Context, "Premissa de produto:"); i >= 0 {
		w("- [produto] %s\n", strings.TrimSpace(strings.TrimPrefix(f.Context[i:], "Premissa de produto:")))
	}
	for _, ws := range spec.Workspaces {
		if ws.Name != "" {
			w("- Workspace afetado: `%s` (%s, pacote `%s`): rode os testes dele, com o filtro do gerenciador.\n", ws.Dir, ws.Kind, ws.Name)
		} else {
			w("- Unidade de build afetada: `%s` (%s).\n", ws.Dir, ws.Kind)
		}
	}
	if others := otherDocs(spec); len(others) > 0 {
		w("- Os comandos de build, teste e lint são os de `%s`, o mais próximo do escopo; o setup e o que ele não cobrir estão em `%s`.\n", spec.CommandsDoc, strings.Join(others, "`, `"))
	} else {
		w("- Os comandos de build, teste e lint são os de `%s`.\n", spec.CommandsDoc)
	}
	if spec.ReworkNote != "" {
		w("\n## Nota de rework (humano)\n\n%s\n", spec.ReworkNote)
	}
	return b.String()
}

// otherDocs são os arquivos de comandos além do principal.
func otherDocs(spec JobSpec) []string {
	var out []string
	for _, d := range spec.CommandsDocs {
		if d != spec.CommandsDoc {
			out = append(out, d)
		}
	}
	return out
}

// agentModels liga cada subagente do plugin ao papel no sentinela.yaml.
func agentModels(m config.ModelRoles) map[string]string {
	return map[string]string{
		"spec-writer.md": m.SpecWriter, "builder.md": m.Executor, "approver.md": m.Approver,
		"reviewer-security.md": m.ReviewerSecurity, "reviewer-code.md": m.ReviewerCode,
		"reviewer-reliability.md": m.ReviewerReliability, "reviewer-domain.md": m.ReviewerDomain,
	}
}

var modelLine = regexp.MustCompile(`(?m)^model:.*$`)

// RenderPlugin copia o plugin para o job trocando o `model:` de cada subagente pelo modelo do
// papel no sentinela.yaml, que é a fonte da verdade dos modelos.
func RenderPlugin(src, dst string, m config.ModelRoles) error {
	models := agentModels(m)
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		data, err := os.ReadFile(p) //nolint:gosec // arquivos do plugin do próprio Sentinela
		if err != nil {
			return err
		}
		if filepath.Base(filepath.Dir(p)) == "agents" {
			if model := models[d.Name()]; model != "" {
				data = modelLine.ReplaceAll(data, []byte("model: "+model))
			}
		}
		return os.WriteFile(target, data, 0o600) //nolint:gosec // destino é o diretório do job, montado pelo worker
	})
}

// ForeignInstructions lista CLAUDE.md e CLAUDE.local.md acima do worktree. O Claude Code os
// carregaria junto com os do repo-alvo; com algum deles, o job falha em vez de rodar com
// instruções de outro projeto.
func ForeignInstructions(worktree string) []string { return llm.InstructionsAbove(worktree, false) }

// prefixed devolve a seção seguida de uma linha em branco, ou "" se vazia.
func prefixed(section string) string {
	if section == "" {
		return ""
	}
	return section + "\n"
}
