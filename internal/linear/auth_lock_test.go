package linear_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/linear"
)

// C6 (revisão 2026-10-11): serve e worker são processos diferentes, cada um com o token na
// memória. Quando o token vence, os dois renovam ao mesmo tempo com o mesmo refresh token. Com a
// trava entre processos (flock no arquivo do token) e a releitura depois da trava, o Linear vê uma
// renovação só, e os dois usam o mesmo token novo.
func TestTwoProcessesRenewingAtOnceMakeASingleRefresh(t *testing.T) {
	first, ts := newApp(t, "authorization_code")
	ctx := context.Background()
	old, _ := json.Marshal(linear.Token{AccessToken: "velho", RefreshToken: "rt-0", ExpiresAt: time.Now().Add(time.Hour), Grant: "authorization_code"})
	_ = os.MkdirAll(filepath.Dir(first.TokenFile), 0o700)
	_ = os.WriteFile(first.TokenFile, old, 0o600)
	second := &linear.AppAuth{ClientID: first.ClientID, ClientSecret: first.ClientSecret, Grant: first.Grant, Scopes: first.Scopes, TokenFile: first.TokenFile, TokenURL: first.TokenURL}

	// Os dois "processos" sobem com o token válido na memória.
	for _, a := range []*linear.AppAuth{first, second} {
		if h, err := a.Header(ctx); err != nil || h != "Bearer velho" {
			t.Fatalf("Header = %q, %v", h, err)
		}
	}
	// O tempo passa: o token fica a menos de 5 minutos de vencer para os dois.
	later := time.Now().Add(58 * time.Minute)
	first.Now, second.Now = func() time.Time { return later }, func() time.Time { return later }

	var wg sync.WaitGroup
	got := make([]string, 2)
	errs := make([]error, 2)
	for i, a := range []*linear.AppAuth{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = a.Header(ctx)
		}()
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("processo %d: %v", i, errs[i])
		}
	}
	ts.mu.Lock()
	refreshes := len(ts.forms)
	ts.mu.Unlock()
	if refreshes != 1 || got[0] != got[1] || got[0] != "Bearer at-1" {
		t.Errorf("renovações no Linear = %d (quer 1); headers %q e %q (quer os dois Bearer at-1)", refreshes, got[0], got[1])
	}
	if tok := readToken(t, first.TokenFile); tok.RefreshToken != "rt-1" {
		t.Errorf("arquivo com refresh token %q, quer rt-1", tok.RefreshToken)
	}
}
