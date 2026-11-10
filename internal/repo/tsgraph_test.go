package repo

import (
	"path/filepath"
	"slices"
	"testing"
)

// graphOf mapeia os pacotes e devolve as arestas como "de -> para".
func graphOf(t *testing.T, root string) []string {
	t.Helper()
	pkgs, err := MapPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range DependencyGraph(root, pkgs) {
		out = append(out, e.From+" -> "+e.To)
	}
	return out
}

func TestDependencyGraphOfTheTypeScriptMonorepoFixture(t *testing.T) {
	got := graphOf(t, filepath.Join("..", "..", "testdata", "ts-monorepo"))
	want := []string{
		"apps/bff/src -> apps/bff/src/backend",
		"apps/bff/src -> apps/bff/src/routes",
		"apps/bff/src/backend -> apps/bff/src",
		// @acme/shared pelo nome do workspace e @acme/shared/types pelo paths herdado da base.
		"apps/bff/src/backend -> packages/shared/src",
		"apps/bff/src/routes -> apps/bff/src/backend",
		"apps/bff/src/routes -> packages/shared/src", // import type
		// @/ do tsconfig do web (baseUrl próprio, paths que substituem os da base).
		"apps/web/src/app/pedidos -> apps/web/src/components",
		"apps/web/src/app/pedidos -> apps/web/src/lib",
		"apps/web/src/app/pedidos -> packages/shared/src",
		"apps/web/src/components -> apps/web/src/lib",
		"apps/web/src/components -> packages/shared/src", // exports "./types"
		"apps/web/src/lib -> packages/shared/src",
	}
	if !slices.Equal(got, want) {
		t.Errorf("arestas:\n got %q\nwant %q", got, want)
	}
}

