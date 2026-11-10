package linear

// Identidade do Sentinela no Linear (ADR 0032). Com um app OAuth autorizado com actor=app, as
// mutações (issues, comentários, labels, estados) saem como o app, não como a pessoa dona de uma
// chave pessoal. Fontes, conferidas em 2026-10-11:
//   - https://linear.app/developers/oauth-actor-authorization: "add actor=app parameter to your
//     OAuth authorization URL"; "all mutations such as creating issues, comments, and status
//     changes will be performed by the app"; actor=application foi descontinuado.
//   - https://linear.app/developers/oauth-2-0-authentication: authorize em
//     https://linear.app/oauth/authorize, token em POST https://api.linear.app/oauth/token
//     (form-urlencoded), PKCE S256, access token de 24 h com refresh token que gira a cada uso
//     (30 min de tolerância), client_credentials (habilitado no app) com token de app de ~30 dias
//     e sem refresh token, header "Authorization: Bearer <token>".

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Endpoints do OAuth do Linear (fonte acima).
const (
	AuthorizeURL = "https://linear.app/oauth/authorize"
	TokenURL     = "https://api.linear.app/oauth/token" //nolint:gosec // URL do endpoint, não credencial
)

// Auth dá o valor do header Authorization de cada chamada.
type Auth interface {
	Header(ctx context.Context) (string, error)
	// Invalidate descarta o token em memória depois de um 401, para a próxima chamada renovar.
	Invalidate()
	// AppActor diz se as ações saem como o app (actor=app) e não como uma pessoa.
	AppActor() bool
}

// APIKey é a chave pessoal: vai crua no header, sem "Bearer", e age como a pessoa dona dela.
type APIKey string

func (k APIKey) Header(context.Context) (string, error) {
	if k == "" {
		return "", errors.New("LINEAR_API_KEY não definida")
	}
	return string(k), nil
}
func (APIKey) Invalidate()    {}
func (APIKey) AppActor() bool { return false }

// Token é o que fica no arquivo do token (0600, fora do git).
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope,omitempty"`
	Grant        string    `json:"grant"`
	// AuthorizedAt é quando o app foi autorizado pela primeira vez: comentários com o prefixo
	// "**Sentinela" anteriores a ela são da época da chave pessoal.
	AuthorizedAt time.Time `json:"authorized_at"`
}

func (t *Token) valid(now time.Time) bool {
	return t != nil && t.AccessToken != "" && now.Add(5*time.Minute).Before(t.ExpiresAt)
}

// AppAuth é o app OAuth do Sentinela, com actor=app.
type AppAuth struct {
	ClientID, ClientSecret string
	Grant                  string // authorization_code | client_credentials
	Scopes                 []string
	RedirectURI            string
	TokenFile              string
	TokenURL               string // vazio: TokenURL
	HTTP                   *http.Client
	Now                    func() time.Time

	mu       sync.Mutex
	tok      *Token
	rejected string // access token que o Linear recusou (401); não volta a ser usado
}

func (a *AppAuth) AppActor() bool { return true }

func (a *AppAuth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *AppAuth) Invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tok != nil {
		a.rejected = a.tok.AccessToken // o Linear recusou: não vale, mesmo que o arquivo ainda o tenha
		a.tok.ExpiresAt = time.Time{}
	}
}

