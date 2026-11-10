package linear_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/linear/lineartest"
)

// tokenServer é o endpoint de token do Linear, falso: devolve tokens numerados e grava os forms.
type tokenServer struct {
	mu     sync.Mutex
	forms  []url.Values
	basic  []string
	n      int
	expiry int
}

func (ts *tokenServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.forms = append(ts.forms, r.PostForm)
	u, p, _ := r.BasicAuth()
	ts.basic = append(ts.basic, u+":"+p)
	ts.n++
	body := map[string]any{"access_token": fmt.Sprintf("at-%d", ts.n), "expires_in": ts.expiry, "scope": []string{"read", "write"}}
	if r.PostForm.Get("grant_type") != "client_credentials" {
		body["refresh_token"] = fmt.Sprintf("rt-%d", ts.n)
	}
	_ = json.NewEncoder(w).Encode(body)
}

func newApp(t *testing.T, grant string) (*linear.AppAuth, *tokenServer) {
	t.Helper()
	ts := &tokenServer{expiry: 86399}
	srv := httptest.NewServer(ts)
	t.Cleanup(srv.Close)
	return &linear.AppAuth{
		ClientID: "cid", ClientSecret: "csecret", Grant: grant, Scopes: []string{"read", "write"},
		TokenFile: filepath.Join(t.TempDir(), "cfg", "linear-token.json"), TokenURL: srv.URL,
	}, ts
}

func readToken(t *testing.T, path string) linear.Token {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("arquivo do token com permissão %v, quer 0600", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	var tok linear.Token
	if err := json.Unmarshal(raw, &tok); err != nil {
		t.Fatal(err)
	}
	return tok
}

// client_credentials: pede o token na primeira chamada, grava em 0600, reusa até perto de vencer
// e manda "Bearer" ao Linear.
func TestClientCredentialsTokenIsCachedAndSentAsBearer(t *testing.T) {
	app, ts := newApp(t, "client_credentials")
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	app.Now = func() time.Time { return now }
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if h, err := app.Header(ctx); err != nil || h != "Bearer at-1" {
			t.Fatalf("Header = %q, %v", h, err)
		}
	}
	if len(ts.forms) != 1 || ts.forms[0].Get("grant_type") != "client_credentials" || ts.forms[0].Get("scope") != "read,write" || ts.basic[0] != "cid:csecret" {
		t.Fatalf("pedidos de token = %v %v", ts.forms, ts.basic)
	}
	tok := readToken(t, app.TokenFile)
	if tok.AccessToken != "at-1" || tok.Grant != "client_credentials" || !tok.AuthorizedAt.Equal(now) {
		t.Errorf("token gravado = %+v", tok)
	}

	now = now.Add(24 * time.Hour) // venceu: pede outro e mantém a data da autorização
	if h, _ := app.Header(ctx); h != "Bearer at-2" {
		t.Fatalf("depois de vencer, Header = %q", h)
	}
	if tok := readToken(t, app.TokenFile); !tok.AuthorizedAt.Equal(now.Add(-24 * time.Hour)) {
		t.Errorf("authorized_at mudou: %v", tok.AuthorizedAt)
	}

	lin := lineartest.New()
	lsrv := httptest.NewServer(lin)
	defer lsrv.Close()
	c := linear.NewClientAuth(lsrv.URL, app, time.Time{})
	if _, err := c.Viewer(ctx); err != nil {
		t.Fatal(err)
	}
	if lin.Auth[0] != "Bearer at-2" {
		t.Errorf("Authorization ao Linear = %q", lin.Auth[0])
	}
}

// authorization_code: sem token, pede para autorizar; com token vencido, renova pelo refresh
// token e grava o novo (o Linear gira o refresh token a cada uso).
func TestRefreshTokenRotatesAndIsSavedBeforeUse(t *testing.T) {
	app, ts := newApp(t, "authorization_code")
	ctx := context.Background()
	if _, err := app.Header(ctx); err == nil || !strings.Contains(err.Error(), "sentinela linear auth") {
		t.Fatalf("sem token, erro = %v", err)
	}
	authorized := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	old, _ := json.Marshal(linear.Token{AccessToken: "velho", RefreshToken: "rt-0", ExpiresAt: time.Now().Add(-time.Hour), Grant: "authorization_code", AuthorizedAt: authorized})
	_ = os.MkdirAll(filepath.Dir(app.TokenFile), 0o700)
	_ = os.WriteFile(app.TokenFile, old, 0o600)

	if h, err := app.Header(ctx); err != nil || h != "Bearer at-1" {
		t.Fatalf("Header = %q, %v", h, err)
	}
	if f := ts.forms[0]; f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "rt-0" {
		t.Fatalf("form = %v", f)
	}
	tok := readToken(t, app.TokenFile)
	if tok.RefreshToken != "rt-1" || tok.AccessToken != "at-1" || !tok.AuthorizedAt.Equal(authorized) {
		t.Errorf("token gravado = %+v", tok)
	}
}