func TestDependencyGraphResolvesTypeScriptAliasesAndWorkspaces(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name: "paths sem baseUrl é relativo ao tsconfig, com import multilinha",
			files: map[string]string{
				"web/tsconfig.json":            `{"compilerOptions": {"paths": {"@/*": ["./src/*"]}}}`,
				"web/src/app/page.tsx":         "import {\n  Board,\n} from \"@/components/Board\";\n",
				"web/src/components/Board.tsx": "export function Board() {}\n",
			},
			want: []string{"web/src/app -> web/src/components"},
		},
		{
			name: "baseUrl sozinho resolve import sem alias",
			files: map[string]string{
				"tsconfig.json":   `{"compilerOptions": {"baseUrl": "src"}}`,
				"src/app/main.ts": "import { db } from 'infra/db';\n",
				"src/infra/db.ts": "export const db = 1;\n",
			},
			want: []string{"src/app -> src/infra"},
		},
		{
			name: "extends em lista, com comentários e vírgula final; o filho herda paths e baseUrl do pai",
			files: map[string]string{
				"tsconfig.base.json":     "{\n  // base\n  \"compilerOptions\": {\n    \"baseUrl\": \".\", /* raiz */\n    \"paths\": {\"#core/*\": [\"libs/core/*\"],},\n  },\n}\n",
				"tsconfig.strict.json":   `{"compilerOptions": {"strict": true}}`,
				"apps/api/tsconfig.json": `{"extends": ["../../tsconfig.base", "../../tsconfig.strict.json"]}`,
				"apps/api/src/main.ts":   "import { log } from '#core/log';\n",
				"libs/core/log.ts":       "export const log = 1;\n",
			},
			want: []string{"apps/api/src -> libs/core"},
		},
		{
			name: "baseUrl do filho muda a base dos paths herdados",
			files: map[string]string{
				"tsconfig.base.json":         `{"compilerOptions": {"paths": {"~/*": ["src/*"]}}}`,
				"apps/api/tsconfig.json":     `{"extends": "../../tsconfig.base.json", "compilerOptions": {"baseUrl": "."}}`,
				"apps/api/src/main.ts":       "import { cfg } from '~/config/env';\n",
				"apps/api/src/config/env.ts": "export const cfg = 1;\n",
			},
			want: []string{"apps/api/src -> apps/api/src/config"},
		},
		{
			name: "extends de um pacote do workspace",
			files: map[string]string{
				"package.json":                   `{"name": "root", "workspaces": ["apps/*", "packages/*"]}`,
				"packages/tsconfig/package.json": `{"name": "@acme/tsconfig"}`,
				"packages/tsconfig/base.json":    `{"compilerOptions": {"paths": {"@/*": ["./src/*"]}}}`,
				"apps/web/package.json":          `{"name": "@acme/web"}`,
				"apps/web/tsconfig.json":         `{"extends": "@acme/tsconfig/base.json", "compilerOptions": {"baseUrl": "."}}`,
				"apps/web/src/app/page.tsx":      "import { x } from '@/lib/x';\n",
				"apps/web/src/lib/x.ts":          "export const x = 1;\n",
			},
			want: []string{"apps/web/src/app -> apps/web/src/lib"},
		},
		{
			name: "npm: pacote do workspace pelo nome, com main apontando o build e a fonte em src",
			files: map[string]string{
				"package.json":              `{"name": "root", "workspaces": ["apps/*", "packages/*"]}`,
				"package-lock.json":         "{}",
				"packages/ui/package.json":  `{"name": "@acme/ui", "main": "dist/index.js"}`,
				"packages/ui/src/index.ts":  "export * from './button';\n",
				"packages/ui/src/button.ts": "export const Button = 1;\n",
				"apps/web/package.json":     `{"name": "@acme/web", "dependencies": {"@acme/ui": "*"}}`,
				"apps/web/src/page.tsx":     "import { Button } from '@acme/ui';\nimport React from 'react';\nimport fs from 'node:fs';\n",
			},
			want: []string{"apps/web/src -> packages/ui/src"},
		},
		{
			name: "yarn: workspaces em objeto, exports com condições e subcaminho com *",
			files: map[string]string{
				"package.json":                          `{"name": "root", "private": true, "workspaces": {"packages": ["packages/*", "services/*"]}}`,
				"yarn.lock":                             "",
				"packages/contracts/package.json":       `{"name": "contracts", "exports": {".": {"types": "./src/index.ts", "default": "./dist/index.js"}, "./http/*": {"import": "./src/http/*.ts"}}}`,
				"packages/contracts/src/index.ts":       "export type A = 1;\n",
				"packages/contracts/src/http/orders.ts": "export type O = 1;\n",
				"services/bff/package.json":             `{"name": "bff"}`,
				"services/bff/src/server.ts":            "import type { A } from 'contracts';\n",
				"services/bff/src/routes/orders.ts":     "import type { O } from \"contracts/http/orders\";\n",
			},
			want: []string{"services/bff/src -> packages/contracts/src", "services/bff/src/routes -> packages/contracts/src/http"},
		},
		{
			name: "pnpm: pacote do workspace com import type e import() de tipo",
			files: map[string]string{
				"pnpm-workspace.yaml":          "packages:\n  - 'apps/*'\n  - 'packages/*'\n",
				"package.json":                 `{"name": "root"}`,
				"packages/shared/package.json": `{"name": "@acme/shared", "types": "src/index.ts"}`,
				"packages/shared/src/index.ts": "export type Order = { id: string };\n",
				"apps/bff/package.json":        `{"name": "@acme/bff"}`,
				"apps/bff/src/a.ts":            "let o: import('@acme/shared').Order;\n",
				"apps/bff/src/handlers/b.ts":   "import { type Order } from '@acme/shared';\n",
			},
			want: []string{"apps/bff/src -> packages/shared/src", "apps/bff/src/handlers -> packages/shared/src"},
		},
		{
			name: "import relativo de um diretório com index aponta o diretório, não o pai",
			files: map[string]string{
				"src/app.ts":         "import { u } from './utils';\nimport { v } from './view.js';\n",
				"src/view.ts":        "export const v = 1;\n",
				"src/utils/index.ts": "export const u = 1;\n",
			},
			want: []string{"src -> src/utils"},
		},
		{
			name: "pacote que não é do workspace e alias que não existe ficam de fora",
			files: map[string]string{
				"web/tsconfig.json":  `{"compilerOptions": {"paths": {"@/*": ["./src/*"]}}}`,
				"web/src/a/page.tsx": "import x from '@/nao/existe';\nimport y from 'lodash';\nimport z from '@acme/fora';\n",
				"web/src/b/c.ts":     "export const c = 1;\n",
			},
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, tc.files)
			if got := graphOf(t, root); !slices.Equal(got, tc.want) {
				t.Errorf("arestas:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestStripJSONCKeepsStringsWithCommentMarkers(t *testing.T) {
	in := "{\"a\": \"http://x/*y*/\", // c\n \"b\": [1, 2,], /* d */ \"c\": \"\\\"//\",\n}"
	want := "{\"a\": \"http://x/*y*/\", \n \"b\": [1, 2],   \"c\": \"\\\"//\"\n}"
	if got := string(stripJSONC([]byte(in))); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
