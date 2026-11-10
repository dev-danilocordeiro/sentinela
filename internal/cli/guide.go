package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/guide"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/notify"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

// GuideFile é o guia commitado, relativo à raiz do repo.
const GuideFile = "docs/guia/index.html"

func newDocsCmd(_ *deps) *cobra.Command {
	cmd := &cobra.Command{Use: "docs", Short: "Gera a documentação derivada do código"}
	var out, root string
	g := &cobra.Command{
		Use:   "guide",
		Short: "Gera o guia interativo (docs/guia/index.html) a partir de docs/guia/fontes, dos docs, dos exemplos, do schema e da saída real do binário",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			page, err := BuildGuide(root)
			if err != nil {
				return err
			}
			if out == "-" {
				_, err = c.OutOrStdout().Write(page)
				return err
			}
			if !filepath.IsAbs(out) {
				out = filepath.Join(root, out)
			}
			if err := os.WriteFile(out, page, 0o644); err != nil { //nolint:gosec // documentação pública
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "guia gerado: %s (%d KB)\n", out, len(page)/1024)
			return nil
		},
	}
	g.Flags().StringVar(&out, "out", GuideFile, "arquivo de saída, relativo à raiz (- escreve na saída padrão)")
	g.Flags().StringVar(&root, "root", ".", "raiz do repositório do Sentinela")
	cmd.AddCommand(g)
	return cmd
}

// BuildGuide monta o guia. Os comandos citados nas fontes rodam em processo, com a raiz do repo
// como diretório atual, para que os caminhos da saída sejam os mesmos que o leitor digitaria.
func BuildGuide(root string) ([]byte, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	prev, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if err := os.Chdir(abs); err != nil {
		return nil, err
	}
	defer func() { _ = os.Chdir(prev) }()

	d := guide.Data{Root: ".", Run: runSelf, RunInit: runInitScenario}
	if d.Simulator, err = guideSimulator(); err != nil {
		return nil, fmt.Errorf("simulador: %w", err)
	}
	if d.Errors, err = guideErrors(); err != nil {
		return nil, fmt.Errorf("erros comuns: %w", err)
	}
	if d.ConfigRef, d.TeamRef, err = guideConfigRef(); err != nil {
		return nil, fmt.Errorf("referência: %w", err)
	}
	d.Commands = guideCommands()
	d.Schemas = map[string]json.RawMessage{}
	for k, f := range map[string]string{"main": "schema/sentinela.schema.json", "team": "schema/team.schema.json"} {
		b, err := os.ReadFile(f) //nolint:gosec // arquivo do repo
		if err != nil {
			return nil, err
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, b); err != nil {
			return nil, err
		}
		d.Schemas[k] = compact.Bytes()
	}
	base, err := yamlFileJSON("examples/empresa-pequena/sentinela.yaml")
	if err != nil {
		return nil, err
	}
	d.Bases = map[string]json.RawMessage{"pequena": base}
	return guide.Render(d)
}

// runSelf roda o sentinela em processo, sem variáveis de ambiente (nada de rede nem banco), e
// devolve a saída. config validate com erro devolve a saída mesmo assim: é o que o leitor veria.
func runSelf(args []string) (string, error) {
	cmd := NewRootCmd(func(string) string { return "" })
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	if errors.Is(err, ErrValidation) {
		err = nil
	}
	if err != nil {
		return buf.String(), fmt.Errorf("sentinela %s: %w", strings.Join(args, " "), err)
	}
	return buf.String(), nil
}

const acmeRoot = "examples/monorepo-acme"

var acmeCodeownersFiles = map[string]string{
	"acme/plataforma":     acmeRoot + "/repos/plataforma/.github/CODEOWNERS",
	"acme/vitrine-web":    acmeRoot + "/repos/vitrine-web/.github/CODEOWNERS",
	"acme/sdk-identidade": acmeRoot + "/repos/sdk-identidade/.github/CODEOWNERS",
}

type simSpec struct {
	id, group, label, why, repo string
	paths                       []string
}

