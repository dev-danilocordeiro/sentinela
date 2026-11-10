package repo

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// Routing é a decisão de ownership gravada no Finding (campo "routing"), para que
// mudanças futuras no CODEOWNERS não reescrevam o passado.
type Routing struct {
	Team          string `json:"team"`
	LinearTeam    string `json:"linear_team"`
	LinearProject string `json:"linear_project"`
	Assignee      string `json:"assignee"`
	Rule          string `json:"rule"`
}

// Resolution é o resultado completo do resolvedor: além do Routing, traz o que o
// publisher e o gate precisam (inscritos, estado inicial, aviso, política) e o
// rastro usado por `sentinela owners explain`.
type Resolution struct {
	Routing
	Repo         string
	Path         string
	Ignored      bool
	Owners       []string // donos brutos da regra do CODEOWNERS que casou, se houver
	Subscribers  []string // demais times da regra com vários donos
	AssigneeMode config.AssigneeMode
	TriageState  string
	NotifySlack  string
	Policy       config.Policy
	Warnings     []string
	Trace        []string
	// ProjectSource diz de onde veio o projeto: teams.<t>.linear.project, repos[r].linear_project
	// ou defaults.linear.project. Fica fora do Routing gravado no achado.
	ProjectSource string
	// NotifySource, Mention e MentionSource: o canal do aviso e a menção efetivos e de onde vieram.
	NotifySource, Mention, MentionSource string
	// Pattern é o módulo que casou: o glob da config ou o padrão da regra do CODEOWNERS.
	Pattern string
	// CategoryProjects são os projetos por categoria do time: valem na publicação quando o
	// projeto não vem de um módulo.
	CategoryProjects []string
	// Generic indica que o dono veio de uma regra que cobre o repo inteiro: o "*" do CODEOWNERS
	// (ou equivalente), repos[].team, fallback_team ou defaults. Specificity é o tamanho do
	// padrão que casou (glob de modules ou do CODEOWNERS); maior é mais específico.
	Generic     bool
	Specificity int
}

// genericPatterns são padrões do CODEOWNERS que casam com o repo inteiro.
var genericPatterns = map[string]bool{"*": true, "**": true, "/*": true, "/**": true, "**/*": true, "/": true}

// CodeownersSource devolve o CODEOWNERS de um repo; nil sem erro significa "o repo não tem".
type CodeownersSource interface {
	Codeowners(repo string) (*Codeowners, error)
}

// StaticCodeowners é um CodeownersSource em memória.
type StaticCodeowners map[string]*Codeowners

func (s StaticCodeowners) Codeowners(repo string) (*Codeowners, error) { return s[repo], nil }

// ErrUnknownRepo indica um repo ausente do sentinela.yaml.
var ErrUnknownRepo = errors.New("repo não está no sentinela.yaml")

// Resolver aplica as regras de roteamento do sentinela.yaml, nesta ordem:
//  1. modules[].path da config, vencendo o glob mais longo;
//  2. CODEOWNERS com a semântica do GitHub (última regra que casa vence);
//  3. repos[].team;
//  4. repos[].fallback_team e, por fim, defaults.
type Resolver struct {
	cfg        *config.Config
	codeowners CodeownersSource
}

func NewResolver(cfg *config.Config, co CodeownersSource) *Resolver {
	return &Resolver{cfg: cfg, codeowners: co}
}

// Resolve decide o dono de um caminho de um repo.
func (r *Resolver) Resolve(repoName, path string) (Resolution, error) {
	rp, ok := r.cfg.Repo(repoName)
	if !ok {
		return Resolution{}, fmt.Errorf("%w: %s", ErrUnknownRepo, repoName)
	}
	path = strings.TrimPrefix(path, "/")
	res := Resolution{Repo: rp.Name, Path: path}
	res.tracef("owners_source=%s", rp.OwnersSource)

	if rp.OwnersSource == config.OwnersCodeowners {
		res.tracef("passo 1 (modules) pulado: owners_source=codeowners")
	} else if done := r.fromModules(rp, &res); done {
		return res, nil
	}

	if rp.OwnersSource == config.OwnersConfig {
		res.tracef("passo 2 (CODEOWNERS) pulado: owners_source=config")
	} else {
		done, err := r.fromCodeowners(rp, &res)
		if err != nil {
			return Resolution{}, err
		}
		if done {
			return res, nil
		}
	}

	if rp.Team != "" {
		res.tracef("passo 3: repos[].team = %s", rp.Team)
		r.finish(&res, rp, rp.Team, "repo")
		res.Generic = true
		return res, nil
	}
	res.tracef("passo 3: repo sem team")

	if rp.FallbackTeam != "" {
		res.tracef("passo 4: repos[].fallback_team = %s", rp.FallbackTeam)
		r.finish(&res, rp, rp.FallbackTeam, "fallback")
		res.Generic = true
		return res, nil
	}
	res.tracef("passo 4: sem fallback_team; usando defaults (time do Linear %s)", r.cfg.Defaults.Linear.Team)
	r.finish(&res, rp, "", "defaults")
	res.Generic = true
	return res, nil
}

