package config

import (
	"strings"
	"testing"
)

// execution.home_files (ADR 0057): caminhos relativos ao HOME da máquina, sem sair dele e sem as
// credenciais que o HOME do job existe para não ter (git, ssh, gh, Sentinela, Claude Code).
func TestHomeFilesRefuseCredentialsOfTheMachine(t *testing.T) {
	base := example(t)
	ok := strings.Replace(base, "\nexecution:", "\nexecution:\n  home_files: [.npmrc, .gradle/gradle.properties, .m2/settings.xml]", 1)
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatalf("lista válida: %v", err)
	}
	for _, bad := range []string{".ssh/id_ed25519", ".gitconfig", ".config/gh/hosts.yml", ".config/sentinela/linear-token.json", ".claude/.credentials.json", "/etc/passwd", "../fora", ".git-credentials", ""} {
		y := strings.Replace(base, "\nexecution:", "\nexecution:\n  home_files: [\""+bad+"\"]", 1)
		if _, err := Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), "execution.home_files") {
			t.Errorf("%q deveria ser recusado: %v", bad, err)
		}
	}
}

// discovery.semgrep_languages (Fase C, passo 5): padrão para Java, Kotlin, TypeScript e
// JavaScript; um mapa no arquivo substitui o padrão, e {} desliga.
func TestSemgrepLanguagesDefaultAndOverride(t *testing.T) {
	base := example(t)
	cfg, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	l := cfg.Discovery.SemgrepLanguages
	if len(l["java"]) == 0 || len(l["kotlin"]) == 0 || len(l["typescript"]) == 0 || len(l["javascript"]) == 0 {
		t.Errorf("padrão = %v", l)
	}
	y := strings.Replace(base, "\ndiscovery:", "\ndiscovery:\n  semgrep_languages: {java: [p/java, p/spring]}", 1)
	cfg, err = Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Discovery.SemgrepLanguages; len(got) != 1 || len(got["java"]) != 2 {
		t.Errorf("substituído = %v", got)
	}
	y = strings.Replace(base, "\ndiscovery:", "\ndiscovery:\n  semgrep_languages: {}", 1)
	if cfg, err = Parse([]byte(y)); err != nil || len(cfg.Discovery.SemgrepLanguages) != 0 {
		t.Errorf("{} desliga: %v (%v)", cfg.Discovery.SemgrepLanguages, err)
	}
	y = strings.Replace(base, "\ndiscovery:", "\ndiscovery:\n  semgrep_languages: {java: [\"\"]}", 1)
	if _, err := Parse([]byte(y)); err == nil || !strings.Contains(err.Error(), "semgrep_languages") {
		t.Errorf("config vazia deveria falhar: %v", err)
	}
}
