package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

func fakeSlack(t *testing.T, lists *atomic.Int32, posted *[]string) *Slack {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-t" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			http.Error(w, "headers", http.StatusBadRequest)
			return
		}
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/conversations.list":
			lists.Add(1)
			if r.Form.Get("cursor") == "" {
				_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C0AAAAAAAA1","name":"geral"}],"response_metadata":{"next_cursor":"p2"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C0TIMEPAG01","name":"time-pagamentos"}],"response_metadata":{"next_cursor":""}}`))
		case "/chat.postMessage":
			if r.Form.Get("channel") == "C0FORA00001" {
				_, _ = w.Write([]byte(`{"ok":false,"error":"not_in_channel"}`))
				return
			}
			*posted = append(*posted, r.Form.Get("channel")+"|"+r.Form.Get("text"))
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/conversations.info":
			_, _ = w.Write([]byte(`{"ok":true,"channel":{"id":"` + r.Form.Get("channel") + `","name":"x","is_member":false,"is_archived":false}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return &Slack{Token: "xoxb-t", BaseURL: srv.URL}
}

func TestSlackResolvesChannelNamesOnceAndPostsByID(t *testing.T) {
	var lists atomic.Int32
	var posted []string
	s := fakeSlack(t, &lists, &posted)
	ctx := context.Background()
	for range 2 {
		if err := s.Post(ctx, "#time-pagamentos", "oi"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Post(ctx, "C0AAAAAAAA1", "direto"); err != nil {
		t.Fatal(err)
	}
	if lists.Load() != 2 { // duas páginas na primeira resolução; depois, cache
		t.Errorf("conversations.list chamado %d vezes", lists.Load())
	}
	if len(posted) != 3 || posted[0] != "C0TIMEPAG01|oi" || posted[2] != "C0AAAAAAAA1|direto" {
		t.Errorf("postagens = %v", posted)
	}
	if err := s.Post(ctx, "#nao-existe", "x"); err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("canal inexistente: %v", err)
	}
	if err := s.Post(ctx, "C0FORA00001", "x"); err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Errorf("bot fora do canal: %v", err)
	}
	info, err := s.Channel(ctx, "C0AAAAAAAA1")
	if err != nil || info.IsMember {
		t.Errorf("channel = %+v (%v)", info, err)
	}
	if err := (&Slack{}).Post(ctx, "C0AAAAAAAA1", "x"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("sem token: %v", err)
	}
}

type rec struct {
	posts map[string][]string
	fail  map[string]bool
}

func (r *rec) Post(_ context.Context, ch, text string) error {
	if r.fail[ch] {
		return errors.New("slack chat.postMessage: channel_not_found")
	}
	if r.posts == nil {
		r.posts = map[string][]string{}
	}
	r.posts[ch] = append(r.posts[ch], text)
	return nil
}

func TestRouterSendsToTeamChannelWithMentionAndFallsBackOnFailure(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.Defaults{Notify: config.Notify{Slack: "C0DEFAULT01"}},
		Teams: map[string]*config.Team{
			"pay":    {Notify: config.Notify{Slack: "#pagamentos", Mention: "S0PAYGROUP1"}},
			"plat":   {},
			"broken": {Notify: config.Notify{Slack: "C0QUEBRADO1"}},
		},
	}
	r := &rec{fail: map[string]bool{"C0QUEBRADO1": true}}
	router := &Router{Notifier: r, Cfg: cfg}
	ctx := context.Background()
	if w := router.Team(ctx, "pay", "nova issue"); w != nil {
		t.Errorf("avisos = %v", w)
	}
	router.Team(ctx, "plat", "sem canal")
	w := router.Team(ctx, "broken", "vai falhar")
	if r.posts["#pagamentos"][0] != "<!subteam^S0PAYGROUP1> nova issue" || r.posts["C0DEFAULT01"][0] != "sem canal" {
		t.Errorf("postagens = %v", r.posts)
	}
	if len(w) != 1 || len(r.posts["C0DEFAULT01"]) != 2 || !strings.Contains(r.posts["C0DEFAULT01"][1], "C0QUEBRADO1") || !strings.Contains(r.posts["C0DEFAULT01"][1], "channel_not_found") {
		t.Errorf("fallback: avisos %v, padrão %v", w, r.posts["C0DEFAULT01"])
	}
	var nilRouter *Router
	if nilRouter.Summary(ctx, "x") != nil || nilRouter.Team(ctx, "pay", "x") != nil {
		t.Error("roteador nil deveria ignorar")
	}
}

func TestMention(t *testing.T) {
	for in, want := range map[string]string{
		"S0PAYGROUP1": "<!subteam^S0PAYGROUP1>", "U0USUARIO01": "<@U0USUARIO01>", "@here": "<!here>", "@grupo": "@grupo",
	} {
		if got := Mention(in); got != want {
			t.Errorf("Mention(%q) = %q, quer %q", in, got, want)
		}
	}
	if MentionIsID("@grupo") || !MentionIsID("S0PAYGROUP1") {
		t.Error("MentionIsID")
	}
}
