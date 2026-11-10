package github

// GitHub Apps (Fase 4): a app bot (github_apps.bot) abre branches e PRs em draft; a revisora (github_apps.reviewer)
// publica a revisão. A autenticação é em duas etapas: um JWT RS256 assinado com a chave privada
// da app (válido por até 10 minutos) troca-se por um token de instalação (válido por 1 hora).
//
// Endpoints (API version 2022-11-28):
//   - JWT da app:          https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app
//   - GET  /app                                        https://docs.github.com/en/rest/apps/apps#get-the-authenticated-app
//   - GET  /app/installations/{id}                     https://docs.github.com/en/rest/apps/apps#get-an-installation-for-the-authenticated-app
//   - POST /app/installations/{id}/access_tokens       https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app
//   - GET  /installation/repositories                  https://docs.github.com/en/rest/apps/installations#list-repositories-accessible-to-the-app-installation

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// App identifica uma GitHub App e a instalação usada pelo Sentinela.
type App struct {
	ID             int64
	InstallationID int64
	Key            *rsa.PrivateKey
	BaseURL        string
	HTTP           *http.Client
	Now            func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// LoadApp lê a chave privada (PEM, PKCS#1 ou PKCS#8) e monta a app. A chave fica só em memória.
func LoadApp(appID, installationID, keyPath, baseURL string) (*App, error) {
	id, err := strconv.ParseInt(appID, 10, 64)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("app id inválido %q", appID)
	}
	inst, err := strconv.ParseInt(installationID, 10, 64)
	if err != nil || inst <= 0 {
		return nil, fmt.Errorf("installation id inválido %q", installationID)
	}
	data, err := os.ReadFile(keyPath) //nolint:gosec // caminho vem da variável de ambiente do operador
	if err != nil {
		return nil, fmt.Errorf("lendo chave da app: %w", err)
	}
	key, err := ParsePrivateKey(data)
	if err != nil {
		return nil, err
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &App{ID: id, InstallationID: inst, Key: key, BaseURL: baseURL}, nil
}

// ParsePrivateKey aceita o PEM que o GitHub entrega (PKCS#1) e PKCS#8.
func ParsePrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("chave da app: PEM inválido")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("chave da app: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("chave da app: não é RSA")
	}
	return rk, nil
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// JWT gera o token da app: iat 60 s no passado (relógio do GitHub), exp em 9 minutos.
func (a *App) JWT() (string, error) {
	now := a.now()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(a.ID, 10)})
	if err != nil {
		return "", err
	}
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.Key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// AppInfo é o que GET /app devolve que interessa ao validate.
type AppInfo struct {
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	Permissions map[string]string `json:"permissions"`
}

// Installation é a instalação da app: permissões efetivas e seleção de repos.
type Installation struct {
	ID                  int64                  `json:"id"`
	Account             struct{ Login string } `json:"account"`
	Permissions         map[string]string      `json:"permissions"`
	RepositorySelection string                 `json:"repository_selection"`
	SuspendedAt         *time.Time             `json:"suspended_at"`
}

// InstallationToken é o token de instalação e o que ele pode fazer.
type InstallationToken struct {
	Token               string            `json:"token"`
	ExpiresAt           time.Time         `json:"expires_at"`
	Permissions         map[string]string `json:"permissions"`
	RepositorySelection string            `json:"repository_selection"`
}

// Info lê a app autenticada pelo JWT.
func (a *App) Info(ctx context.Context) (AppInfo, error) {
	var out AppInfo
	err := a.appCall(ctx, http.MethodGet, "/app", &out)
	return out, err
}

// Installation lê a instalação configurada.
func (a *App) Installation(ctx context.Context) (Installation, error) {
	var out Installation
	err := a.appCall(ctx, http.MethodGet, "/app/installations/"+strconv.FormatInt(a.InstallationID, 10), &out)
	return out, err
}

// NewToken troca o JWT por um token de instalação novo.
func (a *App) NewToken(ctx context.Context) (InstallationToken, error) {
	var out InstallationToken
	err := a.appCall(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(a.InstallationID, 10)+"/access_tokens", &out)
	return out, err
}

// Token devolve um token de instalação válido, renovando quando faltam menos de 5 minutos.
func (a *App) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && a.now().Add(5*time.Minute).Before(a.expires) {
		return a.token, nil
	}
	t, err := a.NewToken(ctx)
	if err != nil {
		return "", err
	}
	a.token, a.expires = t.Token, t.ExpiresAt
	return a.token, nil
}

// Repositories lista os repos acessíveis à instalação (full_name), com paginação.
func (a *App) Repositories(ctx context.Context) ([]string, error) {
	tok, err := a.Token(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for page := 1; ; page++ {
		var out struct {
			Total        int `json:"total_count"`
			Repositories []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		if err := a.do(ctx, http.MethodGet, "/installation/repositories?per_page=100&page="+strconv.Itoa(page), tok, nil, &out); err != nil {
			return nil, err
		}
		for _, r := range out.Repositories {
			names = append(names, r.FullName)
		}
		if len(out.Repositories) < 100 || len(names) >= out.Total {
			return names, nil
		}
	}
}

func (a *App) appCall(ctx context.Context, method, path string, out any) error {
	jwt, err := a.JWT()
	if err != nil {
		return err
	}
	return a.do(ctx, method, path, jwt, nil, out)
}

// Client devolve um cliente REST autenticado como a instalação.
func (a *App) Client(ctx context.Context) (*Client, error) {
	tok, err := a.Token(ctx)
	if err != nil {
		return nil, err
	}
	return NewClient(a.BaseURL, tok), nil
}

func (a *App) do(ctx context.Context, method, path, bearer string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+bearer)
	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("github app: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("github app: %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(string(data), 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// APIBase é a URL da API usada pela app (para montar clientes com o token de instalação).
func (a *App) APIBase() string { return a.BaseURL }
