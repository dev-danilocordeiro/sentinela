package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// toolsDir é o tools/node do repo; os testes que rodam o eslint de verdade pulam sem ele instalado.
func toolsDir(t *testing.T, bin string) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "tools", "node"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node fora do PATH")
	}
	if why := nodeTools(dir).missing(bin); why != "" {
		t.Skip(why)
	}
	return dir
}

// O eslint com a config neutra roda por pacote do workspace (a raiz sem repetir os workspaces) e
// acha os três bugs plantados no testdata/ts-monorepo, sem instalar nada do repo.
func TestESLintFindsThePlantedBugsOfTheFixtureWithTheNeutralConfig(t *testing.T) {
	tools := toolsDir(t, eslintBin)
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "ts-monorepo"))
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Scanners: []Scanner{ESLint{Tools: tools}}}
	sigs, results := r.Run(context.Background(), fixture, map[string]bool{"typescript": true})
	if res := results[0]; res.Status != StatusOK || res.Units != 4 || res.Analyzed != 14 || res.Unit != "arquivos" {
		t.Fatalf("resultado = %+v", res)
	}
	var got []string
	for _, s := range sigs {
		got = append(got, s.Rule+" "+s.File+":"+strconv.Itoa(s.Line)+" "+s.Severity)
	}
	want := []string{
		"@typescript-eslint/no-unused-vars apps/bff/src/backend/orders.ts:13 medium", // catch que engole o erro
		"require-await apps/bff/src/routes/orders.ts:17 medium",                      // await esquecido
		"@typescript-eslint/no-explicit-any apps/web/src/lib/api.ts:10 medium",       // any escondendo o campo
		"@typescript-eslint/no-explicit-any apps/web/src/lib/api.ts:13 medium",
	}
	if !slices.Equal(got, want) {
		t.Errorf("sinais:\n got %q\nwant %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(fixture, "node_modules")); err == nil {
		t.Error("o scanner não pode instalar nada no repo")
	}
}

