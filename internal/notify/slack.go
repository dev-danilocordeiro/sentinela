// Package notify avisa times e o canal do Sentinela no Slack.
//
// Métodos da Web API (https://docs.slack.dev/reference/methods): chat.postMessage (escopo
// chat:write, bot membro do canal), conversations.list (channels:read; groups:read para
// privados) e conversations.info. Token de bot no header Authorization: Bearer; a resposta
// traz ok/error com HTTP 200 (ex.: not_in_channel, channel_not_found).
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Notifier envia uma mensagem de texto (mrkdwn do Slack) para um canal.
type Notifier interface {
	Post(ctx context.Context, channel, text string) error
}

const DefaultSlackURL = "https://slack.com/api"

// Slack implementa Notifier com um token de bot.
type Slack struct {
	Token   string
	BaseURL string
	HTTP    *http.Client

	mu    sync.Mutex
	names map[string]string // nome do canal -> ID
}

// ErrNotConfigured indica que não há token: quem chama registra e segue sem aviso.
var ErrNotConfigured = errors.New("slack não configurado (SLACK_BOT_TOKEN vazio)")

func (s *Slack) Post(ctx context.Context, channel, text string) error {
	id, err := s.ChannelID(ctx, channel)
	if err != nil {
		return err
	}
	return s.call(ctx, "chat.postMessage", map[string]any{"channel": id, "text": text, "unfurl_links": false, "unfurl_media": false}, nil)
}

// IsChannelID diz se ref já é um ID de conversa do Slack (C público, G privado legado).
func IsChannelID(ref string) bool {
	if len(ref) < 9 || (ref[0] != 'C' && ref[0] != 'G') {
		return false
	}
	for _, r := range ref {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// ChannelID aceita um ID (C...) ou um nome (#canal). Nomes são resolvidos uma vez por
// conversations.list (escopo channels:read, e groups:read para privados) e ficam em cache.
func (s *Slack) ChannelID(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if IsChannelID(ref) {
		return ref, nil
	}
	name := strings.TrimPrefix(ref, "#")
	if name == "" {
		return "", errors.New("slack: canal vazio")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.names[name]; ok {
		return id, nil
	}
	if s.names == nil {
		s.names = map[string]string{}
	}
	cursor := ""
	for {
		var out struct {
			Channels []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"channels"`
			Meta struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		params := map[string]any{"types": "public_channel,private_channel", "exclude_archived": true, "limit": 200}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := s.call(ctx, "conversations.list", params, &out); err != nil {
			return "", err
		}
		for _, c := range out.Channels {
			s.names[c.Name] = c.ID
		}
		if id, ok := s.names[name]; ok {
			return id, nil
		}
		if cursor = out.Meta.NextCursor; cursor == "" {
			return "", fmt.Errorf("slack: channel_not_found (%s)", ref)
		}
	}
}

// ChannelInfo é o que o config validate checa de um canal.
type ChannelInfo struct {
	ID         string
	Name       string
	IsMember   bool
	IsArchived bool
}

// Channel lê um canal (conversations.info) a partir de ID ou nome.
func (s *Slack) Channel(ctx context.Context, ref string) (ChannelInfo, error) {
	id, err := s.ChannelID(ctx, ref)
	if err != nil {
		return ChannelInfo{}, err
	}
	var out struct {
		Channel struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			IsMember   bool   `json:"is_member"`
			IsArchived bool   `json:"is_archived"`
		} `json:"channel"`
	}
	if err := s.call(ctx, "conversations.info", map[string]any{"channel": id}, &out); err != nil {
		return ChannelInfo{}, err
	}
	c := out.Channel
	return ChannelInfo{ID: c.ID, Name: c.Name, IsMember: c.IsMember, IsArchived: c.IsArchived}, nil
}

// call faz um POST JSON num método da Web API e decodifica a resposta em out.
func (s *Slack) call(ctx context.Context, method string, params map[string]any, out any) error {
	if s.Token == "" {
		return ErrNotConfigured
	}
	base := s.BaseURL
	if base == "" {
		base = DefaultSlackURL
	}
	hc := s.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	// Form-encoded: todos os métodos aceitam; os de leitura (conversations.*) recusam JSON
	// com invalid_arguments.
	form := url.Values{}
	for k, v := range params {
		form.Set(k, fmt.Sprint(v))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("slack %s: lendo resposta: %w", method, err)
	}
	var envelope struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("slack %s: HTTP %d, resposta inválida", method, resp.StatusCode)
	}
	if !envelope.OK {
		return fmt.Errorf("slack %s: %s", method, envelope.Error)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Escape protege texto livre para o mrkdwn do Slack (&, < e > têm significado especial).
func Escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// Link monta um link do mrkdwn.
func Link(url, text string) string {
	return "<" + url + "|" + Escape(text) + ">"
}
