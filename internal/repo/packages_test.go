package repo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

const jvm = "backend/src/main/java/com/acme/app"

func sampleTree() map[string]string {
	return map[string]string{
		jvm + "/service/UrlService.java":                                 "package com.acme.app.service;\nimport com.acme.app.model.UrlData;\nimport java.util.List;\nclass UrlService {}\n",
		jvm + "/controller/UrlController.java":                           "package com.acme.app.controller;\nimport com.acme.app.service.UrlService;\nimport static com.acme.app.model.UrlData.of;\nclass C {}\n",
		jvm + "/model/UrlData.java":                                      "package com.acme.app.model;\nrecord UrlData() {}\n",
		"backend/src/test/java/com/acme/app/service/UrlServiceTest.java": "class T {}\n",
		"backend/src/main/resources/application.yaml":                    "server:\n  port: 8080\n",
		"frontend/src/app/app.ts":                                        "import { Api } from './api/client';\nimport { Component } from '@angular/core';\n",
		"frontend/src/app/api/client.ts":                                 "export class Api {}\n",
		"frontend/src/app/app.spec.ts":                                   "describe('x', () => {})\n",
		"frontend/node_modules/x/index.js":                               "module.exports = 1\n",
		"frontend/package-lock.json":                                     "{}\n",
		"go.mod":                                                         "module example.com/svc\n\ngo 1.23\n",
		"cmd/main.go":                                                    "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/svc/internal/store\"\n)\n",
		"internal/store/store.go":                                        "package store\n",
		"internal/store/store_test.go":                                   "package store\n",
		"README.md":                                                      "# docs\n",
		"Dockerfile":                                                     "FROM scratch\n",
	}
}

func TestMapPackagesGroupsSourceByDirectoryAndSkipsTestsAndVendored(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, sampleTree())

	pkgs, err := MapPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	var files []string
	for _, p := range pkgs {
		got[p.Path] = p.Language
		for _, f := range p.Files {
			files = append(files, f.Path)
		}
	}
	want := map[string]string{
		".": "config", "cmd": "go", "internal/store": "go",
		jvm + "/service": "java", jvm + "/controller": "java", jvm + "/model": "java",
		"backend/src/main/resources": "config",
		"frontend/src/app":           "typescript", "frontend/src/app/api": "typescript",
	}
	for dir, lang := range want {
		if got[dir] != lang {
			t.Errorf("pacote %s = %q, quer %q (todos: %v)", dir, got[dir], lang, got)
		}
	}
	for _, f := range files {
		if strings.Contains(f, "Test") || strings.Contains(f, ".spec.") || strings.Contains(f, "node_modules") ||
			strings.HasSuffix(f, "_test.go") || strings.HasSuffix(f, "package-lock.json") || strings.HasSuffix(f, ".md") {
			t.Errorf("arquivo não deveria entrar na análise: %s", f)
		}
	}
}

func TestDependencyGraphFindsInternalImportsAcrossLanguages(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, sampleTree())
	pkgs, err := MapPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	edges := DependencyGraph(root, pkgs)
	for _, want := range []Edge{
		{From: jvm + "/controller", To: jvm + "/service"},
		{From: jvm + "/controller", To: jvm + "/model"},
		{From: jvm + "/service", To: jvm + "/model"},
		{From: "frontend/src/app", To: "frontend/src/app/api"},
		{From: "cmd", To: "internal/store"},
	} {
		if !slices.Contains(edges, want) {
			t.Errorf("aresta ausente %v (todas: %v)", want, edges)
		}
	}
	if len(edges) != 5 {
		t.Errorf("arestas = %v, quer só as 5 internas", edges)
	}
}

