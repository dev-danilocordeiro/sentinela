package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// TeamFilesDir é onde ficam os arquivos dos times, ao lado do sentinela.yaml (ADR 0041).
const TeamFilesDir = "config.d/teams"

// TeamFile é config.d/teams/<time>.yaml: o que um time mantém por PR no repo do Sentinela. O nome
// do arquivo é o nome do time. Política da organização (auto-aprovação, revisora que aprova,
// modelos, limites de custo, adapters) só vale no sentinela.yaml principal.
type TeamFile struct {
	GitHub   []string     `yaml:"github,omitempty"`
	Members  []string     `yaml:"members,omitempty"`
	Linear   LinearTarget `yaml:"linear,omitempty"`
	Assignee AssigneeMode `yaml:"assignee,omitempty"`
	Lead     string       `yaml:"lead,omitempty"`
	Notify   Notify       `yaml:"notify,omitempty"`
	Policy   TeamPolicy   `yaml:"policy,omitempty"`
	Labels   TeamLabels   `yaml:"labels,omitempty"`
	// Modules são os módulos do time em repos do sentinela.yaml (ou de Repos abaixo).
	Modules []TeamModule `yaml:"modules,omitempty"`
	// Repos são repos inteiros do time (o caso comum: um repo, um time).
	Repos []TeamRepo `yaml:"repos,omitempty"`
	// BlockedPaths só acrescenta caminhos à lista da organização.
	BlockedPaths []string `yaml:"blocked_paths,omitempty"`
}

// TeamPolicy é a parte da política que o time pode apertar: tamanho do PR e bloqueio do domínio.
// auto_approve é decisão da organização.
type TeamPolicy struct {
	Blocked  *bool `yaml:"blocked,omitempty"`
	MaxLines *int  `yaml:"max_lines,omitempty"`
	MaxFiles *int  `yaml:"max_files,omitempty"`
}

// TeamModule é um módulo do time num repo.
type TeamModule struct {
	Repo          string `yaml:"repo"`
	Path          string `yaml:"path"`
	LinearProject string `yaml:"linear_project,omitempty"`
	// Override: vence os módulos comuns que também casam (arquivos transversais, ADR 0043).
	Override bool `yaml:"override,omitempty"`
}

// TeamRepo é um repo inteiro do time.
type TeamRepo struct {
	Name          string   `yaml:"name"`
	DefaultBranch string   `yaml:"default_branch,omitempty"`
	Languages     []string `yaml:"languages,omitempty"`
	LinearProject string   `yaml:"linear_project,omitempty"`
}

// orgOnly são as chaves que só valem no arquivo principal, com o motivo.
var orgOnly = map[string]string{
	"models":            "modelos e custos são da organização",
	"budget":            "limites de custo são da organização",
	"pricing":           "a tabela de preços é da organização",
	"llm":               "o provider de modelos é da organização",
	"adapters":          "os modos de operação (tracker, avisos, host git) são da organização",
	"github_apps":       "reviewer_approves e as GitHub Apps são da organização",
	"execution":         "a linha de execução é da organização",
	"discovery":         "a descoberta é da organização",
	"defaults":          "os padrões valem para todos os times",
	"teams":             "cada arquivo já é um time (o nome do arquivo); não declare outros times",
	"publish":           "os limites de publicação são da organização",
	"feedback":          "o Feedback Curator é da organização",
	"trigger":           "o gatilho do sync é da organização",
	"linear_auth":       "a identidade no Linear é da organização",
	"workspace":         "o workspace é da organização",
	"telemetry":         "a telemetria é da organização",
	"signals":           "as fontes de sinal são da organização",
	"reviewer_approves": "a revisora que aprova é decisão da organização (github_apps.reviewer_approves)",
	"auto_approve":      "auto-aprovação é decisão da organização (defaults.policy ou teams.<time>.policy no principal)",
}

