package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Testes do runtime container com a imagem de verdade (ADR 0053). Só rodam com
// SENTINELA_CONTAINER_TESTS=1 e a imagem construída (docker build -f Dockerfile.worker -t
// sentinela-job:latest .). Todo contêiner e rede criados levam o label sentinela.teste=1, mais os
// de SENTINELA_CONTAINER_TEST_LABELS (k=v,k2=v2).

func containerForTest(t *testing.T) (*ContainerJob, JobHome) {
	t.Helper()
	if os.Getenv("SENTINELA_CONTAINER_TESTS") != "1" {
		t.Skip("defina SENTINELA_CONTAINER_TESTS=1 (e construa a imagem) para os testes do runtime container")
	}
	image := os.Getenv("SENTINELA_JOB_IMAGE")
	if image == "" {
		image = "sentinela-job:latest"
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Skipf("imagem %s ausente: %v", image, err)
	}
	labels := map[string]string{"sentinela.teste": "1"}
	for _, kv := range strings.Split(os.Getenv("SENTINELA_CONTAINER_TEST_LABELS"), ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
			labels[k] = v
		}
	}
	src := t.TempDir()
	creds := `{"claudeAiOauth":{"accessToken":"sk-ant-oat-teste","refreshToken":"sk-ant-ort-teste","expiresAt":1}}`
	if err := os.WriteFile(filepath.Join(src, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	home := JobHome{Dir: filepath.Join(t.TempDir(), "home"), Source: src}
	c := &ContainerJob{Engine: "docker", Image: image, Egress: []string{"api.anthropic.com", "github.com"},
		Labels: labels, Binary: "/var/lib/sentinela/bin/claude"}
	t.Cleanup(func() {
		// Nada desta sessão pode sobrar: a limpeza do runner remove pelo nome.
		out, _ := exec.Command("docker", "ps", "-a", "-q", "--filter", "label=sentinela.teste=1").Output()
		nets, _ := exec.Command("docker", "network", "ls", "-q", "--filter", "label=sentinela.teste=1").Output()
		vols, _ := exec.Command("docker", "volume", "ls", "-q", "--filter", "label=sentinela.teste=1").Output()
		if strings.TrimSpace(string(out)) != "" || strings.TrimSpace(string(nets)) != "" || strings.TrimSpace(string(vols)) != "" {
			t.Errorf("sobraram contêineres, redes ou volumes do teste: %q %q %q", out, nets, vols)
		}
	})
	return c, home
}

// fakeClaudeIn grava o claude falso no HOME do job (montado em /var/lib/sentinela).
func fakeClaudeIn(t *testing.T, home JobHome, body string) {
	t.Helper()
	bin := filepath.Join(home.Dir, "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(bin), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"+initLine+"\n"+resultLine+"\n"), 0o700); err != nil { //nolint:gosec // script de teste executável
		t.Fatal(err)
	}
}