func TestFilterChangedKeepsOnlyTouchedPackages(t *testing.T) {
	pkgs := []Package{{Path: "a"}, {Path: "b"}, {Path: "c/d"}}
	got := FilterChanged(pkgs, []string{"b/x.go", "c/d/y.go", "e/z.go"})
	if len(got) != 2 || got[0].Path != "b" || got[1].Path != "c/d" {
		t.Errorf("got %v", got)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitSyncClonesThenFetchesAndListsChangedFiles(t *testing.T) {
	remote := t.TempDir()
	src := filepath.Join(remote, "src")
	writeTree(t, src, map[string]string{"a/x.go": "package a\n"})
	git(t, src, "init", "-q", "-b", "main")
	git(t, src, "add", ".")
	git(t, src, "commit", "-qm", "1")
	git(t, remote, "clone", "-q", "--bare", src, filepath.Join(remote, "o", "r.git"))

	g := &Git{Root: t.TempDir(), BaseURL: "file://" + remote}
	ctx := context.Background()
	c1, err := g.Sync(ctx, "o/r", "main")
	if err != nil {
		t.Fatal(err)
	}

	writeTree(t, src, map[string]string{"b/y.go": "package b\n"})
	git(t, src, "add", ".")
	git(t, src, "commit", "-qm", "2")
	git(t, src, "push", "-q", filepath.Join(remote, "o", "r.git"), "main")

	c2, err := g.Sync(ctx, "o/r", "main")
	if err != nil {
		t.Fatal(err)
	}
	if c2.HeadSHA == c1.HeadSHA {
		t.Fatal("o segundo sync deveria trazer o commit novo")
	}
	changed, err := g.ChangedFiles(ctx, c2, c1.HeadSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(changed, []string{"b/y.go"}) {
		t.Errorf("changed = %v", changed)
	}
	if changed, _ := g.ChangedFiles(ctx, c2, c2.HeadSHA); len(changed) != 0 {
		t.Errorf("sem commits novos não deveria haver mudanças: %v", changed)
	}
	if _, err := g.ChangedFiles(ctx, c2, "0123456789abcdef0123456789abcdef01234567"); !errors.Is(err, ErrUnknownBase) {
		t.Errorf("SHA inexistente deveria dar ErrUnknownBase, veio %v", err)
	}
}

// Fase C, passo 2: testes e gerados de Kotlin e TypeScript ficam fora da análise.
func TestMapPackagesSkipsKotlinAndTypeScriptTestsAndGenerated(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"app/src/main/kotlin/acme/Ledger.kt":      "package acme\nclass Ledger\n",
		"app/src/main/kotlin/acme/LedgerTests.kt": "class LedgerTests\n",
		"app/src/main/kotlin/acme/LedgerIT.kt":    "class LedgerIT\n",
		"app/src/main/kotlin/acme/LedgerSpec.kt":  "class LedgerSpec\n",
		"web/src/lib/api.ts":                      "export const a = 1\n",
		"web/src/lib/api.generated.ts":            "export const b = 2\n",
		"web/src/lib/types.d.ts":                  "declare const c: number\n",
		"web/next-env.d.ts":                       "/// <reference types=\"next\" />\n",
		"web/pnpm-lock.yaml":                      "lockfileVersion: 9\n",
		"web/yarn.lock":                           "# yarn\n",
		"web/package.json":                        "{}\n",
	})
	pkgs, err := MapPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, p := range pkgs {
		for _, f := range p.Files {
			files = append(files, f.Path)
		}
	}
	all := strings.Join(files, " ")
	for _, gone := range []string{"LedgerTests.kt", "LedgerIT.kt", "LedgerSpec.kt", "api.generated.ts", "types.d.ts", "next-env.d.ts", "pnpm-lock.yaml", "yarn.lock"} {
		if strings.Contains(all, gone) {
			t.Errorf("%s não deveria entrar na análise: %v", gone, files)
		}
	}
	for _, kept := range []string{"Ledger.kt", "api.ts"} {
		if !strings.Contains(all, kept) {
			t.Errorf("%s deveria entrar: %v", kept, files)
		}
	}
}
