package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeEngine grava os comandos do engine e responde o mínimo para o start seguir.
type fakeEngine struct {
	mu    sync.Mutex
	calls [][]string
}

func (f *fakeEngine) run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	if len(args) > 0 && args[0] == "logs" {
		return "proxy de saída ouvindo em [::]:3128", nil
	}
	return "", nil
}

func (f *fakeEngine) find(prefix ...string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], " ") == strings.Join(prefix, " ") {
			out = append(out, c)
		}
	}
	return out
}

// ADR 0053 (D-02, D-03, D-04): o contêiner do job monta só o worktree, o diretório do job e o
// HOME do job; roda sem capabilities, com o uid do worker, numa rede interna cuja saída é o
// proxy; o ambiente não leva segredos do Sentinela; a limpeza remove só o que a sessão criou.
func TestContainerJobMountsOnlyTheJobAndUsesTheEgressProxy(t *testing.T) {
	eng := &fakeEngine{}
	c := &ContainerJob{Engine: "docker", Image: "sentinela-job:test", Egress: []string{"api.anthropic.com", "github.com"},
		Labels: map[string]string{"sentinela.fase-b": "1"}, UID: 1000, GID: 1000, run: eng.run}
	wt, jobDir, home := t.TempDir(), t.TempDir(), t.TempDir()
	inv := Invocation{Prompt: "/x", Dir: wt, JobDir: jobDir, PluginDir: filepath.Join(jobDir, "plugin")}
	prepared := []string{"HOME=/host/job-home", "PATH=/usr/bin", "LANG=pt_BR.UTF-8", "GIT_AUTHOR_NAME=Pessoa",
		"GIT_SSH_COMMAND=false", "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat-segredo", "XDG_CONFIG_HOME=/host/job-home/.config"}
	args, clientEnv, job, err := c.start(context.Background(), inv, prepared, JobHome{Dir: home})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(args, " ")
	for _, want := range []string{
		"run --rm --name " + job.name + " --network " + job.name + "-net",
		"--user 1000:1000", "--cap-drop ALL", "--security-opt no-new-privileges", "--workdir " + wt,
		"--volume " + wt + ":" + wt, "--volume " + jobDir + ":" + jobDir, "--volume " + home + ":/var/lib/sentinela",
		"--env HOME=/var/lib/sentinela", "--env HTTPS_PROXY=http://proxy:3128", "--env GIT_AUTHOR_NAME=Pessoa",
		"--env COREPACK_ENABLE_DOWNLOAD_PROMPT=0", "--env NODE_USE_ENV_PROXY=1", "--env NODE_OPTIONS=--disable-warning=UNDICI-EHPA",
		"--env GIT_SSH_COMMAND=false", "--env CLAUDE_CODE_OAUTH_TOKEN ", "--label sentinela.job=" + job.name, "--label sentinela.fase-b=1",
	} {
		if !strings.Contains(line+" ", want) {
			t.Errorf("comando sem %q:\n%s", want, line)
		}
	}
	if !strings.HasSuffix(line, " sentinela-job:test") {
		t.Errorf("a imagem precisa fechar o comando (o claude vem depois): %s", line)
	}
	for _, bad := range []string{"docker.sock", ".ssh", "secrets", ".env", ".config/sentinela", "sk-ant-oat-segredo", "/host/job-home", "--privileged", "PATH="} {
		if strings.Contains(line, bad) {
			t.Errorf("o contêiner do job não pode ter %q:\n%s", bad, line)
		}
	}
	if len(clientEnv) != 1 || clientEnv[0] != "CLAUDE_CODE_OAUTH_TOKEN=sk-ant-oat-segredo" {
		t.Errorf("o token de assinatura vai pelo ambiente do cliente, não pela linha de comando: %v", clientEnv)
	}
	if n := eng.find("network", "create", "--internal"); len(n) != 1 || n[0][len(n[0])-1] != job.name+"-net" {
		t.Errorf("rede interna: %v", n)
	}
	proxy := eng.find("run", "--detach", "--name", job.name+"-proxy")
	if len(proxy) != 1 || !strings.Contains(strings.Join(proxy[0], " "), "sentinela egress-proxy --listen :3128 --allow api.anthropic.com,github.com") {
		t.Errorf("proxy: %v", proxy)
	}
	if len(eng.find("network", "connect", "bridge", job.name+"-proxy")) != 1 {
		t.Error("o proxy precisa da rede com saída")
	}
	if len(eng.find("run", "--detach", "--privileged")) != 0 {
		t.Error("sem dind, nada privilegiado")
	}
	job.cleanup()
	rm := eng.find("rm", "--force", "--volumes")
	if len(rm) != 1 || strings.Join(rm[0][3:], " ") != job.name+" "+job.name+"-proxy" {
		t.Errorf("a limpeza remove só os contêineres desta sessão, pelo nome: %v", rm)
	}
	if len(eng.find("network", "rm", job.name+"-net")) != 1 {
		t.Error("a rede da sessão não foi removida")
	}
	for _, c := range eng.calls {
		if strings.Contains(strings.Join(c, " "), "prune") {
			t.Errorf("a limpeza nunca usa prune: %v", c)
		}
	}
	if b, err := os.ReadFile(filepath.Join(jobDir, "egress.log")); err != nil || !strings.Contains(string(b), "ouvindo") {
		t.Errorf("o log do proxy vai para o diretório do job: %v %s", err, b)
	}
}

