package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/notify"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

var acmeDir = filepath.Join("..", "..", "examples", "monorepo-acme")

// acmeLinear é o workspace do Linear da acme: os times, os estados, os projetos e as labels que o
// cenário usa (o Linear falso do teste do exemplo).
type acmeLinear struct{}

func (acmeLinear) Workspace(context.Context) (*linear.Workspace, error) {
	states := []string{"Triage", "Backlog", "Todo", "In Review", "Done", "Canceled"}
	labels := []string{"sentinela", "sentinela-fix", "sentinela-done", "sentinela-failed", "pagamentos-fix", "identidade-falhou"}
	return &linear.Workspace{Teams: []linear.Team{
		{Key: "PAG", States: states, Labels: labels, Projects: []string{"Pagamentos · Cobrança", "Pagamentos · Carteira", "Pagamentos · Repasse", "Pagamentos · Fraude", "Pagamentos · Segurança"}},
		{Key: "CAT", States: states, Labels: labels, Projects: []string{"Catálogo"}},
		{Key: "IDN", States: states, Labels: labels, Projects: []string{"Identidade"}},
		{Key: "PLAT", States: states, Labels: labels, Projects: []string{"Plataforma", "Sentinela · Geral"}},
	}}, nil
}

// acmeGitHub responde pelos repos e times da acme, com os CODEOWNERS da árvore falsa.
type acmeGitHub struct{ co repo.StaticCodeowners }

func (acmeGitHub) RepoExists(context.Context, string) (bool, error)         { return true, nil }
func (acmeGitHub) TeamExists(context.Context, string, string) (bool, error) { return true, nil }
func (acmeGitHub) UserExists(context.Context, string) (bool, error)         { return true, nil }
func (acmeGitHub) CommandsDoc(context.Context, string, string) (string, error) {
	return "AGENTS.md", nil
}
func (acmeGitHub) Languages(context.Context, string) (map[string]int64, error) {
	return map[string]int64{"Go": 1000}, nil
}
func (g acmeGitHub) Codeowners(_ context.Context, name string) (*repo.Codeowners, error) {
	return g.co[name], nil
}

type acmeSlack struct{}

func (acmeSlack) Channel(_ context.Context, ref string) (notify.ChannelInfo, error) {
	return notify.ChannelInfo{ID: ref, Name: strings.ToLower(ref), IsMember: true}, nil
}