func TestESLintIgnoresNestedWorkspacesAndConfigNoise(t *testing.T) {
	dir := t.TempDir()
	for f, content := range map[string]string{
		"package.json":          `{"name": "root", "workspaces": ["apps/*"]}`,
		"apps/web/package.json": `{"name": "web"}`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin, args := ESLint{Tools: "/t"}.Command(dir)
	joined := strings.Join(args, " ")
	if bin != "node" || !strings.HasPrefix(joined, "/t/node_modules/eslint/bin/eslint.js --config /t/eslint.config.mjs --format json") ||
		!strings.Contains(joined, "--ignore-pattern apps/web/**") || !strings.HasSuffix(joined, " .") {
		t.Errorf("comando = %s %s", bin, joined)
	}

	out := []byte(`[
	 {"filePath": "/r/src/a.tsx", "messages": [
	   {"ruleId": "@next/next/no-img-element", "severity": 2, "message": "Definition for rule '@next/next/no-img-element' was not found.", "line": 3},
	   {"ruleId": null, "severity": 1, "message": "Unused eslint-disable directive (no problems were reported from 'x').", "line": 4},
	   {"ruleId": "react-hooks/exhaustive-deps", "severity": 1, "message": "React Hook useEffect has a missing dependency", "line": 9},
	   {"ruleId": null, "fatal": true, "severity": 2, "message": "Parsing error: Unexpected token", "line": 20}
	 ]},
	 {"filePath": "/r/src/b.ts", "messages": []}
	]`)
	sigs, err := ESLint{}.Parse(out, "/r")
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 2 || sigs[0].Rule != "react-hooks/exhaustive-deps" || sigs[0].Severity != "low" || sigs[0].File != "src/a.tsx" ||
		sigs[1].Rule != "parse-error" || sigs[1].Line != 20 {
		t.Errorf("sinais = %+v", sigs)
	}
	if n, unit := (ESLint{}).Coverage(out, "/r"); n != 2 || unit != "arquivos" {
		t.Errorf("cobertura = %d %s", n, unit)
	}
}

func TestESLintAndTSCAreSkippedWithoutTheNodeTools(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{}`), 0o600)
	tools := filepath.Join(t.TempDir(), "tools")
	r := &Runner{Scanners: []Scanner{ESLint{Tools: tools}, TSC{Tools: tools}}, LookPath: func(string) (string, error) { return "/bin/node", nil }}
	_, results := r.Run(context.Background(), dir, map[string]bool{"typescript": true})
	for _, res := range results {
		if res.Status != StatusSkipped || !strings.Contains(res.Detail, "npm ci --ignore-scripts --prefix "+tools) {
			t.Errorf("%s: %+v", res.Scanner, res)
		}
	}
	_, results = r.Run(context.Background(), dir, map[string]bool{"go": true})
	if results[0].Detail != "não se aplica às linguagens do repo" || results[1].Detail != "não se aplica às linguagens do repo" {
		t.Errorf("sem TS nem JS: %+v", results)
	}
}

// O tsc só roda no pacote com tsconfig.json e com node_modules (nele ou acima, até a raiz do
// checkout); sem nenhum assim, fica pulado com os motivos agrupados.
func TestTSCRunsOnlyWhereTheDependenciesAreInstalled(t *testing.T) {
	dir := t.TempDir()
	for f, content := range map[string]string{
		"package.json":                 `{"name": "root", "workspaces": ["apps/*", "packages/*"]}`,
		"apps/web/package.json":        `{"name": "web"}`,
		"apps/web/tsconfig.json":       `{}`,
		"apps/bff/package.json":        `{"name": "bff"}`,
		"apps/bff/tsconfig.json":       `{}`,
		"packages/cfg/package.json":    `{"name": "cfg"}`,
		".git/HEAD":                    "ref: refs/heads/main\n",
		"apps/bff/node_modules/.keep":  "",
		"packages/cfg/node_modules/.k": "",
	} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tsc := TSC{}
	for unit, want := range map[string]string{
		"apps/bff":     "",
		"apps/web":     "sem node_modules",
		"packages/cfg": "sem tsconfig.json",
		".":            "sem tsconfig.json",
	} {
		if got := tsc.skipUnit(filepath.Join(dir, unit)); (want == "" && got != "") || !strings.HasPrefix(got, want) {
			t.Errorf("%s: %q, quer %q", unit, got, want)
		}
	}
	// node_modules só na raiz (npm e yarn hoisted) serve para os workspaces.
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o750); err != nil {
		t.Fatal(err)
	}
	if got := tsc.skipUnit(filepath.Join(dir, "apps/web")); got != "" {
		t.Errorf("com node_modules na raiz: %q", got)
	}

	var s skipReasons
	s.add("sem tsconfig.json", ".")
	s.add("sem node_modules", "apps/web")
	s.add("sem node_modules", "apps/bff")
	if got := s.String(3); got != "sem tsconfig.json: .; sem node_modules: apps/web, apps/bff" {
		t.Errorf("motivos = %q", got)
	}
	var root skipReasons
	root.add("sem node_modules", ".")
	if got := root.String(1); got != "sem node_modules" {
		t.Errorf("só a raiz = %q", got)
	}
}

func TestTSCParsesDiagnosticsAndCountsTheFilesOfThePackage(t *testing.T) {
	out := []byte(strings.Join([]string{
		"src/routes/orders.ts(17,7): error TS2322: Type 'string' is not assignable to type 'number'.",
		"../../packages/shared/src/client.ts(3,1): error TS2304: Cannot find name 'x'.",
		"node_modules/@types/express/index.d.ts(1,1): error TS2300: Duplicate identifier.",
		"error TS5083: Cannot read file '/r/tsconfig.base.json'.",
		"/usr/lib/node_modules/typescript/lib/lib.es2022.d.ts",
		"/r/apps/bff/src/server.ts",
		"/r/apps/bff/src/routes/orders.ts",
		"/r/packages/shared/src/client.ts",
		"/r/apps/bff/node_modules/express/index.d.ts",
	}, "\n"))
	sigs, err := TSC{}.Parse(out, "/r/apps/bff")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range sigs {
		got = append(got, s.Rule+" "+s.File+":"+strconv.Itoa(s.Line))
	}
	if want := []string{"TS2322 src/routes/orders.ts:17", "TS2304 ../../packages/shared/src/client.ts:3", "TS5083 tsconfig.json:1"}; !slices.Equal(got, want) {
		t.Errorf("sinais = %q, quer %q", got, want)
	}
	if n, _ := (TSC{}).Coverage(out, "/r/apps/bff"); n != 2 {
		t.Errorf("cobertura = %d, quer os 2 arquivos do pacote", n)
	}
	_, args := TSC{Tools: "/t"}.Command(".")
	if want := "/t/node_modules/typescript/bin/tsc --noEmit -p . --pretty false --incremental false --composite false --listFiles"; strings.Join(args, " ") != want {
		t.Errorf("args = %v", args)
	}
}
