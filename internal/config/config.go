// Package config carrega e valida o sentinela.yaml: repos, módulos, times,
// paths bloqueados, orçamentos, modelos e tabela de preços.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// OwnersSource decide quais fontes o resolvedor de ownership consulta.
type OwnersSource string

const (
	OwnersCodeowners OwnersSource = "codeowners"
	OwnersConfig     OwnersSource = "config"
	OwnersBoth       OwnersSource = "both"
)

// AssigneeMode define como a issue recebe um responsável.
type AssigneeMode string

const (
	AssigneeNone       AssigneeMode = "none"
	AssigneeLead       AssigneeMode = "lead"
	AssigneeRoundRobin AssigneeMode = "round_robin"
)

type Config struct {
	Workspace    Workspace        `yaml:"workspace,omitempty"`
	Defaults     Defaults         `yaml:"defaults,omitempty"`
	Labels       Labels           `yaml:"labels,omitempty"`
	Budget       Budget           `yaml:"budget,omitempty"`
	Models       Models           `yaml:"models,omitempty"`
	Pricing      Pricing          `yaml:"pricing,omitempty"`
	BlockedPaths []string         `yaml:"blocked_paths,omitempty"`
	Teams        map[string]*Team `yaml:"teams,omitempty"`
	Repos        []*Repo          `yaml:"repos,omitempty"`
	LLM          LLM              `yaml:"llm,omitempty"`
	Discovery    Discovery        `yaml:"discovery,omitempty"`
	Telemetry    Telemetry        `yaml:"telemetry,omitempty"`
	Signals      Signals          `yaml:"signals,omitempty"`
	Trigger      Trigger          `yaml:"trigger,omitempty"`
	Publish      Publish          `yaml:"publish,omitempty"`
	Feedback     Feedback         `yaml:"feedback,omitempty"`
	GitHubApps   GitHubApps       `yaml:"github_apps,omitempty"`
	Execution    Execution        `yaml:"execution,omitempty"`
	LinearAuth   LinearAuth       `yaml:"linear_auth,omitempty"`
	Adapters     Adapters         `yaml:"adapters,omitempty"`
	// FixTrigger: quem pode disparar a correção (anyone por padrão; ADR 0042).
	FixTrigger FixTrigger `yaml:"fix_trigger,omitempty"`

	// teamFiles: time -> arquivo de config.d/teams que o declarou (ADR 0040).
	teamFiles map[string]string
}

// Adaptadores das pontas (ADR 0038). O Postgres é o mesmo em todos os modos; só mudam a saída
// dos achados, a entrada das decisões, os avisos e o host git.
const (
	TrackerLinearAPI    = "linear_api" // issues no Linear pela API (app OAuth ou chave pessoal)
	TrackerLocal        = "local"      // achados em markdown/HTML em adapters.local_dir; decisões pela CLI
	TrackerLinearMCP    = "linear_mcp" // issues no Linear pelo MCP do Claude Code do usuário (ADR 0039)
	NotifierSlackMCP    = "slack_mcp"  // avisos pelo MCP do Slack do Claude Code do usuário (ADR 0039)
	NotifierSlackAPI    = "slack_api"  // bot do Slack (SLACK_BOT_TOKEN)
	NotifierNone        = "none"       // sem avisos
	GitHostGitHubApps   = "github_apps"
	GitHostGHCLI        = "gh_cli" // gh autenticado do usuário: PR em draft, gate só comenta
	defaultLocalDir     = "reports/local"
	localIssuePrefix    = "F-"
	defaultTrackerLabel = "sentinela"
)

// Adapters escolhe as pontas. Padrão: linear_api, slack_api e github_apps (o modo completo).
type Adapters struct {
	Tracker  string `yaml:"tracker,omitempty"`
	Notifier string `yaml:"notifier,omitempty"`
	GitHost  string `yaml:"git_host,omitempty"`
	// LocalDir é onde o tracker local escreve os relatórios (padrão reports/local).
	LocalDir string `yaml:"local_dir,omitempty"`
	// MCP configura linear_mcp e slack_mcp (ADR 0039).
	MCP MCPAdapters `yaml:"mcp,omitempty"`
}

// MCPAdapters: os servidores MCP do Claude Code do usuário e quanto cada leitura pode custar.
type MCPAdapters struct {
	LinearServer string `yaml:"linear_server,omitempty"` // padrão "claude.ai Linear"
	SlackServer  string `yaml:"slack_server,omitempty"`  // padrão "claude.ai Slack"
	// PollInterval é o intervalo do sync e a validade do estado lido das issues (padrão 15m):
	// cada leitura é uma chamada `claude -p` e gasta cota.
	PollInterval time.Duration `yaml:"poll_interval,omitempty"`
	// Model é o modelo das chamadas; padrão: o mais barato de pricing.models.
	Model string `yaml:"model,omitempty"`
}

// Categories são as categorias de achado (contrato Finding).
var Categories = []string{"bug", "performance", "improvement", "security"}

// ModuleProject diz se project é o projeto de algum módulo do time: um achado gravado com ele
// veio de um módulo, que vence a categoria do time.
func (c *Config) ModuleProject(team, project string) bool {
	if project == "" {
		return false
	}
	for _, r := range c.Repos {
		for _, m := range r.Modules {
			if m.Team == team && m.LinearProject == project {
				return true
			}
		}
	}
	return false
}

// CategoryProject é o projeto da categoria no time, se ele definir um (linear.category_projects).
func (c *Config) CategoryProject(team, category string) string {
	if t := c.Teams[team]; t != nil {
		return t.Linear.CategoryProjects[category]
	}
	return ""
}

// PublishProject é o projeto em que a issue de um achado é criada (ADR 0040): o do módulo
// (gravado no roteamento) > o da categoria no time > o gravado (time > repo > defaults) >
// defaults.linear.project, para achados gravados sem projeto.
func (c *Config) PublishProject(team, category, stored string) string {
	if c.ModuleProject(team, stored) {
		return stored
	}
	if p := c.CategoryProject(team, category); p != "" {
		return p
	}
	if stored != "" {
		return stored
	}
	return c.Defaults.Linear.Project
}

// FileOf diz de onde veio o módulo, para as mensagens: vazio no sentinela.yaml principal.
func (c *Config) FileOf(m Module) string {
	if m.from == "" {
		return ""
	}
	return " (" + m.from + ")"
}

// TeamFile diz em que arquivo o time foi declarado: vazio no sentinela.yaml principal.
func (c *Config) TeamFile(team string) string {
	if f := c.teamFiles[team]; f != "" {
		return " (" + f + ")"
	}
	return ""
}

// ProjectFor é o projeto do Linear efetivo para o time dono num repo, e de onde ele veio: o do
// time, senão o do repo, senão o de defaults (último recurso). Vazio: a issue sai sem projeto.
func (c *Config) ProjectFor(team *Team, teamName string, rp *Repo) (project, source string) {
	switch {
	case team != nil && team.Linear.Project != "":
		return team.Linear.Project, "teams." + teamName + ".linear.project" + c.TeamFile(teamName)
	case rp != nil && rp.LinearProject != "":
		return rp.LinearProject, "repos[" + rp.Name + "].linear_project"
	case c.Defaults.Linear.Project != "":
		return c.Defaults.Linear.Project, "defaults.linear.project"
	}
	return "", "nenhum (a issue sai sem projeto)"
}

