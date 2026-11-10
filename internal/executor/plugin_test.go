package executor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

const pluginDir = "../../plugins/sentinela-factory"

// frontmatter lê o bloco --- ... --- de um arquivo do plugin.
func frontmatter(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(data), "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		t.Fatalf("%s sem frontmatter", path)
	}
	out := map[string]string{}
	for _, l := range strings.Split(parts[1], "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	out["body"] = parts[2]
	return out
}

// O plugin é o contrato entre a skill e o worker: cada subagente citado pela skill existe, tem
// nome, ferramentas e modelo, e os vereditos que a skill exige são os que o Go confere.
func TestPluginMatchesTheContractsTheWorkerChecks(t *testing.T) {
	skill := frontmatter(t, filepath.Join(pluginDir, "skills", "sentinela-fix", "SKILL.md"))
	if skill["name"] != "sentinela-fix" {
		t.Fatalf("skill = %q", skill["name"])
	}
	cited := regexp.MustCompile("`sentinela-factory:([a-z-]+)`").FindAllStringSubmatch(skill["body"], -1)
	if len(cited) < 7 {
		t.Fatalf("a skill deveria citar os 7 subagentes, citou %d", len(cited))
	}
	readOnly := map[string]bool{"spec-writer": true}
	for _, c := range cited {
		fm := frontmatter(t, filepath.Join(pluginDir, "agents", c[1]+".md"))
		if fm["name"] != c[1] || fm["description"] == "" || fm["model"] == "" || fm["tools"] == "" {
			t.Errorf("subagente %s com frontmatter incompleto: %v", c[1], fm)
		}
		if readOnly[c[1]] && strings.Contains(fm["tools"], "Bash") {
			t.Errorf("%s não precisa de Bash", c[1])
		}
		if c[1] != "builder" && strings.Contains(fm["tools"], "Edit") {
			t.Errorf("só o builder edita código; %s tem Edit", c[1])
		}
	}
	for _, v := range append(append(append(append([]string{}, specVerdicts...), buildVerdicts...), reviewVerdicts...), decisionVerdicts...) {
		if !strings.Contains(skill["body"], "`"+v+"`") {
			t.Errorf("a skill não cita o veredito %q que o worker exige", v)
		}
	}
	for _, f := range []string{"state.json", "max_rounds", "SendMessage", "em paralelo"} {
		if !strings.Contains(skill["body"], f) {
			t.Errorf("a skill deveria tratar %q", f)
		}
	}
	sh := frontmatter(t, filepath.Join(pluginDir, "skills", "sentinela-shepherd", "SKILL.md"))
	for _, v := range shepherdVerdicts {
		if !strings.Contains(sh["body"], "`"+v+"`") {
			t.Errorf("o shepherd não cita %q", v)
		}
	}
	for _, body := range []string{skill["body"], sh["body"], frontmatter(t, filepath.Join(pluginDir, "agents", "builder.md"))["body"]} {
		if !strings.Contains(body, "git push") {
			t.Error("skill e builder precisam proibir git push explicitamente")
		}
	}
}

func TestRenderPluginUsesTheModelsFromTheConfig(t *testing.T) {
	m := config.ModelRoles{SpecWriter: "s", Executor: "builder-model", Approver: "a", ReviewerSecurity: "rs", ReviewerCode: "rc", ReviewerReliability: "rr", ReviewerDomain: "rd"}
	dst := t.TempDir()
	if err := RenderPlugin(pluginDir, dst, m); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"builder": "builder-model", "spec-writer": "s", "approver": "a", "reviewer-security": "rs", "reviewer-code": "rc", "reviewer-reliability": "rr", "reviewer-domain": "rd"}
	for agent, model := range want {
		if got := frontmatter(t, filepath.Join(dst, "agents", agent+".md"))["model"]; got != model {
			t.Errorf("%s: model %q, quer %q", agent, got, model)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, ".claude-plugin", "plugin.json")); err != nil {
		t.Error("manifesto do plugin não foi copiado")
	}
}

func TestForeignInstructionsAboveTheWorktreeAreDetected(t *testing.T) {
	root := t.TempDir()
	wt := filepath.Join(root, "proj", "worktrees", "EX-1")
	_ = os.MkdirAll(wt, 0o750)
	if got := ForeignInstructions(wt); len(got) != 0 {
		t.Fatalf("sem CLAUDE.md acima: %v", got)
	}
	_ = os.WriteFile(filepath.Join(root, "proj", "CLAUDE.md"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(wt, "CLAUDE.md"), []byte("o do próprio repo vale"), 0o600)
	if got := ForeignInstructions(wt); len(got) != 1 || got[0] != filepath.Join(root, "proj", "CLAUDE.md") {
		t.Errorf("achados = %v", got)
	}
}
