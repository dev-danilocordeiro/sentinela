package cli

import (
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// O init gera o bloco models completo do perfil escolhido, com o perfil num comentário, nos dois
// modos (ADR 0051).
func TestInitConfigCarriesTheChosenModelsProfile(t *testing.T) {
	for _, name := range []string{config.ProfileQuality, config.ProfileEconomy, config.ProfileCustom} {
		p, err := config.Profile(name)
		if err != nil {
			t.Fatal(err)
		}
		for mode, y := range map[string]string{
			"local": LocalConfigWith([]string{"acme/svc"}, p),
			"mcp":   MCPConfigWith([]string{"acme/svc"}, "HAY", "", p),
		} {
			cfg, err := config.Parse([]byte(y))
			if err != nil {
				t.Fatalf("%s/%s não carrega: %v", name, mode, err)
			}
			wantProfile := name
			if name == config.ProfileCustom {
				wantProfile = config.ProfileQuality // parte do qualidade, para editar
			}
			if got := cfg.ModelsProfile(); got != wantProfile {
				t.Errorf("%s/%s: bloco models do perfil %s", name, mode, got)
			}
			if !strings.Contains(y, "# Modelos: perfil "+name) || cfg.Discovery.JudgeVotes != p.JudgeVotes {
				t.Errorf("%s/%s: sem o comentário do perfil ou judge_votes %d", name, mode, cfg.Discovery.JudgeVotes)
			}
			d, cc := cfg.Discovery, cfg.LLM.ClaudeCode
			if d.TriageContextTokens != p.TriageContextTokens || d.AnalysisContextTokens != p.AnalysisContextTokens ||
				cc.Concurrency != p.ClaudeCodeConcurrency || cc.Timeout != p.ClaudeCodeTimeout || cfg.Budget.PerRunUSD != p.BudgetPerRunUSD {
				t.Errorf("%s/%s: ajustes do perfil = %+v %+v %v", name, mode, d, cc, cfg.Budget)
			}
			if cfg.Models.Shepherd == "" || cfg.Models.Notes == "" || cfg.Models.Orchestrator == "" {
				t.Errorf("%s/%s: bloco incompleto: %+v", name, mode, cfg.Models.ModelRoles)
			}
		}
	}
	if got, _ := config.Parse([]byte(LocalConfig(nil))); got.ModelsProfile() != config.DefaultProfile {
		t.Errorf("padrão do init: %s", got.ModelsProfile())
	}
}

func TestConfigBootstrapWritesTheChosenModelsProfile(t *testing.T) {
	env := envFor(mockLinear(t, nil), mockGitHub(t))
	for flag, want := range map[string]string{"": config.ProfileQuality, "economia": config.ProfileEconomy} {
		args := []string{"config", "bootstrap", "--repo", "acme/monorepo-poliglota"}
		if flag != "" {
			args = append(args, "--models", flag)
		}
		out, err := run(t, env, args...)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		cfg, err := config.Parse([]byte(out))
		if err != nil {
			t.Fatalf("não carrega: %v\n%s", err, out)
		}
		if cfg.ModelsProfile() != want || !strings.Contains(out, "# Modelos: perfil "+want) {
			t.Errorf("--models %q: perfil %s\n%s", flag, cfg.ModelsProfile(), out)
		}
	}
	out, err := run(t, env, "config", "bootstrap", "--repo", "acme/monorepo-poliglota", "--models", "personalizado")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	base, _ := config.Load(rootConfig)
	cfg, _ := config.Parse([]byte(out))
	if cfg.Models.ModelRoles != base.Models.ModelRoles || !strings.Contains(out, "# Modelos: perfil personalizado, copiado de --config") {
		t.Errorf("personalizado mantém os modelos da config base:\n%s", out)
	}
	if _, err := run(t, env, "config", "bootstrap", "--repo", "acme/monorepo-poliglota", "--models", "barato"); err == nil {
		t.Error("perfil inexistente deveria falhar")
	}
}

func TestConfigModelsShowsTheProfileOfEachRole(t *testing.T) {
	out, err := run(t, map[string]string{}, "config", "models")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "bloco models: perfil ") || !strings.Contains(out, "perfil\n") {
		t.Errorf("sem o perfil:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "judge ") && !strings.Contains(l, "economia, qualidade") && !strings.Contains(l, "personalizado") {
			t.Errorf("linha do judge: %s", l)
		}
	}
}
