package repo

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// Fase C, passo 3: unidades de build lidas dos manifestos, sem rodar o build do repo.
func TestBuildUnitsReadsGradleMavenAndJSWorkspaces(t *testing.T) {
	cases := []struct {
		name string
		tree map[string]string
		want []BuildUnit
	}{
		{"gradle kotlin dsl multimódulo", map[string]string{
			"settings.gradle.kts":        "rootProject.name = \"ledger\"\ninclude(\":app\", \":libs:core\")\ninclude(\"api\")\n",
			"build.gradle.kts":           "",
			"app/build.gradle.kts":       "",
			"libs/core/build.gradle.kts": "",
			"api/build.gradle.kts":       "",
		}, []BuildUnit{{".", KindGradle}, {"api", KindGradle}, {"app", KindGradle}, {"libs/core", KindGradle}}},
		{"gradle groovy e um módulo só", map[string]string{
			"settings.gradle":  "include 'web', ':svc'\n// include 'antigo'\n",
			"web/build.gradle": "",
			"svc/build.gradle": "",
		}, []BuildUnit{{".", KindGradle}, {"svc", KindGradle}, {"web", KindGradle}}},
		{"gradle sem settings", map[string]string{"build.gradle.kts": ""}, []BuildUnit{{".", KindGradle}}},
		{"maven multimódulo em subdiretório e npm avulso (o ceres)", map[string]string{
			"api/pom.xml":                     "<project><modules>\n<module>domain</module>\n<module>adapter</module>\n</modules></project>",
			"api/domain/pom.xml":              "<project/>",
			"api/adapter/pom.xml":             "<project><modules><module>../nao-existe</module></modules></project>",
			"web/package.json":                `{"name":"ceres-web"}`,
			"web/package-lock.json":           "{}",
			"web/node_modules/x/package.json": "{}",
		}, []BuildUnit{{"api", KindMaven}, {"api/adapter", KindMaven}, {"api/domain", KindMaven}, {"web", KindNPM}}},
		{"npm workspaces", map[string]string{
			"package.json":               `{"workspaces":["apps/*","packages/core"]}`,
			"package-lock.json":          "{}",
			"apps/backend/package.json":  "{}",
			"apps/frontend/package.json": "{}",
			"apps/sem-pacote/x.ts":       "",
			"packages/core/package.json": "{}",
		}, []BuildUnit{{".", KindNPM}, {"apps/backend", KindNPM}, {"apps/frontend", KindNPM}, {"packages/core", KindNPM}}},
		{"yarn workspaces (objeto)", map[string]string{
			"package.json":          `{"workspaces":{"packages":["pkgs/**"]}}`,
			"yarn.lock":             "",
			"pkgs/a/package.json":   "{}",
			"pkgs/b/c/package.json": "{}",
		}, []BuildUnit{{".", KindYarn}, {"pkgs/a", KindYarn}, {"pkgs/b/c", KindYarn}}},
		{"pnpm workspace", map[string]string{
			"package.json":               "{}",
			"pnpm-workspace.yaml":        "packages:\n  - 'apps/*'\n  - \"!apps/ignorado\"\n",
			"pnpm-lock.yaml":             "",
			"apps/web/package.json":      "{}",
			"apps/ignorado/package.json": "{}",
		}, []BuildUnit{{".", KindPNPM}, {"apps/web", KindPNPM}}},
		{"go e js no mesmo repo", map[string]string{
			"go/go.mod":             "module x\n",
			"go/tools/go.mod":       "module y\n",
			"frontend/package.json": "{}",
		}, []BuildUnit{{"frontend", KindNPM}, {"go", KindGo}, {"go/tools", KindGo}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, tc.tree)
			if got := BuildUnits(root); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("unidades = %v, quer %v", got, tc.want)
			}
		})
	}
}

func TestUnitsForPicksTheDeepestUnitOfEachScopePath(t *testing.T) {
	fixture := filepath.Join("..", "..", "testdata", "ts-monorepo")
	for _, c := range []struct {
		scope []string
		want  []BuildUnit
	}{
		{[]string{"apps/bff/src/routes/orders.ts"}, []BuildUnit{{"apps/bff", KindPNPM}}},
		{[]string{"apps/bff/**", "apps/bff/src/backend"}, []BuildUnit{{"apps/bff", KindPNPM}}},
		{[]string{"apps/web/src/lib/api.ts", "packages/shared/src/types.ts"}, []BuildUnit{{"apps/web", KindPNPM}, {"packages/shared", KindPNPM}}},
		{[]string{"tsconfig.base.json"}, []BuildUnit{{".", KindPNPM}}},
		{[]string{"../fora"}, nil},
	} {
		if got := UnitsFor(fixture, c.scope); !slices.Equal(got, c.want) {
			t.Errorf("UnitsFor(%q) = %v, quer %v", c.scope, got, c.want)
		}
	}
	if n := PackageName(fixture, BuildUnit{"apps/bff", KindPNPM}); n != "@acme/bff" {
		t.Errorf("PackageName = %q", n)
	}
	tmp := t.TempDir()
	writeTree(t, tmp, map[string]string{"go.mod": "module x\n", "a/go.mod": "module x/a\n", "a/b.go": "package a\n", "ab/c.go": "package ab\n"})
	if got := UnitsFor(tmp, []string{"a/b.go", "ab/c.go"}); !slices.Equal(got, []BuildUnit{{"a", KindGo}, {".", KindGo}}) {
		t.Errorf("a e ab: %v", got)
	}
}
