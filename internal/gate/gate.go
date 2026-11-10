// Package gate é o Policy Gate: código determinístico que decide se um PR do Sentinela poderia
// ser aprovado pela GitHub App revisora ou precisa de um humano (CODEOWNERS). A revisora só
// publica APPROVE quando o gate aprova E github_apps.reviewer_approves está ligado; senão,
// publica uma review COMMENT com a decisão do approver e do gate.
package gate

import (
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// Estados do CI no head do PR.
const (
	CISuccess = "success"
	CIFailure = "failure"
	CIPending = "pending"
	CINone    = "none" // nenhum check configurado
)

// Input é tudo que o gate considera.
type Input struct {
	Team             string
	Policy           config.Policy // política resolvida do time (time sobre defaults)
	Category         string        // bug | performance | improvement | security
	ApproverDecision string        // APPROVE | ESCALATE | "" (rodadas esgotadas, builder bloqueado)
	ApproverReason   string
	HumanApprovedBy  string // sentinela job approve
	ChangedFiles     []string
	ChangedLines     int
	ScopePaths       []string
	BlockedPaths     []string // blocked_paths globais
	CI               string
	ModuleReverts    int
	ReviewerApproves bool // github_apps.reviewer_approves
}

// Result é a decisão do gate.
type Result struct {
	Decision string   // approve | human
	Reasons  []string // por que precisa de humano (vazio quando approve)
	Event    string   // review a publicar: COMMENT ou APPROVE
}

// Decisões.
const (
	Approve = "approve"
	Human   = "human"
)

// Evaluate aplica a política. Toda condição que falha vira um motivo; o gate só aprova sem
// nenhum motivo.
func Evaluate(in Input) Result {
	var reasons []string
	add := func(format string, args ...any) { reasons = append(reasons, fmt.Sprintf(format, args...)) }

	switch {
	case in.ApproverDecision == "APPROVE":
	case in.HumanApprovedBy != "":
		// Um humano aprovou o job em ESCALATE: a decisão do approver deixa de bloquear, mas a
		// política do time ainda vale.
	case in.ApproverDecision == "ESCALATE":
		add("approver pediu humano: %s", orDash(in.ApproverReason))
	default:
		add("sem decisão APPROVE do approver (%s)", orDash(in.ApproverReason))
	}
	if b := in.Policy.Blocked; b != nil && *b {
		add("time %s tem policy.blocked: nunca é auto-aprovado", in.Team)
	}
	if a := in.Policy.AutoApprove; a == nil || !*a {
		add("time %s com policy.auto_approve desligado", in.Team)
	}
	if in.Category == "security" {
		add("categoria security sempre passa por humano")
	}
	if m := in.Policy.MaxLines; m != nil && in.ChangedLines > *m {
		add("%d linhas alteradas, limite do time %d", in.ChangedLines, *m)
	}
	if m := in.Policy.MaxFiles; m != nil && len(in.ChangedFiles) > *m {
		add("%d arquivos alterados, limite do time %d", len(in.ChangedFiles), *m)
	}
	for _, f := range in.ChangedFiles {
		for _, g := range in.BlockedPaths {
			if ok, _ := doublestar.Match(g, f); ok {
				add("arquivo em path bloqueado: %s (%s)", f, g)
				break
			}
		}
		if len(in.ScopePaths) > 0 && !inScope(f, in.ScopePaths) {
			add("arquivo fora de scope_paths: %s", f)
		}
	}
	switch in.CI {
	case CISuccess:
	case CIFailure:
		add("CI vermelho")
	case CIPending:
		add("CI ainda rodando")
	default:
		add("PR sem checks de CI")
	}
	if in.ModuleReverts > 0 {
		add("módulo com %d revert(s) de PR do Sentinela", in.ModuleReverts)
	}

	res := Result{Decision: Approve, Reasons: reasons, Event: "COMMENT"}
	if len(reasons) > 0 {
		res.Decision = Human
	}
	if res.Decision == Approve && in.ReviewerApproves {
		res.Event = "APPROVE"
	}
	return res
}

func inScope(file string, scope []string) bool {
	for _, s := range scope {
		s = strings.TrimSuffix(strings.TrimPrefix(s, "/"), "/")
		if s == "" || s == "." || file == s || strings.HasPrefix(file, s+"/") {
			return true
		}
		if ok, _ := doublestar.Match(s, file); ok {
			return true
		}
	}
	return false
}

// Comment monta o corpo da review da revisora.
func Comment(in Input, res Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Sentinela · Policy Gate**\n\n")
	fmt.Fprintf(&b, "- Approver: **%s**", orDash(in.ApproverDecision))
	if in.ApproverReason != "" {
		fmt.Fprintf(&b, " · %s", in.ApproverReason)
	}
	b.WriteString("\n")
	if in.HumanApprovedBy != "" {
		fmt.Fprintf(&b, "- Aprovado por humano no Sentinela: %s (`sentinela job approve`)\n", in.HumanApprovedBy)
	}
	fmt.Fprintf(&b, "- Gate: **%s**\n", map[string]string{Approve: "aprovaria", Human: "precisa de humano"}[res.Decision])
	for _, r := range res.Reasons {
		fmt.Fprintf(&b, "  - %s\n", r)
	}
	if res.Event != "APPROVE" {
		b.WriteString("\nEsta revisão não aprova o PR: a aprovação e o merge são de um humano")
		if !in.ReviewerApproves {
			b.WriteString(" (`github_apps.reviewer_approves` está desligado no sentinela.yaml)")
		}
		b.WriteString(".\n")
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