// FindingPaths são os caminhos de um achado que o roteamento considera (ADR 0017).
type FindingPaths struct {
	Primary  string   // arquivo da evidência principal
	Scope    []string // scope_paths, na ordem do achado
	Evidence []string // demais arquivos de evidência
	Refs     []string // pacotes que o código da evidência ou a correção citam (ex.: turmas.ErrX)
}

// ResolveFinding decide o dono de um achado. Vale o dono do arquivo da evidência principal,
// exceto quando ele vem de regra genérica ("*" do CODEOWNERS, repos[].team, fallback ou
// defaults): aí vence o caminho mais específico de scope_paths; sem nenhum, o das demais
// evidências; sem nenhum, o dos pacotes referenciados. Só se nada casar com regra
// específica fica a genérica. Foi o caso das EX-14 e EX-15, publicadas na plataforma pelo
// "*" do CODEOWNERS embora o escopo e a correção fossem do acadêmico.
func (r *Resolver) ResolveFinding(repoName string, p FindingPaths) (Resolution, error) {
	primary, err := r.Resolve(repoName, p.Primary)
	if err != nil || primary.Ignored || !primary.Generic {
		return primary, err
	}
	groups := []struct {
		name  string
		paths []string
	}{{"scope", p.Scope}, {"evidência", p.Evidence}, {"referência", p.Refs}}
	for _, g := range groups {
		var best *Resolution
		for _, path := range g.paths {
			for _, form := range pathForms(path) {
				res, err := r.Resolve(repoName, form)
				if err != nil {
					return Resolution{}, err
				}
				if res.Ignored || res.Generic {
					continue
				}
				if best == nil || res.Specificity > best.Specificity {
					b := res
					best = &b
				}
			}
		}
		if best != nil {
			out := *best
			out.Trace = append(append([]string{}, primary.Trace...), best.Trace...)
			out.tracef("evidência principal %s caiu em regra genérica (%s); vence %s %s (%s)", primary.Path, primary.Rule, g.name, best.Path, best.Rule)
			out.Rule = fmt.Sprintf("%s via %s:%s", best.Rule, g.name, strings.TrimSuffix(best.Path, "/"))
			out.Path = primary.Path
			out.Warnings = append(append([]string{}, primary.Warnings...), best.Warnings...)
			return out, nil
		}
	}
	return primary, nil
}

// pathForms devolve o caminho como veio e, se não termina em "/", também como diretório:
// scope_paths costuma trazer diretórios ("go/internal/turmas"), e regras de diretório do
// CODEOWNERS ("/go/internal/turmas/") só casam com o que está dentro deles.
func pathForms(p string) []string {
	p = strings.TrimPrefix(strings.TrimSpace(p), "/")
	if p == "" || p == "." {
		return nil
	}
	if strings.HasSuffix(p, "/") {
		return []string{p}
	}
	return []string{p, p + "/"}
}

func (r *Resolver) fromModules(rp *config.Repo, res *Resolution) bool {
	best := -1
	for i, m := range rp.Modules {
		if ok, _ := doublestar.Match(m.Path, res.Path); !ok {
			continue
		}
		// Um override vence os módulos comuns (ADR 0043); entre iguais, o glob mais longo é o mais
		// específico, e no empate vence o declarado primeiro.
		if best < 0 {
			best = i
			continue
		}
		b := rp.Modules[best]
		if (m.Override && !b.Override) || (m.Override == b.Override && len(m.Path) > len(b.Path)) {
			best = i
		}
	}
	if best < 0 {
		res.tracef("passo 1: nenhum modules[].path casou")
		return false
	}
	m := rp.Modules[best]
	rule := "config:" + m.Path
	res.Pattern = m.Path
	if m.Ignore {
		res.tracef("passo 1: %s casou com ignore=true; caminho fora da análise", m.Path)
		res.Ignored = true
		res.Rule = rule
		return true
	}
	if m.Override {
		res.tracef("passo 1: %s casou (override: vence os módulos comuns) -> time %s", m.Path, m.Team)
	} else {
		res.tracef("passo 1: %s casou (glob mais específico) -> time %s", m.Path, m.Team)
	}
	r.finish(res, rp, m.Team, rule)
	if m.LinearProject != "" {
		// O projeto do módulo vence o do time e o do repo (ADR 0040).
		res.LinearProject = m.LinearProject
		res.ProjectSource = "repos[" + rp.Name + "].modules[" + m.Path + "].linear_project" + r.cfg.FileOf(m)
	}
	res.Generic, res.Specificity = genericPatterns[m.Path], len(m.Path)
	return true
}

