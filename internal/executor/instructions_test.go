package executor

import (
	"strings"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// A linha do tempo real da EX-14 (2026-10-10, horários UTC): uma automação antiga, que comenta
// pela conta de uma pessoa, começou e falhou (com um diff que usava um teto de 1000); a pessoa
// aplicou sentinela-fix às 19:17 e, às 19:26, comentou a decisão do time. O achado gravado pede
// "um teto razoável".
var (
	at = func(hhmm string) time.Time {
		t, _ := time.Parse("2006-01-02 15:04", "2026-10-10 "+hhmm)
		return t
	}
	person  = &linear.Actor{Name: "Fulano"}
	ex14Raw = []linear.IssueComment{
		{ID: "c-team", Body: "Validar apenas capacidade > 0, sem teto superior.", CreatedAt: at("19:26"), User: person},
		{ID: "c-fail", Body: "❌ **Não consegui abrir o PR: o repositório está inacessível para escrita.**\n\n- `CapacidadeMaxima` (1000, um teto que escolhi)", CreatedAt: at("19:07"), User: person},
		{ID: "c-start", Body: "🤖 Claude começou a trabalhar nesta issue", CreatedAt: at("19:05"), User: person},
		{ID: "c-sentinela", Body: "**Sentinela · execução #3**: falhou.", CreatedAt: at("19:10"), User: person, BySentinela: true}, // marcado pelo cliente (ADR 0032)
		{ID: "c-bot", Body: "Comentário de integração", CreatedAt: at("19:11"), BotActor: &linear.Actor{Name: "GitHub"}},
		{ID: "c-late", Body: "Depois de o job começar não entra.", CreatedAt: at("19:40"), User: person},
	}
	ex14Ignore  = []string{"🤖 Claude", "❌ **Não consegui"}
	ex14Finding = signals.Finding{
		Fingerprint: "db75422c2ff1", Category: "bug", Severity: "medium", Title: "Valida capacidade positiva ao abrir turma",
		Context:            "O handler aceita qualquer inteiro em capacidade, inclusive zero ou negativo.",
		Evidence:           []signals.Evidence{{File: "go/cmd/api/main.go", Line: 27, Snippet: `t.AbrirTurma(r.PathValue("id"), capacidade)`}},
		ProposedFix:        "Após o Atoi, rejeitar capacidade <= 0 (e um teto razoável) com 400. Idealmente fazer AbrirTurma retornar erro de domínio, tratado no handler.",
		AcceptanceCriteria: []string{"POST /turmas/{id}?capacidade=0 e capacidade=-1 retornam 400", "Capacidade válida continua retornando 201", "Teste cobre os casos de borda"},
		ScopePaths:         []string{"go/cmd/api", "go/internal/turmas"},
	}
)

func TestEX14TeamInstructionOverridesTheProposedCeiling(t *testing.T) {
	win := InstructionWindow{Until: at("19:30")} // o job começou às 19:30; primeiro job do achado
	ins := TeamInstructions(ex14Raw, win, ex14Ignore)
	if len(ins) != 1 || ins[0].CommentID != "c-team" || ins[0].Author != "Fulano" {
		t.Fatalf("instruções = %+v", ins)
	}
	// Sem a lista de prefixos, a automação antiga (pela conta da pessoa) entraria, em ordem.
	if all := TeamInstructions(ex14Raw, win, nil); len(all) != 3 || all[0].CommentID != "c-start" || all[2].CommentID != "c-team" {
		t.Errorf("sem prefixos ignorados = %+v", all)
	}

	spec := JobSpec{Issue: "EX-14", Instructions: ins, InstructionWindow: win}
	got := SpecFromFinding(spec, ex14Finding)
	sec := strings.Index(got, "## Instruções da equipe")
	req := strings.Index(got, "## Requisitos")
	if sec < 0 || req < sec {
		t.Fatalf("a seção de instruções deve vir antes dos Requisitos:\n%s", got)
	}
	for _, want := range []string{
		"> Validar apenas capacidade > 0, sem teto superior.",
		"**Estas instruções têm prioridade sobre a Correção proposta e os Requisitos que vêm do achado.**",
		"comentário `c-team`", "até 2026-10-10 19:30 UTC, quando este job começou",
		"_Correção proposta pelo achado; as Instruções da equipe acima prevalecem._",
		"(e um teto razoável)", // o achado gravado não muda
	} {
		if !strings.Contains(got, want) {
			t.Errorf("spec sem %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "CapacidadeMaxima") || strings.Contains(got, "Depois de o job começar") {
		t.Errorf("comentário fora da janela ou ignorado entrou:\n%s", got)
	}
	if plain := SpecFromFinding(JobSpec{Issue: "EX-14"}, ex14Finding); strings.Contains(plain, "Instruções da equipe") || strings.Contains(plain, "prevalecem") {
		t.Errorf("sem instruções a spec não muda:\n%s", plain)
	}

	body := PRBody(db.Execution{ID: 4, Kind: "fix"}, spec, Result{}, RunResult{})
	if !strings.Contains(body, "### Instruções da equipe") || !strings.Contains(body, "> Validar apenas capacidade > 0, sem teto superior.") {
		t.Errorf("PR sem as instruções:\n%s", body)
	}
	a := instructionsArtifact(ins, win)
	if a.Verdict != "INSTRUCTIONS: 1" || len(a.Items) != 1 || !strings.HasPrefix(a.Items[0], "c-team · Fulano · 2026-10-10T19:26:00Z") {
		t.Errorf("registro = %+v", a)
	}
}

// Num job seguinte do mesmo achado, só entram os comentários feitos depois de o anterior começar.
func TestInstructionWindowStartsAtThePreviousJob(t *testing.T) {
	win := InstructionWindow{From: at("19:20"), Until: at("19:45")}
	ins := TeamInstructions(ex14Raw, win, ex14Ignore)
	if len(ins) != 2 || ins[0].CommentID != "c-team" || ins[1].CommentID != "c-late" {
		t.Fatalf("instruções = %+v", ins)
	}
	if !strings.Contains(win.String(), "de 2026-10-10 19:20 UTC, quando o job anterior começou") {
		t.Errorf("janela = %s", win)
	}
	if a := instructionsArtifact(nil, win); a.Verdict != "INSTRUCTIONS: 0" || !strings.Contains(a.Summary, "19:20") {
		t.Errorf("registro sem instruções = %+v", a)
	}
}