// Header devolve "Bearer <token>", lendo o arquivo e renovando quando preciso. O refresh token
// gira a cada renovação: o novo é gravado antes de o access token ser usado.
func (a *AppAuth) Header(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if a.tok == nil {
		t, err := a.load()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		a.tok = t
	}
	if a.tok.valid(now) && a.tok.AccessToken != a.rejected {
		return "Bearer " + a.tok.AccessToken, nil
	}
	// Renovação entre processos (revisão 2026-10-11, C6): serve, worker e scan têm cada um o token
	// na memória. A trava no arquivo garante uma renovação por vez; depois dela, o arquivo é relido,
	// e se outro processo já renovou, vale o token dele (o refresh token antigo já girou).
	unlock, err := lockFile(ctx, a.TokenFile+".lock")
	if err != nil {
		return "", err
	}
	defer unlock()
	if fresh, err := a.load(); err == nil {
		a.tok = fresh
		if fresh.valid(now) && fresh.AccessToken != a.rejected {
			return "Bearer " + fresh.AccessToken, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var t *Token
	switch {
	case a.Grant == "client_credentials":
		t, err = a.request(ctx, url.Values{"grant_type": {"client_credentials"}, "scope": {strings.Join(a.Scopes, ",")}})
	case a.tok != nil && a.tok.RefreshToken != "":
		t, err = a.request(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {a.tok.RefreshToken}})
	default:
		return "", fmt.Errorf("sem token do app Linear em %s: rode `sentinela linear auth` para autorizar o app", a.TokenFile)
	}
	if err != nil {
		return "", err
	}
	if a.tok != nil && !a.tok.AuthorizedAt.IsZero() {
		t.AuthorizedAt = a.tok.AuthorizedAt
	}
	if err := a.save(t); err != nil {
		return "", err
	}
	a.tok = t
	return "Bearer " + t.AccessToken, nil
}

// AuthorizedAt é quando o app foi autorizado (zero se não houver token).
func (a *AppAuth) AuthorizedAt() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tok == nil {
		if t, err := a.load(); err == nil {
			a.tok = t
		}
	}
	if a.tok == nil {
		return time.Time{}
	}
	return a.tok.AuthorizedAt
}

