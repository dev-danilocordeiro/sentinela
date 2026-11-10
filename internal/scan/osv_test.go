package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fase C, passo 4: o osv-scanner lê lockfiles e manifestos; cada vulnerabilidade de cada pacote
// vira um sinal com o manifesto, o ecossistema, a severidade, a versão corrigida e se a
// dependência é só de desenvolvimento.
func TestOSVScannerParsesLockfilesAndManifests(t *testing.T) {
	out := fixture(t, "osv-npm-maven.json")
	sigs, err := OSVScanner{}.Parse(out, "/checkout")
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 9 {
		t.Fatalf("sinais = %d, quer 9 (braces 1, next 2, postcss 4, sharp 1, source-map-js 1)", len(sigs))
	}
	by := map[string]*Vuln{}
	for _, s := range sigs {
		if s.Vuln == nil || s.File != "web/package-lock.json" || s.Vuln.Manifest != "web/package-lock.json" || s.Vuln.Ecosystem != "npm" {
			t.Fatalf("sinal = %+v / %+v", s, s.Vuln)
		}
		by[s.Vuln.Module+" "+s.Vuln.OSV] = s.Vuln
	}
	for key, want := range map[string]struct{ fixed, sev string }{
		"next GHSA-4jqv-mc3x-m676":          {"15.5.27", "moderate"},
		"postcss GHSA-6g55-p6wh-862q":       {"8.5.12", "high"},
		"sharp GHSA-wq5f-xc86-pv6w":         {"0.35.5", "high"},
		"source-map-js GHSA-68fv-2mgg-jv7q": {"1.2.2", "high"},
	} {
		v := by[key]
		if v == nil || v.FixedVersion != want.fixed || v.Severity != want.sev || v.Dev {
			t.Errorf("%s = %+v, quer corrigida em %s, %s, de produção", key, v, want.fixed, want.sev)
		}
	}
	if v := by["braces GHSA-vfj7-8cjw-p6xm"]; v == nil || !v.Dev || v.FixedVersion != "" {
		t.Errorf("braces (só desenvolvimento, sem correção) = %+v", v)
	}
	if n, unit := (OSVScanner{}).Coverage(out, "/checkout"); n != 2 || unit != "manifestos" {
		t.Errorf("cobertura = %d %s", n, unit)
	}
	// No pom.xml, as versões vêm do parent ou do BOM: o osv-scanner não as resolve e não confere.
	if note := (OSVScanner{}).note(out, t.TempDir()); !strings.Contains(note, "15 pacote(s) sem versão resolvida") {
		t.Errorf("nota = %q", note)
	}
}

// Sem lockfile nem manifesto, o osv-scanner fica pulado com o motivo; Gradle sem gradle.lockfile
// também (sem lockfile, as versões só saem rodando o Gradle, que executa código do repo).
func TestOSVScannerPreflight(t *testing.T) {
	write := func(files ...string) string {
		dir := t.TempDir()
		for _, f := range files {
			p := filepath.Join(dir, filepath.FromSlash(f))
			_ = os.MkdirAll(filepath.Dir(p), 0o750)
			_ = os.WriteFile(p, []byte("x"), 0o600)
		}
		return dir
	}
	for name, tc := range map[string]struct {
		dir  string
		want string
	}{
		"nada":                 {write("main.go"), "sem lockfile nem manifesto"},
		"gradle sem lock":      {write("build.gradle.kts", "settings.gradle.kts"), "sem lockfile do Gradle"},
		"gradle com lock":      {write("build.gradle.kts", "gradle.lockfile"), ""},
		"npm":                  {write("web/package-lock.json"), ""},
		"maven":                {write("api/pom.xml"), ""},
		"go (govulncheck)":     {write("go.mod"), "govulncheck"},
		"lock em node_modules": {write("node_modules/x/package-lock.json"), "sem lockfile nem manifesto"},
	} {
		got := OSVScanner{}.preflight(tc.dir)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: preflight = %q, quer %q", name, got, tc.want)
		}
	}
}
