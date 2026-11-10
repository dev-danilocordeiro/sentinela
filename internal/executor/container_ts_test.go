package executor

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/scan"
)

// fixtureFiles lê o testdata/ts-monorepo como o conteúdo de um repo.
func fixtureFiles(t *testing.T) map[string]string {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "ts-monorepo")
	files := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p) //nolint:gosec // fixture do repo
		files[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// Item 5 do plano de TypeScript: o job em contêiner roda os testes do workspace afetado com Node e
// corepack, pelos comandos do AGENTS.md que o job.json aponta. O builder falso lê o job.json,
// faz o setup da raiz (corepack e pnpm), escreve um teste para o await esquecido do BFF, vê o teste
// falhar, aplica um fix falso e vê o teste passar. Depois, no host, o tsc do Sentinela roda no
// worktree, que agora tem node_modules.
func TestContainerJobRunsTheTestsOfTheAffectedTypeScriptWorkspace(t *testing.T) {
	c, home := containerForTest(t)
	c.Egress = append(c.Egress, "registry.npmjs.org")
	e := setupWith(t, setupOpts{
		files:    fixtureFiles(t),
		module:   "apps/bff/src/routes",
		evidence: `[{"file":"apps/bff/src/routes/orders.ts","line":17,"snippet":"client.cancel(String(req.params.id));"}]`,
		scope:    []string{"apps/bff/src/routes"},
	})
	fakeClaudeIn(t, home, `prompt=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-p" ]; then prompt="$2"; fi
  shift
done
job="${prompt#/sentinela-factory:sentinela-fix }"
doc=$(sed -n 's/^  "commands_doc": "\(.*\)",$/\1/p' "$job/job.json")
pkg=$(sed -n 's/^      "name": "\(.*\)"$/\1/p' "$job/job.json" | head -1)
bt=$(printf '\140')
testcmd=$(grep -m1 'Testes' "$doc" | sed "s/^[^$bt]*$bt\\([^$bt]*\\)$bt.*\$/\\1/")
{
echo "DOC=$doc PACOTE=$pkg"
echo "TESTE=$testcmd"
node --version
# Setup do AGENTS.md da raiz.
corepack enable && pnpm install --frozen-lockfile && echo SETUP_OK
cat > apps/bff/src/routes/cancel.test.ts <<'TESTE'
import { describe, expect, it, vi } from "vitest";
import type { OrdersClient } from "@acme/shared";
import { orderRoutes } from "./orders";

describe("cancel", () => {
  it("responde só depois do backend e propaga a falha dele", async () => {
    const client = { cancel: vi.fn().mockRejectedValue(new Error("backend fora")) } as unknown as OrdersClient;
    const res = { status: vi.fn().mockReturnThis(), end: vi.fn() };
    await expect(orderRoutes(client).cancel({ params: { id: "o-1" } } as never, res as never)).rejects.toThrow("backend fora");
    expect(res.status).not.toHaveBeenCalled();
  });
});
TESTE
sh -c "$testcmd"; echo "ANTES=$?"
sed -i 's/^      client.cancel(/      await client.cancel(/' apps/bff/src/routes/orders.ts
sh -c "$testcmd"; echo "DEPOIS=$?"
git add apps/bff/src && git commit -qm "EX-1: cancel espera o backend (fix falso)"
} > "$job/probe.txt" 2>&1
mkdir -p "$job/round-1"
printf 'STATUS: READY\n## Mudanças\n- orders.ts\n## Critérios\n- ok\n' > "$job/round-1/build.md"
for lens in security code reliability domain; do printf 'VERDICT: PASS\n## Achados\n- nenhum\n' > "$job/round-1/review-$lens.md"; done
printf 'APPROVE\nmotivo do approver\n## O que o humano precisa checar\n- o diff\n' > "$job/decision.md"
printf '{"status":"approve","rounds":1}' > "$job/state.json"`)
	e.w.Runner = ClaudeRunner{Home: home, Container: c}
	e.w.ClaudeHome = filepath.Join(home.Dir, ".claude")
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	job := e.runOnce(t)
	raw, _ := os.ReadFile(filepath.Join(job.JobDir.String, "probe.txt"))
	probe := string(raw)
	t.Logf("de dentro do contêiner:\n%s", probe)
	if logs, err := os.ReadFile(filepath.Join(job.JobDir.String, "egress.log")); err == nil {
		t.Logf("egress.log (fim):\n%s", tail(string(logs), 1500))
	}
	if job.Status != "succeeded" {
		t.Fatalf("job = %s: %s", job.Status, job.Error.String)
	}
	for _, want := range []string{
		"DOC=apps/bff/AGENTS.md PACOTE=@acme/bff",
		"TESTE=pnpm --filter @acme/bff run test",
		"SETUP_OK", "ANTES=1", "DEPOIS=0", "Tests  3 passed (3)",
	} {
		if !strings.Contains(probe, want) {
			t.Errorf("faltou %q na saída do job", want)
		}
	}
	if strings.Contains(probe, "UNDICI-EHPA") {
		t.Error("o aviso do proxy do Node polui a saída dos testes")
	}
	if log := gitIn(t, e.remote, "log", "--format=%s", "sentinela/EX-1"); !strings.Contains(log, "EX-1: cancel espera o backend (fix falso)") {
		t.Errorf("o commit do job não chegou ao remoto:\n%s", log)
	}

	// No worktree do job, com node_modules, o tsc do Sentinela roda (na descoberta, pula).
	tools, _ := filepath.Abs(filepath.Join("..", "..", "tools", "node"))
	if _, err := os.Stat(filepath.Join(tools, "node_modules", "typescript")); err != nil {
		t.Logf("tsc não conferido: %s sem instalar", tools)
		return
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Log("tsc não conferido: node fora do PATH")
		return
	}
	r := &scan.Runner{Scanners: []scan.Scanner{scan.TSC{Tools: tools}}}
	_, results := r.Run(context.Background(), job.Worktree.String, map[string]bool{"typescript": true})
	if res := results[0]; res.Status != scan.StatusOK || res.Units != 3 || res.Signals != 0 {
		t.Errorf("tsc no worktree do job: %+v", res)
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
