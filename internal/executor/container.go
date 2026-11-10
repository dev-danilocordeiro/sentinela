package executor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ContainerJob roda cada sessão do Claude Code do job num contêiner (ADR 0053): monta só o
// worktree, o diretório git comum do clone, o diretório do job e o HOME do job; põe o contêiner
// numa rede interna sem rota para fora, cuja única saída é um proxy com lista permitida; e, com
// Dind, sobe um Docker isolado só para o job. Push, PR e comentários continuam no worker.
type ContainerJob struct {
	Engine    string   // docker | podman
	Image     string   // imagem do Dockerfile.worker
	Egress    []string // destinos permitidos no proxy de saída
	Dind      bool     // execution.job_docker: dind
	DindImage string
	// Labels vão em todo contêiner e rede criados, além de sentinela.job=<nome>.
	Labels map[string]string
	// Binary é o claude dentro da imagem (padrão "claude", no PATH da imagem).
	Binary string
	// UID e GID do processo no contêiner (padrão: os do worker, donos dos arquivos montados).
	UID, GID int
	// ReadyTimeout é quanto esperar o proxy e o dind subirem (padrão 90s).
	ReadyTimeout time.Duration
	// run roda o binário do engine e devolve a saída; nos testes, um falso.
	run func(ctx context.Context, args ...string) (string, error)
}

// containerHome é onde o HOME do job é montado na imagem.
const containerHome = "/var/lib/sentinela"

// proxyPort é a porta do proxy de saída na rede do job.
const proxyPort = "3128"

func (c *ContainerJob) engine(ctx context.Context, args ...string) (string, error) {
	if c.run != nil {
		return c.run(ctx, args...)
	}
	cmd := exec.CommandContext(ctx, c.Engine, args...) //nolint:gosec // binário da config; argumentos montados aqui
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("%s %s: %w: %s", c.Engine, strings.Join(args[:min(2, len(args))], " "), err, truncate(strings.TrimSpace(string(out)), 300))
	}
	return strings.TrimSpace(string(out)), nil
}

func (c *ContainerJob) labelArgs(name string) []string {
	args := []string{"--label", "sentinela.job=" + name}
	keys := make([]string, 0, len(c.Labels))
	for k := range c.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--label", k+"="+c.Labels[k])
	}
	return args
}

// defaultNetwork é a rede com saída do engine, onde o proxy também fica.
func (c *ContainerJob) defaultNetwork() string {
	if c.Engine == "podman" {
		return "podman"
	}
	return "bridge"
}

func newJobName() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "sentinela-job-" + hex.EncodeToString(b)
}

