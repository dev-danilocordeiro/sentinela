package gate

import (
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

func ptr[T any](v T) *T { return &v }

// base passa em tudo; cada caso muda uma coisa.
func base() Input {
	return Input{
		Team: "plataforma", Policy: config.Policy{AutoApprove: ptr(true), MaxLines: ptr(50), MaxFiles: ptr(3)},
		Category: "bug", ApproverDecision: "APPROVE", ChangedFiles: []string{"go/cmd/api/main.go"}, ChangedLines: 12,
		ScopePaths: []string{"go/cmd/api"}, BlockedPaths: []string{"**/financeiro/**", "**/migrations/**"}, CI: CISuccess,
		ReviewerApproves: true,
	}
}

func TestGate(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Input)
		reason string // "" = aprova
		event  string
	}{
		{"tudo passa e a revisora pode aprovar", func(*Input) {}, "", "APPROVE"},
		{"tudo passa mas reviewer_approves desligado: só COMMENT", func(i *Input) { i.ReviewerApproves = false }, "", "COMMENT"},
		{"approver ESCALATE", func(i *Input) { i.ApproverDecision, i.ApproverReason = "ESCALATE", "toca em auth" }, "approver pediu humano: toca em auth", "COMMENT"},
		{"rodadas esgotadas (sem decisão)", func(i *Input) { i.ApproverDecision, i.ApproverReason = "", "rodadas esgotadas" }, "sem decisão APPROVE", "COMMENT"},
		{"humano aprovou o ESCALATE, política ok", func(i *Input) { i.ApproverDecision, i.HumanApprovedBy = "ESCALATE", "Fulano" }, "", "APPROVE"},
		{"time bloqueado (financeiro)", func(i *Input) { i.Team, i.Policy.Blocked = "financeiro", ptr(true) }, "policy.blocked", "COMMENT"},
		{"auto_approve desligado (todos os times hoje)", func(i *Input) { i.Policy.AutoApprove = ptr(false) }, "auto_approve desligado", "COMMENT"},
		{"auto_approve não definido conta como desligado", func(i *Input) { i.Policy.AutoApprove = nil }, "auto_approve desligado", "COMMENT"},
		{"categoria security", func(i *Input) { i.Category = "security" }, "categoria security", "COMMENT"},
		{"linhas acima do limite", func(i *Input) { i.ChangedLines = 51 }, "51 linhas alteradas, limite do time 50", "COMMENT"},
		{"arquivos acima do limite", func(i *Input) {
			i.ChangedFiles = []string{"go/cmd/api/a.go", "go/cmd/api/b.go", "go/cmd/api/c.go", "go/cmd/api/d.go"}
		}, "4 arquivos alterados", "COMMENT"},
		{"path bloqueado", func(i *Input) { i.ChangedFiles, i.ScopePaths = []string{"go/internal/financeiro/f.go"}, nil }, "path bloqueado: go/internal/financeiro/f.go", "COMMENT"},
		{"fora do escopo", func(i *Input) { i.ChangedFiles = []string{"go/internal/turmas/t.go"} }, "fora de scope_paths: go/internal/turmas/t.go", "COMMENT"},
		{"CI vermelho", func(i *Input) { i.CI = CIFailure }, "CI vermelho", "COMMENT"},
		{"CI rodando", func(i *Input) { i.CI = CIPending }, "CI ainda rodando", "COMMENT"},
		{"sem CI", func(i *Input) { i.CI = CINone }, "sem checks", "COMMENT"},
		{"módulo com revert", func(i *Input) { i.ModuleReverts = 1 }, "1 revert", "COMMENT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.change(&in)
			res := Evaluate(in)
			if tc.reason == "" {
				if res.Decision != Approve || len(res.Reasons) != 0 {
					t.Fatalf("esperava aprovar: %+v", res)
				}
			} else if res.Decision != Human || !strings.Contains(strings.Join(res.Reasons, " | "), tc.reason) {
				t.Fatalf("esperava humano com %q: %+v", tc.reason, res)
			}
			if res.Event != tc.event {
				t.Errorf("evento = %s, quer %s", res.Event, tc.event)
			}
		})
	}
}

// A config real das primeiras execuções da Fase 4: nenhum time aprova, nem a plataforma, e a
// revisora nunca publica APPROVE.
func TestRealConfigNeverApproves(t *testing.T) {
	cfg, err := config.Load("../../testdata/sentinela.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name, team := range cfg.Teams {
		in := base()
		in.Team, in.Policy, in.ReviewerApproves = name, team.Policy.Merge(cfg.Defaults.Policy), cfg.GitHubApps.ReviewerApproves
		res := Evaluate(in)
		if res.Decision != Human || res.Event != "COMMENT" {
			t.Errorf("time %s: %+v", name, res)
		}
	}
	if !strings.Contains(Comment(base(), Result{Decision: Human, Reasons: []string{"x"}, Event: "COMMENT"}), "não aprova o PR") {
		t.Error("o comentário precisa dizer que não aprova")
	}
}
