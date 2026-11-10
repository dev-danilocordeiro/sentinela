package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// Router escolhe o canal de cada aviso: o resumo da rodada vai para defaults.notify.slack;
// cada issue (e, na Fase 4, cada PR) vai para teams.<time>.notify.slack do time dono, ou para
// o canal padrão quando o time não tem canal. Falha de postagem nunca derruba a rodada: vira
// aviso, e o canal padrão recebe uma mensagem dizendo qual canal falhou e por quê.
type Router struct {
	Notifier Notifier
	Cfg      *config.Config
}

// DefaultChannel é o canal do resumo da rodada.
func (r *Router) DefaultChannel() string { return r.Cfg.Defaults.Notify.Slack }

// TeamChannel devolve o canal do time e se ele é o próprio do time (false = caiu no padrão).
func (r *Router) TeamChannel(team string) (string, bool) {
	if t := r.Cfg.Teams[team]; t != nil && t.Notify.Slack != "" {
		return t.Notify.Slack, true
	}
	return r.DefaultChannel(), false
}

// Summary posta no canal padrão. Devolve um aviso legível em caso de falha.
func (r *Router) Summary(ctx context.Context, text string) []string {
	if r == nil || r.Notifier == nil || r.DefaultChannel() == "" {
		return nil
	}
	if err := r.Notifier.Post(ctx, r.DefaultChannel(), text); err != nil {
		return []string{fmt.Sprintf("aviso no Slack (%s) falhou: %v", r.DefaultChannel(), err)}
	}
	return nil
}

// Team posta no canal do time, com a menção configurada em notify.mention. Se o canal do
// time falhar, avisa no canal padrão e devolve os avisos.
func (r *Router) Team(ctx context.Context, team, text string) []string {
	if r == nil || r.Notifier == nil {
		return nil
	}
	channel, own := r.TeamChannel(team)
	if channel == "" {
		return nil
	}
	if t := r.Cfg.Teams[team]; t != nil && t.Notify.Mention != "" {
		text = Mention(t.Notify.Mention) + " " + text
	}
	err := r.Notifier.Post(ctx, channel, text)
	if err == nil {
		return nil
	}
	warns := []string{fmt.Sprintf("aviso do time %s no Slack (%s) falhou: %v", team, channel, err)}
	if own && r.DefaultChannel() != "" && r.DefaultChannel() != channel {
		msg := fmt.Sprintf("Não consegui postar no canal %s do time %s (%s). A mensagem era:\n%s", channel, team, Escape(err.Error()), text)
		if err2 := r.Notifier.Post(ctx, r.DefaultChannel(), msg); err2 != nil {
			warns = append(warns, fmt.Sprintf("aviso de falha no canal padrão (%s) também falhou: %v", r.DefaultChannel(), err2))
		}
	}
	return warns
}

// Mention formata notify.mention para o mrkdwn do Slack: ID de user group (S...) vira
// <!subteam^S...>, ID de usuário (U.../W...) vira <@U...>, @here/@channel viram os especiais.
// Um handle (@grupo) sai como texto: o Slack só notifica grupos mencionados por ID.
func Mention(m string) string {
	m = strings.TrimSpace(m)
	switch {
	case m == "@here" || m == "@channel":
		return "<!" + strings.TrimPrefix(m, "@") + ">"
	case isSlackID(m, 'S'):
		return "<!subteam^" + m + ">"
	case isSlackID(m, 'U') || isSlackID(m, 'W'):
		return "<@" + m + ">"
	}
	return Escape(m)
}

// MentionIsID diz se a menção notifica de fato (ID ou especial), para o config validate.
func MentionIsID(m string) bool { return strings.HasPrefix(Mention(m), "<") }

func isSlackID(s string, prefix byte) bool {
	if len(s) < 9 || s[0] != prefix {
		return false
	}
	for _, r := range s {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
