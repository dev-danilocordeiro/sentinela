package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// JobHome é o HOME próprio do Sentinela para o Claude Code do job (M6 da revisão 2026-10-11).
// Sem usuário de serviço, o job roda com o usuário do worker; com o HOME dele, um `git push` de
// dentro do job autenticaria pelo credential helper do ~/.gitconfig, pela chave em ~/.ssh ou pelo
// login do gh. Neste HOME só entra a credencial do Claude Code; o git roda sem configuração global
// nem de sistema, sem prompt e sem ssh, com a identidade do usuário do worker no ambiente.
//
// O limite: é o mesmo usuário. Um comando que leia o ~/.ssh do usuário pelo caminho absoluto
// ainda chega lá; isso só fecha com usuário de serviço ou contêiner (docs/seguranca.md).
type JobHome struct {
	Dir    string // HOME do job; fica entre os jobs (caches do Go, do npm e afins)
	Source string // diretório de configuração do Claude Code da máquina (~/.claude ou CLAUDE_CONFIG_DIR)
	// Files são caminhos relativos ao HOME da máquina copiados para o HOME do job antes de cada
	// job (execution.home_files, ADR 0057).
	Files []string
}

// homeFilesManifest guarda, no HOME do job, a lista de arquivos copiados da máquina: o que sai de
// execution.home_files é removido no próximo job.
const homeFilesManifest = ".sentinela-home-files"

// credentialsFile é onde o Claude Code guarda o login da assinatura no Linux, dentro do
// diretório de configuração (https://code.claude.com/docs/en/iam#credential-management).
const credentialsFile = ".credentials.json"

// credMu serializa a cópia da credencial entre jobs paralelos, que dividem o mesmo HOME.
var credMu sync.Mutex

// prepare cria o HOME, sincroniza a credencial do Claude Code e devolve o ambiente do job.
func (h JobHome) prepare(ctx context.Context, base []string, worktree string) ([]string, error) {
	claudeDir := filepath.Join(h.Dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		return nil, fmt.Errorf("HOME do job: %w", err)
	}
	if err := h.syncCredentials(); err != nil {
		return nil, err
	}
	if err := h.syncFiles(envValue(base, "HOME")); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(claudeDir, credentialsFile)); err != nil && envValue(base, "CLAUDE_CODE_OAUTH_TOKEN") == "" {
		return nil, fmt.Errorf("HOME do job: sem login do Claude Code em %s nem CLAUDE_CODE_OAUTH_TOKEN (rode `claude` uma vez e faça login, ou `claude setup-token`)", filepath.Join(h.Source, credentialsFile))
	}
	// A identidade do commit vem da configuração do git da máquina (local do repo, depois global),
	// lida antes de trocar o HOME.
	name, email := gitIdentity(ctx, base, worktree)
	env := make([]string, 0, len(base)+12)
	for _, kv := range jobEnv(base) {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "HOME", k == "CLAUDE_CONFIG_DIR", strings.HasPrefix(k, "XDG_"):
			// XDG_CONFIG_HOME levaria o gh e o git à configuração da máquina.
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+h.Dir,
		"XDG_CONFIG_HOME="+filepath.Join(h.Dir, ".config"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		// O git roda o valor num shell com os argumentos do ssh no fim; `false` ignora todos.
		"GIT_SSH_COMMAND=false",
		// Sem barramento de sessão, nada chega ao chaveiro do sistema: o gh guarda o token lá e
		// o acha mesmo sem configuração nenhuma (pelo /run/user/<uid>/bus, sem XDG_RUNTIME_DIR).
		"DBUS_SESSION_BUS_ADDRESS=disabled:",
	)
	env = append(env, toolDirs(base)...)
	if name != "" {
		env = append(env, "GIT_AUTHOR_NAME="+name, "GIT_COMMITTER_NAME="+name)
	}
	if email != "" {
		env = append(env, "GIT_AUTHOR_EMAIL="+email, "GIT_COMMITTER_EMAIL="+email)
	}
	return env, nil
}

// finish devolve à máquina a credencial que o Claude Code renovou dentro do job: se o refresh
// token for rotativo, a cópia antiga da máquina deixaria de valer.
func (h JobHome) finish() error { return h.syncCredentials() }

// syncCredentials deixa os dois lados com a credencial mais nova (expiresAt maior). No empate com
// conteúdo diferente, vale a da máquina (um login novo feito lá).
func (h JobHome) syncCredentials() error {
	credMu.Lock()
	defer credMu.Unlock()
	src, dst := filepath.Join(h.Source, credentialsFile), filepath.Join(h.Dir, ".claude", credentialsFile)
	s, serr := os.ReadFile(src) //nolint:gosec // diretório de configuração do Claude Code da máquina
	d, derr := os.ReadFile(dst) //nolint:gosec // HOME do job
	switch {
	case serr != nil && derr != nil:
		return nil
	case serr != nil:
		if errors.Is(serr, os.ErrNotExist) {
			return nil // login só no job (ou por token); nada a devolver para onde não havia
		}
		return fmt.Errorf("credencial do Claude Code: %w", serr)
	case derr != nil:
		return writePrivate(dst, s)
	case bytes.Equal(s, d):
		return chmodPrivate(dst)
	case expiresAt(d) > expiresAt(s):
		return writePrivate(src, d)
	default:
		return writePrivate(dst, s)
	}
}