func (a *AppAuth) request(ctx context.Context, form url.Values) (*Token, error) {
	if a.ClientID == "" || a.ClientSecret == "" {
		return nil, errors.New("LINEAR_OAUTH_CLIENT_ID e LINEAR_OAUTH_CLIENT_SECRET são obrigatórias para o app Linear")
	}
	endpoint := a.TokenURL
	if endpoint == "" {
		endpoint = TokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode())) //nolint:gosec // endpoint fixo ou do operador (LINEAR_OAUTH_TOKEN_URL)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(a.ClientID, a.ClientSecret)
	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req) //nolint:gosec // endpoint fixo ou do operador
	if err != nil {
		return nil, fmt.Errorf("linear oauth: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linear oauth (%s): HTTP %d: %s", form.Get("grant_type"), resp.StatusCode, truncate(raw))
	}
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        any    `json:"scope"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.AccessToken == "" {
		return nil, fmt.Errorf("linear oauth: resposta sem access_token: %s", truncate(raw))
	}
	now := a.now()
	t := &Token{AccessToken: body.AccessToken, RefreshToken: body.RefreshToken, ExpiresAt: now.Add(time.Duration(body.ExpiresIn) * time.Second),
		Grant: a.Grant, AuthorizedAt: now}
	switch s := body.Scope.(type) {
	case string:
		t.Scope = s
	case []any:
		parts := make([]string, 0, len(s))
		for _, p := range s {
			parts = append(parts, fmt.Sprint(p))
		}
		t.Scope = strings.Join(parts, ",")
	}
	return t, nil
}

func (a *AppAuth) load() (*Token, error) {
	raw, err := os.ReadFile(a.TokenFile) //nolint:gosec // caminho do sentinela.yaml (linear_auth.token_file)
	if err != nil {
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("token do app Linear inválido em %s: %w", a.TokenFile, err)
	}
	return &t, nil
}

// save grava o token com permissão 0600, trocando o arquivo de uma vez.
func (a *AppAuth) save(t *Token) error {
	if err := os.MkdirAll(filepath.Dir(a.TokenFile), 0o700); err != nil { //nolint:gosec // caminho do sentinela.yaml
		return err
	}
	raw, err := json.MarshalIndent(t, "", "  ") //nolint:gosec // o token vai para o arquivo 0600 dele, de propósito
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(a.TokenFile), ".linear-token-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() //nolint:gosec // temporário criado aqui
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), a.TokenFile) //nolint:gosec // caminho do sentinela.yaml
}

// Fetch pede um token novo pelo client_credentials e o grava.
func (a *AppAuth) Fetch(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.request(ctx, url.Values{"grant_type": {"client_credentials"}, "scope": {strings.Join(a.Scopes, ",")}})
	if err != nil {
		return err
	}
	if prev, err := a.load(); err == nil && !prev.AuthorizedAt.IsZero() {
		t.AuthorizedAt = prev.AuthorizedAt
	}
	if err := a.save(t); err != nil {
		return err
	}
	a.tok = t
	return nil
}

// AuthorizeURL monta o link de autorização com actor=app, state e PKCE S256.
func (a *AppAuth) AuthorizeURL(state, challenge string) string {
	q := url.Values{
		"client_id": {a.ClientID}, "redirect_uri": {a.RedirectURI}, "response_type": {"code"},
		"scope": {strings.Join(a.Scopes, ",")}, "state": {state}, "actor": {"app"}, "prompt": {"consent"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	return AuthorizeURL + "?" + q.Encode()
}

// Authorize roda o authorization_code com actor=app: sobe um servidor temporário no endereço do
// redirect_uri, mostra o link para uma pessoa autorizar o app no navegador, troca o código pelo
// token e o grava. show recebe o link; o servidor fecha ao receber o código ou no fim do ctx.
func (a *AppAuth) Authorize(ctx context.Context, show func(link string)) error {
	if a.ClientID == "" || a.ClientSecret == "" {
		return errors.New("LINEAR_OAUTH_CLIENT_ID e LINEAR_OAUTH_CLIENT_SECRET são obrigatórias para o app Linear")
	}
	ru, err := url.Parse(a.RedirectURI)
	if err != nil || ru.Host == "" {
		return fmt.Errorf("linear_auth.redirect_uri inválido: %q", a.RedirectURI)
	}
	if h := ru.Hostname(); h != "localhost" && h != "127.0.0.1" {
		return fmt.Errorf("linear_auth.redirect_uri precisa ser local (localhost ou 127.0.0.1): %q", a.RedirectURI)
	}
	state, verifier := randomString(24), randomString(48)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", ru.Port()))
	if err != nil {
		return fmt.Errorf("abrindo %s para o callback: %w", ru.Host, err)
	}
	type result struct {
		code string
		err  error
	}
	got := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(ru.Path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		switch {
		case q.Get("state") != state:
			res.err = errors.New("callback com state diferente do pedido")
		case q.Get("error") != "":
			res.err = fmt.Errorf("o Linear recusou a autorização: %s %s", q.Get("error"), q.Get("error_description"))
		case q.Get("code") == "":
			res.err = errors.New("callback sem code")
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "<p>Sentinela: autorização falhou: %s</p>", res.err)
		} else {
			_, _ = io.WriteString(w, "<p>Sentinela: app autorizado. Pode fechar esta aba.</p>")
		}
		select {
		case got <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	show(a.AuthorizeURL(state, challenge))
	var res result
	select {
	case <-ctx.Done():
		return ctx.Err()
	case res = <-got:
	}
	if res.err != nil {
		return res.err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	t, err := a.request(ctx, url.Values{
		"grant_type": {"authorization_code"}, "code": {res.code}, "redirect_uri": {a.RedirectURI}, "code_verifier": {verifier},
	})
	if err != nil {
		return err
	}
	if prev, err := a.load(); err == nil && !prev.AuthorizedAt.IsZero() {
		t.AuthorizedAt = prev.AuthorizedAt // reautorizar não muda o corte do prefixo antigo
	}
	if err := a.save(t); err != nil {
		return err
	}
	a.tok = t
	return nil
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