var simSpecs = []simSpec{
	{"cobranca", "Módulos de um time", "modules/cobranca/fatura.go: módulo com projeto próprio", "O passo 1 (módulo da config) casa primeiro, e o projeto vem do módulo. Um achado de segurança aqui continua no projeto do módulo, que vence o projeto por categoria.", "acme/plataforma", []string{"modules/cobranca/fatura.go"}},
	{"longo", "Módulos de um time", "modules/conciliacao-de-repasses/extrato.go: módulo de nome longo", "O glob do módulo (modules/conciliacao-de-repasses/**) é mais longo que **/Dockerfile. Para o código do módulo isso não importa: só ele casa.", "acme/plataforma", []string{"modules/conciliacao-de-repasses/extrato.go"}},
	{"busca", "Módulos de um time", "modules/busca/indice.go: regra genérica perdendo para uma específica", "O * do CODEOWNERS também casa, mas a semântica do GitHub é \"a última regra que casa vence\", e /modules/busca/ vem depois.", "acme/plataforma", []string{"modules/busca/indice.go"}},
	{"docker-longo", "Arquivos transversais", "modules/conciliacao-de-repasses/Dockerfile: Dockerfile em módulo de nome longo", "O glob do módulo de pagamentos é mais longo que **/Dockerfile e, sem nada mais, ganharia. O módulo **/Dockerfile da plataforma tem override: true e vence os módulos comuns.", "acme/plataforma", []string{"modules/conciliacao-de-repasses/Dockerfile"}},
	{"docker", "Arquivos transversais", "modules/cobranca/Dockerfile: Dockerfile dentro de um módulo de outro time", "Mesmo caso, num módulo de nome curto: o override decide, não o tamanho do glob.", "acme/plataforma", []string{"modules/cobranca/Dockerfile"}},
	{"ci", "Arquivos transversais", ".github/workflows/ci.yml: CI", "/.github/ é da plataforma pelo CODEOWNERS.", "acme/plataforma", []string{".github/workflows/ci.yml"}},
	{"infra", "Arquivos transversais", "infra/terraform/rede.tf: infra compartilhada", "/infra/ é da plataforma, e infra/** está em blocked_paths: nada aqui é auto-aprovado.", "acme/plataforma", []string{"infra/terraform/rede.tf"}},
	{"comum", "Compartilhado e sem dono", "libs/comum/moeda.go: arquivo compartilhado, dois donos", "A regra tem dois donos: o primeiro é o principal; o segundo entra como inscrito na issue.", "acme/plataforma", []string{"libs/comum/moeda.go"}},
	{"dinheiro", "Compartilhado e sem dono", "libs/dinheiro/arredonda.go: código de um time fora dos módulos", "Sem projeto de módulo nem do time, o projeto cai em defaults.linear.project. Um achado de segurança aqui iria para \"Pagamentos · Segurança\" (a categoria vence time, repo e defaults).", "acme/plataforma", []string{"libs/dinheiro/arredonda.go"}},
	{"rascunho", "Compartilhado e sem dono", "docs/rascunhos/notas.md: caminho sem dono", "A regra /docs/rascunhos/ não tem donos; o roteamento passa para repos[].team (vazio) e cai no fallback_team.", "acme/plataforma", []string{"docs/rascunhos/notas.md"}},
	{"readme", "Compartilhado e sem dono", "README.md: só a regra genérica", "Nada mais específico casa: vale o * do CODEOWNERS, da plataforma.", "acme/plataforma", []string{"README.md"}},
	{"vitrine", "Repos de um time só", "acme/vitrine-web src/App.tsx: repo exclusivo do catálogo", "O CODEOWNERS do repo dá tudo ao catálogo; o projeto vem do time (time vence repo).", "acme/vitrine-web", []string{"src/App.tsx"}},
	{"sdk", "Repos de um time só", "acme/sdk-identidade client.go: repo exclusivo da identidade", "Repo inteiro de um time, declarado no arquivo do time; assignee round_robin entre os membros.", "acme/sdk-identidade", []string{"client.go"}},
	{"cruza", "Achado que cruza times", "cobranca/fatura.go + contas/login.go: achado entre pagamentos e identidade", "O dono é um só (o do primeiro caminho, a evidência principal). A identidade entra em \"Também afeta\" na issue e é mencionada pelo grupo no aviso do dono, sem issue própria.", "acme/plataforma", []string{"modules/cobranca/fatura.go", "modules/contas/login.go", "modules/cobranca/juros.go"}},
}

var fieldLine = regexp.MustCompile(`^  ([a-zà-ú ]+):\s+(.*)$`)

func guideSimulator() ([]guide.SimCase, error) {
	var out []guide.SimCase
	for _, s := range simSpecs {
		args := append([]string{"--config", acmeRoot + "/sentinela.yaml", "owners", "explain", s.repo}, s.paths...)
		args = append(args, "--codeowners", acmeCodeownersFiles[s.repo])
		text, err := runSelf(args)
		if err != nil {
			return nil, err
		}
		c := guide.SimCase{ID: s.id, Group: s.group, Label: s.label, Why: s.why, Command: "sentinela " + strings.Join(args, " "), Output: strings.TrimRight(text, "\n")}
		for _, l := range strings.Split(text, "\n") {
			m := fieldLine.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			name, val := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
			if name == "secundário" {
				c.Secondary = append(c.Secondary, val)
				continue
			}
			if name == "secundários" {
				continue
			}
			f := guide.Field{Name: name, Value: val}
			if i := strings.Index(val, " (de "); i > 0 && strings.HasSuffix(val, ")") {
				f.Value, f.Source = val[:i], val[i+5:len(val)-1]
			}
			c.Fields = append(c.Fields, f)
		}
		if len(c.Fields) == 0 {
			return nil, fmt.Errorf("%s: owners explain sem resultado:\n%s", s.id, text)
		}
		out = append(out, c)
	}
	return out, nil
}

