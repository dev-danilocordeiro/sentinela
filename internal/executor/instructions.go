package executor

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
)

// Instruções da equipe (ADR 0026): a spec de um achado do Judge é montada por código a partir do
// achado gravado, e antes disto não havia como uma pessoa ajustar a correção antes da primeira
// rodada (EX-14: a correção proposta pedia "um teto razoável", e o time queria só > 0). Os
// comentários humanos da issue feitos até o job começar (desde o job anterior do mesmo achado,
// se houver) entram na spec com prioridade sobre a correção proposta. O achado gravado não muda.

// Instruction é um comentário humano que entrou na spec.
type Instruction struct {
	CommentID string    `json:"comment_id"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	Body      string    `json:"body"`
}

// InstructionWindow é o intervalo de comentários considerado: (From, Until]. From zero = desde a
// criação da issue (primeiro job do achado).
type InstructionWindow struct {
	From  time.Time `json:"from,omitzero"`
	Until time.Time `json:"until"`
}

func (w InstructionWindow) String() string {
	until := w.Until.UTC().Format("2006-01-02 15:04 UTC")
	if w.From.IsZero() {
		return "até " + until + ", quando este job começou"
	}
	return fmt.Sprintf("de %s, quando o job anterior começou, até %s, quando este começou", w.From.UTC().Format("2006-01-02 15:04 UTC"), until)
}

// TeamInstructions escolhe os comentários humanos da janela, em ordem cronológica. Ficam de fora
// os do Sentinela, os de integração e os que começam com um dos prefixos ignorados.
func TeamInstructions(comments []linear.IssueComment, win InstructionWindow, ignore []string) []Instruction {
	var out []Instruction
	for _, cm := range comments {
		if !cm.Human() || !cm.CreatedAt.After(win.From) || cm.CreatedAt.After(win.Until) || ignored(cm.Body, ignore) {
			continue
		}
		out = append(out, Instruction{CommentID: cm.ID, Author: cm.User.Name, CreatedAt: cm.CreatedAt, Body: strings.TrimSpace(cm.Body)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func ignored(body string, prefixes []string) bool {
	b := strings.TrimSpace(body)
	for _, p := range prefixes {
		if p = strings.TrimSpace(p); p != "" && strings.HasPrefix(b, p) {
			return true
		}
	}
	return false
}

// instructionsSection é a seção "Instruções da equipe" da spec e do issue.md; vazia sem instruções.
func instructionsSection(ins []Instruction, win InstructionWindow, ignoredAuthors []string) string {
	if len(ins) == 0 && len(ignoredAuthors) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Instruções da equipe\n\n")
	if len(ignoredAuthors) > 0 {
		fmt.Fprintf(&b, "Comentários de %s ficaram de fora: pela política do time (fix_trigger), só valem os de membros do time dono e de quem aplicou a label de gatilho.\n\n", strings.Join(ignoredAuthors, ", "))
	}
	if len(ins) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "Comentários de pessoas da equipe na issue, %s. **Estas instruções têm prioridade "+
		"sobre a Correção proposta e os Requisitos que vêm do achado.** Entre elas, em conflito, vale a "+
		"mais recente. Se uma instrução contradiz um critério de aceite, siga a instrução e diga isso "+
		"no build.md.\n\n", win)
	for _, in := range ins {
		fmt.Fprintf(&b, "- **%s**, %s (comentário `%s`):\n", in.Author, in.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"), in.CommentID)
		for _, l := range strings.Split(in.Body, "\n") {
			fmt.Fprintf(&b, "  > %s\n", l)
		}
	}
	return b.String()
}

// instructionsKey é a chave das instruções em executions.artifacts.
const instructionsKey = "team-instructions"

// instructionsArtifact registra, em executions.artifacts, quais comentários entraram na spec
// deste job e a janela considerada (também quando nenhum entrou).
func instructionsArtifact(ins []Instruction, win InstructionWindow) Artifact {
	a := Artifact{File: "spec.md", Verdict: fmt.Sprintf("INSTRUCTIONS: %d", len(ins))}
	a.Summary = "Comentários considerados " + win.String() + "."
	for _, in := range ins {
		a.Items = append(a.Items, fmt.Sprintf("%s · %s · %s · %s", in.CommentID, in.Author, in.CreatedAt.UTC().Format(time.RFC3339), firstLine(in.Body)))
	}
	return a
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(l); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return l
}