// 401 do Linear com o token do app: renova e tenta de novo uma vez.
func TestUnauthorizedRenewsTheAppTokenOnce(t *testing.T) {
	app, ts := newApp(t, "client_credentials")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "Bearer at-1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"errors":[{"message":"Authentication required"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"id":"u","name":"Sentinela","app":true}}}`))
	}))
	defer srv.Close()
	v, err := linear.NewClientAuth(srv.URL, app, time.Time{}).Viewer(context.Background())
	if err != nil || !v.App || calls != 2 || ts.n != 2 {
		t.Fatalf("viewer = %+v, %v; chamadas %d, tokens %d", v, err, calls, ts.n)
	}
}

// O fluxo no navegador: link com actor=app, state e PKCE; o callback local recebe o código, que é
// trocado com o code_verifier certo.
func TestAuthorizeRunsTheLocalCallbackWithActorAppAndPKCE(t *testing.T) {
	app, ts := newApp(t, "authorization_code")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	app.RedirectURI = fmt.Sprintf("http://localhost:%d/oauth/linear/callback", port)

	var link string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = app.Authorize(ctx, func(l string) {
		link = l
		u, _ := url.Parse(l)
		q := u.Query()
		go func() {
			cb := fmt.Sprintf("http://127.0.0.1:%d/oauth/linear/callback?code=c0de&state=%s", port, url.QueryEscape(q.Get("state")))
			resp, err := http.Get(cb) //nolint:gosec,noctx // callback local do teste
			if err == nil {
				_ = resp.Body.Close()
			}
		}()
	})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	q := u.Query()
	if u.Scheme+"://"+u.Host+u.Path != linear.AuthorizeURL || q.Get("actor") != "app" || q.Get("client_id") != "cid" ||
		q.Get("redirect_uri") != app.RedirectURI || q.Get("scope") != "read,write" || q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" {
		t.Fatalf("link = %s", link)
	}
	f := ts.forms[0]
	sum := sha256.Sum256([]byte(f.Get("code_verifier")))
	if f.Get("grant_type") != "authorization_code" || f.Get("code") != "c0de" || f.Get("redirect_uri") != app.RedirectURI ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Fatalf("troca do código = %v", f)
	}
	if tok := readToken(t, app.TokenFile); tok.AccessToken != "at-1" || tok.RefreshToken != "rt-1" {
		t.Errorf("token = %+v", tok)
	}
}

// Com o app, o Sentinela é reconhecido pelo autor (o usuário autenticado), não pelo prefixo: um
// comentário humano que começa com "**Sentinela" depois da autorização é humano; os antigos, da
// época da chave pessoal, continuam do Sentinela; outro app não é humano.
func TestAppIdentifiesItsOwnCommentsByAuthor(t *testing.T) {
	lin := lineartest.New()
	lin.AppActor = true
	srv := httptest.NewServer(lin)
	defer srv.Close()
	ctx := context.Background()
	cutover := time.Now().Add(-time.Hour)
	c := linear.NewClientAuth(srv.URL, fakeApp{}, cutover)
	is, err := c.CreateIssue(ctx, linear.IssueInput{TeamID: "team-ex", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	lin.CommentAt(is.Identifier, "Lead", "**Sentinela · execução #1**: antigo, pela chave pessoal.", cutover.Add(-time.Hour))
	lin.Comment(is.Identifier, "Lead", "**Sentinela** é o nome do projeto; podem mexer só no handler.")
	if err := c.Comment(ctx, is.ID, "Execução #2: PR aberto (sem prefixo)."); err != nil {
		t.Fatal(err)
	}
	d, err := c.Detail(ctx, is.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, cm := range d.Comments.Nodes {
		got = append(got, fmt.Sprintf("%s:%v:%v", cm.User.Name, cm.BySentinela, cm.Human()))
	}
	want := "Lead:true:false Lead:false:true Sentinela:true:false"
	if strings.Join(got, " ") != want {
		t.Errorf("comentários = %v, quer %s", got, want)
	}

	// Com a chave pessoal, o autor é a pessoa: só o prefixo separa o Sentinela.
	lin.AppActor = false
	d, _ = linear.NewClient(srv.URL, "k").Detail(ctx, is.ID)
	if d.Comments.Nodes[1].Human() {
		t.Error("com a chave pessoal, o prefixo ainda marca o comentário como do Sentinela")
	}
}

type fakeApp struct{}

func (fakeApp) Header(context.Context) (string, error) { return "Bearer t", nil }
func (fakeApp) Invalidate()                            {}
func (fakeApp) AppActor() bool                         { return true }

// Aceite do roadmap: um cancelamento humano sem comentário, depois de um comentário do app, fica
// com motivo vazio; o comentário do app não vira o motivo da decisão, mesmo sem o prefixo.
func TestHumanCancelAfterAppCommentHasEmptyReason(t *testing.T) {
	lin := lineartest.New()
	lin.AppActor = true
	srv := httptest.NewServer(lin)
	defer srv.Close()
	ctx := context.Background()
	c := linear.NewClientAuth(srv.URL, fakeApp{}, time.Time{})
	is, err := c.CreateIssue(ctx, linear.IssueInput{TeamID: "team-ex", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Comment(ctx, is.ID, "Execução #3 terminou sem PR."); err != nil {
		t.Fatal(err)
	}
	lin.Cancel(is.Identifier, "Lead", "")
	cl, err := c.Closure(ctx, is.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cl.Reason != "" || cl.Actor != "Lead" {
		t.Errorf("closure = motivo %q, ator %q", cl.Reason, cl.Actor)
	}
}