// --- Erros comuns: cada caso muda uma cópia do cenário acme e roda a validação de verdade. ---

type errSpec struct {
	where, cause, fix string
	remote            bool             // validate completo (Linear, Slack e GitHub falsos)
	edit              func(dir string) // muda a cópia
	ws                func(*linear.Workspace)
	slack             map[string]notify.ChannelInfo
	hiddenRepos       []string
	match             string // só as mensagens com este trecho (um caso que gera mais de uma)
}

func replaceIn(dir, file, old, repl string) {
	p := filepath.Join(dir, file)
	b, err := os.ReadFile(p) //nolint:gosec // cópia temporária
	if err != nil || !bytes.Contains(b, []byte(old)) {
		panic(fmt.Sprintf("guia: %s não tem %q", file, old))
	}
	_ = os.WriteFile(p, bytes.Replace(b, []byte(old), []byte(repl), 1), 0o600) //nolint:gosec // cópia temporária do exemplo
}

func appendTo(dir, file, text string) {
	p := filepath.Join(dir, file)
	b, _ := os.ReadFile(p)                                 //nolint:gosec // cópia temporária
	_ = os.WriteFile(p, append(b, []byte(text)...), 0o600) //nolint:gosec // cópia temporária do exemplo
}

func writeTo(dir, file, text string) {
	p := filepath.Join(dir, file)
	_ = os.MkdirAll(filepath.Dir(p), 0o750)
	_ = os.WriteFile(p, []byte(text), 0o600)
}

const (
	pagFile  = "config.d/teams/pagamentos.yaml"
	catFile  = "config.d/teams/catalogo.yaml"
	idnFile  = "config.d/teams/identidade.yaml"
	mainFile = "sentinela.yaml"
)