// loadTeamFiles junta config.d/teams/*.yaml ao config principal (antes dos padrões e da validação).
func (c *Config) loadTeamFiles(mainPath string) error {
	dir := filepath.Join(filepath.Dir(mainPath), filepath.FromSlash(TeamFilesDir))
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil || len(files) == 0 {
		return err
	}
	sort.Strings(files)
	var errs []error
	for _, f := range files {
		rel := filepath.ToSlash(filepath.Join(TeamFilesDir, filepath.Base(f)))
		if err := c.mergeTeamFile(f, rel); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Config) mergeTeamFile(path, rel string) error {
	data, err := os.ReadFile(path) //nolint:gosec // arquivo de config.d ao lado do principal
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	name := strings.TrimSuffix(filepath.Base(path), ".yaml")
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: "+format, append([]any{rel}, args...)...))
	}
	// Antes do decode estrito: chave de política da organização ganha uma mensagem clara.
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	for _, k := range sortedKeys(raw) {
		if why, ok := orgOnly[k]; ok {
			add("chave %q só vale no sentinela.yaml principal: %s", k, why)
		}
	}
	if pol, ok := raw["policy"].(map[string]any); ok {
		if _, ok := pol["auto_approve"]; ok {
			add("chave \"policy.auto_approve\" só vale no sentinela.yaml principal: %s", orgOnly["auto_approve"])
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	var tf TeamFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&tf); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	team := &Team{
		GitHub: tf.GitHub, Members: tf.Members, Linear: tf.Linear, Assignee: tf.Assignee, Lead: tf.Lead,
		Notify: tf.Notify, Labels: tf.Labels,
		Policy: Policy{Blocked: tf.Policy.Blocked, MaxLines: tf.Policy.MaxLines, MaxFiles: tf.Policy.MaxFiles},
	}
	if org, dup := c.Teams[name]; dup {
		// O principal pode ter teams.<time> só com a parte da organização (policy e models); o resto
		// vem do arquivo do time. Política do principal vence a do arquivo.
		if !orgOnlyTeam(org) {
			add("o time %q já está em teams do sentinela.yaml com chaves do time; no principal, teams.%s só pode ter policy, models e fix_trigger (a parte da organização), o resto fica neste arquivo", name, name)
			return errors.Join(errs...)
		}
		team.Policy = org.Policy.Merge(team.Policy)
		team.Models, team.FixTrigger = org.Models, org.FixTrigger
	}
	if c.Teams == nil {
		c.Teams = map[string]*Team{}
	}
	c.Teams[name] = team
	if c.teamFiles == nil {
		c.teamFiles = map[string]string{}
	}
	c.teamFiles[name] = rel
	for _, r := range tf.Repos {
		if r.Name == "" {
			add("repos: name é obrigatório")
			continue
		}
		if _, exists := c.Repo(r.Name); exists {
			add("o repo %s já está no sentinela.yaml ou em outro arquivo de time; um repo inteiro tem um time só (para dividir por módulo, use modules)", r.Name)
			continue
		}
		c.Repos = append(c.Repos, &Repo{Name: r.Name, DefaultBranch: r.DefaultBranch, Languages: r.Languages, LinearProject: r.LinearProject, Team: name, from: rel})
	}
	for _, m := range tf.Modules {
		if m.Repo == "" || m.Path == "" {
			add("modules: repo e path são obrigatórios")
			continue
		}
		rp, ok := c.Repo(m.Repo)
		if !ok {
			add("modules: o repo %s não está no sentinela.yaml (o principal declara os repos compartilhados)", m.Repo)
			continue
		}
		rp.Modules = append(rp.Modules, Module{Path: m.Path, Team: name, LinearProject: m.LinearProject, Override: m.Override, from: rel})
	}
	for _, g := range tf.BlockedPaths {
		if !slices.Contains(c.BlockedPaths, g) {
			c.BlockedPaths = append(c.BlockedPaths, g)
		}
	}
	return errors.Join(errs...)
}

// orgOnlyTeam diz se o time do principal só tem a parte da organização (policy, models e
// fix_trigger).
func orgOnlyTeam(t *Team) bool {
	rest := *t
	rest.Policy, rest.Models, rest.FixTrigger = Policy{}, ModelRoles{}, nil
	return reflect.ValueOf(rest).IsZero()
}

// duplicateModules acha o mesmo caminho de módulo declarado para dois times no mesmo repo.
func (c *Config) duplicateModules() []error {
	var errs []error
	for _, r := range c.Repos {
		seen := map[string]Module{}
		for _, m := range r.Modules {
			if prev, ok := seen[m.Path]; ok && prev.Team != m.Team {
				errs = append(errs, fmt.Errorf("repos[%s]: o módulo %q está em dois times: %s (%s) e %s (%s); cada caminho tem um dono só",
					r.Name, m.Path, orStr(prev.Team, "ignore"), orStr(prev.from, "sentinela.yaml"), orStr(m.Team, "ignore"), orStr(m.from, "sentinela.yaml")))
				continue
			}
			seen[m.Path] = m
		}
	}
	return errs
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
