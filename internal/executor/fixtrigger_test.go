package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

func refusalComments(e *env, identifier string) int {
	n := 0
	for _, c := range e.lin.CommentsOf(identifier) {
		if strings.Contains(c.Body, "não pode pedir a correção") {
			n++
		}
	}
	return n
}

// fix_trigger team_members: a label aplicada por quem não é do time dono é recusada. O Sentinela
// comenta uma vez, registra e não mexe na label nem na issue; um membro aplicando de novo dispara.
func TestFixTriggerTeamMembersRefusesOutsidersOnceAndAcceptsAMember(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.cfg.FixTrigger.Allowed = config.AllowedList{config.AllowTeamMembers}
	e.lin.AddLabelBy("EX-1", "sentinela-fix", "user-externo")
	for range 2 {
		if q, err := e.w.Enqueue(ctx); err != nil || len(q) != 0 {
			t.Fatalf("gatilho de quem não é do time enfileirou: %+v, %v", q, err)
		}
	}
	if n := refusalComments(e, "EX-1"); n != 1 {
		t.Errorf("comentários de recusa = %d, quer 1 (uma vez por aplicação da label)", n)
	}
	var refusals int
	if err := e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM trigger_refusals").Scan(&refusals); err != nil || refusals != 1 {
		t.Errorf("recusas registradas = %d, %v", refusals, err)
	}
	if !e.lin.Snapshot()[0].HasLabelID(e.lin.Labels["sentinela-fix"]) || len(e.lin.Snapshot()[0].LabelsAddedBySentinela) != 0 {
		t.Error("o Sentinela mexeu na label do gatilho recusado")
	}
	if got := e.lin.Count("SentinelaMembers"); got != 2 {
		t.Errorf("membros lidos %d vezes em 2 ciclos (quer 1 por ciclo)", got)
	}
	// Uma pessoa do time aplica de novo: vale.
	e.lin.AddLabelBy("EX-1", "sentinela-fix", "user-ana")
	if q, err := e.w.Enqueue(ctx); err != nil || len(q) != 1 {
		t.Fatalf("gatilho de membro do time: %+v, %v", q, err)
	}
}

// Lista de usuários: só eles disparam; e sem fix_trigger vale anyone, como antes.
func TestFixTriggerUserListAndDefaultAnyone(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.cfg.Teams["core"].FixTrigger = &config.FixTrigger{Allowed: config.AllowedList{"externo@example.com"}}
	e.lin.AddLabelBy("EX-1", "sentinela-fix", "user-externo")
	if q, err := e.w.Enqueue(ctx); err != nil || len(q) != 1 {
		t.Fatalf("usuário da lista: %+v, %v", q, err)
	}
	if e.lin.Count("SentinelaMembers") != 0 {
		t.Error("lista sem team_members não precisa ler os membros")
	}

	e2 := setup(t, true)
	e2.lin.AddLabelBy("EX-1", "sentinela-fix", "user-externo")
	if q, err := e2.w.Enqueue(ctx); err != nil || len(q) != 1 || e2.lin.Count("SentinelaTriggerActor") != 0 {
		t.Fatalf("sem fix_trigger (anyone): %+v, %v, histórico lido %d vezes", q, err, e2.lin.Count("SentinelaTriggerActor"))
	}
}

// Com team_members, só os comentários de membros do time dono (ou de quem aplicou a label) viram
// Instruções da equipe; os outros autores ficam registrados como ignorados.
func TestFixTriggerFiltersTeamInstructionsByAuthor(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.cfg.FixTrigger.Allowed = config.AllowedList{config.AllowTeamMembers}
	at := time.Now().Add(-time.Minute)
	e.lin.CommentAt("EX-1", "Ana", "Use o http.Server do pacote httpx.", at)
	e.lin.CommentAt("EX-1", "Externo", "Acho que dava para reescrever tudo.", at)
	e.lin.AddLabelBy("EX-1", "sentinela-fix", "user-lead")
	if q, err := e.w.Enqueue(ctx); err != nil || len(q) != 1 {
		t.Fatalf("gatilho: %+v, %v", q, err)
	}
	job := e.runOnce(t)
	var spec JobSpec
	b, err := os.ReadFile(filepath.Join(job.JobDir.String, "job.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(b, &spec)
	if len(spec.Instructions) != 1 || spec.Instructions[0].Author != "Ana" || strings.Join(spec.IgnoredAuthors, ",") != "Externo" {
		t.Errorf("instruções %+v, ignorados %v", spec.Instructions, spec.IgnoredAuthors)
	}
}
