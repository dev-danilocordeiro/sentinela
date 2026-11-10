package linear

// Webhooks do Linear: https://linear.app/developers/webhooks (conferido em 2026-10-10).
// Headers Linear-Signature (HMAC-SHA256 em hex do corpo cru, com o segredo do webhook),
// Linear-Delivery (UUID do envio) e Linear-Event (tipo da entidade). O corpo traz
// webhookTimestamp em milissegundos; a recomendação é aceitar no máximo 1 minuto de diferença.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// WebhookHandler recebe eventos do Linear, valida a assinatura, deduplica por
// Linear-Delivery e, para eventos de Issue, relê a issue na API e aplica o estado.
// Reler na API em vez de confiar no payload deixa polling e webhook com o mesmo caminho.
type WebhookHandler struct {
	Secret string
	Syncer *Syncer
	Now    func() time.Time
	Log    *slog.Logger
}

// MaxWebhookSkew é a diferença máxima aceita entre webhookTimestamp e o relógio local.
const MaxWebhookSkew = time.Minute

func (h *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	if h.Secret == "" {
		http.Error(w, "webhook sem segredo configurado", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "corpo ilegível", http.StatusBadRequest)
		return
	}
	if !ValidSignature(h.Secret, body, r.Header.Get("Linear-Signature")) {
		http.Error(w, "assinatura inválida", http.StatusUnauthorized)
		return
	}
	var ev struct {
		Action           string `json:"action"`
		Type             string `json:"type"`
		WebhookTimestamp int64  `json:"webhookTimestamp"`
		Data             struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "json inválido", http.StatusBadRequest)
		return
	}
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	if d := now().Sub(time.UnixMilli(ev.WebhookTimestamp)); d > MaxWebhookSkew || d < -MaxWebhookSkew {
		http.Error(w, "webhookTimestamp fora da janela", http.StatusUnauthorized)
		return
	}
	delivery := r.Header.Get("Linear-Delivery")
	if delivery == "" {
		http.Error(w, "Linear-Delivery ausente", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	// O evento só fica processado depois de aplicado: uma entrega que falhou (500) é aplicada de
	// novo na reentrega do Linear, com o mesmo Linear-Delivery; uma já aplicada responde 200.
	ev2 := syncEvent{id: delivery, typ: ev.Type + "." + ev.Action, payload: body}
	var issues []Issue
	if ev.Type == "Issue" && ev.Action != "remove" && ev.Data.ID != "" {
		done, err := h.Syncer.Store.Q.WebhookEventProcessed(ctx, db.WebhookEventProcessedParams{Provider: "linear", EventID: delivery})
		if err != nil {
			h.fail(w, "lendo evento", err)
			return
		}
		if done {
			w.WriteHeader(http.StatusOK) // reentrega: já processado
			return
		}
		if issues, err = h.Syncer.Linear.Issues(ctx, []string{ev.Data.ID}); err != nil {
			h.fail(w, "relendo issue", err)
			return
		}
	}
	changes, _, err := h.Syncer.applyEvent(ctx, ev2, issues)
	if err != nil {
		h.fail(w, "aplicando estado", err)
		return
	}
	for _, ch := range changes {
		if h.Log != nil {
			h.Log.Info("issue sincronizada", "issue", ch.Identifier, "state", ch.State, "outcome", ch.Outcome)
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (h *WebhookHandler) fail(w http.ResponseWriter, what string, err error) {
	if h.Log != nil {
		h.Log.Error("webhook do Linear", "etapa", what, "err", err)
	}
	// 500 faz o Linear reenviar; o evento já gravado sem processed_at fica para o polling.
	http.Error(w, what, http.StatusInternalServerError)
}

// ValidSignature confere o HMAC-SHA256 (hex) do corpo cru em tempo constante.
func ValidSignature(secret string, body []byte, signature string) bool {
	got, err := hex.DecodeString(signature)
	if err != nil || len(got) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// ErrNoSecret indica modo webhook sem LINEAR_WEBHOOK_SECRET.
var ErrNoSecret = errors.New("LINEAR_WEBHOOK_SECRET não definida (obrigatória no modo webhook)")