// Com job_docker: dind, um Docker isolado e privilegiado só para o job, na rede interna; o job o
// usa por DOCKER_HOST e ele sai na limpeza.
func TestContainerJobWithDindStartsAnIsolatedDocker(t *testing.T) {
	eng := &fakeEngine{}
	c := &ContainerJob{Engine: "docker", Image: "img", DindImage: "docker:27-dind", Dind: true, UID: 1, GID: 1, run: eng.run}
	args, _, job, err := c.start(context.Background(), Invocation{Dir: t.TempDir()}, nil, JobHome{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dind := eng.find("run", "--detach", "--privileged", "--name", job.name+"-dind", "--network", job.name+"-net")
	if len(dind) != 1 || dind[0][len(dind[0])-1] != "docker:27-dind" {
		t.Errorf("dind: %v", dind)
	}
	if len(eng.find("network", "connect", "bridge", job.name+"-dind")) != 0 {
		t.Error("o dind fica só na rede interna (baixa imagens pelo proxy)")
	}
	line := strings.Join(args, " ")
	if !strings.Contains(line, "--env DOCKER_HOST=tcp://dind:2375") || !strings.Contains(line, "NO_PROXY=localhost,127.0.0.1,::1,dind") {
		t.Errorf("o job usa o dind: %s", line)
	}
	job.cleanup()
	if rm := eng.find("rm", "--force", "--volumes"); len(rm) != 1 || !strings.Contains(strings.Join(rm[0], " "), job.name+"-dind") {
		t.Errorf("o dind sai na limpeza: %v", rm)
	}
	if v := eng.find("volume", "create", "--label", "sentinela.job="+job.name); len(v) != 1 || v[0][len(v[0])-1] != job.name+"-dind-data" {
		t.Errorf("o volume do dind é criado com os labels da sessão: %v", v)
	}
	if v := eng.find("volume", "rm", "--force", job.name+"-dind-data"); len(v) != 1 {
		t.Errorf("o volume do dind sai na limpeza, pelo nome: %v", eng.calls)
	}
}

func TestContainerJobNeedsTheJobHome(t *testing.T) {
	c := &ContainerJob{Engine: "docker", Image: "img", run: (&fakeEngine{}).run}
	if _, _, _, err := c.start(context.Background(), Invocation{Dir: t.TempDir()}, nil, JobHome{}); err == nil {
		t.Error("sem o HOME do job (execution.home) não há onde pôr a cópia do login")
	}
}

func TestDockerWarningWhenTheCommandsAskForDocker(t *testing.T) {
	wt := t.TempDir()
	write(wt, "AGENTS.md", "## Testes\n\n- `./gradlew test`: sobe Postgres com Testcontainers.\n")
	if msg := DockerWarning(wt, "AGENTS.md"); !strings.Contains(msg, "job_docker: dind") {
		t.Errorf("aviso = %q", msg)
	}
	write(wt, "CLAUDE.md", "## Testes\n\n- `go test ./...`\n")
	if msg := DockerWarning(wt, "CLAUDE.md"); msg != "" {
		t.Errorf("sem Docker nos comandos, sem aviso: %q", msg)
	}
	if DockerWarning(wt, "") != "" {
		t.Error("sem arquivo de comandos, sem aviso")
	}
}
