package cli

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/github"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/notify"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// LinearAPI é o que validate e bootstrap precisam do Linear.
type LinearAPI interface {
	Workspace(ctx context.Context) (*linear.Workspace, error)
}

// GitHubAPI é o que validate, bootstrap e explain precisam do GitHub.
type GitHubAPI interface {
	RepoExists(ctx context.Context, fullName string) (bool, error)
	TeamExists(ctx context.Context, org, slug string) (bool, error)
	UserExists(ctx context.Context, login string) (bool, error)
	Codeowners(ctx context.Context, fullName string) (*repo.Codeowners, error)
	// CommandsDoc: o AGENTS.md ou CLAUDE.md na raiz da branch ref; "" se não há nenhum.
	CommandsDoc(ctx context.Context, fullName, ref string) (string, error)
	// Languages: bytes de código por linguagem do linguist (GET /repos/{repo}/languages).
	Languages(ctx context.Context, fullName string) (map[string]int64, error)
}

// SlackAPI é o que validate precisa do Slack.
type SlackAPI interface {
	Channel(ctx context.Context, ref string) (notify.ChannelInfo, error)
}

// GitHubAppAPI é o que validate precisa de cada GitHub App (tudo leitura, exceto gerar o token
// de instalação, que não muda nada no GitHub).
type GitHubAppAPI interface {
	Info(ctx context.Context) (github.AppInfo, error)
	Installation(ctx context.Context) (github.Installation, error)
	NewToken(ctx context.Context) (github.InstallationToken, error)
	Repositories(ctx context.Context) ([]string, error)
}

// AppCheck é uma app a validar e o que o papel dela exige.
type AppCheck struct {
	Role string // "bot" ou "reviewer"
	App  GitHubAppAPI
	Err  error // erro ao carregar a app (variável faltando, chave ilegível)
	// Missing: nenhuma variável da app definida. Vira aviso: a linha de execução (Fase 4) não
	// roda sem as apps, mas a descoberta não depende delas.
	Missing bool
}

// Permissões mínimas e proibidas por papel. A revisora não escreve código: contents além de
// read é erro. Nenhuma das duas administra repo nem edita workflows.
var (
	appRequired = map[string]map[string]string{
		"bot":      {"contents": "write", "pull_requests": "write", "metadata": "read", "checks": "read", "statuses": "read"},
		"reviewer": {"pull_requests": "write", "contents": "read", "metadata": "read"},
	}
	appForbidden = map[string]map[string]string{
		"bot":      {"administration": "read", "workflows": "write"},
		"reviewer": {"contents": "write", "administration": "read", "workflows": "write"},
	}
	permLevel = map[string]int{"": 0, "read": 1, "write": 2, "admin": 3}
)

// Report separa erros (config quebrada) de avisos (algo a revisar, sem bloquear). Info são
// fatos conferidos que valem mostrar (repos e permissões efetivas das apps).
type Report struct {
	Errors   []string
	Warnings []string
	Info     []string
}

func (r *Report) infof(format string, args ...any) {
	r.Info = append(r.Info, fmt.Sprintf(format, args...))
}

