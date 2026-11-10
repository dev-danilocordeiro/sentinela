package repo

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// CommandsDocs são os arquivos do repo-alvo de onde os agentes da linha de execução tiram os
// comandos de setup, build, teste e lint, na ordem de preferência. Num monorepo, cada workspace
// pode ter o seu (apps/bff/AGENTS.md): o job usa o mais próximo do escopo do achado e o da raiz
// como reserva (CommandsDocsFor). Sem nenhum, o job de correção falha (executor.ErrNoCommandsDoc).
var CommandsDocs = []string{"AGENTS.md", "CLAUDE.md"}

// CommandsDocIn devolve o primeiro arquivo de comandos que existe na raiz de dir, ou "".
func CommandsDocIn(dir string) string {
	return commandsDocAt(dir, ".")
}

// commandsDocAt devolve o arquivo de comandos do subdiretório rel de dir (relativo à raiz, com
// "/"), ou "".
func commandsDocAt(dir, rel string) string {
	for _, name := range CommandsDocs {
		if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel), name)); err == nil && st.Mode().IsRegular() {
			return path.Join(rel, name)
		}
	}
	return ""
}

// CommandsDocsFor devolve os arquivos de comandos de um achado, relativos à raiz de dir: para cada
// caminho do escopo (diretório, arquivo ou glob), o mais próximo subindo até a raiz, na ordem do
// escopo e sem repetir, e por último o da raiz, se ainda não entrou (traz o setup comum). O
// primeiro é o principal. Sem escopo, só o da raiz. Vazio: não há nenhum.
func CommandsDocsFor(dir string, scope []string) []string {
	var out []string
	add := func(doc string) {
		if doc != "" && !slices.Contains(out, doc) {
			out = append(out, doc)
		}
	}
	for _, s := range scope {
		start := scopeDir(dir, s)
		if start == "" {
			continue
		}
		for d := start; ; d = path.Dir(d) {
			if doc := commandsDocAt(dir, d); doc != "" {
				add(doc)
				break
			}
			if d == "." {
				break
			}
		}
	}
	add(CommandsDocIn(dir))
	return out
}

// scopeDir reduz um caminho do escopo ao diretório de onde a busca começa: corta no primeiro
// caractere de glob e, se o que sobra não é um diretório do checkout, sobe para o pai. "" quando
// o caminho sai do repo.
func scopeDir(dir, s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "./")
	if i := strings.IndexAny(s, "*?[{"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return "."
	}
	p := path.Clean(s)
	if p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(p) {
		return ""
	}
	if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err == nil && st.IsDir() {
		return p
	}
	return path.Dir(p)
}

// WorkspaceCommandsDocs lista os arquivos de comandos fora da raiz (AGENTS.md de workspace), sem
// entrar em dependências baixadas, saídas de build e diretórios ocultos.
func WorkspaceCommandsDocs(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // diretório ilegível não impede achar os outros
		}
		if !d.IsDir() {
			return nil
		}
		if p != dir && (unitSkipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		if r := rel(dir, p); r != "." {
			if doc := commandsDocAt(dir, r); doc != "" {
				out = append(out, doc)
			}
		}
		return nil
	})
	return out
}

// LocalCommandsDoc confere o clone do scan (workdir/<owner>/<repo>): o arquivo de comandos que o
// job usaria para o escopo (sem escopo, o da raiz) e se o clone existe. Sem clone (o repo ainda
// não foi escaneado), cloned é false.
func LocalCommandsDoc(workdir, fullName string, scope ...string) (doc string, cloned bool) {
	dir := filepath.Join(workdir, filepath.FromSlash(fullName))
	if st, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !st.IsDir() {
		return "", false
	}
	if docs := CommandsDocsFor(dir, scope); len(docs) > 0 {
		return docs[0], true
	}
	return "", true
}