// LinearTeamKeys são as chaves dos times do Linear da config (defaults e times), sem repetição.
func (c *Config) LinearTeamKeys() []string {
	var out []string
	add := func(k string) {
		if k != "" && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	add(c.Defaults.Linear.Team)
	for _, n := range c.TeamNames() {
		add(c.LinearTeamFor(c.Teams[n]))
	}
	return out
}

// LinearMCP diz se o tracker é o Linear pelo MCP.
func (c *Config) LinearMCP() bool { return c.Adapters.Tracker == TrackerLinearMCP }

// cheapestModel é o modelo de menor preço (entrada + saída) em pricing.models.
func (c *Config) cheapestModel() string {
	best, bestPrice := "", 0.0
	for id, p := range c.Pricing.Models {
		price := p.Input + p.Output
		if best == "" || price < bestPrice || (price == bestPrice && id < best) {
			best, bestPrice = id, price
		}
	}
	return best
}

// LocalTracker diz se as decisões vêm da CLI (`sentinela finding ...`) e não do Linear.
func (c *Config) LocalTracker() bool { return c.Adapters.Tracker == TrackerLocal }

// LocalIssueID é o identificador de um achado no tracker local (vira a branch sentinela/F-<id>).
func LocalIssueID(findingID int64) string { return fmt.Sprintf("%s%d", localIssuePrefix, findingID) }

// LinearAuth escolhe com que identidade o Sentinela fala com o Linear (ADR 0032). Com mode app,
// issues, comentários, labels e mudanças de estado saem como o app OAuth (actor=app), com
// LINEAR_OAUTH_CLIENT_ID e LINEAR_OAUTH_CLIENT_SECRET; com api_key, pela chave pessoal
// LINEAR_API_KEY, como antes.
type LinearAuth struct {
	Mode  string `yaml:"mode,omitempty"`  // app | api_key (padrão: app se houver LINEAR_OAUTH_CLIENT_ID)
	Grant string `yaml:"grant,omitempty"` // authorization_code (padrão) | client_credentials
	// Scopes pedidos ao Linear; padrão [read, write] (write cobre issues, comentários, labels e
	// estados).
	Scopes      []string `yaml:"scopes,omitempty"`
	RedirectURI string   `yaml:"redirect_uri,omitempty"` // callback do authorization_code
	// TokenFile guarda o token do app (0600, fora do git); padrão ~/.config/sentinela/linear-token.json.
	TokenFile string `yaml:"token_file,omitempty"`
	// FallbackAPIKey usa LINEAR_API_KEY quando o token do app falta ou não renova. As ações saem
	// com o nome da pessoa dona da chave, e os comentários do Sentinela voltam a ser reconhecidos
	// pelo prefixo "**Sentinela".
	FallbackAPIKey bool `yaml:"fallback_api_key,omitempty"`
	// LegacyPrefixUntil: comentários anteriores a esta data que começam com "**Sentinela" são do
	// Sentinela da época da chave pessoal. Depois dela, só o autor (o app) identifica o Sentinela.
	LegacyPrefixUntil time.Time `yaml:"legacy_prefix_until,omitempty"`
}

// Modos e grants de LinearAuth.
const (
	LinearAuthApp         = "app"
	LinearAuthAPIKey      = "api_key"
	GrantAuthCode         = "authorization_code"
	GrantClientCreds      = "client_credentials"
	DefaultLinearRedirect = "http://localhost:8787/oauth/linear/callback"
)

// Execution configura a linha de execução (Fase 4): worker, worktrees, plugin e travas.
type Execution struct {
	// DefaultDirs diz quais de worktrees, jobs e home vieram do padrão desta instalação (não estão
	// no arquivo). O worker usa isso para manter nos diretórios antigos os de uma instalação que
	// já tem execuções neles (ADR 0056).
	DefaultDirs ExecDirs      `yaml:"-"`
	Concurrency int           `yaml:"concurrency,omitempty"`   // jobs em paralelo (padrão 1)
	Worktrees   string        `yaml:"worktrees,omitempty"`     // um git worktree por job
	Jobs        string        `yaml:"jobs,omitempty"`          // artefatos (spec, build, reviews, decision)
	Plugin      string        `yaml:"plugin,omitempty"`        // plugins/sentinela-factory
	Binary      string        `yaml:"binary,omitempty"`        // claude
	Timeout     time.Duration `yaml:"timeout,omitempty"`       // por job
	Poll        time.Duration `yaml:"poll_interval,omitempty"` // fila e shepherd
	// Runtime é onde o Claude Code do job roda (ADR 0053): host (padrão; processo do worker, com
	// o HOME próprio) ou container (um contêiner por sessão, com montagens mínimas e saída de rede
	// só pelo proxy com lista permitida).
	Runtime string `yaml:"runtime,omitempty"`
	// Container configura o runtime container.
	Container JobContainer `yaml:"container,omitempty"`
	// JobDocker é o Docker que o job vê no runtime container: none (padrão; nenhum) ou dind (um
	// docker:dind isolado só para o job, para Testcontainers, derrubado no fim).
	JobDocker string `yaml:"job_docker,omitempty"`
	// Home é o HOME próprio do Claude Code do job, só com o login dele, sem as credenciais de git,
	// ssh e gh da máquina (M6 da revisão 2026-10-11). Padrão ~/.cache/sentinela/job-home; "off"
	// usa o HOME do worker.
	Home string `yaml:"home,omitempty"`
	// HomeFiles são arquivos do HOME da máquina copiados para o HOME do job antes de cada job
	// (ex.: .npmrc, .gradle/gradle.properties, .m2/settings.xml, com token de leitura de um
	// registro privado). Ficam legíveis pelo job: veja o risco na ADR 0057. Vazio por padrão.
	HomeFiles []string `yaml:"home_files,omitempty"`
	// MaxBudgetUSD vai para --max-budget-usd do Claude Code; 0 usa budget.per_pr_usd.
	MaxBudgetUSD      float64 `yaml:"max_budget_usd,omitempty"`
	ShepherdMaxCycles int     `yaml:"shepherd_max_cycles,omitempty"` // padrão 3
	// MaxResumes limita as retomadas de sessão seguidas de uma execução (ADR 0030); padrão 2,
	// negativo desliga a retomada.
	MaxResumes int `yaml:"max_resumes,omitempty"`
	// IdleTimeout encerra a sessão do Claude Code que passa esse tempo sem saída nova (o job falha
	// com idle_timeout, retomável no retry); padrão 15m, negativo desliga (ADR 0034).
	IdleTimeout time.Duration `yaml:"idle_timeout,omitempty"`
	// CompletionTimeout encerra, com aviso, o processo que não sai depois de sinalizar o fim; o
	// resultado vale e os commits ficam no worktree. Padrão 2m, negativo desliga (ADR 0034).
	CompletionTimeout time.Duration `yaml:"completion_timeout,omitempty"`
	// FailedWorktreeRetention é quanto o worktree de uma execução que falhou fica guardado antes
	// da limpeza automática; o de PR mergeado ou fechado sai no ciclo seguinte. Padrão 168h (7
	// dias), negativo nunca remove os de falha (ADR 0037).
	FailedWorktreeRetention time.Duration `yaml:"failed_worktree_retention,omitempty"`
	// JobsRetention é quanto o diretório de artefatos de um job (jobs/<id>: spec, build, reviews,
	// decisão e o stream.jsonl da sessão) fica guardado depois do desfecho: PR mergeado ou
	// fechado, falha ou cancelamento. Nunca com o achado na fila ou rodando. Padrão 2160h (90
	// dias); negativo nunca remove (D-12 da revisão de prontidão).
	JobsRetention time.Duration `yaml:"jobs_retention,omitempty"`
	// WebhookEventsRetention é quanto o corpo de um webhook já processado (issues, comentários,
	// PRs) fica guardado; depois, o payload é esvaziado e a linha fica só para o dedup. Padrão
	// 720h (30 dias); negativo nunca esvazia.
	WebhookEventsRetention time.Duration `yaml:"webhook_events_retention,omitempty"`
	// DisallowedTools são negados ao Claude Code no worktree (push, gh, rede).
	DisallowedTools []string `yaml:"disallowed_tools,omitempty"`
	// ReviewNotes decide o que fazer com as observações fora do escopo dos reviewers ao fim de um
	// job com PR (ADR 0019): off | shadow (só registra o que o Judge decidiria) | publish.
	ReviewNotes string `yaml:"review_notes,omitempty"`
	// IgnoreCommentPrefixes são começos de comentário que não viram instrução da equipe na spec,
	// além do "**Sentinela" do próprio Sentinela: outra automação que comenta pela conta de uma
	// pessoa (ADR 0026).
	IgnoreCommentPrefixes []string `yaml:"ignore_comment_prefixes,omitempty"`
}

// JobContainer é o contêiner do job (ADR 0053).
type JobContainer struct {
	// Engine é o binário do contêiner: docker (padrão) ou podman.
	Engine string `yaml:"engine,omitempty"`
	// Image é a imagem do job (Dockerfile.worker); padrão sentinela-job:latest.
	Image string `yaml:"image,omitempty"`
	// EgressAllow são os únicos destinos de rede do job, pelo proxy de saída: host exato, ou
	// ".dominio" para os subdomínios. Vazio usa DefaultEgressAllow.
	EgressAllow []string `yaml:"egress_allow,omitempty"`
	// DindImage é a imagem do Docker isolado do job_docker: dind (padrão docker:27-dind).
	DindImage string `yaml:"dind_image,omitempty"`
	// Labels vão em cada contêiner e rede que o worker cria, além de sentinela.job=<nome>.
	Labels map[string]string `yaml:"labels,omitempty"`
}

// Runtimes e Docker do job (ADR 0053).
const (
	RuntimeHost      = "host"
	RuntimeContainer = "container"
	JobDockerNone    = "none"
	JobDockerDind    = "dind"
)

// DefaultEgressAllow é a lista de saída padrão do job em contêiner: a Anthropic (modelo e login),
// o GitHub para clonar e baixar, e os registros de Go, npm, Maven, Gradle e PyPI.
var DefaultEgressAllow = []string{
	"api.anthropic.com", "console.anthropic.com", "claude.ai", "platform.claude.com",
	"github.com", "codeload.github.com", ".githubusercontent.com",
	"proxy.golang.org", "sum.golang.org", "storage.googleapis.com",
	"registry.npmjs.org", "registry.yarnpkg.com", "repo.yarnpkg.com",
	"repo.maven.apache.org", "repo1.maven.org", "plugins.gradle.org", "services.gradle.org", "downloads.gradle.org", "plugins-artifacts.gradle.org",
	"pypi.org", "files.pythonhosted.org",
	// Pull de imagens do dind (job_docker: dind).
	"registry-1.docker.io", "auth.docker.io", "production.cloudfront.docker.com", "production.cloudflare.docker.com",
}

// HomeOff desliga o HOME próprio do job (execution.home).
const HomeOff = "off"

// MaxRounds é o limite da spec: builder, reviewers e no máximo 2 retomadas (3 rodadas).
const MaxRounds = 3

// GitHubApps nomeia as duas GitHub Apps (slug) que o `config validate` confere, e a trava da
// revisora: com ReviewerApproves false (padrão), ela só publica review do tipo COMMENT com a
// decisão do approver e do gate, nunca APPROVE, qualquer que seja a política do time.
type GitHubApps struct {
	Bot              string `yaml:"bot,omitempty"`
	Reviewer         string `yaml:"reviewer,omitempty"`
	ReviewerApproves bool   `yaml:"reviewer_approves,omitempty"`
}

// Publish controla a publicação no Linear (Fase 3). Desligada, a descoberta volta ao modo sombra.
type Publish struct {
	Enabled bool `yaml:"enabled,omitempty"`
	// MaxIssuesPerTeamPerRun limita as issues novas (principais e relacionadas) de cada time
	// numa rodada; o excedente fica pendente para a rodada seguinte. No scan --all, a rodada é a
	// execução inteira: o limite soma os repos e a ordem é por severidade entre eles (ADR 0050).
	MaxIssuesPerTeamPerRun int `yaml:"max_issues_per_team_per_run,omitempty"`
}

// Feedback configura o Feedback Curator e as supressões que ele cria.
type Feedback struct {
	CuratorInterval time.Duration `yaml:"curator_interval,omitempty"`
	SuppressionTTL  time.Duration `yaml:"suppression_ttl,omitempty"` // supressões por padrão do Curator
	MaxExamples     int           `yaml:"max_examples,omitempty"`
}

type Workspace struct {
	LinearWorkspace string `yaml:"linear_workspace,omitempty"`
	SlackWorkspace  string `yaml:"slack_workspace,omitempty"`
	GitHubOwner     string `yaml:"github_owner,omitempty"`
}

type Defaults struct {
	Linear       LinearTarget `yaml:"linear,omitempty"`
	OwnersSource OwnersSource `yaml:"owners_source,omitempty"`
	Notify       Notify       `yaml:"notify,omitempty"`
	Policy       Policy       `yaml:"policy,omitempty"`
}

type LinearTarget struct {
	Team        string `yaml:"team,omitempty"`
	Project     string `yaml:"project,omitempty"`
	TriageState string `yaml:"triage_state,omitempty"`
	// ReviewState é o estado para onde a issue vai quando o worker abre o PR (ADR 0027); vazio
	// não move.
	ReviewState string `yaml:"review_state,omitempty"`
	// DoneState é o estado para onde a issue vai quando o worker vê o PR mergeado (ADR 0028);
	// vazio usa DefaultDoneState.
	DoneState string   `yaml:"done_state,omitempty"`
	Labels    []string `yaml:"labels,omitempty"`
	// CategoryProjects manda os achados de uma categoria do time para outro projeto (ex.:
	// security -> "Pagamentos · Segurança"). Só por time; vence o projeto do time e do repo e perde
	// para o do módulo (ADR 0040).
	CategoryProjects map[string]string `yaml:"category_projects,omitempty"`
}

// Notify escolhe o canal do Slack (ID C... ou nome #canal; ID é preferível) e, por time,
// quem mencionar (ID de user group S..., de usuário U..., ou @here/@channel).
type Notify struct {
	Slack   string `yaml:"slack,omitempty"`
	Mention string `yaml:"mention,omitempty"`
}

// Policy usa ponteiros para distinguir "não definido" de "false/0" ao
// combinar o padrão com a política do time.
type Policy struct {
	AutoApprove *bool `yaml:"auto_approve,omitempty"`
	Blocked     *bool `yaml:"blocked,omitempty"`
	MaxLines    *int  `yaml:"max_lines,omitempty"`
	MaxFiles    *int  `yaml:"max_files,omitempty"`
}

// Merge devolve p com os campos não definidos preenchidos por base.
func (p Policy) Merge(base Policy) Policy {
	out := base
	if p.AutoApprove != nil {
		out.AutoApprove = p.AutoApprove
	}
	if p.Blocked != nil {
		out.Blocked = p.Blocked
	}
	if p.MaxLines != nil {
		out.MaxLines = p.MaxLines
	}
	if p.MaxFiles != nil {
		out.MaxFiles = p.MaxFiles
	}
	return out
}

type Labels struct {
	Trigger string `yaml:"trigger,omitempty"`
	Done    string `yaml:"done,omitempty"`
	Failed  string `yaml:"failed,omitempty"`
	Finding string `yaml:"finding,omitempty"`
	// Achado com premissa de produto vira pergunta ao time (Question); a resposta vem por label
	// aplicada por uma pessoa: NotIntentional vira achado normal, Intentional vira supressão.
	Question       string `yaml:"question,omitempty"`
	Intentional    string `yaml:"intentional,omitempty"`
	NotIntentional string `yaml:"not_intentional,omitempty"`
}

type Budget struct {
	PerRunUSD   float64 `yaml:"per_run_usd,omitempty"`
	PerPRUSD    float64 `yaml:"per_pr_usd,omitempty"`
	PerMonthUSD float64 `yaml:"per_month_usd,omitempty"`
}

// Models mapeia cada papel do roster para um ID de modelo da Anthropic.
// ModelRoles é o modelo de cada papel. Em `models:` vale para tudo; em `repos[].models` e
// `teams.<time>.models` só os papéis preenchidos sobrescrevem, com o time dono na frente do repo
// (veja Config.ModelsFor).
type ModelRoles struct {
	// Descoberta
	Triage    string `yaml:"triage,omitempty"`
	Analysis  string `yaml:"analysis,omitempty"`
	Architect string `yaml:"architect,omitempty"`
	Judge     string `yaml:"judge,omitempty"`
	Curator   string `yaml:"curator,omitempty"`
	Notes     string `yaml:"notes,omitempty"` // intérprete de notas de review (padrão: analysis)
	// Linha de execução (Fase 4)
	Orchestrator        string `yaml:"orchestrator,omitempty"` // sessão da skill sentinela-fix (padrão: spec_writer)
	SpecWriter          string `yaml:"spec_writer,omitempty"`
	Executor            string `yaml:"executor,omitempty"`
	ReviewerSecurity    string `yaml:"reviewer_security,omitempty"`
	ReviewerCode        string `yaml:"reviewer_code,omitempty"`
	ReviewerReliability string `yaml:"reviewer_reliability,omitempty"`
	ReviewerDomain      string `yaml:"reviewer_domain,omitempty"`
	Approver            string `yaml:"approver,omitempty"`
	Shepherd            string `yaml:"shepherd,omitempty"` // PR Shepherd (padrão: reviewer_code)
	// RefusalFallback é o modelo usado quando o do papel recusa. Só em repos[].models e
	// teams.<time>.models; no global, o valor vem de models.refusal.fallback_model.
	RefusalFallback string `yaml:"refusal_fallback,omitempty"`
}

// Models é o bloco global `models:`: um modelo por papel, mais a política de recusa e o batch.
type Models struct {
	ModelRoles `yaml:",inline"`

	Refusal  RefusalPolicy `yaml:"refusal,omitempty"`
	UseBatch bool          `yaml:"use_batch,omitempty"`
}

// RefusalAction decide o que fazer quando o modelo recusa (stop_reason=refusal).
type RefusalAction string

const (
	RefusalFallbackModel RefusalAction = "fallback_model"
	RefusalSkip          RefusalAction = "skip"
)

type RefusalPolicy struct {
	Action        RefusalAction `yaml:"action,omitempty"`
	FallbackModel string        `yaml:"fallback_model,omitempty"`
}

// roles devolve cada papel com sua chave no YAML, na ordem do roster.
func (m ModelRoles) roles() []struct{ key, id string } {
	return []struct{ key, id string }{
		{"triage", m.Triage}, {"analysis", m.Analysis}, {"architect", m.Architect}, {"judge", m.Judge},
		{"curator", m.Curator}, {"notes", m.Notes}, {"orchestrator", m.Orchestrator},
		{"spec_writer", m.SpecWriter}, {"executor", m.Executor}, {"reviewer_security", m.ReviewerSecurity},
		{"reviewer_code", m.ReviewerCode}, {"reviewer_reliability", m.ReviewerReliability},
		{"reviewer_domain", m.ReviewerDomain}, {"approver", m.Approver}, {"shepherd", m.Shepherd},
	}
}

// explained são os papéis mais o fallback de recusa, na ordem do `config models`.
func (m ModelRoles) explained() []struct{ key, id string } {
	return append(m.roles(), struct{ key, id string }{"refusal_fallback", m.RefusalFallback})
}

// over devolve m com os papéis preenchidos em o no lugar.
func (m ModelRoles) over(o ModelRoles) ModelRoles {
	pick := func(base *string, v string) {
		if v != "" {
			*base = v
		}
	}
	pick(&m.Triage, o.Triage)
	pick(&m.Analysis, o.Analysis)
	pick(&m.Architect, o.Architect)
	pick(&m.Judge, o.Judge)
	pick(&m.Curator, o.Curator)
	pick(&m.Notes, o.Notes)
	pick(&m.Orchestrator, o.Orchestrator)
	pick(&m.SpecWriter, o.SpecWriter)
	pick(&m.Executor, o.Executor)
	pick(&m.ReviewerSecurity, o.ReviewerSecurity)
	pick(&m.ReviewerCode, o.ReviewerCode)
	pick(&m.ReviewerReliability, o.ReviewerReliability)
	pick(&m.ReviewerDomain, o.ReviewerDomain)
	pick(&m.Approver, o.Approver)
	pick(&m.Shepherd, o.Shepherd)
	pick(&m.RefusalFallback, o.RefusalFallback)
	// Quem troca o reviewer de código sem dizer o shepherd leva o shepherd junto, como no global.
	if o.ReviewerCode != "" && o.Shepherd == "" {
		m.Shepherd = o.ReviewerCode
	}
	return m
}

// ModelChoice é o modelo de um papel e o nível do sentinela.yaml que o decidiu.
type ModelChoice struct {
	Role  string // chave no YAML, ex.: triage
	Model string
	From  string // models | repos[<repo>].models | teams.<time>.models
}

// ExplainModels mostra, papel a papel, o modelo resolvido por ModelsFor e de onde ele veio.
func (c *Config) ExplainModels(repo, team string) []ModelChoice {
	type level struct {
		from  string
		roles ModelRoles
	}
	base := c.Models.ModelRoles
	base.RefusalFallback = c.Models.Refusal.FallbackModel
	levels := []level{{"models", base}}
	if r, ok := c.Repo(repo); ok {
		levels = append(levels, level{fmt.Sprintf("repos[%s].models", r.Name), r.Models})
	}
	if t := c.Teams[team]; t != nil {
		levels = append(levels, level{fmt.Sprintf("teams.%s.models", team), t.Models})
	}
	final := c.ModelsFor(repo, team).explained()
	out := make([]ModelChoice, len(final))
	for i, r := range final {
		out[i] = ModelChoice{Role: r.key, Model: r.id, From: "models"}
		for _, l := range levels[1:] {
			own := l.roles.explained()[i].id
			if own != "" || (r.key == "shepherd" && l.roles.ReviewerCode != "" && l.roles.Shepherd == "") {
				out[i].From = l.from
			}
		}
	}
	return out
}

// ModelsFor resolve o modelo de cada papel para um repo e o time dono: `teams.<time>.models`
// vence `repos[].models`, que vence `models:`. Repo ou time vazio pula o nível.
func (c *Config) ModelsFor(repo, team string) ModelRoles {
	m := c.Models.ModelRoles
	m.RefusalFallback = c.Models.Refusal.FallbackModel
	if r, ok := c.Repo(repo); ok {
		m = m.over(r.Models)
	}
	if t := c.Teams[team]; t != nil {
		m = m.over(t.Models)
	}
	return m
}

// All devolve os IDs de modelo configurados (papéis e fallback), sem repetição e em ordem.
func (m Models) All() []string {
	return m.with()
}

func (m Models) with(extra ...ModelRoles) []string {
	set := map[string]bool{}
	for _, roles := range append([]ModelRoles{m.ModelRoles}, extra...) {
		for _, r := range roles.explained() {
			if r.id != "" {
				set[r.id] = true
			}
		}
	}
	if m.Refusal.FallbackModel != "" {
		set[m.Refusal.FallbackModel] = true
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Pricing é a tabela de preços em USD por milhão de tokens.
type Pricing struct {
	BatchDiscount          float64                `yaml:"batch_discount,omitempty"`
	CacheWrite5mMultiplier float64                `yaml:"cache_write_5m_multiplier,omitempty"`
	CacheWrite1hMultiplier float64                `yaml:"cache_write_1h_multiplier,omitempty"`
	Models                 map[string]*ModelPrice `yaml:"models,omitempty"`
}

type ModelPrice struct {
	Input     float64     `yaml:"input,omitempty"`
	Output    float64     `yaml:"output,omitempty"`
	CacheRead float64     `yaml:"cache_read,omitempty"`
	Tiers     []PriceTier `yaml:"tiers,omitempty"`
}

// PriceTier substitui os preços base quando o prompt passa de AboveInputTokens.
type PriceTier struct {
	AboveInputTokens int64   `yaml:"above_input_tokens,omitempty"`
	Input            float64 `yaml:"input,omitempty"`
	Output           float64 `yaml:"output,omitempty"`
	CacheRead        float64 `yaml:"cache_read,omitempty"`
}

type Team struct {
	GitHub   []string     `yaml:"github,omitempty"`
	Members  []string     `yaml:"members,omitempty"`
	Linear   LinearTarget `yaml:"linear,omitempty"`
	Assignee AssigneeMode `yaml:"assignee,omitempty"`
	Lead     string       `yaml:"lead,omitempty"`
	Notify   Notify       `yaml:"notify,omitempty"`
	Policy   Policy       `yaml:"policy,omitempty"`
	Models   ModelRoles   `yaml:"models,omitempty"` // só os papéis que mudam para este time
	// Labels troca, só para este time, as labels de gatilho, de resultado e de falha (ADR 0038).
	Labels TeamLabels `yaml:"labels,omitempty"`
	// FixTrigger troca, para o time, quem pode disparar a correção (ADR 0042). Política da
	// organização: só no sentinela.yaml principal.
	FixTrigger *FixTrigger `yaml:"fix_trigger,omitempty"`
}

// TeamLabels são as labels que um time pode trocar; vazio usa labels.* da organização.
type TeamLabels struct {
	Trigger string `yaml:"trigger,omitempty"`
	Done    string `yaml:"done,omitempty"`
	Failed  string `yaml:"failed,omitempty"`
}

// LabelsFor devolve as labels valendo para o time (nome em teams.*): as da organização com as
// trocas do time. Time desconhecido ou vazio fica com as da organização.
func (c *Config) LabelsFor(team string) Labels {
	l := c.Labels
	if t := c.Teams[team]; t != nil {
		if t.Labels.Trigger != "" {
			l.Trigger = t.Labels.Trigger
		}
		if t.Labels.Done != "" {
			l.Done = t.Labels.Done
		}
		if t.Labels.Failed != "" {
			l.Failed = t.Labels.Failed
		}
	}
	return l
}

type Repo struct {
	Name          string       `yaml:"name,omitempty"`
	DefaultBranch string       `yaml:"default_branch,omitempty"`
	Languages     []string     `yaml:"languages,omitempty"`
	LinearProject string       `yaml:"linear_project,omitempty"`
	OwnersSource  OwnersSource `yaml:"owners_source,omitempty"`
	Modules       []Module     `yaml:"modules,omitempty"`
	Team          string       `yaml:"team,omitempty"`
	FallbackTeam  string       `yaml:"fallback_team,omitempty"`
	Models        ModelRoles   `yaml:"models,omitempty"` // só os papéis que mudam para este repo
	// from é o arquivo de config.d/teams que declarou o repo, quando não é o principal.
	from string
}

type Module struct {
	Path   string `yaml:"path,omitempty"`
	Team   string `yaml:"team,omitempty"`
	Ignore bool   `yaml:"ignore,omitempty"`
	// LinearProject é o projeto do Linear dos achados deste módulo: vence o do time e o do repo
	// (ADR 0040).
	LinearProject string `yaml:"linear_project,omitempty"`
	// Override faz o módulo vencer os módulos comuns que também casam, qualquer que seja o tamanho
	// do glob: para arquivos transversais (Dockerfile, CI, infra) de um time dentro dos módulos de
	// outros. Entre overrides, vale o glob mais longo (ADR 0043).
	Override bool `yaml:"override,omitempty"`
	// from é o arquivo que declarou o módulo, quando não é o sentinela.yaml principal.
	from string
}

// Providers de LLM (ADR 0013).
const (
	ProviderAnthropicAPI = "anthropic_api"
	ProviderClaudeCode   = "claude_code"
	ProviderReplay       = "replay"
)

// LLM escolhe como os agentes chamam modelos.
type LLM struct {
	Provider   string     `yaml:"provider,omitempty"`
	ClaudeCode ClaudeCode `yaml:"claude_code,omitempty"`
	Replay     Replay     `yaml:"replay,omitempty"`
}

// ClaudeCode configura o provider que usa o Claude Code CLI (claude -p) pela assinatura.
type ClaudeCode struct {
	Binary         string        `yaml:"binary,omitempty"`
	Concurrency    int           `yaml:"concurrency,omitempty"`
	MaxCallsPerRun int           `yaml:"max_calls_per_run,omitempty"`
	Timeout        time.Duration `yaml:"timeout,omitempty"`
	// RecordDir, se definido, grava cada resposta válida para reuso pelo provider replay.
	RecordDir string `yaml:"record_dir,omitempty"`
	// WorkDir é onde o CLI roda: fora de qualquer projeto, porque o Claude Code carrega o
	// CLAUDE.md do diretório e de todos os pais (padrão ~/.cache/sentinela/claude-cwd).
	WorkDir string `yaml:"workdir,omitempty"`
}

// Replay responde com respostas gravadas em disco, sem chamar modelo nenhum.
type Replay struct {
	Dir string `yaml:"dir,omitempty"`
}

// Discovery configura a Fase 2 (scan, triagem, análise, arquitetura e juiz).
type Discovery struct {
	Workdir       string   `yaml:"workdir,omitempty"`
	Scanners      []string `yaml:"scanners,omitempty"`
	SemgrepConfig string   `yaml:"semgrep_config,omitempty"`
	// SemgrepLanguages acrescenta à config de base os pacotes de regras de cada linguagem
	// presente no repo (Fase C, passo 5). Ausente, vale DefaultSemgrepLanguages; {} desliga.
	SemgrepLanguages map[string][]string `yaml:"semgrep_languages,omitempty"`
	// NodeTools é o diretório das ferramentas de Node do Sentinela (eslint com a config neutra e
	// tsc; ADR 0060), instalado com `npm ci --ignore-scripts`. Relativo ao diretório de onde o
	// Sentinela roda; padrão tools/node.
	NodeTools             string        `yaml:"node_tools,omitempty"`
	ScannerTimeout        time.Duration `yaml:"scanner_timeout,omitempty"`
	TriageContextTokens   int           `yaml:"triage_context_tokens,omitempty"`
	AnalysisContextTokens int           `yaml:"analysis_context_tokens,omitempty"`
	MaxFindingsPerPackage int           `yaml:"max_findings_per_package,omitempty"`
	MinConfidence         float64       `yaml:"min_confidence,omitempty"`
	SimilarityThreshold   float64       `yaml:"similarity_threshold,omitempty"`
	// LocationCandidates é o teto de achados existentes no mesmo lugar do código (arquivo,
	// símbolo ou linhas próximas, mesmo módulo) mostrados ao Judge; 0 desliga (ADR 0025).
	LocationCandidates *int `yaml:"location_candidates,omitempty"`
	// LocationLineWindow é a distância máxima, em linhas, para evidências no mesmo arquivo
	// contarem como linhas próximas.
	LocationLineWindow int           `yaml:"location_line_window,omitempty"`
	BatchPollInterval  time.Duration `yaml:"batch_poll_interval,omitempty"`
	// PremiseMargin não tem mais efeito: achado que depende de premissa de produto sai como
	// pergunta sem passar pelo limiar (ADR 0054). Fica aceito para não quebrar configs antigas.
	PremiseMargin float64 `yaml:"premise_margin,omitempty"`
	// RevoteBand: com JudgeVotes > 1, veredito de publicação a menos disso do limiar roda
	// JudgeVotes vezes e vale a mediana. JudgeVotes padrão é 1 (desligado).
	RevoteBand float64 `yaml:"revote_band,omitempty"`
	JudgeVotes int     `yaml:"judge_votes,omitempty"`
	// MaxNewReposPerRun limita quantos repos nunca analisados o `scan --all` inclui por rodada
	// (na ordem do sentinela.yaml); os já analisados rodam sempre. Padrão 1; 0 só roda os
	// já analisados (ADR 0035).
	MaxNewReposPerRun *int `yaml:"max_new_repos_per_run,omitempty"`
}

// Telemetry configura o OpenTelemetry do próprio Sentinela; sem endpoint, fica desligado.
type Telemetry struct {
	OTLPEndpoint string `yaml:"otlp_endpoint,omitempty"`
	ServiceName  string `yaml:"service_name,omitempty"`
}

type Signals struct {
	Sentry  SignalSource `yaml:"sentry,omitempty"`
	Datadog SignalSource `yaml:"datadog,omitempty"`
}

type SignalSource struct {
	Enabled    bool              `yaml:"enabled,omitempty"`
	Org        string            `yaml:"org,omitempty"`
	Site       string            `yaml:"site,omitempty"`
	ServiceMap map[string]string `yaml:"service_map,omitempty"`
}

// Modos de gatilho do sync com o Linear.
const (
	TriggerPolling = "polling"
	TriggerWebhook = "webhook"
)

type Trigger struct {
	Mode            string        `yaml:"mode,omitempty"`
	PollingInterval time.Duration `yaml:"polling_interval,omitempty"`
	ListenAddr      string        `yaml:"listen_addr,omitempty"` // modo webhook
}

// Load lê e valida o arquivo de configuração e, se houver, os arquivos dos times em
// config.d/teams/*.yaml ao lado dele (ADR 0041). Sem config.d, é o sentinela.yaml único de antes.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // caminho vem do operador via flag
	if err != nil {
		return nil, fmt.Errorf("lendo config: %w", err)
	}
	c, err := decode(data)
	if err != nil {
		return nil, err
	}
	var errs []error
	if err := c.loadTeamFiles(path); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, c.duplicateModules()...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, c.finish()
}

// Parse decodifica um YAML só (rejeitando campos desconhecidos), aplica padrões e valida.
func Parse(data []byte) (*Config, error) {
	c, err := decode(data)
	if err != nil {
		return nil, err
	}
	if errs := c.duplicateModules(); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, c.finish()
}

func decode(data []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("decodificando config: %w", err)
	}
	return &c, nil
}

func (c *Config) finish() error {
	c.applyDefaults()
	return c.Validate()
}

func (c *Config) applyDefaults() {
	if c.Defaults.OwnersSource == "" {
		c.Defaults.OwnersSource = OwnersBoth
	}
	ad := &c.Adapters
	if ad.Tracker == "" {
		ad.Tracker = TrackerLinearAPI
	}
	if ad.Notifier == "" {
		ad.Notifier = NotifierSlackAPI
	}
	if ad.GitHost == "" {
		ad.GitHost = GitHostGitHubApps
	}
	if ad.LocalDir == "" {
		ad.LocalDir = defaultLocalDir
	}
	if ad.MCP.LinearServer == "" {
		ad.MCP.LinearServer = "claude.ai Linear"
	}
	if ad.MCP.SlackServer == "" {
		ad.MCP.SlackServer = "claude.ai Slack"
	}
	if ad.MCP.PollInterval == 0 {
		ad.MCP.PollInterval = 15 * time.Minute
	}
	if ad.MCP.Model == "" {
		ad.MCP.Model = c.cheapestModel()
	}
	if c.Labels.Finding == "" {
		c.Labels.Finding = defaultTrackerLabel
	}
	d := &c.Discovery
	if d.SemgrepLanguages == nil {
		d.SemgrepLanguages = map[string][]string{}
		for lang, cs := range DefaultSemgrepLanguages {
			d.SemgrepLanguages[lang] = append([]string(nil), cs...)
		}
	}
	if d.Workdir == "" {
		d.Workdir = ".sentinela/repos"
	}
	if d.NodeTools == "" {
		d.NodeTools = "tools/node"
	}
	if d.ScannerTimeout == 0 {
		d.ScannerTimeout = 5 * time.Minute
	}
	if d.TriageContextTokens == 0 {
		d.TriageContextTokens = 90_000
	}
	if d.AnalysisContextTokens == 0 {
		d.AnalysisContextTokens = 150_000
	}
	if d.MaxFindingsPerPackage == 0 {
		d.MaxFindingsPerPackage = 5
	}
	if d.MinConfidence == 0 {
		d.MinConfidence = 0.6
	}
	if d.SimilarityThreshold == 0 {
		d.SimilarityThreshold = 0.92
	}
	if d.LocationCandidates == nil {
		n := 5
		d.LocationCandidates = &n
	}
	if d.LocationLineWindow == 0 {
		d.LocationLineWindow = 10
	}
	if d.PremiseMargin == 0 {
		d.PremiseMargin = 0.10
	}
	if d.RevoteBand == 0 {
		d.RevoteBand = 0.10
	}
	if d.JudgeVotes == 0 {
		// Desligada por padrão: com a mesma entrada os votos saíram idênticos (rejudge de
		// 2026-10-10); a variação vinha da entrada, não do sorteio. judge_votes > 1 religa.
		d.JudgeVotes = 1
	}
	if d.BatchPollInterval == 0 {
		d.BatchPollInterval = 30 * time.Second
	}
	if d.MaxNewReposPerRun == nil {
		n := 1
		d.MaxNewReposPerRun = &n
	}
	if c.LLM.Provider == "" {
		c.LLM.Provider = ProviderClaudeCode
	}
	cc := &c.LLM.ClaudeCode
	if cc.Binary == "" {
		cc.Binary = "claude"
	}
	if cc.Concurrency == 0 {
		cc.Concurrency = 2
	}
	if cc.MaxCallsPerRun == 0 {
		cc.MaxCallsPerRun = 200
	}
	if cc.Timeout == 0 {
		cc.Timeout = 10 * time.Minute
	}
	if cc.WorkDir == "" {
		cc.WorkDir = "~/.cache/sentinela/claude-cwd"
	}
	cc.WorkDir = expandHome(cc.WorkDir)
	if c.Trigger.Mode == "" {
		c.Trigger.Mode = TriggerPolling
	}
	if c.Trigger.PollingInterval == 0 {
		c.Trigger.PollingInterval = 2 * time.Minute
	}
	if c.Trigger.ListenAddr == "" {
		c.Trigger.ListenAddr = "127.0.0.1:8080"
	}
	if c.Publish.MaxIssuesPerTeamPerRun == 0 {
		c.Publish.MaxIssuesPerTeamPerRun = 3
	}
	la := &c.LinearAuth
	if la.Grant == "" {
		la.Grant = GrantAuthCode
	}
	if len(la.Scopes) == 0 {
		la.Scopes = []string{"read", "write"}
	}
	if la.RedirectURI == "" {
		la.RedirectURI = DefaultLinearRedirect
	}
	if la.TokenFile == "" {
		la.TokenFile = "~/.config/sentinela/linear-token.json"
	}
	la.TokenFile = expandHome(la.TokenFile)
	e := &c.Execution
	if e.Concurrency == 0 {
		e.Concurrency = 1
	}
	// Fora de qualquer projeto: o Claude Code lê o CLAUDE.md do diretório atual e de todos os
	// pais, então um worktree dentro do repo do Sentinela levaria as instruções dele aos agentes.
	// Um diretório por instalação (ADR 0056): duas instalações na mesma máquina não dividem
	// worktrees, artefatos nem o HOME do job.
	e.DefaultDirs = 0
	root := InstallCacheDir()
	if e.Worktrees == "" {
		e.Worktrees, e.DefaultDirs = filepath.Join(root, "worktrees"), e.DefaultDirs|DirWorktrees
	}
	if e.Jobs == "" {
		e.Jobs, e.DefaultDirs = filepath.Join(root, "jobs"), e.DefaultDirs|DirJobs
	}
	if e.Home == "" {
		e.Home, e.DefaultDirs = filepath.Join(root, "job-home"), e.DefaultDirs|DirHome
	}
	if e.Runtime == "" {
		e.Runtime = RuntimeHost
	}
	if e.JobDocker == "" {
		e.JobDocker = JobDockerNone
	}
	if e.Container.Engine == "" {
		e.Container.Engine = "docker"
	}
	if e.Container.Image == "" {
		e.Container.Image = "sentinela-job:latest"
	}
	if len(e.Container.EgressAllow) == 0 {
		e.Container.EgressAllow = append([]string(nil), DefaultEgressAllow...)
	}
	if e.Container.DindImage == "" {
		e.Container.DindImage = "docker:27-dind"
	}
	e.Worktrees, e.Jobs, e.Home = expandHome(e.Worktrees), expandHome(e.Jobs), expandHome(e.Home)
	if e.Plugin == "" {
		e.Plugin = "plugins/sentinela-factory"
	}
	if e.Binary == "" {
		e.Binary = orStr(c.LLM.ClaudeCode.Binary, "claude")
	}
	if e.Timeout == 0 {
		e.Timeout = 60 * time.Minute
	}
	if e.IdleTimeout == 0 {
		e.IdleTimeout = 15 * time.Minute
	}
	if e.CompletionTimeout == 0 {
		e.CompletionTimeout = 2 * time.Minute
	}
	if e.FailedWorktreeRetention == 0 {
		e.FailedWorktreeRetention = 7 * 24 * time.Hour
	}
	if e.JobsRetention == 0 {
		e.JobsRetention = 90 * 24 * time.Hour
	}
	if e.WebhookEventsRetention == 0 {
		e.WebhookEventsRetention = 30 * 24 * time.Hour
	}
	if e.Poll == 0 {
		e.Poll = time.Minute
	}
	if e.MaxBudgetUSD == 0 {
		e.MaxBudgetUSD = c.Budget.PerPRUSD
	}
	if e.ShepherdMaxCycles == 0 {
		e.ShepherdMaxCycles = 3
	}
	if e.MaxResumes == 0 {
		e.MaxResumes = 2
	}
	if e.ReviewNotes == "" {
		e.ReviewNotes = "shadow"
	}
	if e.DisallowedTools == nil {
		e.DisallowedTools = []string{"Bash(git push:*)", "Bash(gh:*)", "Bash(curl:*)", "Bash(wget:*)", "WebFetch", "WebSearch"}
	}
	if c.Models.Shepherd == "" {
		c.Models.Shepherd = c.Models.ReviewerCode
	}
	// Configs anteriores à ADR 0024 não têm estas chaves: valem os modelos que elas usavam.
	if c.Models.Orchestrator == "" {
		c.Models.Orchestrator = c.Models.SpecWriter
	}
	if c.Models.Notes == "" {
		c.Models.Notes = c.Models.Analysis
	}
	if c.Feedback.CuratorInterval == 0 {
		c.Feedback.CuratorInterval = 7 * 24 * time.Hour
	}
	if c.Feedback.SuppressionTTL == 0 {
		c.Feedback.SuppressionTTL = 90 * 24 * time.Hour
	}
	if c.Feedback.MaxExamples == 0 {
		c.Feedback.MaxExamples = 6
	}
	if c.Telemetry.ServiceName == "" {
		c.Telemetry.ServiceName = "sentinela"
	}
	if c.Models.Refusal.Action == "" {
		c.Models.Refusal.Action = RefusalSkip
	}
	if c.Pricing.BatchDiscount == 0 {
		c.Pricing.BatchDiscount = 0.5
	}
	if c.Pricing.CacheWrite5mMultiplier == 0 {
		c.Pricing.CacheWrite5mMultiplier = 1.25
	}
	if c.Pricing.CacheWrite1hMultiplier == 0 {
		c.Pricing.CacheWrite1hMultiplier = 2.0
	}
	for _, t := range c.Teams {
		if t.Assignee == "" {
			t.Assignee = AssigneeNone
		}
	}
	for _, r := range c.Repos {
		if r.DefaultBranch == "" {
			r.DefaultBranch = "main"
		}
		if r.OwnersSource == "" {
			r.OwnersSource = c.Defaults.OwnersSource
		}
	}
}

// Validate checa a consistência interna, sem acessar Linear nem GitHub.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if c.Defaults.Linear.Team == "" && c.Adapters.Tracker != TrackerLocal {
		add("defaults.linear.team é obrigatório com adapters.tracker %s", c.Adapters.Tracker)
	}
	if a := c.Adapters; !slices.Contains([]string{TrackerLinearAPI, TrackerLinearMCP, TrackerLocal}, a.Tracker) {
		add("adapters.tracker inválido %q (use linear_api, linear_mcp ou local)", a.Tracker)
	}
	if a := c.Adapters; !slices.Contains([]string{NotifierSlackAPI, NotifierSlackMCP, NotifierNone}, a.Notifier) {
		add("adapters.notifier inválido %q (use slack_api, slack_mcp ou none)", a.Notifier)
	}
	if err := c.FixTrigger.Allowed.validate(); err != nil {
		add("%v", err)
	}
	for _, name := range c.TeamNames() {
		if ft := c.Teams[name].FixTrigger; ft != nil {
			if err := ft.Allowed.validate(); err != nil {
				add("teams.%s.%v", name, err)
			}
		}
	}
	if len(c.Defaults.Linear.CategoryProjects) > 0 {
		add("defaults.linear.category_projects: projeto por categoria só vale por time (teams.<time>.linear.category_projects)")
	}
	for _, name := range c.TeamNames() {
		for cat := range c.Teams[name].Linear.CategoryProjects {
			if !slices.Contains(Categories, cat) {
				add("teams.%s.linear.category_projects: categoria %q não existe (use %s)%s", name, cat, strings.Join(Categories, ", "), c.TeamFile(name))
			}
		}
	}
	for _, name := range c.TeamNames() {
		t := c.Teams[name]
		if len(t.Linear.CategoryProjects) > 0 && c.ModuleProject(name, t.Linear.Project) {
			add("teams.%s: linear.project %q é também o projeto de um módulo do time; com category_projects, o Sentinela não distingue os dois na publicação: use projetos diferentes%s", name, t.Linear.Project, c.TeamFile(name))
		}
	}
	for _, r := range c.Repos {
		for _, m := range r.Modules {
			if m.LinearProject != "" && m.Ignore {
				add("repos[%s].modules[%s]: linear_project num módulo ignorado não faz sentido%s", r.Name, m.Path, c.FileOf(m))
			}
		}
	}
	if c.LinearMCP() && c.Trigger.Mode == TriggerWebhook {
		add("adapters.tracker linear_mcp só funciona com trigger.mode polling: sem app, o Linear não tem para onde mandar webhook")
	}
	if m := c.Adapters.MCP; (c.LinearMCP() || c.Adapters.Notifier == NotifierSlackMCP) && (m.Model == "" || m.PollInterval < time.Minute) {
		add("adapters.mcp: model é obrigatório (ou um preço em pricing.models) e poll_interval tem de ser de pelo menos 1m")
	}
	if a := c.Adapters; !slices.Contains([]string{GitHostGitHubApps, GitHostGHCLI}, a.GitHost) {
		add("adapters.git_host inválido %q (use github_apps ou gh_cli)", a.GitHost)
	}
	if c.Adapters.GitHost == GitHostGHCLI && c.GitHubApps.ReviewerApproves {
		add("github_apps.reviewer_approves não vale com adapters.git_host gh_cli: o gh é a sua conta, e o gate só comenta")
	}
	if !validOwnersSource(c.Defaults.OwnersSource) {
		add("defaults.owners_source inválido: %q", c.Defaults.OwnersSource)
	}
	if c.Budget.PerRunUSD <= 0 {
		add("budget.per_run_usd deve ser maior que zero")
	}
	if c.Budget.PerPRUSD <= 0 {
		add("budget.per_pr_usd deve ser maior que zero")
	}
	if c.Models.RefusalFallback != "" {
		add("models.refusal_fallback só vale em repos[].models e teams.<time>.models; no global use models.refusal.fallback_model")
	}
	for _, r := range c.Models.roles() {
		if r.id == "" {
			add("models.%s é obrigatório", r.key)
		}
	}
	switch c.Models.Refusal.Action {
	case RefusalSkip:
	case RefusalFallbackModel:
		if c.Models.Refusal.FallbackModel == "" {
			add("models.refusal.fallback_model é obrigatório com action fallback_model")
		}
	default:
		add("models.refusal.action inválido: %q (use fallback_model ou skip)", c.Models.Refusal.Action)
	}
	var overrides []ModelRoles
	for _, r := range c.Repos {
		overrides = append(overrides, r.Models)
	}
	for _, t := range c.Teams {
		if t != nil {
			overrides = append(overrides, t.Models)
		}
	}
	for _, id := range c.Models.with(overrides...) {
		if _, ok := c.Pricing.Models[id]; !ok {
			add("modelo %q sem preço em pricing.models", id)
		}
	}
	for id, p := range c.Pricing.Models {
		if p == nil || p.Input <= 0 || p.Output <= 0 {
			add("pricing.models.%s: input e output devem ser maiores que zero", id)
		}
	}
	switch c.LLM.Provider {
	case ProviderAnthropicAPI, ProviderClaudeCode:
	case ProviderReplay:
		if c.LLM.Replay.Dir == "" {
			add("llm.replay.dir é obrigatório com provider replay")
		}
	default:
		add("llm.provider inválido %q (use anthropic_api, claude_code ou replay)", c.LLM.Provider)
	}
	if c.LLM.ClaudeCode.Concurrency < 1 || c.LLM.ClaudeCode.MaxCallsPerRun < 1 {
		add("llm.claude_code: concurrency e max_calls_per_run devem ser positivos")
	}
	if d := c.Discovery; d.TriageContextTokens >= haikuLongContextLimit && c.triageUsesHaiku() {
		add("discovery.triage_context_tokens deve ficar abaixo de 100000 com a triagem em Haiku (faixa de preço do Haiku); o perfil qualidade usa Opus na triagem")
	}
	if d := c.Discovery; d.MinConfidence < 0 || d.MinConfidence > 1 || d.SimilarityThreshold <= 0 || d.SimilarityThreshold > 1 {
		add("discovery.min_confidence e similarity_threshold devem estar entre 0 e 1")
	}
	if d := c.Discovery; (d.LocationCandidates != nil && *d.LocationCandidates < 0) || d.LocationLineWindow < 0 {
		add("discovery.location_candidates e location_line_window não podem ser negativos")
	}
	if d := c.Discovery; d.MaxNewReposPerRun != nil && *d.MaxNewReposPerRun < 0 {
		add("discovery.max_new_repos_per_run não pode ser negativo")
	}
	for _, n := range c.Discovery.Scanners {
		if !slices.Contains([]string{"golangci-lint", "gosec", "govulncheck", "semgrep", "go-test", "osv-scanner", "eslint", "tsc"}, n) {
			add("discovery.scanners: scanner desconhecido %q", n)
		}
	}
	switch c.Trigger.Mode {
	case TriggerPolling, TriggerWebhook:
	default:
		add("trigger.mode inválido %q (use polling ou webhook)", c.Trigger.Mode)
	}
	switch c.LinearAuth.Mode {
	case "", LinearAuthApp, LinearAuthAPIKey:
	default:
		add("linear_auth.mode inválido %q (use app ou api_key)", c.LinearAuth.Mode)
	}
	switch c.LinearAuth.Grant {
	case GrantAuthCode, GrantClientCreds:
	default:
		add("linear_auth.grant inválido %q (use authorization_code ou client_credentials)", c.LinearAuth.Grant)
	}
	if c.Publish.MaxIssuesPerTeamPerRun < 1 {
		add("publish.max_issues_per_team_per_run deve ser positivo")
	}
	switch c.Execution.ReviewNotes {
	case "off", "shadow", "publish":
	default:
		add("execution.review_notes inválido %q (use off, shadow ou publish)", c.Execution.ReviewNotes)
	}
	if r := c.Execution.Runtime; r != RuntimeHost && r != RuntimeContainer {
		add("execution.runtime inválido %q (use host ou container)", r)
	}
	if d := c.Execution.JobDocker; d != JobDockerNone && d != JobDockerDind {
		add("execution.job_docker inválido %q (use none ou dind)", d)
	}
	if e := c.Execution.Container.Engine; e != "docker" && e != "podman" {
		add("execution.container.engine inválido %q (use docker ou podman)", e)
	}
	if c.Execution.JobDocker == JobDockerDind && c.Execution.Runtime != RuntimeContainer {
		add("execution.job_docker: dind só vale com execution.runtime: container")
	}
	for lang, cs := range c.Discovery.SemgrepLanguages {
		for _, cfg := range cs {
			if strings.TrimSpace(cfg) == "" {
				add("discovery.semgrep_languages.%s: config vazia", lang)
			}
		}
	}
	for _, f := range c.Execution.HomeFiles {
		if msg := homeFileProblem(f); msg != "" {
			add("execution.home_files: %q %s", f, msg)
		}
	}
	for where, l := range c.allLabels() {
		if ForeignLabel(l) {
			add("%s: label %q é de outro sistema (claude-*); o Sentinela usa só as suas", where, l)
		}
	}
	for _, g := range c.BlockedPaths {
		if !doublestar.ValidatePattern(g) {
			add("blocked_paths: glob inválido %q", g)
		}
	}

	for _, name := range c.TeamNames() {
		t := c.Teams[name]
		if t == nil {
			add("teams.%s está vazio", name)
			continue
		}
		switch t.Assignee {
		case AssigneeNone:
		case AssigneeLead:
			if t.Lead == "" {
				add("teams.%s: assignee lead exige lead", name)
			}
		case AssigneeRoundRobin:
			if len(t.Members) == 0 {
				add("teams.%s: assignee round_robin exige members", name)
			}
		default:
			add("teams.%s: assignee inválido %q", name, t.Assignee)
		}
	}

	seen := map[string]bool{}
	for i, r := range c.Repos {
		if r.Name == "" || strings.Count(r.Name, "/") != 1 {
			add("repos[%d]: name deve ser owner/repo, recebido %q", i, r.Name)
			continue
		}
		if seen[r.Name] {
			add("repos: %s duplicado", r.Name)
		}
		seen[r.Name] = true
		if !validOwnersSource(r.OwnersSource) {
			add("repos %s: owners_source inválido %q", r.Name, r.OwnersSource)
		}
		c.checkTeamRef(r.Team, "repos "+r.Name+".team", add)
		c.checkTeamRef(r.FallbackTeam, "repos "+r.Name+".fallback_team", add)
		for j, m := range r.Modules {
			where := fmt.Sprintf("repos %s.modules[%d]", r.Name, j)
			if !doublestar.ValidatePattern(m.Path) || m.Path == "" {
				add("%s: glob inválido %q", where, m.Path)
			}
			if !m.Ignore && m.Team == "" {
				add("%s: team é obrigatório quando ignore é false", where)
			}
			c.checkTeamRef(m.Team, where+".team", add)
		}
	}
	return errors.Join(errs...)
}