// D-02, D-03, D-04: de dentro do contêiner do job, falham a leitura do .env e do secrets/ do
// Sentinela, o socket do Docker do host, o git push, o gh e a saída para um host fora da lista; a
// Anthropic (na lista) responde, e sem o proxy não há rota nenhuma.
func TestContainerJobCannotReachSecretsHostDockerPushOrTheInternet(t *testing.T) {
	c, home := containerForTest(t)
	for k, v := range map[string]string{"GITHUB_TOKEN": "ghp_segredo", "SLACK_BOT_TOKEN": "xoxb-segredo", "LINEAR_API_KEY": "lin_api_segredo", "DATABASE_URL": "postgres://u:senha@h/db"} {
		t.Setenv(k, v)
	}
	sentinela := t.TempDir() // como o diretório do Sentinela: .env e secrets/ do host
	write(sentinela, ".env", "GITHUB_TOKEN=ghp_segredo\n")
	write(sentinela, "secrets/bot.pem", "-----BEGIN RSA PRIVATE KEY-----\n")
	root, _ := filepath.Abs(filepath.Join("..", ".."))

	wt := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}, {"remote", "add", "origin", "https://github.com/acme/sentinela-repo-que-nao-existe.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = wt
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	fakeClaudeIn(t, home, `{
cat `+sentinela+`/.env >/dev/null 2>&1 && echo LEU_ENV
ls `+sentinela+`/secrets >/dev/null 2>&1 && echo LEU_SECRETS
cat `+root+`/.env >/dev/null 2>&1 && echo LEU_ENV_REAL
test -e /var/run/docker.sock && echo SOCKET_DOCKER
curl -s --max-time 5 --unix-socket /var/run/docker.sock http://docker/version >/dev/null 2>&1 && echo API_DOCKER
git push origin HEAD:refs/heads/x >/dev/null 2>&1 && echo PUSH_OK
git push git@github.com:acme/sentinela-repo-que-nao-existe.git HEAD:refs/heads/x >/dev/null 2>&1 && echo PUSH_SSH_OK
command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1 && echo GH_OK
curl -s --max-time 20 -o /dev/null https://example.com && echo FORA_OK
curl -s --max-time 10 --noproxy '*' -o /dev/null https://1.1.1.1 && echo DIRETO_OK
curl -s --max-time 20 -o /dev/null -w 'ANTHROPIC %{http_code}\n' https://api.anthropic.com/
env | grep -E '^(GITHUB_TOKEN|SLACK_BOT_TOKEN|LINEAR_API_KEY|DATABASE_URL|ANTHROPIC_)' && echo SEGREDO_NO_AMBIENTE
echo "HOME=$HOME USER=$(id -u)"
test -r "$HOME/.claude/.credentials.json" && echo CREDENCIAL_OK
} > probe.txt 2>&1`)
	inv := Invocation{Prompt: "/x", Dir: wt, JobDir: t.TempDir(), Timeout: 3 * time.Minute}
	if _, err := (ClaudeRunner{Home: home, Container: c}).Run(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(wt, "probe.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	t.Logf("de dentro do contêiner:\n%s", got)
	for _, bad := range []string{"LEU_ENV", "LEU_SECRETS", "SOCKET_DOCKER", "API_DOCKER", "PUSH_OK", "PUSH_SSH_OK", "GH_OK", "FORA_OK", "DIRETO_OK", "SEGREDO_NO_AMBIENTE"} {
		if strings.Contains(got, bad) {
			t.Errorf("de dentro do contêiner: %s", bad)
		}
	}
	if !strings.Contains(got, "HOME=/var/lib/sentinela USER="+strconv.Itoa(os.Getuid())) || !strings.Contains(got, "CREDENCIAL_OK") {
		t.Errorf("o job roda com o uid do worker e a cópia do login no HOME:\n%s", got)
	}
	if strings.Contains(got, "ANTHROPIC 000") || !strings.Contains(got, "ANTHROPIC ") {
		t.Errorf("a Anthropic está na lista e precisa responder pelo proxy:\n%s", got)
	}
	logs, _ := os.ReadFile(filepath.Join(inv.JobDir, "egress.log"))
	if !strings.Contains(string(logs), "negado CONNECT example.com:443") || !strings.Contains(string(logs), "permitido CONNECT api.anthropic.com:443") {
		t.Errorf("egress.log:\n%s", logs)
	}
}

// Um job falso completo no runtime container: o builder commita de dentro do contêiner, e o
// worker, no host, faz o push e abre o PR em draft.
func TestContainerJobEndToEndOpensThePRFromTheWorker(t *testing.T) {
	c, home := containerForTest(t)
	e := setup(t, true)
	ctx := context.Background()
	fakeClaudeIn(t, home, `prompt=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-p" ]; then prompt="$2"; fi
  shift
done
job="${prompt#/sentinela-factory:sentinela-fix }"
echo "correção feita no contêiner" >> cmd/api/fix.txt
git add -A && git commit -qm "EX-1: correção no contêiner"
mkdir -p "$job/round-1"
printf 'STATUS: READY\n## Mudanças\n- fix.txt\n## Critérios\n- ok\n' > "$job/round-1/build.md"
for lens in security code reliability domain; do printf 'VERDICT: PASS\n## Achados\n- nenhum\n' > "$job/round-1/review-$lens.md"; done
printf 'APPROVE\nmotivo do approver\n## O que o humano precisa checar\n- o diff\n' > "$job/decision.md"
printf '{"status":"approve","rounds":1}' > "$job/state.json"`)
	e.w.Runner = ClaudeRunner{Home: home, Container: c}
	e.w.ClaudeHome = filepath.Join(home.Dir, ".claude")
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	job := e.runOnce(t)
	if job.Status != "succeeded" || !job.PrNumber.Valid {
		t.Fatalf("job = %s, PR %v, erro %q", job.Status, job.PrNumber, job.Error.String)
	}
	prs, _ := e.gh.snapshot()
	if len(prs) != 1 || !prs[0].Draft || prs[0].Head.Ref != "sentinela/EX-1" {
		t.Fatalf("PR = %+v", prs)
	}
	log := gitIn(t, e.remote, "log", "--format=%s", "sentinela/EX-1")
	if !strings.Contains(log, "EX-1: correção no contêiner") {
		t.Errorf("o commit feito no contêiner não chegou ao remoto pelo push do worker:\n%s", log)
	}
}

// job_docker: dind: um Docker isolado só para o job, na rede interna, que o job usa por
// DOCKER_HOST e que baixa imagens pelo proxy (o registro está na lista); sai na limpeza.
func TestContainerJobWithDindGetsAnIsolatedDocker(t *testing.T) {
	c, home := containerForTest(t)
	c.Dind, c.DindImage = true, "docker:27-dind"
	c.Egress = append(c.Egress, "registry-1.docker.io", "auth.docker.io", "production.cloudfront.docker.com")
	wt := t.TempDir()
	fakeClaudeIn(t, home, `{
echo "DOCKER_HOST=$DOCKER_HOST"
curl -s --max-time 10 "$(echo "$DOCKER_HOST" | sed 's#^tcp#http#')/_ping" && echo " PING"
curl -s --max-time 120 -X POST "http://dind:2375/images/create?fromImage=hello-world&tag=latest" | tail -1
test -e /var/run/docker.sock && echo SOCKET_DOCKER
} > probe.txt 2>&1`)
	inv := Invocation{Prompt: "/x", Dir: wt, JobDir: t.TempDir(), Timeout: 5 * time.Minute}
	if _, err := (ClaudeRunner{Home: home, Container: c}).Run(context.Background(), inv); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(wt, "probe.txt"))
	got := string(raw)
	t.Logf("de dentro do contêiner:\n%s", got)
	if logs, err := os.ReadFile(filepath.Join(inv.JobDir, "egress.log")); err == nil {
		t.Logf("egress.log:\n%s", logs)
	}
	if !strings.Contains(got, "DOCKER_HOST=tcp://dind:2375") || !strings.Contains(got, "OK PING") || !strings.Contains(got, "hello-world:latest") {
		t.Errorf("o job precisa do Docker isolado, com pull pelo proxy:\n%s", got)
	}
	if strings.Contains(got, "SOCKET_DOCKER") {
		t.Error("o socket do Docker do host não entra nem com dind")
	}
}
