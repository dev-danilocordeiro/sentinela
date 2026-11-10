package executor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// GitHubWebhook recebe eventos do GitHub (pull_request, pull_request_review, check_suite,
// check_run): valida X-Hub-Signature-256, deduplica por X-GitHub-Delivery e dispara o mesmo
// acompanhamento do polling. Docs: https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries
type GitHubWebhook struct {
	Secret string
	Store  *store.Store
	// OnEvent roda o acompanhamento (Worker.FollowUp); fica fora do handler para os testes.
	OnEvent func(ctx context.Context, event string) error
}

// interesting são os eventos que mudam algo para o Sentinela.
var interesting = map[string]bool{"pull_request": true, "pull_request_review": true, "check_suite": true, "check_run": true}

func (h *GitHubWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.Secret == "" {
		http.Error(w, "GITHUB_WEBHOOK_SECRET não definida", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 25<<20))
	if err != nil {
		http.Error(w, "corpo ilegível", http.StatusBadRequest)
		return
	}
	if !ValidGitHubSignature(h.Secret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "assinatura inválida", http.StatusUnauthorized)
		return
	}
	event, delivery := r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery")
	if delivery == "" {
		http.Error(w, "sem X-GitHub-Delivery", http.StatusBadRequest)
		return
	}
	id, err := h.Store.Q.InsertWebhookEvent(r.Context(), db.InsertWebhookEventParams{Provider: "github", EventID: delivery, EventType: event, Payload: body})
	if errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(http.StatusOK) // entrega repetida
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if interesting[event] && h.OnEvent != nil {
		if err := h.OnEvent(r.Context(), event); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	_ = h.Store.Q.MarkWebhookEventProcessed(r.Context(), id)
	w.WriteHeader(http.StatusOK)
}

// ValidGitHubSignature confere "sha256=<hex>" do HMAC-SHA256 do corpo bruto.
func ValidGitHubSignature(secret string, body []byte, header string) bool {
	got, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil || len(got) == 0 || !strings.HasPrefix(header, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