// ForeignLabel identifica labels de outro sistema (as scheduled tasks usam claude-*), que o
// Sentinela nunca aplica nem lê.
func ForeignLabel(name string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "claude-")
}

// allLabels devolve cada label configurada com onde ela aparece.
func (c *Config) allLabels() map[string]string {
	out := map[string]string{}
	for i, l := range c.Defaults.Linear.Labels {
		out[fmt.Sprintf("defaults.linear.labels[%d]", i)] = l
	}
	for _, n := range c.TeamNames() {
		if t := c.Teams[n]; t != nil {
			for i, l := range t.Linear.Labels {
				out[fmt.Sprintf("teams.%s.linear.labels[%d]", n, i)] = l
			}
			for k, l := range map[string]string{"trigger": t.Labels.Trigger, "done": t.Labels.Done, "failed": t.Labels.Failed} {
				if l != "" {
					out[fmt.Sprintf("teams.%s.labels.%s", n, k)] = l
				}
			}
		}
	}
	for k, l := range map[string]string{"trigger": c.Labels.Trigger, "done": c.Labels.Done, "failed": c.Labels.Failed, "finding": c.Labels.Finding,
		"question": c.Labels.Question, "intentional": c.Labels.Intentional, "not_intentional": c.Labels.NotIntentional} {
		if l != "" {
			out["labels."+k] = l
		}
	}
	return out
}