var errSpecs = []errSpec{
	{where: "arquivo do time", cause: "Chave da organização (modelos) no arquivo do time.", fix: "Tire a chave do arquivo do time. Se a organização quiser outro modelo para o time, ela põe em teams.pagamentos.models no principal.",
		edit: func(d string) { appendTo(d, pagFile, "models:\n  analysis: claude-opus-5-5\n") }},
	{where: "arquivo do time", cause: "O time tentou liberar a própria auto-aprovação.", fix: "Peça aos administradores: eles põem teams.pagamentos.policy.auto_approve no principal.",
		edit: func(d string) { replaceIn(d, pagFile, "  max_lines: 20", "  max_lines: 20\n  auto_approve: true") }},
	{where: "arquivo do time", cause: "fix_trigger (quem pode pedir a correção) é política da organização.", fix: "Tire do arquivo do time; os administradores põem teams.<time>.fix_trigger no principal.",
		edit: func(d string) { appendTo(d, pagFile, "fix_trigger: { allowed: anyone }\n") }},
	{where: "arquivo do time", cause: "Chave que não existe (aqui, canal em vez de notify.slack).", fix: "Use as chaves de schema/team.schema.json; com o schema no editor, o erro aparece na hora.",
		edit: func(d string) { appendTo(d, catFile, "canal: C0ACMECAT01\n") }},
	{where: "dois arquivos de time", cause: "Dois times declararam o mesmo módulo.", fix: "Combine qual time é o dono; o outro tira a linha. Para dividir, use globs diferentes.",
		edit: func(d string) {
			appendTo(d, catFile, "modules:\n  - { repo: acme/plataforma, path: \"modules/cobranca/**\" }\n")
		}},
	{where: "arquivo do time", cause: "Módulo num repo que nenhum arquivo declarou.", fix: "Peça para o repo entrar no principal ou, se ele for só do time, declare-o em repos: do arquivo do time.",
		edit: func(d string) { appendTo(d, catFile, "modules:\n  - { repo: acme/loja, path: \"web/**\" }\n") }},
	{where: "dois arquivos de time", cause: "O mesmo repo inteiro declarado em dois lugares.", fix: "Deixe o repo num lugar só; para dividir um repo entre times, use modules.",
		edit: func(d string) {
			writeTo(d, "config.d/teams/vitrine.yaml", "github: [\"@acme/vitrine\"]\nlinear: { team: CAT }\nrepos:\n  - { name: acme/vitrine-web }\n")
		}},
	{where: "principal e arquivo do time", cause: "O time foi declarado nos dois arquivos, com chaves de time no principal.", fix: "No principal, teams.<time> fica só com policy, models e fix_trigger; o resto vai no arquivo do time.",
		edit: func(d string) {
			replaceIn(d, mainFile, "  pagamentos:\n    policy:", "  pagamentos:\n    notify: { slack: C0OUTRO0001 }\n    policy:")
		}},
	{where: "principal", cause: "Nome de time errado.", fix: "Use o nome do arquivo do time (sem .yaml) ou a chave em teams.",
		edit: func(d string) { replaceIn(d, mainFile, "fallback_team: plataforma", "fallback_team: plataformas") }},
	{where: "arquivo do time", cause: "Categoria em português.", fix: "Use bug, performance, improvement ou security.",
		edit: func(d string) { replaceIn(d, pagFile, "    security: ", "    seguranca: ") }},
	{where: "arquivo do time", cause: "Label reservada a outro sistema (claude-*).", fix: "Escolha outro nome para a label.",
		edit: func(d string) { replaceIn(d, pagFile, "trigger: pagamentos-fix", "trigger: claude-fix") }},
	{where: "arquivo do time", cause: "O projeto do time é igual ao de um módulo dele, e o time usa projeto por categoria: na publicação não dá para saber de onde veio o projeto.", fix: "Dê ao time outro projeto (ou nenhum).",
		edit: func(d string) {
			replaceIn(d, pagFile, "  team: PAG", "  team: PAG\n  project: \"Pagamentos · Cobrança\"")
		}},
	{where: "principal", cause: "anyone libera todo mundo e não se combina com outros valores.", fix: "Use anyone sozinho, ou team_members e/ou uma lista de usuários.",
		edit: func(d string) { replaceIn(d, mainFile, "  allowed: team_members", "  allowed: [anyone, team_members]") }},
	{where: "principal", cause: "Valor de adapter que não existe.", fix: "tracker: linear_api, linear_mcp ou local; notifier: slack_api, slack_mcp ou none; git_host: github_apps ou gh_cli.",
		edit: func(d string) { replaceIn(d, mainFile, "tracker: linear_api", "tracker: jira") }},
	{where: "principal", cause: "fix_trigger não definido: vale anyone.", fix: "Defina fix_trigger: { allowed: team_members } (recomendado) ou declare anyone explicitamente.",
		remote: true, edit: func(d string) {
			replaceIn(d, mainFile, "fix_trigger:                     # quem pode pedir a correção com a label de gatilho (ADR 0042)\n  allowed: team_members", "")
		}},
	{where: "Linear", cause: "A label de gatilho do time não foi criada no time do Linear.", fix: "Crie a label no time do Linear (Settings > Labels do time).",
		remote: true, ws: func(w *linear.Workspace) { dropLabel(w, "PAG", "pagamentos-fix") }},
	{where: "Linear", cause: "Uma label da organização não existe num time do Linear.", fix: "Crie a label no time do Linear (ou no workspace).",
		remote: true, ws: func(w *linear.Workspace) { dropLabel(w, "PLAT", "sentinela-done") }},
	{where: "Linear", cause: "O projeto citado na config não existe no time do Linear (nome exato, com acento e ·).", fix: "Crie o projeto no time ou corrija o nome.",
		remote: true, ws: func(w *linear.Workspace) {
			t, _ := w.Team("PAG")
			t.Projects = without(t.Projects, "Pagamentos · Fraude")
		}},
	{where: "Linear", cause: "O estado inicial (triage_state) não existe no time.", fix: "Crie o estado no time do Linear ou troque linear.triage_state.",
		remote: true, ws: func(w *linear.Workspace) {
			t, _ := w.Team("CAT")
			t.States = without(t.States, "Triage")
		}},
	{where: "Linear", cause: "A chave do time do Linear está errada ou o time não existe.", fix: "Use a chave do identificador das issues (PAG em PAG-123).",
		remote: true, edit: func(d string) { replaceIn(d, idnFile, "  team: IDN", "  team: IDT") }},
	{where: "Slack", cause: "O bot do Slack não foi convidado no canal.", fix: "/invite @Sentinela no canal (para canal privado, é obrigatório).",
		remote: true, slack: map[string]notify.ChannelInfo{"C0ACMEIDN01": {ID: "C0ACMEIDN01", Name: "identidade-alertas"}}},
	{where: "Slack", cause: "O canal foi arquivado.", fix: "Use outro canal no notify.slack do time.",
		remote: true, slack: map[string]notify.ChannelInfo{"C0ACMECAT01": {ID: "C0ACMECAT01", Name: "catalogo-alertas", IsMember: true, IsArchived: true}}},
	{where: "Slack", cause: "A menção é um handle (@grupo), e o Slack só notifica grupos mencionados pelo ID.", fix: "Use o ID do grupo (S...), em Pessoas > Grupos > o grupo > Copiar ID.",
		remote: true, edit: func(d string) { replaceIn(d, idnFile, "mention: S0ACMEIDN01", "mention: \"@identidade-team\"") }},
	{where: "Slack", cause: "Canal pelo nome: funciona, mas é resolvido a cada início e quebra se o canal for renomeado.", fix: "Use o ID do canal (C...).",
		remote: true, edit: func(d string) { replaceIn(d, catFile, "slack: C0ACMECAT01", "slack: \"#catalogo-alertas\"") }},
	{where: "GitHub", cause: "Um dono do CODEOWNERS não corresponde a nenhum time do sentinela.yaml.", fix: "Mapeie o dono num time (teams.<time>.github ou members) ou aceite que esses caminhos sigam o próximo passo do roteamento.",
		remote: true, edit: func(d string) {
			appendTo(d, "repos/plataforma/.github/CODEOWNERS", "\n/testes/                   @acme/qa\n")
		}},
	{where: "GitHub", cause: "O repo não existe ou o token não o enxerga.", fix: "Confira o nome (owner/repo) e se a app ou o token tem acesso ao repo.",
		remote: true, hiddenRepos: []string{"acme/sdk-identidade"}},
	{where: "modo MCP", cause: "No modo MCP, o Linear não mostra quem aplicou a label: o gatilho não é conferido.", fix: "É uma limitação do MCP; para conferir quem pede a correção, use o modo completo.",
		match: "fix_trigger no modo MCP", remote: true, edit: func(d string) { replaceIn(d, mainFile, "tracker: linear_api", "tracker: linear_mcp") }},
	{where: "modo MCP", cause: "Com tracker linear_mcp, issues, comentários e mudanças de estado saem com a conta de uma pessoa (a do MCP do Claude Code); o Sentinela só é reconhecido pelo prefixo **Sentinela, e a cota e as permissões são as dela.", fix: "Serve para avaliação ou piloto. Para uso real, use tracker linear_api com o app OAuth do Sentinela (sentinela linear auth, actor=app), que dá identidade própria e webhook.",
		match: "adapters.tracker linear_mcp", remote: true, edit: func(d string) { replaceIn(d, mainFile, "tracker: linear_api", "tracker: linear_mcp") }},
}