// gitCommonDir é o diretório git comum do worktree (o .git do clone), que o contêiner precisa para
// o builder commitar: o .git do worktree só aponta para ele.
func gitCommonDir(ctx context.Context, worktree string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("diretório git comum de %s: %w", worktree, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// mounts são os diretórios do host montados no contêiner, no mesmo caminho (as sessões guardam o
// diretório de trabalho e a retomada procura por ele), mais o HOME do job em /var/lib/sentinela.
func (c *ContainerJob) mounts(ctx context.Context, inv Invocation, home JobHome) []string {
	paths := []string{inv.Dir}
	if inv.Dir != "" {
		// Fora de um repo git (não acontece num job), não há diretório comum a montar.
		if common, err := gitCommonDir(ctx, inv.Dir); err == nil {
			paths = append(paths, common)
		}
	}
	if inv.JobDir != "" {
		paths = append(paths, inv.JobDir)
	}
	var args []string
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		args = append(args, "--volume", p+":"+p)
	}
	args = append(args, "--volume", home.Dir+":"+containerHome)
	return args
}

// containerEnv é o ambiente do Claude Code dentro do contêiner: a lista fechada que o HOME do job
// preparou (identidade do git, git sem credencial e sem ssh), com o HOME, o PATH e o proxy da
// imagem. Variáveis com segredo (o token de assinatura) vão só pelo nome, sem o valor na linha de
// comando; o valor fica no ambiente do cliente do engine.
func (c *ContainerJob) containerEnv(prepared []string) (args []string, clientEnv []string) {
	proxy := "http://proxy:" + proxyPort
	noProxy := "localhost,127.0.0.1,::1"
	if c.Dind {
		noProxy += ",dind"
	}
	set := map[string]string{
		"HOME":                            containerHome,
		"USER":                            "sentinela",
		"XDG_CONFIG_HOME":                 containerHome + "/.config",
		"HTTPS_PROXY":                     proxy,
		"HTTP_PROXY":                      proxy,
		"https_proxy":                     proxy,
		"http_proxy":                      proxy,
		"NO_PROXY":                        noProxy,
		"no_proxy":                        noProxy,
		"JAVA_TOOL_OPTIONS":               "-Dhttps.proxyHost=proxy -Dhttps.proxyPort=" + proxyPort + " -Dhttp.proxyHost=proxy -Dhttp.proxyPort=" + proxyPort + " -Dhttp.nonProxyHosts=localhost|127.0.0.1|dind",
		"GRADLE_OPTS":                     "-Dorg.gradle.daemon=false",
		"COREPACK_ENABLE_DOWNLOAD_PROMPT": "0",
		// O fetch embutido do Node (o do corepack, ao baixar o pnpm ou o yarn) ignora HTTPS_PROXY
		// sem isto e tenta DNS direto, que a rede interna não tem
		// (https://nodejs.org/api/cli.html#node_use_env_proxy1, Node 22.21+).
		"NODE_USE_ENV_PROXY": "1",
		// Sem isto, todo processo Node imprime o aviso de EnvHttpProxyAgent experimental na saída
		// que o builder lê; os outros avisos continuam.
		"NODE_OPTIONS": "--disable-warning=UNDICI-EHPA",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
	}
	if c.Dind {
		set["DOCKER_HOST"] = "tcp://dind:2375"
		set["TESTCONTAINERS_HOST_OVERRIDE"] = "dind"
	}
	for _, kv := range prepared {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch {
		case k == "CLAUDE_CODE_OAUTH_TOKEN":
			args = append(args, "--env", k)
			clientEnv = append(clientEnv, kv)
		case strings.HasPrefix(k, "GIT_"), strings.HasPrefix(k, "LC_"), k == "LANG", k == "LANGUAGE", k == "TZ", k == "TERM":
			if _, fixed := set[k]; !fixed {
				set[k] = v
			}
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--env", k+"="+set[k])
	}
	return args, clientEnv
}

// jobContainer é o que uma sessão criou, para a limpeza.
type jobContainer struct {
	c          *ContainerJob
	name       string
	containers []string
	volumes    []string
	network    string
	jobDir     string
}

// cleanup salva o log do proxy no diretório do job e remove os contêineres e a rede desta
// sessão, pelos nomes que ela mesma criou.
func (j *jobContainer) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if j.jobDir != "" {
		if logs, err := j.c.engine(ctx, "logs", j.name+"-proxy"); err == nil && logs != "" {
			f, err := os.OpenFile(filepath.Join(j.jobDir, "egress.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // diretório do job
			if err == nil {
				_, _ = fmt.Fprintf(f, "== %s %s\n%s\n", j.name, time.Now().Format(time.RFC3339), logs)
				_ = f.Close()
			}
		}
	}
	if len(j.containers) > 0 {
		// --volumes: nenhum volume anônimo de imagem (o docker:dind declara /var/lib/docker) fica para trás.
		_, _ = j.c.engine(ctx, append([]string{"rm", "--force", "--volumes"}, j.containers...)...)
	}
	if len(j.volumes) > 0 {
		_, _ = j.c.engine(ctx, append([]string{"volume", "rm", "--force"}, j.volumes...)...)
	}
	if j.network != "" {
		_, _ = j.c.engine(ctx, "network", "rm", j.network)
	}
}

// waitFor tenta check até dar certo ou acabar o prazo.
func waitFor(ctx context.Context, timeout time.Duration, check func() error) error {
	deadline := time.Now().Add(timeout)
	for {
		err := check()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// start cria a rede interna, o proxy de saída e, com Dind, o Docker do job, e devolve o comando
// do contêiner do job (o engine e os argumentos até a imagem, inclusive) e a limpeza.
func (c *ContainerJob) start(ctx context.Context, inv Invocation, prepared []string, home JobHome) (args []string, clientEnv []string, j *jobContainer, err error) {
	if home.Dir == "" {
		return nil, nil, nil, errors.New("runtime container: o HOME do job (execution.home) é obrigatório")
	}
	name := newJobName()
	j = &jobContainer{c: c, name: name, jobDir: inv.JobDir}
	defer func() {
		if err != nil {
			j.cleanup()
			j = nil
		}
	}()
	labels := c.labelArgs(name)
	net := name + "-net"
	if _, err = c.engine(ctx, append(append([]string{"network", "create", "--internal"}, labels...), net)...); err != nil {
		return nil, nil, j, err
	}
	j.network = net

	// Proxy de saída: na rede interna (alias proxy) e na rede com saída.
	proxy := name + "-proxy"
	proxyArgs := append([]string{"run", "--detach", "--name", proxy, "--network", net, "--network-alias", "proxy",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges"}, labels...)
	proxyArgs = append(proxyArgs, c.Image, "sentinela", "egress-proxy", "--listen", ":"+proxyPort, "--allow", strings.Join(c.Egress, ","))
	if _, err = c.engine(ctx, proxyArgs...); err != nil {
		return nil, nil, j, err
	}
	j.containers = append(j.containers, proxy)
	if _, err = c.engine(ctx, "network", "connect", c.defaultNetwork(), proxy); err != nil {
		return nil, nil, j, err
	}
	ready := c.ReadyTimeout
	if ready <= 0 {
		ready = 90 * time.Second
	}
	if err = waitFor(ctx, ready, func() error {
		logs, lerr := c.engine(ctx, "logs", proxy)
		if lerr == nil && strings.Contains(logs, "ouvindo") {
			return nil
		}
		return fmt.Errorf("o proxy de saída não subiu (%s): %w", truncate(logs, 200), errors.Join(lerr, errors.New("sem a linha de escuta no log")))
	}); err != nil {
		return nil, nil, j, err
	}

	if c.Dind {
		dind := name + "-dind"
		// O /var/lib/docker do dind num volume desta sessão, com os labels, removido pelo nome.
		data := dind + "-data"
		if _, err = c.engine(ctx, append(append([]string{"volume", "create"}, labels...), data)...); err != nil {
			return nil, nil, j, err
		}
		j.volumes = append(j.volumes, data)
		dindArgs := append([]string{"run", "--detach", "--privileged", "--name", dind, "--network", net, "--network-alias", "dind",
			"--volume", data + ":/var/lib/docker",
			"--env", "DOCKER_TLS_CERTDIR=", "--env", "HTTPS_PROXY=http://proxy:" + proxyPort, "--env", "HTTP_PROXY=http://proxy:" + proxyPort,
			"--env", "NO_PROXY=localhost,127.0.0.1"}, labels...)
		dindArgs = append(dindArgs, c.DindImage)
		if _, err = c.engine(ctx, dindArgs...); err != nil {
			return nil, nil, j, err
		}
		j.containers = append(j.containers, dind)
		if err = waitFor(ctx, ready, func() error {
			_, derr := c.engine(ctx, "exec", dind, "docker", "info")
			return derr
		}); err != nil {
			return nil, nil, j, fmt.Errorf("o Docker do job (dind) não subiu: %w", err)
		}
	}

	mounts := c.mounts(ctx, inv, home)
	uid, gid := c.UID, c.GID
	if uid == 0 && gid == 0 {
		uid, gid = os.Getuid(), os.Getgid()
	}
	envArgs, clientEnv := c.containerEnv(prepared)
	args = []string{"run", "--rm", "--name", name, "--network", net,
		"--user", strconv.Itoa(uid) + ":" + strconv.Itoa(gid),
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--workdir", inv.Dir}
	args = append(args, labels...)
	args = append(args, mounts...)
	args = append(args, envArgs...)
	args = append(args, c.Image)
	j.containers = append([]string{name}, j.containers...)
	return args, clientEnv, j, nil
}

// dockerInCommands reconhece um AGENTS.md (ou CLAUDE.md) que pede Docker para os testes.
var dockerInCommands = regexp.MustCompile(`(?i)\b(docker|testcontainers|podman|docker-compose)\b`)

// DockerWarning é o aviso do job quando o runtime container não dá Docker ao job e os comandos
// do repo pedem Docker (ADR 0053). Confere cada arquivo de comandos do achado (o do workspace e o
// da raiz).
func DockerWarning(worktree string, commandsDocs ...string) string {
	for _, doc := range commandsDocs {
		if doc == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(worktree, filepath.FromSlash(doc))) //nolint:gosec // arquivo de comandos do worktree
		if err != nil || !dockerInCommands.Match(b) {
			continue
		}
		return fmt.Sprintf("o %s do repo pede Docker (Docker, Testcontainers ou compose), e o job roda em contêiner sem Docker (execution.job_docker: none). Os testes que precisam dele não rodam no job; para rodá-los, use execution.job_docker: dind (ADR 0053)", doc)
	}
	return ""
}
