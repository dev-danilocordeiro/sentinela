package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// O perfil qualidade é o padrão de quem instala do zero (ADR 0051); o economia é o conjunto de
// antes, para orçamento ou cota limitados.
func TestModelProfiles(t *testing.T) {
	q, err := Profile(ProfileQuality)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"triage": opus, "analysis": opus, "architect": opus, "judge": sonnet,
		"spec_writer": sonnet, "executor": sonnet, "reviewer_security": opus,
		"reviewer_code": sonnet, "reviewer_reliability": sonnet, "reviewer_domain": sonnet,
		"approver": opus, "curator": sonnet, "notes": sonnet, "orchestrator": sonnet, "shepherd": sonnet,
	}
	for _, r := range q.Roles.roles() {
		if want[r.key] != r.id {
			t.Errorf("qualidade.%s = %s, quer %s", r.key, r.id, want[r.key])
		}
	}
	if q.JudgeVotes != 3 || q.RefusalFallback == q.Roles.Triage {
		t.Errorf("qualidade: judge_votes %d, fallback %s", q.JudgeVotes, q.RefusalFallback)
	}
	e, _ := Profile(ProfileEconomy)
	if e.Roles.Triage != haiku || e.Roles.Curator != haiku || e.Roles.Analysis != sonnet || e.Roles.Executor != opus || e.JudgeVotes != 1 {
		t.Errorf("economia = %+v", e)
	}
	c, _ := Profile(ProfileCustom)
	if c.Name != ProfileCustom || !c.Roles.sameRoles(q.Roles) {
		t.Errorf("personalizado parte do qualidade: %+v", c)
	}
	if _, err := Profile("barato"); err == nil {
		t.Error("perfil inexistente deveria falhar")
	}
	if got := ProfilesOf("judge", sonnet); len(got) != 2 {
		t.Errorf("judge em Sonnet é dos dois perfis: %v", got)
	}
	if got := ProfilesOf("triage", "claude-fable-5-1"); len(got) != 0 {
		t.Errorf("modelo fora dos perfis: %v", got)
	}
}

// O exemplo do modo completo segue o perfil padrão.
func TestExampleConfigUsesTheQualityProfile(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "sentinela.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ModelsProfile(); got != ProfileQuality {
		t.Errorf("sentinela.example.yaml: perfil %s", got)
	}
	if cfg.Discovery.JudgeVotes != 3 {
		t.Errorf("judge_votes = %d", cfg.Discovery.JudgeVotes)
	}
}

// O teto de 100k da triagem só vale com Haiku, onde o preço muda de faixa; com Opus (perfil
// qualidade) a triagem usa até 150k.
func TestTriageContextLimitOnlyAppliesToHaiku(t *testing.T) {
	q, _ := Profile(ProfileQuality)
	e, _ := Profile(ProfileEconomy)
	if q.TriageContextTokens <= 100_000 || q.AnalysisContextTokens <= e.AnalysisContextTokens ||
		q.ClaudeCodeConcurrency <= e.ClaudeCodeConcurrency || e.TriageContextTokens >= 100_000 {
		t.Errorf("qualidade %+v / economia %+v", q, e)
	}
	load := func(t *testing.T, triage, override string) error {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", "sentinela.example.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		s := strings.Replace(string(data), "  triage: claude-opus-5-5\n", "  triage: "+triage+"\n", 1)
		s = strings.Replace(s, "  - name: acme/monorepo\n", "  - name: acme/monorepo\n"+override, 1)
		_, err = Parse([]byte(s))
		return err
	}
	if err := load(t, "claude-opus-5-5", ""); err != nil {
		t.Errorf("Opus com 150k: %v", err)
	}
	if err := load(t, "claude-haiku-5-5", ""); err == nil || !strings.Contains(err.Error(), "triage_context_tokens") {
		t.Errorf("Haiku com 150k deveria falhar: %v", err)
	}
	if err := load(t, "claude-opus-5-5", "    models: { triage: claude-haiku-5-5 }\n"); err == nil {
		t.Error("Haiku num repo com 150k deveria falhar")
	}
}