func without(l []string, x string) []string {
	var out []string
	for _, v := range l {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func dropLabel(w *linear.Workspace, team, label string) {
	t, _ := w.Team(team)
	t.Labels = without(t.Labels, label)
}

// Linear, GitHub e Slack falsos do guia: o workspace da acme, como o teste do exemplo.
type guideLinear struct{ ws linear.Workspace }

func (g guideLinear) Workspace(context.Context) (*linear.Workspace, error) { return &g.ws, nil }

func acmeWorkspace() linear.Workspace {
	states := []string{"Triage", "Backlog", "Todo", "In Review", "Done", "Canceled"}
	labels := []string{"sentinela", "sentinela-fix", "sentinela-done", "sentinela-failed", "pagamentos-fix", "identidade-falhou"}
	cp := func() []string { return append([]string(nil), states...) }
	cl := func() []string { return append([]string(nil), labels...) }
	return linear.Workspace{Teams: []linear.Team{
		{Key: "PAG", States: cp(), Labels: cl(), Projects: []string{"Pagamentos · Cobrança", "Pagamentos · Carteira", "Pagamentos · Repasse", "Pagamentos · Fraude", "Pagamentos · Segurança"}},
		{Key: "CAT", States: cp(), Labels: cl(), Projects: []string{"Catálogo"}},
		{Key: "IDN", States: cp(), Labels: cl(), Projects: []string{"Identidade"}},
		{Key: "PLAT", States: cp(), Labels: cl(), Projects: []string{"Plataforma", "Sentinela · Geral"}},
	}}
}

type guideGitHub struct {
	co     repo.StaticCodeowners
	hidden map[string]bool
}

func (g guideGitHub) RepoExists(_ context.Context, name string) (bool, error) {
	return !g.hidden[name], nil
}
func (guideGitHub) TeamExists(context.Context, string, string) (bool, error) { return true, nil }
func (guideGitHub) UserExists(context.Context, string) (bool, error)         { return true, nil }
func (guideGitHub) CommandsDoc(context.Context, string, string) (string, error) {
	return "AGENTS.md", nil
}
func (guideGitHub) Languages(context.Context, string) (map[string]int64, error) {
	return map[string]int64{"Go": 1000}, nil
}
func (g guideGitHub) Codeowners(_ context.Context, name string) (*repo.Codeowners, error) {
	return g.co[name], nil
}

type guideSlack struct{ over map[string]notify.ChannelInfo }

func (g guideSlack) Channel(_ context.Context, ref string) (notify.ChannelInfo, error) {
	if c, ok := g.over[ref]; ok {
		return c, nil
	}
	return notify.ChannelInfo{ID: ref, Name: strings.TrimPrefix(strings.ToLower(ref), "#"), IsMember: true}, nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		b, err := os.ReadFile(p) //nolint:gosec // exemplo do repo
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600) //nolint:gosec // cópia temporária do exemplo
	})
}

