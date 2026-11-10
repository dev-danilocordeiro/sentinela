package linear_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/linear/lineartest"
)

// seedMarked põe no Linear falso uma issue do time EX com a descrição dada.
func (e *env) seedMarked(id, project, desc string, created time.Time, archived, trashed bool) {
	e.fake.Issues = append(e.fake.Issues, &lineartest.Issue{
		ID: "seed-" + id, Identifier: id, URL: "https://linear.app/exemplo/issue/" + id, TeamID: "team-ex", ProjectID: project,
		StateID: e.fake.States["team-ex"][0].ID, Description: desc, UpdatedAt: created, CreatedAt: created, Archived: archived, Trashed: trashed,
	})
}

func (e *env) setLegacyUntil(t *testing.T, at *time.Time) string {
	t.Helper()
	var id string
	if err := e.st.Pool.QueryRow(context.Background(), "UPDATE installation SET legacy_markers_until = $1 RETURNING id", at).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// O caso da ADR 0047: a busca por marcador reaproveitava uma issue arquivada de uma
// instalação de avaliação, num projeto de teste. Agora a publicação só reaproveita issue aberta,
// do time e do projeto do roteamento, e desta instalação; senão cria outra, com o id no marcador.
func TestPublishDoesNotReuseAMarkedIssueFromElsewhere(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	until := time.Now().Add(time.Hour) // como na instalação real: o marcador antigo ainda vale
	inst := e.setLegacyUntil(t, &until)
	e.addFinding(t, "hay20case001", "libs/x/a.go", "high")
	legacy := "`sentinela-fingerprint: hay20case001`"
	old := time.Now().Add(-48 * time.Hour)
	e.seedMarked("EX-901", "proj-plat", legacy, old, true, false)                                            // arquivada (o caso da ADR 0047)
	e.seedMarked("EX-902", "proj-plat", legacy, old, false, true)                                            // na lixeira
	e.seedMarked("EX-903", "proj-core", legacy, old, false, false)                                           // outro projeto
	e.seedMarked("EX-904", "", legacy, old, false, false)                                                    // sem projeto
	e.seedMarked("EX-905", "proj-plat", "`sentinela-fingerprint: hay20case001@deadbeef`", old, false, false) // outra instalação
	e.seedMarked("EX-906", "proj-plat", "`sentinela-fingerprint: hay20case0012`", old, false, false)         // outro fingerprint

	res, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 || res.Issues[0].Reused || e.fake.Count("SentinelaCreateIssue") != 1 {
		t.Fatalf("publicação = %+v, criações = %d (quer 1 issue nova)", res, e.fake.Count("SentinelaCreateIssue"))
	}
	created := e.fake.Issues[len(e.fake.Issues)-1]
	if want := "`sentinela-fingerprint: hay20case001@" + inst + "`"; !strings.Contains(created.Description, want) {
		t.Errorf("marcador da issue nova: quer %s em\n%s", want, created.Description)
	}

	// Publicação retomada: acha a própria issue nova pelo marcador com a instalação.
	if _, err := e.st.Pool.Exec(ctx, "DELETE FROM linear_issues"); err != nil {
		t.Fatal(err)
	}
	res, err = e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil || len(res.Issues) != 1 || !res.Issues[0].Reused || res.Issues[0].Identifier != created.Identifier {
		t.Fatalf("republicação = %+v, %v (quer reaproveitar %s)", res, err, created.Identifier)
	}
}

// As issues antigas desta instalação, com o marcador sem id, continuam reconhecidas se foram
// criadas até legacy_markers_until; depois disso, ou numa instalação nova, não.
func TestPublishRecognizesLegacyMarkersOnlyUntilTheCutoff(t *testing.T) {
	cases := []struct {
		name   string
		until  func() *time.Time
		reused bool
	}{
		{"criada antes do corte", func() *time.Time { u := time.Now().Add(time.Hour); return &u }, true},
		{"criada depois do corte", func() *time.Time { u := time.Now().Add(-72 * time.Hour); return &u }, false},
		{"instalação nova, sem corte", func() *time.Time { return nil }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t)
			e.setLegacyUntil(t, c.until())
			e.addFinding(t, "legacycase01", "libs/x/a.go", "medium")
			e.seedMarked("EX-950", "proj-plat", "Corpo.\n\n`sentinela-fingerprint: legacycase01`\n", time.Now().Add(-24*time.Hour), false, false)
			res, err := e.pub.Publish(context.Background(), "acme/mono", 0, e.res)
			if err != nil || len(res.Issues) != 1 {
				t.Fatalf("publicação = %+v, %v", res, err)
			}
			if got := res.Issues[0].Reused && res.Issues[0].Identifier == "EX-950"; got != c.reused {
				t.Errorf("reaproveitou EX-950 = %v, quer %v (%+v)", got, c.reused, res.Issues[0])
			}
		})
	}
}

// finding unlink: o achado aberto sai da issue errada com o motivo registrado, volta a pendente
// e a publicação seguinte cria a issue certa. Achado decidido não é desligado.
func TestUnlinkMakesTheFindingPendingAgain(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id := e.addFinding(t, "unlinkcase01", "libs/x/a.go", "high")
	e.seedMarked("EX-920", "proj-plat", "`sentinela-fingerprint: unlinkcase01`", time.Now().Add(-time.Hour), false, false)
	until := time.Now().Add(time.Hour)
	e.setLegacyUntil(t, &until)
	res, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil || len(res.Issues) != 1 || res.Issues[0].Identifier != "EX-920" {
		t.Fatalf("publicação = %+v, %v (quer reaproveitar EX-920)", res, err)
	}
	if _, err := linear.Unlink(ctx, e.st, id, "EX-920", "", "teste"); err == nil {
		t.Fatal("unlink sem motivo passou")
	}
	rows, err := linear.Unlink(ctx, e.st, id, "EX-920", "issue de outra instalação", "teste")
	if err != nil || len(rows) != 1 {
		t.Fatalf("unlink = %+v, %v", rows, err)
	}
	var reason, ident string
	var link *string
	if err := e.st.Pool.QueryRow(ctx, "SELECT u.reason, u.identifier, f.linear_issue_id FROM finding_unlinks u JOIN findings f ON f.id = u.finding_id WHERE u.finding_id = $1", id).Scan(&reason, &ident, &link); err != nil {
		t.Fatal(err)
	}
	if reason != "issue de outra instalação" || ident != "EX-920" || link != nil {
		t.Errorf("registro = %q %q, linear_issue_id = %v", reason, ident, link)
	}
	// Corta o marcador antigo: a próxima publicação não volta para a EX-920.
	e.setLegacyUntil(t, nil)
	res, err = e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil || len(res.Issues) != 1 || res.Issues[0].Reused {
		t.Fatalf("republicação = %+v, %v (quer issue nova)", res, err)
	}
	if _, err := e.st.Pool.Exec(ctx, "UPDATE findings SET state = 'rejected' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := linear.Unlink(ctx, e.st, id, "", "x", "teste"); !errors.Is(err, linear.ErrNotUnlinkable) {
		t.Errorf("unlink de achado rejected: %v (quer ErrNotUnlinkable)", err)
	}
}