func expiresAt(raw []byte) int64 {
	var c struct {
		OAuth struct {
			ExpiresAt int64 `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return 0
	}
	return c.OAuth.ExpiresAt
}

// writePrivate grava com 0600 e troca o arquivo de uma vez (rename), sem deixar meio arquivo para
// outra sessão do Claude Code ler.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("credencial do Claude Code: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("credencial do Claude Code: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("credencial do Claude Code: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("credencial do Claude Code: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("credencial do Claude Code: %w", err)
	}
	return nil
}

func chmodPrivate(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("credencial do Claude Code: %w", err)
	}
	return nil
}

// gitIdentity lê user.name e user.email como o git do worker os vê no worktree.
func gitIdentity(ctx context.Context, base []string, dir string) (name, email string) {
	get := func(key string) string {
		cmd := exec.CommandContext(ctx, "git", "config", "--get", key) //nolint:gosec // chave fixa
		cmd.Dir, cmd.Env = dir, base
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return get("user.name"), get("user.email")
}

// toolDirs mantém os gerenciadores de versão da máquina (mise, asdf), que guardam as ferramentas
// instaladas no HOME: sem isso, o shim do `go` ou do `node` no PATH do worker não acharia nada e
// tentaria instalar de novo. São só ferramentas e a lista delas, não credenciais. Vale o que o
// worker já definiu; senão, o padrão de cada um, quando o diretório existe.
func toolDirs(base []string) []string {
	home := envValue(base, "HOME")
	if home == "" {
		return nil
	}
	xdg := func(key, def string) string {
		if v := envValue(base, key); v != "" {
			return v
		}
		return filepath.Join(home, def)
	}
	dirs := []struct{ key, def string }{
		{"MISE_DATA_DIR", filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "mise")},
		{"MISE_CONFIG_DIR", filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "mise")},
		{"MISE_CACHE_DIR", filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), "mise")},
		{"MISE_STATE_DIR", filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "mise")},
		{"ASDF_DATA_DIR", filepath.Join(home, ".asdf")},
	}
	var out []string
	for _, d := range dirs {
		v := envValue(base, d.key)
		if v == "" {
			if fi, err := os.Stat(d.def); err != nil || !fi.IsDir() {
				continue
			}
			v = d.def
		}
		out = append(out, d.key+"="+v)
	}
	return out
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// syncFiles copia para o HOME do job os arquivos de Files que existem no HOME da máquina (0600) e
// remove os que uma lista anterior copiou e saíram dela.
func (h JobHome) syncFiles(machineHome string) error {
	manifest := filepath.Join(h.Dir, homeFilesManifest)
	keep := map[string]bool{}
	var copied []string
	for _, rel := range h.Files {
		rel = filepath.Clean(rel)
		if machineHome == "" || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		keep[rel] = true
		data, err := os.ReadFile(filepath.Join(machineHome, rel)) //nolint:gosec // lista da config, relativa ao HOME
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("HOME do job: %s: %w", rel, err)
		}
		dst, ok := h.inside(rel)
		if !ok {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fmt.Errorf("HOME do job: %w", err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil { //nolint:gosec // dst passou por inside: dentro do HOME do job
			return fmt.Errorf("HOME do job: %s: %w", rel, err)
		}
		if err := os.Chmod(dst, 0o600); err != nil {
			return fmt.Errorf("HOME do job: %s: %w", rel, err)
		}
		copied = append(copied, rel)
	}
	if prev, err := os.ReadFile(manifest); err == nil { //nolint:gosec // HOME do job
		for _, rel := range strings.Split(strings.TrimSpace(string(prev)), "\n") {
			if rel == "" || keep[rel] {
				continue
			}
			if p, ok := h.inside(rel); ok {
				_ = os.Remove(p)
			}
		}
	}
	if len(copied) == 0 {
		_ = os.Remove(manifest)
		return nil
	}
	return os.WriteFile(manifest, []byte(strings.Join(copied, "\n")+"\n"), 0o600)
}

// inside devolve o caminho de rel dentro do HOME do job, e false se ele sairia do diretório (o
// manifesto e a lista vêm de arquivo; não confiar neles para apagar ou gravar fora).
func (h JobHome) inside(rel string) (string, bool) {
	root := filepath.Clean(h.Dir)
	p := filepath.Clean(filepath.Join(root, rel))
	if !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}