func validateCopy(dir string, s errSpec) ([]string, error) {
	if !s.remote {
		out, err := runSelf([]string{"--config", filepath.Join(dir, mainFile), "config", "validate", "--offline"})
		if err != nil {
			return nil, err
		}
		var msgs []string
		for _, l := range strings.Split(out, "\n") {
			if m, ok := strings.CutPrefix(l, "erro: "); ok {
				msgs = append(msgs, "erro\x00"+strings.ReplaceAll(m, dir+string(filepath.Separator), ""))
			}
		}
		return msgs, nil
	}
	cfg, err := config.Load(filepath.Join(dir, mainFile))
	if err != nil {
		return nil, err
	}
	co := repo.StaticCodeowners{}
	for name, f := range acmeCodeownersFiles {
		fh, err := os.Open(filepath.Join(dir, strings.TrimPrefix(f, acmeRoot+"/"))) //nolint:gosec // cópia temporária
		if err != nil {
			return nil, err
		}
		c, err := repo.ParseCodeowners(fh)
		_ = fh.Close()
		if err != nil {
			return nil, err
		}
		co[name] = c
	}
	ws := acmeWorkspace()
	if s.ws != nil {
		s.ws(&ws)
	}
	hidden := map[string]bool{}
	for _, r := range s.hiddenRepos {
		hidden[r] = true
	}
	rep := ValidateRemote(context.Background(), cfg, guideLinear{ws}, guideGitHub{co: co, hidden: hidden}, guideSlack{s.slack})
	var msgs []string
	for _, m := range rep.Errors {
		msgs = append(msgs, "erro\x00"+m)
	}
	for _, m := range rep.Warnings {
		msgs = append(msgs, "aviso\x00"+m)
	}
	return msgs, nil
}

func guideErrors() ([]guide.ErrorCase, error) {
	tmp, err := os.MkdirTemp("", "sentinela-guia-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	baseline := map[bool]map[string]bool{}
	var out []guide.ErrorCase
	for i, s := range errSpecs {
		dir := filepath.Join(tmp, strconv.Itoa(i))
		if err := copyTree(acmeRoot, dir); err != nil {
			return nil, err
		}
		if baseline[s.remote] == nil {
			msgs, err := validateCopy(dir, errSpec{remote: s.remote})
			if err != nil {
				return nil, err
			}
			baseline[s.remote] = map[string]bool{}
			for _, m := range msgs {
				baseline[s.remote][m] = true
			}
		}
		if s.edit != nil {
			s.edit(dir)
		}
		msgs, err := validateCopy(dir, s)
		if err != nil {
			return nil, fmt.Errorf("caso %d (%s): %w", i, s.cause, err)
		}
		n := 0
		for _, m := range msgs {
			if baseline[s.remote][m] || (s.match != "" && !strings.Contains(m, s.match)) {
				continue
			}
			kind, msg, _ := strings.Cut(m, "\x00")
			where := s.where
			if s.remote {
				where += " · config validate"
			} else {
				where += " · config validate --offline"
			}
			out = append(out, guide.ErrorCase{Kind: kind, Where: where, Message: msg, Cause: s.cause, Fix: s.fix})
			n++
		}
		if n == 0 {
			return nil, fmt.Errorf("caso %d (%s): a validação não mostrou nada novo", i, s.cause)
		}
	}
	return out, nil
}

// --- Referência: as chaves do JSON Schema, com a descrição e o padrão de docs/configuracao.md. ---

type docKey struct{ def, desc string }

var (
	backtick   = regexp.MustCompile("`([^`]+)`")
	mdLink     = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	headingKey = regexp.MustCompile("^### `([^`]+)`")
)

func cleanMD(s string) string {
	s = mdLink.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "`", "")
	s = strings.ReplaceAll(s, `\|`, "|")
	return strings.TrimSpace(s)
}

func normKey(k string) string {
	k = strings.ReplaceAll(k, "<nome>", "<time>")
	return k
}

// docKeys lê as tabelas de referência de docs/configuracao.md: chave completa → padrão e descrição.
func docKeys(md string) map[string]docKey {
	out := map[string]docKey{}
	prefix := ""
	defCol := -1
	header := false
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(l, "## ") || strings.HasPrefix(l, "### ") {
			prefix = ""
			if m := headingKey.FindStringSubmatch(l); m != nil && !strings.Contains(l, "` e `") {
				prefix = normKey(m[1])
			}
			header = true
			continue
		}
		if prefix == "" || !strings.HasPrefix(l, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.ReplaceAll(l, `\|`, "\x01"), "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(strings.ReplaceAll(cells[i], "\x01", `\|`))
		}
		if header {
			defCol = -1
			for i, c := range cells {
				if c == "Padrão" {
					defCol = i
				}
			}
			header = false
			continue
		}
		if strings.HasPrefix(cells[0], "---") || len(cells) < 2 {
			continue
		}
		dk := docKey{desc: cleanMD(cells[len(cells)-1])}
		if defCol > 0 && defCol < len(cells)-1 {
			dk.def = cleanMD(cells[defCol])
		}
		for _, m := range backtick.FindAllStringSubmatch(cells[0], -1) {
			out[prefix+"."+normKey(m[1])] = dk
		}
	}
	return out
}

type refWalker struct {
	out []guide.KeyRef
}