func (r *Report) errorf(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

func (r *Report) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// ValidateRemote confere a config contra o Linear e o GitHub reais (ou mockados nos testes).
// slack nil significa SLACK_BOT_TOKEN vazio: os canais não são checados e isso vira aviso.
func ValidateRemote(ctx context.Context, cfg *config.Config, lin LinearAPI, gh GitHubAPI, slack SlackAPI, apps ...AppCheck) Report {
	var rep Report
	if !cfg.LocalTracker() {
		for _, line := range effectiveProjects(cfg) {
			rep.infof("%s", line)
		}
		validateFixTrigger(cfg, &rep)
	}
	if cfg.LocalTracker() {
		rep.infof("adapters.tracker local: achados em %s, decisões por `sentinela finding` (Linear não é conferido)", cfg.Adapters.LocalDir)
	} else {
		if cfg.LinearMCP() {
			rep.warnf("adapters.tracker linear_mcp: issues e comentários saem como você (a conta do MCP %q), identificados pelo prefixo **Sentinela; para uso real, o certo é o app OAuth (linear_api, ADR 0032). Leituras a cada %s, com %s", cfg.Adapters.MCP.LinearServer, cfg.Adapters.MCP.PollInterval, cfg.Adapters.MCP.Model)
		}
		validateLinear(ctx, cfg, lin, &rep)
	}
	validateGitHub(ctx, cfg, gh, &rep)
	switch cfg.Adapters.Notifier {
	case config.NotifierNone:
		rep.infof("adapters.notifier none: sem avisos no Slack")
	case config.NotifierSlackMCP:
		rep.warnf("adapters.notifier slack_mcp: os avisos saem como você, pelo MCP %q (uma chamada `claude -p` por aviso); os canais não são conferidos", cfg.Adapters.MCP.SlackServer)
	default:
		validateSlack(ctx, cfg, slack, &rep)
	}
	if cfg.Adapters.GitHost == config.GitHostGHCLI {
		rep.infof("adapters.git_host gh_cli: PRs em draft pelo gh do usuário; o gate só comenta (GitHub Apps não são conferidas)")
	} else {
		validateApps(ctx, cfg, apps, &rep)
	}
	return rep
}

// validateApps confere cada GitHub App: slug igual ao da config, instalação ativa, token de
// instalação gerado, permissões efetivas suficientes e sem excesso, e acesso a todos os repos
// do sentinela.yaml.
func validateApps(ctx context.Context, cfg *config.Config, apps []AppCheck, rep *Report) {
	if len(apps) == 0 {
		return
	}
	if cfg.GitHubApps.ReviewerApproves {
		rep.warnf("github_apps.reviewer_approves: true; a revisora pode aprovar PRs onde a política do time permitir")
	}
	for _, a := range apps {
		where := "github_apps." + a.Role
		want := cfg.GitHubApps.Bot
		if a.Role == "reviewer" {
			want = cfg.GitHubApps.Reviewer
		}
		if a.Missing {
			rep.warnf("%s (%s): GITHUB_%s_APP_ID, _INSTALLATION_ID e _PRIVATE_KEY_PATH não definidas; a linha de execução não roda sem ela", where, want, strings.ToUpper(a.Role))
			continue
		}
		if a.Err != nil {
			rep.errorf("%s: %v", where, a.Err)
			continue
		}
		info, err := a.App.Info(ctx)
		if err != nil {
			rep.errorf("%s: autenticando a app (JWT): %v", where, err)
			continue
		}
		if want != "" && info.Slug != want {
			rep.errorf("%s: a chave é da app %q, a config espera %q", where, info.Slug, want)
		}
		inst, err := a.App.Installation(ctx)
		if err != nil {
			rep.errorf("%s: lendo a instalação: %v", where, err)
			continue
		}
		if inst.SuspendedAt != nil {
			rep.errorf("%s: instalação %d suspensa", where, inst.ID)
		}
		tok, err := a.App.NewToken(ctx)
		if err != nil {
			rep.errorf("%s: gerando token de instalação: %v", where, err)
			continue
		}
		perms := tok.Permissions
		var granted []string
		for _, k := range sortedKeys(perms) {
			granted = append(granted, k+":"+perms[k])
		}
		for perm, level := range appRequired[a.Role] {
			if permLevel[perms[perm]] < permLevel[level] {
				rep.errorf("%s (%s): falta %s:%s (efetiva: %q)", where, info.Slug, perm, level, perms[perm])
			}
		}
		for perm, level := range appForbidden[a.Role] {
			if permLevel[perms[perm]] >= permLevel[level] {
				rep.errorf("%s (%s): permissão demais %s:%s; o papel %s não precisa dela", where, info.Slug, perm, perms[perm], a.Role)
			}
		}
		repos, err := a.App.Repositories(ctx)
		if err != nil {
			rep.errorf("%s: listando repos da instalação: %v", where, err)
			continue
		}
		have := map[string]bool{}
		for _, r := range repos {
			have[strings.ToLower(r)] = true
		}
		for _, r := range cfg.Repos {
			if !have[strings.ToLower(r.Name)] {
				rep.errorf("%s (%s): sem acesso ao repo %s; instale a app nele", where, info.Slug, r.Name)
			}
		}
		if tok.RepositorySelection == "all" && len(repos) > len(cfg.Repos) {
			rep.warnf("%s (%s): instalada em todos os repos da conta (%d), o sentinela.yaml usa %d; prefira \"Only select repositories\"", where, info.Slug, len(repos), len(cfg.Repos))
		}
		slices.Sort(repos)
		rep.infof("%s %s: instalação %d (%s, %s), token ok até %s", a.Role, info.Slug, inst.ID, inst.Account.Login, tok.RepositorySelection, tok.ExpiresAt.Format("15:04 MST"))
		rep.infof("%s %s: permissões efetivas %s", a.Role, info.Slug, strings.Join(granted, ", "))
		rep.infof("%s %s: %d repo(s): %s", a.Role, info.Slug, len(repos), strings.Join(repos, ", "))
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// validateSlack confere que cada canal configurado existe, não está arquivado e tem o bot
// como membro (sem isso, chat.postMessage devolve not_in_channel).
func validateSlack(ctx context.Context, cfg *config.Config, slack SlackAPI, rep *Report) {
	type ref struct{ where, channel, mention string }
	refs := []ref{{"defaults.notify.slack", cfg.Defaults.Notify.Slack, cfg.Defaults.Notify.Mention}}
	for _, name := range cfg.TeamNames() {
		n := cfg.Teams[name].Notify
		refs = append(refs, ref{"teams." + name + ".notify", n.Slack, n.Mention})
	}
	if cfg.Defaults.Notify.Slack == "" {
		rep.warnf("defaults.notify.slack vazio: o resumo da rodada não é enviado")
	}
	if slack == nil {
		if cfg.Defaults.Notify.Slack != "" {
			rep.warnf("Slack: SLACK_BOT_TOKEN vazio; canais não checados e nenhum aviso será enviado")
		}
		return
	}
	checked := map[string]bool{}
	for _, r := range refs {
		if r.mention != "" && !notify.MentionIsID(r.mention) {
			rep.warnf("%s.mention %q é um handle: o Slack só notifica grupos mencionados por ID (S...)", r.where, r.mention)
		}
		if r.channel == "" || checked[r.channel] {
			continue
		}
		checked[r.channel] = true
		if !notify.IsChannelID(r.channel) {
			rep.warnf("%s: canal %q por nome; prefira o ID (C...)", r.where, r.channel)
		}
		info, err := slack.Channel(ctx, r.channel)
		switch {
		case err != nil:
			rep.errorf("%s: canal %s: %v", r.where, r.channel, err)
		case info.IsArchived:
			rep.errorf("%s: canal %s (#%s) está arquivado", r.where, r.channel, info.Name)
		case !info.IsMember:
			rep.errorf("%s: o bot não está no canal %s (#%s); convide o app no canal", r.where, r.channel, info.Name)
		}
	}
}

// linearIdentity é a parte do cliente que diz com que identidade ele fala com o Linear.
type linearIdentity interface {
	Viewer(ctx context.Context) (linear.Viewer, error)
	AppActor() bool
}

func validateLinear(ctx context.Context, cfg *config.Config, lin LinearAPI, rep *Report) {
	ws, err := lin.Workspace(ctx)
	if err != nil {
		rep.errorf("Linear: %v", err)
		return
	}
	// Identidade (ADR 0032): com o app, tudo sai como o app; com a chave pessoal, como a pessoa.
	if id, ok := lin.(linearIdentity); ok {
		switch v, err := id.Viewer(ctx); {
		case err != nil:
			rep.errorf("Linear: lendo a identidade: %v", err)
		case id.AppActor() && !v.App:
			rep.errorf("Linear: o token do app autentica como %s, que não é app; autorize de novo com `sentinela linear auth` (actor=app)", v.Name)
		case id.AppActor():
			rep.infof("Linear: ações saem como o app %s", v.Name)
		default:
			rep.warnf("Linear: ações saem como %s (chave pessoal); configure o app com linear_auth e `sentinela linear auth` (ADR 0032)", v.Name)
		}
	}
	checkTarget := func(where, teamKey, project, state string) {
		t, ok := ws.Team(teamKey)
		if !ok {
			rep.errorf("%s: time %q não existe no Linear", where, teamKey)
			return
		}
		if project != "" && !slices.Contains(t.Projects, project) {
			rep.errorf("%s: projeto %q não existe no time %s", where, project, teamKey)
		}
		if state != "" && !slices.Contains(t.States, state) {
			rep.errorf("%s: estado %q não existe no time %s (estados: %s)", where, state, teamKey, strings.Join(t.States, ", "))
		}
	}

	d := cfg.Defaults.Linear
	checkTarget("defaults.linear", d.Team, d.Project, d.TriageState)
	checkTarget("defaults.linear.review_state", d.Team, "", d.ReviewState)
	checkTarget("defaults.linear.done_state", d.Team, "", cfg.DoneStateFor(nil))
	if t, ok := ws.Team(d.Team); ok {
		labels := append([]string{}, d.Labels...)
		labels = append(labels, cfg.Labels.Trigger, cfg.Labels.Done, cfg.Labels.Failed, cfg.Labels.Finding,
			cfg.Labels.Question, cfg.Labels.Intentional, cfg.Labels.NotIntentional)
		for _, l := range labels {
			if l != "" && !slices.Contains(t.Labels, l) {
				rep.warnf("label %q não existe no time %s do Linear", l, d.Team)
			}
		}
	}
	for _, name := range cfg.TeamNames() {
		t := cfg.Teams[name]
		checkTarget("teams."+name, cfg.LinearTeamFor(t), t.Linear.Project, cfg.TriageStateFor(t))
		checkTarget("teams."+name+".linear.review_state", cfg.LinearTeamFor(t), "", cfg.ReviewStateFor(t))
		checkTarget("teams."+name+".linear.done_state", cfg.LinearTeamFor(t), "", cfg.DoneStateFor(t))
		for cat, p := range t.Linear.CategoryProjects {
			checkTarget("teams."+name+".linear.category_projects."+cat, cfg.LinearTeamFor(t), p, "")
		}
		// Labels trocadas pelo time (ADR 0038): precisam existir no time do Linear dele.
		lt, ok := ws.Team(cfg.LinearTeamFor(t))
		if !ok {
			continue
		}
		for k, l := range map[string]string{"trigger": t.Labels.Trigger, "done": t.Labels.Done, "failed": t.Labels.Failed} {
			if l != "" && !slices.Contains(lt.Labels, l) {
				rep.errorf("teams.%s.labels.%s: label %q não existe no time %s do Linear", name, k, l, lt.Key)
			}
		}
	}
	for _, r := range cfg.Repos {
		for _, m := range r.Modules {
			if m.LinearProject != "" {
				checkTarget("repos "+r.Name+".modules["+m.Path+"].linear_project", cfg.LinearTeamFor(cfg.Teams[m.Team]), m.LinearProject, "")
			}
		}
		if r.LinearProject == "" {
			continue
		}
		var team *config.Team
		if r.Team != "" {
			team = cfg.Teams[r.Team]
		} else if r.FallbackTeam != "" {
			team = cfg.Teams[r.FallbackTeam]
		}
		checkTarget("repos "+r.Name+".linear_project", cfg.LinearTeamFor(team), r.LinearProject, "")
	}
}

// validateCommandsDoc avisa o repo sem AGENTS.md nem CLAUDE.md na raiz da branch padrão: a
// descoberta funciona, mas a correção (label de gatilho) falha até o arquivo existir, salvo nos
// workspaces com arquivo próprio (ADR 0059), que esta checagem pela API não lista.
func validateCommandsDoc(ctx context.Context, r *config.Repo, gh GitHubAPI, rep *Report) {
	doc, err := gh.CommandsDoc(ctx, r.Name, r.DefaultBranch)
	switch {
	case err != nil:
		rep.warnf("GitHub: %s: não deu para conferir AGENTS.md/CLAUDE.md: %v", r.Name, err)
	case doc == "":
		rep.warnf("%s: sem AGENTS.md nem CLAUDE.md na raiz (%s): a correção pelo Sentinela só roda depois que o arquivo existir, com os comandos de setup, build, teste e lint (num monorepo, um AGENTS.md por workspace também serve para os achados dele; o status confere pelo clone)", r.Name, orDash(r.DefaultBranch))
	}
}

func validateGitHub(ctx context.Context, cfg *config.Config, gh GitHubAPI, rep *Report) {
	for _, r := range cfg.Repos {
		ok, err := gh.RepoExists(ctx, r.Name)
		switch {
		case err != nil:
			rep.errorf("GitHub: repo %s: %v", r.Name, err)
		case !ok:
			rep.errorf("GitHub: repo %s não existe ou não é visível com o token", r.Name)
		default:
			validateCommandsDoc(ctx, r, gh, rep)
		}
	}

	checked := map[string]bool{}
	for _, name := range cfg.TeamNames() {
		t := cfg.Teams[name]
		handles := append(append([]string{}, t.GitHub...), t.Members...)
		for _, h := range handles {
			if checked[h] || (strings.Contains(h, "@") && !strings.HasPrefix(h, "@")) {
				continue // e-mail não é verificável na API
			}
			checked[h] = true
			login := strings.TrimPrefix(h, "@")
			var exists bool
			var err error
			if org, slug, isTeam := strings.Cut(login, "/"); isTeam {
				exists, err = gh.TeamExists(ctx, org, slug)
			} else {
				exists, err = gh.UserExists(ctx, login)
			}
			switch {
			case err != nil:
				rep.errorf("GitHub: %s: %v", h, err)
			case !exists:
				rep.warnf("teams.%s: %s não existe no GitHub (ok para times fictícios; o roteamento usa só o sentinela.yaml)", name, h)
			}
		}
	}

	resolver := repo.NewResolver(cfg, nil)
	for _, r := range cfg.Repos {
		if r.OwnersSource == config.OwnersConfig {
			continue
		}
		co, err := gh.Codeowners(ctx, r.Name)
		if err != nil {
			rep.errorf("GitHub: CODEOWNERS de %s: %v", r.Name, err)
			continue
		}
		if co == nil {
			continue
		}
		for _, rule := range co.Rules {
			for _, owner := range rule.Owners {
				if team, _ := resolver.TeamForOwner(owner); team == "" {
					rep.warnf("%s: owner %s do CODEOWNERS (L%d %s) sem time mapeado no sentinela.yaml", r.Name, owner, rule.Line, rule.Pattern)
				}
			}
		}
	}
}

// Bootstrap gera uma config inicial para um repo a partir do CODEOWNERS e dos times
// do Linear. Orçamento, modelos, preços, labels e paths bloqueados vêm de base.
func Bootstrap(ctx context.Context, base *config.Config, repoName, linearTeam string, lin LinearAPI, gh GitHubAPI) (*config.Config, []string, error) {
	var notes []string
	ws, err := lin.Workspace(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("lendo Linear: %w", err)
	}
	if linearTeam == "" {
		if len(ws.Teams) != 1 {
			return nil, nil, fmt.Errorf("o workspace tem %d times; escolha um com --linear-team", len(ws.Teams))
		}
		linearTeam = ws.Teams[0].Key
	}
	lt, ok := ws.Team(linearTeam)
	if !ok {
		return nil, nil, fmt.Errorf("time %q não existe no Linear", linearTeam)
	}
	co, err := gh.Codeowners(ctx, repoName)
	if err != nil {
		return nil, nil, fmt.Errorf("lendo CODEOWNERS: %w", err)
	}

	triage := ""
	for _, s := range []string{"Triage", "Backlog"} {
		if slices.Contains(lt.States, s) {
			triage = s
			break
		}
	}
	out := &config.Config{
		Workspace:    base.Workspace,
		Defaults:     config.Defaults{Linear: config.LinearTarget{Team: lt.Key, TriageState: triage, Labels: base.Defaults.Linear.Labels}, OwnersSource: config.OwnersBoth, Notify: base.Defaults.Notify, Policy: base.Defaults.Policy},
		Labels:       base.Labels,
		FixTrigger:   config.FixTrigger{Allowed: config.AllowedList{config.AllowTeamMembers}}, // ADR 0042
		Budget:       base.Budget,
		Models:       base.Models,
		Pricing:      base.Pricing,
		BlockedPaths: base.BlockedPaths,
		Teams:        map[string]*config.Team{},
	}
	r := &config.Repo{Name: repoName, DefaultBranch: "main", OwnersSource: config.OwnersCodeowners}
	if co == nil {
		notes = append(notes, repoName+" não tem CODEOWNERS; defina repos[].team manualmente")
	} else {
		for _, owner := range co.Owners() {
			name := teamNameFor(owner)
			out.Teams[name] = &config.Team{GitHub: []string{owner}, Linear: config.LinearTarget{Team: lt.Key}, Assignee: config.AssigneeNone}
			if !strings.Contains(owner, "/") {
				notes = append(notes, fmt.Sprintf("%s é um usuário: considere movê-lo para members do time certo", owner))
			}
		}
		// Um arquivo qualquer na raiz só casa com a regra curinga ("*"): o dono dela vira fallback.
		if rule, ok := co.Match("__sentinela_bootstrap_probe__"); ok && len(rule.Owners) > 0 {
			r.FallbackTeam = teamNameFor(rule.Owners[0])
		}
	}
	out.Repos = []*config.Repo{r}
	byRepo := map[string]map[string]bool{}
	if bytes, err := gh.Languages(ctx, repoName); err != nil {
		notes = append(notes, fmt.Sprintf("linguagens de %s não detectadas (%v): discovery.scanners com a lista completa", repoName, err))
	} else {
		byRepo[repoName] = scan.LanguagesFromGitHub(bytes)
	}
	plan := planScanners([]string{repoName}, byRepo)
	out.Discovery.Scanners = plan.names
	if len(plan.unknown) == 0 {
		note := "discovery.scanners pelas linguagens de " + repoName + " no GitHub: " + orDash(scan.LanguageList(plan.langs))
		if packs := semgrepPacks(plan.langs); packs != "" {
			note += "; semgrep com p/default e " + packs + " (discovery.semgrep_languages, padrão)"
		}
		notes = append(notes, note)
	}
	notes = append(notes, "cada time aponta para "+lt.Key+"; troque por boards ou projetos próprios quando existirem")
	return out, notes, nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9_-]+`)

func teamNameFor(owner string) string {
	s := strings.ToLower(strings.TrimPrefix(owner, "@"))
	if _, slug, ok := strings.Cut(s, "/"); ok {
		s = slug
	}
	if i := strings.Index(s, "@"); i > 0 { // e-mail
		s = s[:i]
	}
	return strings.Trim(nonSlug.ReplaceAllString(s, "-"), "-")
}

// AgentDecisionsAPI é o que validate precisa do banco para conferir o princípio 6.
type AgentDecisionsAPI interface {
	AgentDecisions(ctx context.Context) ([]db.AgentDecisionsRow, error)
}

// validateDecisions avisa sobre desfechos decididos por agente (decided_by = 'agent'): violam o
// princípio 6 e ficam fora das métricas, do Curator e das supressões (ADR 0022).
func validateDecisions(ctx context.Context, q AgentDecisionsAPI, rep *Report) {
	rows, err := q.AgentDecisions(ctx)
	if err != nil {
		rep.warnf("banco: não deu para conferir desfechos decididos por agente: %v", err)
		return
	}
	for _, r := range rows {
		rep.warnf("outcome %d (%s, %s): decidido por agente, viola o princípio 6; fica fora das métricas, do Curator e das supressões: %s",
			r.ID, r.Kind, r.Repo, r.Title)
	}
}

// effectiveProjects lista, por repo e por time que pode ser dono dele (repos[].team,
// modules[].team, fallback_team ou o padrão), o projeto do Linear, o canal e a menção efetivos e de
// onde vieram; e os projetos de módulo e por categoria (ADR 0040).
func effectiveProjects(cfg *config.Config) []string {
	var out []string
	for _, rp := range cfg.Repos {
		var teams []string
		add := func(t string) {
			if t != "" && !slices.Contains(teams, t) {
				teams = append(teams, t)
			}
		}
		add(rp.Team)
		for _, m := range rp.Modules {
			add(m.Team)
		}
		add(rp.FallbackTeam)
		if len(teams) == 0 {
			teams = []string{""}
		}
		for _, t := range teams {
			p, src := cfg.ProjectFor(cfg.Teams[t], t, rp)
			name := t
			if name == "" {
				name = "(padrão)"
			}
			out = append(out, fmt.Sprintf("projeto efetivo: %s, time %s: %s (de %s)", rp.Name, name, orDash(p), src))
		}
		for _, m := range rp.Modules {
			if m.LinearProject != "" {
				out = append(out, fmt.Sprintf("projeto efetivo: %s, módulo %s (time %s): %s (de repos[%s].modules[%s].linear_project%s)", rp.Name, m.Path, m.Team, m.LinearProject, rp.Name, m.Path, cfg.FileOf(m)))
			}
		}
	}
	for _, name := range cfg.TeamNames() {
		t := cfg.Teams[name]
		for _, cat := range config.Categories {
			if p := t.Linear.CategoryProjects[cat]; p != "" {
				out = append(out, fmt.Sprintf("projeto efetivo: time %s, categoria %s: %s (de teams.%s.linear.category_projects%s; perde para o projeto do módulo)", name, cat, p, name, cfg.TeamFile(name)))
			}
		}
		channel, src := t.Notify.Slack, "teams."+name+".notify.slack"+cfg.TeamFile(name)
		if channel == "" {
			channel, src = cfg.Defaults.Notify.Slack, "defaults.notify.slack"
		}
		if channel == "" {
			src = "nenhum (sem aviso)"
		}
		mention, msrc := t.Notify.Mention, "teams."+name+".notify.mention"+cfg.TeamFile(name)
		if mention == "" {
			msrc = "nenhuma"
		}
		out = append(out, fmt.Sprintf("aviso efetivo: time %s: canal %s (de %s), menção %s (de %s)", name, orDash(channel), src, orDash(mention), msrc))
	}
	return out
}

// validateFixTrigger avisa quando qualquer pessoa pode disparar a correção e, no modo MCP, que o
// gatilho não é conferido (o MCP não dá o histórico de labels); mostra o efetivo por time (ADR 0042).
func validateFixTrigger(cfg *config.Config, rep *Report) {
	restricted := !cfg.FixTrigger.Allowed.Anyone()
	for _, name := range cfg.TeamNames() {
		allowed, src := cfg.FixTriggerFor(name)
		rep.infof("gatilho efetivo: time %s: %s (de %s)", name, allowed.String(), src)
		if !allowed.Anyone() {
			restricted = true
		}
	}
	if len(cfg.FixTrigger.Allowed) == 0 {
		rep.warnf("fix_trigger.allowed não definido: qualquer pessoa com acesso à issue pode disparar a correção com a label de gatilho; recomendado fix_trigger: { allowed: team_members } (ADR 0042)")
	}
	if restricted && cfg.LinearMCP() {
		rep.warnf("fix_trigger no modo MCP: o MCP do Linear não mostra quem aplicou a label, então o gatilho não é conferido; só os comentários das Instruções da equipe são filtrados pelos membros do time (ADR 0042)")
	}
}