func (r *Resolver) fromCodeowners(rp *config.Repo, res *Resolution) (bool, error) {
	co, err := r.codeowners.Codeowners(rp.Name)
	if err != nil {
		return false, fmt.Errorf("carregando CODEOWNERS de %s: %w", rp.Name, err)
	}
	if co == nil {
		res.tracef("passo 2: repo sem CODEOWNERS")
		return false, nil
	}
	rule, ok := co.Match(res.Path)
	if ok {
		res.Pattern = rule.Pattern
	}
	if !ok {
		res.tracef("passo 2: nenhuma regra do CODEOWNERS casou")
		return false, nil
	}
	if len(rule.Owners) == 0 {
		res.tracef("passo 2: regra L%d (%s) casou sem donos; caminho sem dono no CODEOWNERS", rule.Line, rule.Pattern)
		return false, nil
	}
	res.Owners = rule.Owners
	var teams []string
	for _, owner := range rule.Owners {
		team, how := r.TeamForOwner(owner)
		if team == "" {
			res.warnf("owner %s do CODEOWNERS (L%d) sem time mapeado no sentinela.yaml", owner, rule.Line)
			continue
		}
		res.tracef("passo 2: owner %s -> time %s (%s)", owner, team, how)
		if !contains(teams, team) {
			teams = append(teams, team)
		}
	}
	if len(teams) == 0 {
		res.tracef("passo 2: regra L%d (%s) casou, mas nenhum owner tem time; seguindo", rule.Line, rule.Pattern)
		return false, nil
	}
	res.tracef("passo 2: regra L%d (%s) casou, última que casa vence", rule.Line, rule.Pattern)
	res.Subscribers = teams[1:]
	r.finish(res, rp, teams[0], fmt.Sprintf("codeowners:L%d", rule.Line))
	res.Generic, res.Specificity = genericPatterns[rule.Pattern], len(rule.Pattern)
	if res.Generic {
		res.tracef("passo 2: L%d (%s) é regra genérica", rule.Line, rule.Pattern)
	}
	return true, nil
}

// TeamForOwner mapeia um owner do CODEOWNERS (@org/time, @usuario ou e-mail) para um
// time do sentinela.yaml. Usuário individual resolve primeiro pelo time de que é
// membro e só depois pelo teams[].github. Devolve "" quando não há mapeamento.
func (r *Resolver) TeamForOwner(owner string) (team, how string) {
	isUser := !strings.Contains(owner, "/")
	if isUser {
		login := strings.TrimPrefix(owner, "@")
		for _, name := range r.cfg.TeamNames() {
			for _, m := range r.cfg.Teams[name].Members {
				if strings.EqualFold(strings.TrimPrefix(m, "@"), login) {
					return name, "membro de teams." + name
				}
			}
		}
	}
	for _, name := range r.cfg.TeamNames() {
		for _, g := range r.cfg.Teams[name].GitHub {
			if strings.EqualFold(g, owner) {
				return name, "teams." + name + ".github"
			}
		}
	}
	return "", ""
}

// finish preenche board, projeto, estado, assignee, aviso e política a partir do time.
func (r *Resolver) finish(res *Resolution, rp *config.Repo, teamName, rule string) {
	cfg := r.cfg
	team := cfg.Teams[teamName]
	res.Team = teamName
	res.Rule = rule
	res.LinearTeam = cfg.LinearTeamFor(team)
	res.TriageState = cfg.TriageStateFor(team)
	res.NotifySlack, res.NotifySource = cfg.Defaults.Notify.Slack, "defaults.notify.slack"
	if res.NotifySlack == "" {
		res.NotifySource = "nenhum (sem aviso)"
	}
	res.MentionSource = "nenhuma"
	res.Policy = cfg.Defaults.Policy
	res.AssigneeMode = config.AssigneeNone

	// Time > repo > defaults (último recurso).
	res.LinearProject, res.ProjectSource = cfg.ProjectFor(team, teamName, rp)
	if team == nil {
		return
	}
	if team.Notify.Slack != "" {
		res.NotifySlack, res.NotifySource = team.Notify.Slack, "teams."+teamName+".notify.slack"+cfg.TeamFile(teamName)
	}
	if team.Notify.Mention != "" {
		res.Mention, res.MentionSource = team.Notify.Mention, "teams."+teamName+".notify.mention"+cfg.TeamFile(teamName)
	}
	for _, cat := range config.Categories {
		if p := team.Linear.CategoryProjects[cat]; p != "" {
			res.CategoryProjects = append(res.CategoryProjects, cat+" -> "+p)
		}
	}
	res.Policy = team.Policy.Merge(cfg.Defaults.Policy)
	res.AssigneeMode = team.Assignee
	if team.Assignee == config.AssigneeLead {
		res.Assignee = team.Lead
	}
	// round_robin é decidido pelo publisher (Fase 3), que conhece o histórico de atribuições.
}

func (res *Resolution) tracef(format string, args ...any) {
	res.Trace = append(res.Trace, fmt.Sprintf(format, args...))
}

func (res *Resolution) warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	res.Warnings = append(res.Warnings, msg)
	res.Trace = append(res.Trace, "aviso: "+msg)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