func typeName(n map[string]any) string {
	if one, ok := n["oneOf"].([]any); ok {
		var parts []string
		for _, o := range one {
			om, _ := o.(map[string]any)
			if e, ok := om["enum"].([]any); ok {
				for _, v := range e {
					parts = append(parts, fmt.Sprint(v))
				}
			} else {
				parts = append(parts, typeName(om))
			}
		}
		return strings.Join(parts, " | ")
	}
	t, _ := n["type"].(string)
	switch t {
	case "array":
		if it, ok := n["items"].(map[string]any); ok {
			return "lista de " + typeName(it)
		}
	case "object":
		if ap, ok := n["additionalProperties"].(map[string]any); ok {
			return "mapa de " + typeName(ap)
		}
	case "string":
		if _, ok := n["pattern"]; ok {
			return "duração (ex.: 15m)"
		}
		if f, ok := n["format"].(string); ok && f == "date-time" {
			return "data e hora"
		}
	}
	if e, ok := n["enum"].([]any); ok {
		var parts []string
		for _, v := range e {
			parts = append(parts, fmt.Sprint(v))
		}
		return strings.Join(parts, " | ")
	}
	return t
}

func placeholderFor(key string) string {
	switch key {
	case "teams":
		return "<time>"
	case "models":
		return "<modelo>"
	case "category_projects":
		return "<categoria>"
	}
	return "<chave>"
}

func (w *refWalker) walk(n map[string]any, path, last string) {
	if props, ok := n["properties"].(map[string]any); ok {
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			w.walk(props[k].(map[string]any), p, k)
		}
		return
	}
	if ap, ok := n["additionalProperties"].(map[string]any); ok {
		if _, nested := ap["properties"]; nested {
			w.walk(ap, path+"."+placeholderFor(last), last)
			return
		}
	}
	if it, ok := n["items"].(map[string]any); ok {
		if _, nested := it["properties"]; nested {
			w.walk(it, path+"[]", last)
			return
		}
	}
	w.out = append(w.out, guide.KeyRef{Path: path, Type: typeName(n)})
}

func schemaRefs(file string) ([]guide.KeyRef, error) {
	b, err := os.ReadFile(file) //nolint:gosec // arquivo do repo
	if err != nil {
		return nil, err
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	w := &refWalker{}
	w.walk(s, "", "")
	return w.out, nil
}

// lookup acha a descrição da chave ou, sem ela, a do pai mais próximo (sem o padrão, que é só da
// chave exata).
func lookup(docs map[string]docKey, path string) docKey {
	if d, ok := docs[path]; ok {
		return d
	}
	p := path
	for {
		i := strings.LastIndexAny(p, ".[")
		if i < 0 {
			return docKey{}
		}
		p = p[:i]
		if d, ok := docs[p]; ok {
			return docKey{desc: d.desc}
		}
		if d, ok := docs[p+"[]"]; ok {
			return docKey{desc: d.desc}
		}
	}
}

func guideConfigRef() ([]guide.KeyRef, []guide.KeyRef, error) {
	md, err := os.ReadFile("docs/configuracao.md")
	if err != nil {
		return nil, nil, err
	}
	docs := docKeys(string(md))
	main, err := schemaRefs("schema/sentinela.schema.json")
	if err != nil {
		return nil, nil, err
	}
	team, err := schemaRefs("schema/team.schema.json")
	if err != nil {
		return nil, nil, err
	}
	teamPaths := map[string]bool{}
	for _, r := range team {
		teamPaths[r.Path] = true
	}
	for i := range main {
		r := &main[i]
		d := lookup(docs, r.Path)
		r.Desc, r.Default = d.desc, d.def
		if r.Default == "" {
			r.Default = "—"
		}
		r.Where = "só no principal"
		switch {
		case strings.HasPrefix(r.Path, "teams.<time>."):
			if teamPaths[strings.TrimPrefix(r.Path, "teams.<time>.")] {
				r.Where = "principal ou arquivo do time"
			}
		case strings.HasPrefix(r.Path, "repos[].modules[]."):
			if teamPaths[strings.Replace(r.Path, "repos[].modules[]", "modules[]", 1)] {
				r.Where = "principal ou arquivo do time (modules, com repo)"
			}
		case strings.HasPrefix(r.Path, "repos[]."):
			if teamPaths[r.Path] {
				r.Where = "principal ou arquivo do time (repos do time)"
			}
		case r.Path == "blocked_paths":
			r.Where = "principal; o time só acrescenta no arquivo dele"
		}
	}
	for i := range team {
		r := &team[i]
		var d docKey
		switch {
		case strings.HasPrefix(r.Path, "modules[]."):
			d = lookup(docs, "repos[]."+r.Path)
			if r.Path == "modules[].repo" {
				d = docKey{desc: "Repo do módulo (owner/repo): um repo compartilhado declarado no principal, ou um repo do próprio time"}
			}
		case strings.HasPrefix(r.Path, "repos[]."):
			d = lookup(docs, r.Path)
		case r.Path == "blocked_paths":
			d = docKey{desc: "Caminhos que o time acrescenta à lista da organização (nunca auto-aprovados)"}
		default:
			d = lookup(docs, "teams.<time>."+r.Path)
		}
		r.Desc, r.Default = d.desc, d.def
		if r.Default == "" {
			r.Default = "—"
		}
	}
	return main, team, nil
}

// --- Comandos e flags, do próprio binário. ---

func guideCommands() []guide.CommandRef {
	root := NewRootCmd(func(string) string { return "" })
	var out []guide.CommandRef
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		ref := guide.CommandRef{Path: c.CommandPath(), Short: c.Short}
		if u := strings.TrimPrefix(c.Use, c.Name()); strings.TrimSpace(u) != "" {
			ref.Path += u
		}
		fs := c.LocalNonPersistentFlags()
		if c == root {
			fs = c.PersistentFlags()
		}
		fs.VisitAll(func(fl *pflag.Flag) {
			if fl.Hidden || fl.Name == "help" {
				return
			}
			def := fl.DefValue
			if def == "" || def == "[]" {
				def = "—"
			}
			ref.Flags = append(ref.Flags, guide.FlagRef{Name: fl.Name, Short: fl.Shorthand, Type: fl.Value.Type(), Default: def, Usage: fl.Usage})
		})
		out = append(out, ref)
		subs := c.Commands()
		sort.Slice(subs, func(i, j int) bool { return subs[i].Name() < subs[j].Name() })
		for _, s := range subs {
			walk(s)
		}
	}
	walk(root)
	return out
}