// IssueLabels devolve as labels de uma issue de achado para o time: as padrão, as do time e a
// label de achado, sem repetição.
func (c *Config) IssueLabels(t *Team) []string {
	var out []string
	add := func(ls ...string) {
		for _, l := range ls {
			if l != "" && !slices.Contains(out, l) {
				out = append(out, l)
			}
		}
	}
	add(c.Defaults.Linear.Labels...)
	if t != nil {
		add(t.Linear.Labels...)
	}
	add(c.Labels.Finding)
	return out
}

func (c *Config) checkTeamRef(name, where string, add func(string, ...any)) {
	if name == "" {
		return
	}
	if _, ok := c.Teams[name]; !ok {
		add("%s: time %q não existe em teams", where, name)
	}
}

func validOwnersSource(s OwnersSource) bool {
	return s == OwnersCodeowners || s == OwnersConfig || s == OwnersBoth
}

// TeamNames devolve os nomes dos times em ordem alfabética, para saídas determinísticas.
func (c *Config) TeamNames() []string {
	names := make([]string, 0, len(c.Teams))
	for n := range c.Teams {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Repo procura um repo pelo nome owner/repo.
func (c *Config) Repo(name string) (*Repo, bool) {
	for _, r := range c.Repos {
		if strings.EqualFold(r.Name, name) {
			return r, true
		}
	}
	return nil, false
}

// LinearTeamFor devolve a chave do time do Linear usada por um time de engenharia.
func (c *Config) LinearTeamFor(t *Team) string {
	if t != nil && t.Linear.Team != "" {
		return t.Linear.Team
	}
	return c.Defaults.Linear.Team
}

// TriageStateFor devolve o estado inicial das issues de um time.
func (c *Config) TriageStateFor(t *Team) string {
	if t != nil && t.Linear.TriageState != "" {
		return t.Linear.TriageState
	}
	return c.Defaults.Linear.TriageState
}

// ReviewStateFor devolve o estado da issue com PR aberto de um time ("" = não move).
func (c *Config) ReviewStateFor(t *Team) string {
	if t != nil && t.Linear.ReviewState != "" {
		return t.Linear.ReviewState
	}
	return c.Defaults.Linear.ReviewState
}

// DefaultDoneState é o estado de concluído quando nem o time nem defaults definem done_state.
const DefaultDoneState = "Done"

// DoneStateFor devolve o estado da issue com PR mergeado de um time (ADR 0028).
func (c *Config) DoneStateFor(t *Team) string {
	if t != nil && t.Linear.DoneState != "" {
		return t.Linear.DoneState
	}
	if c.Defaults.Linear.DoneState != "" {
		return c.Defaults.Linear.DoneState
	}
	return DefaultDoneState
}

// IsBlockedPath informa se o caminho casa com algum glob de blocked_paths.
func (c *Config) IsBlockedPath(path string) bool {
	path = strings.TrimPrefix(path, "/")
	for _, g := range c.BlockedPaths {
		if ok, _ := doublestar.Match(g, path); ok {
			return true
		}
	}
	return false
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// expandHome troca o "~/" inicial pelo diretório do usuário.
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return home + p[1:]
}

// Watchdog devolve os timeouts de inatividade e de conclusão a passar ao runner (negativo
// desliga e vira zero).
func (e Execution) Watchdog() (idle, completion time.Duration) {
	return max(e.IdleTimeout, 0), max(e.CompletionTimeout, 0)
}

// LegacyCacheDir é o diretório que todas as instalações dividiam antes da ADR 0056.
const LegacyCacheDir = "~/.cache/sentinela"

// InstallKey identifica a instalação do diretório dir: os 8 primeiros hex do sha256 do caminho,
// o mesmo esquema do COMPOSE_PROJECT_NAME que o init grava (ADR 0052).
func InstallKey(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(sum[:])[:8]
}

// InstallDir é o diretório da instalação: o atual, absoluto e sem links simbólicos (o mesmo que
// o banco registra; as units rodam no SENTINELA_HOME).
func InstallDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(wd); err == nil {
		wd = real
	}
	abs, err := filepath.Abs(wd)
	if err != nil {
		return wd
	}
	return abs
}

// InstallCacheDir é o diretório desta instalação em ~/.cache/sentinela (ADR 0056).
func InstallCacheDir() string {
	return filepath.Join(LegacyCacheDir, InstallKey(InstallDir()))
}

// LegacyExecutionDirs são os worktrees, os artefatos e o HOME do job compartilhados de antes da
// ADR 0056, já expandidos.
func LegacyExecutionDirs() (worktrees, jobs, home string) {
	return expandHome(LegacyCacheDir + "/worktrees"), expandHome(LegacyCacheDir + "/jobs"), expandHome(LegacyCacheDir + "/job-home")
}

// ExecDirs marca diretórios de execution (worktrees, jobs e home).
type ExecDirs uint8

const (
	DirWorktrees ExecDirs = 1 << iota
	DirJobs
	DirHome
)

// homeFilesForbidden são as credenciais que o HOME do job existe para não ter (ADR 0049): git,
// ssh, gh, o token do Sentinela e o login do Claude Code (este o job já recebe, sincronizado).
var homeFilesForbidden = []string{".ssh", ".gnupg", ".gitconfig", ".git-credentials", ".config/git", ".config/gh", ".config/sentinela", ".claude", ".claude.json", ".docker", ".kube", ".aws", ".netrc"}

// homeFileProblem diz por que um caminho de execution.home_files não vale ("" se vale).
func homeFileProblem(f string) string {
	clean := filepath.ToSlash(filepath.Clean(f))
	switch {
	case strings.TrimSpace(f) == "":
		return "é vazio"
	case filepath.IsAbs(f) || strings.HasPrefix(f, "~"):
		return "precisa ser relativo ao HOME da máquina (ex.: .npmrc)"
	case clean == ".." || strings.HasPrefix(clean, "../"):
		return "sai do HOME da máquina"
	}
	for _, bad := range homeFilesForbidden {
		if clean == bad || strings.HasPrefix(clean, bad+"/") {
			return "é credencial da máquina que o job não pode ler (ADR 0049); leve só configuração de registro com token de leitura"
		}
	}
	return ""
}

// DefaultSemgrepLanguages são os pacotes de regras do registro do semgrep por linguagem
// (https://semgrep.dev/explore), somados à discovery.semgrep_config quando a linguagem está no
// repo. O p/default tem poucas regras de Java, Kotlin e TypeScript. Para front e BFF em Node:
// p/nodejs e p/expressjs (o pacote oficial do Express; o p/nestjs do registro estava vazio em
// 2026-10-18, por isso o Nest não tem pacote próprio). O semgrep não repete a regra que aparece em
// mais de um pacote.
var DefaultSemgrepLanguages = map[string][]string{
	"java":       {"p/java"},
	"kotlin":     {"p/kotlin"},
	"typescript": {"p/typescript", "p/react", "p/nodejs", "p/expressjs"},
	"javascript": {"p/javascript", "p/nodejs", "p/expressjs"},
}