func acmeCodeowners(t *testing.T) repo.StaticCodeowners {
	t.Helper()
	out := repo.StaticCodeowners{}
	for name, dir := range map[string]string{"acme/plataforma": "plataforma", "acme/vitrine-web": "vitrine-web", "acme/sdk-identidade": "sdk-identidade"} {
		f, err := os.Open(filepath.Join(acmeDir, "repos", dir, ".github", "CODEOWNERS"))
		if err != nil {
			t.Fatal(err)
		}
		co, err := repo.ParseCodeowners(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[name] = co
	}
	return out
}

// O cenário de referência (examples/monorepo-acme) passa no config validate com Linear, Slack e
// GitHub falsos, e o roteamento dos caminhos do guia é o documentado. Se o código ou o exemplo
// mudarem e ficarem diferentes, este teste falha.
func TestAcmeExampleValidates(t *testing.T) {
	cfg, err := config.Load(filepath.Join(acmeDir, "sentinela.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Teams) != 4 || len(cfg.Repos) != 3 {
		t.Fatalf("times %d, repos %d", len(cfg.Teams), len(cfg.Repos))
	}
	co := acmeCodeowners(t)
	rep := ValidateRemote(context.Background(), cfg, acmeLinear{}, acmeGitHub{co: co}, acmeSlack{})
	if len(rep.Errors) > 0 {
		t.Errorf("erros do validate: %v", rep.Errors)
	}
	for _, l := range rep.Info {
		t.Logf("ok: %s", l)
	}
	for _, l := range rep.Warnings {
		t.Logf("aviso: %s", l)
	}
	t.Logf("%d erro(s), %d aviso(s)", len(rep.Errors), len(rep.Warnings))
	// modules/conciliacao-de-repasses tem nome longo (o glob é maior que "**/Dockerfile"): o override
	// explícito continua mandando o Dockerfile para a plataforma (ADR 0043).
	r := repo.NewResolver(cfg, co)
	for _, tc := range []struct {
		repo, path, team, rule, project, channel, mention string
	}{
		{"acme/plataforma", "modules/cobranca/fatura.go", "pagamentos", "config:modules/cobranca/**", "Pagamentos · Cobrança", "C0ACMEPAG01", "S0ACMEPAG01"},
		{"acme/plataforma", "modules/fraude/regra.go", "pagamentos", "config:modules/fraude/**", "Pagamentos · Fraude", "C0ACMEPAG01", "S0ACMEPAG01"},
		{"acme/plataforma", "modules/cobranca/Dockerfile", "plataforma", "config:**/Dockerfile", "Plataforma", "C0ACMEPLAT1", "S0ACMEPLAT1"},
		{"acme/plataforma", "modules/conciliacao-de-repasses/Dockerfile", "plataforma", "config:**/Dockerfile", "Plataforma", "C0ACMEPLAT1", "S0ACMEPLAT1"},
		{"acme/plataforma", "modules/conciliacao-de-repasses/extrato.go", "pagamentos", "config:modules/conciliacao-de-repasses/**", "Pagamentos · Repasse", "C0ACMEPAG01", "S0ACMEPAG01"},
		{"acme/plataforma", "modules/busca/indice.go", "catalogo", "codeowners:L17", "Catálogo", "C0ACMECAT01", "S0ACMECAT01"},
		{"acme/plataforma", "modules/contas/login.go", "identidade", "codeowners:L20", "Identidade", "C0ACMEIDN01", "S0ACMEIDN01"},
		{"acme/plataforma", "libs/comum/moeda.go", "plataforma", "codeowners:L32", "Plataforma", "C0ACMEPLAT1", "S0ACMEPLAT1"},
		{"acme/plataforma", "libs/dinheiro/arredonda.go", "pagamentos", "codeowners:L13", "Sentinela · Geral", "C0ACMEPAG01", "S0ACMEPAG01"},
		{"acme/plataforma", ".github/workflows/ci.yml", "plataforma", "codeowners:L26", "Plataforma", "C0ACMEPLAT1", "S0ACMEPLAT1"},
		{"acme/plataforma", "infra/terraform/rede.tf", "plataforma", "codeowners:L27", "Plataforma", "C0ACMEPLAT1", "S0ACMEPLAT1"},
		{"acme/plataforma", "docs/rascunhos/notas.md", "plataforma", "fallback", "Plataforma", "C0ACMEPLAT1", "S0ACMEPLAT1"},
		{"acme/plataforma", "README.md", "plataforma", "codeowners:L6", "Plataforma", "C0ACMEPLAT1", "S0ACMEPLAT1"},
		{"acme/vitrine-web", "src/App.tsx", "catalogo", "codeowners:L2", "Catálogo", "C0ACMECAT01", "S0ACMECAT01"},
		{"acme/sdk-identidade", "token.go", "identidade", "codeowners:L2", "Identidade", "C0ACMEIDN01", "S0ACMEIDN01"},
	} {
		res, err := r.Resolve(tc.repo, tc.path)
		if err != nil || res.Team != tc.team || res.Rule != tc.rule || res.LinearProject != tc.project || res.NotifySlack != tc.channel || res.Mention != tc.mention {
			t.Errorf("%s %s: time %s (%s), projeto %q, canal %s, menção %s, %v; quer %s (%s), %q, %s, %s",
				tc.repo, tc.path, res.Team, res.Rule, res.LinearProject, res.NotifySlack, res.Mention, err, tc.team, tc.rule, tc.project, tc.channel, tc.mention)
		}
	}
	// Publicação: segurança em módulo fica no projeto do módulo; fora de módulo, no de segurança.
	if p := cfg.PublishProject("pagamentos", "security", "Pagamentos · Cobrança"); p != "Pagamentos · Cobrança" {
		t.Errorf("segurança em módulo: %s", p)
	}
	if p := cfg.PublishProject("pagamentos", "security", "Sentinela · Geral"); p != "Pagamentos · Segurança" {
		t.Errorf("segurança fora de módulo: %s", p)
	}
	if res, _ := r.Resolve("acme/plataforma", "libs/comum/moeda.go"); strings.Join(res.Subscribers, ",") != "pagamentos" {
		t.Errorf("inscritos do compartilhado: %v", res.Subscribers)
	}
}

// owners explain com as evidências de um achado entre pagamentos e identidade: o dono é um só, e o
// secundário aparece com o módulo e a menção do grupo (ADR 0044).
func TestAcmeExplainShowsOwnerAndSecondaryTeams(t *testing.T) {
	out, err := runWith(t, map[string]string{}, nil, "--config", filepath.Join(acmeDir, "sentinela.yaml"), "owners", "explain", "acme/plataforma",
		"modules/cobranca/fatura.go", "modules/contas/login.go", "modules/cobranca/juros.go",
		"--codeowners", filepath.Join(acmeDir, "repos", "plataforma", ".github", "CODEOWNERS"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"time:            pagamentos", "secundário:      identidade (módulo /modules/contas/: modules/contas/login.go; menção S0ACMEIDN01 no aviso do dono)"} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "secundário:") != 1 {
		t.Errorf("só identidade é secundário:\n%s", out)
	}
}

// smallLinear é o Linear da empresa pequena: um time ENG com o projeto e as labels do exemplo.
type smallLinear struct{}

func (smallLinear) Workspace(context.Context) (*linear.Workspace, error) {
	return &linear.Workspace{Teams: []linear.Team{{Key: "ENG", States: []string{"Triage", "Backlog", "In Review", "Done", "Canceled"},
		Labels: []string{"sentinela", "sentinela-fix", "sentinela-done", "sentinela-failed"}, Projects: []string{"Sentinela"}}}}, nil
}

// O exemplo da empresa pequena (docs/empresa.md) passa no config validate com Linear, Slack e
// GitHub falsos, sem erros nem avisos.
func TestSmallCompanyExampleValidates(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "examples", "empresa-pequena", "sentinela.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Teams) != 1 || len(cfg.Repos) != 2 || cfg.LLM.Provider != config.ProviderAnthropicAPI {
		t.Fatalf("times %d, repos %d, provider %s", len(cfg.Teams), len(cfg.Repos), cfg.LLM.Provider)
	}
	rep := ValidateRemote(context.Background(), cfg, smallLinear{}, acmeGitHub{co: repo.StaticCodeowners{}}, acmeSlack{})
	for _, l := range rep.Info {
		t.Logf("ok: %s", l)
	}
	for _, l := range rep.Warnings {
		t.Logf("aviso: %s", l)
	}
	t.Logf("%d erro(s), %d aviso(s)", len(rep.Errors), len(rep.Warnings))
	if len(rep.Errors) > 0 || len(rep.Warnings) > 0 {
		t.Errorf("erros %v, avisos %v", rep.Errors, rep.Warnings)
	}
	r := repo.NewResolver(cfg, repo.StaticCodeowners{})
	res, err := r.Resolve("empresa/api", "cmd/server/main.go")
	if err != nil || res.Team != "engenharia" || res.LinearProject != "Sentinela" || res.NotifySlack != "C0EMPRESA01" || res.Mention != "S0EMPRESA01" {
		t.Errorf("roteamento = %+v, %v", res, err)
	}
}