// yamlFileJSON converte um yaml em JSON mantendo a ordem das chaves (o gerador do guia parte dele).
func yamlFileJSON(file string) (json.RawMessage, error) {
	b, err := os.ReadFile(file) //nolint:gosec // arquivo do repo
	if err != nil {
		return nil, err
	}
	var n yaml.Node
	if err := yaml.Unmarshal(b, &n); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := nodeJSON(&buf, &n); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func nodeJSON(w *bytes.Buffer, n *yaml.Node) error {
	switch n.Kind {
	case yaml.DocumentNode:
		return nodeJSON(w, n.Content[0])
	case yaml.MappingNode:
		w.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			if i > 0 {
				w.WriteByte(',')
			}
			k, _ := json.Marshal(n.Content[i].Value)
			w.Write(k)
			w.WriteByte(':')
			if err := nodeJSON(w, n.Content[i+1]); err != nil {
				return err
			}
		}
		w.WriteByte('}')
	case yaml.SequenceNode:
		w.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				w.WriteByte(',')
			}
			if err := nodeJSON(w, c); err != nil {
				return err
			}
		}
		w.WriteByte(']')
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!int", "!!float", "!!bool":
			w.WriteString(n.Value)
		case "!!null":
			w.WriteString("null")
		default:
			v, _ := json.Marshal(n.Value)
			w.Write(v)
		}
	case yaml.AliasNode:
		return nodeJSON(w, n.Alias)
	}
	return nil
}

// runInitScenario roda o init de verdade num diretório temporário, com as pontas (docker, banco,
// gh, claude) simuladas pelo cenário: é a saída que o leitor vê na máquina dele nesse caso.
func runInitScenario(scenario string, args []string) (string, error) {
	migrations, err := filepath.Glob("internal/store/migrations/*.sql")
	if err != nil {
		return "", err
	}
	prev, err := os.Getwd()
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp("", "sentinela-guia-init-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Chdir(prev); _ = os.RemoveAll(tmp) }()
	if err := os.Chdir(tmp); err != nil {
		return "", err
	}
	_ = os.WriteFile("docker-compose.yml", []byte("services: {}\n"), 0o600)
	mcpList := "claude.ai Linear: https://mcp.linear.app/mcp - ✔ Connected\nclaude.ai Slack: https://mcp.slack.com/mcp - ✔ Connected\n"
	if scenario == "mcp-sem-linear" {
		mcpList = "claude.ai Slack: https://mcp.slack.com/mcp - ✔ Connected\n"
	}
	teams := []string{"ENG"}
	if scenario == "mcp-varios-times" {
		teams = []string{"ENG", "OPS"}
	}
	hooks := &initHooks{
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Run: func(_ context.Context, name string, a ...string) (string, error) {
			if name == "claude" && len(a) > 1 && a[0] == "mcp" {
				return mcpList, nil
			}
			return "", nil
		},
		Connect:    func(context.Context, string) error { return nil },
		Migrate:    func(context.Context, string) (int, error) { return len(migrations), nil },
		ClaudeAuth: func(context.Context) (bool, string, error) { return true, "claude.ai", nil },
		Claim:      func(context.Context, string, string, bool) error { return nil },
		GHLogin: func(context.Context) (string, error) {
			if scenario == "gh-sem-login" {
				return "", errors.New("not logged into any GitHub hosts; run gh auth login")
			}
			return "dev-exemplo", nil
		},
		LinearTeams: func(context.Context, string) ([]string, error) { return teams, nil },
	}
	cmd := newRootCmd(func(string) string { return "" }, hooks)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(context.Background()); err != nil && !errors.Is(err, ErrInitIncomplete) {
		return buf.String(), fmt.Errorf("sentinela %s: %w", strings.Join(args, " "), err)
	}
	return buf.String(), nil
}
