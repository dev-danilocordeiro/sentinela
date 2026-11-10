package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Quem pode disparar a correção com a label de gatilho (ADR 0042).
const (
	AllowAnyone      = "anyone"       // qualquer pessoa (o comportamento de antes)
	AllowTeamMembers = "team_members" // só membros do time do Linear dono do achado
)

// FixTrigger diz quem pode pedir a correção aplicando a label de gatilho, e de quem os comentários
// entram como "Instruções da equipe". É política da organização: fica no sentinela.yaml principal
// (fix_trigger, sobrescrito por teams.<time>.fix_trigger), nunca no arquivo do time.
type FixTrigger struct {
	// Allowed é anyone, team_members ou uma lista com usuários do Linear (e-mail ou nome) e,
	// opcionalmente, team_members: [team_members, lider@acme.com] aceita os membros e a líder.
	Allowed AllowedList `yaml:"allowed,omitempty"`
}

// AllowedList aceita uma palavra (anyone, team_members) ou uma lista.
type AllowedList []string

// UnmarshalYAML aceita string ou lista de strings.
func (a *AllowedList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*a = AllowedList{n.Value}
		return nil
	case yaml.SequenceNode:
		var l []string
		if err := n.Decode(&l); err != nil {
			return err
		}
		*a = l
		return nil
	}
	return fmt.Errorf("fix_trigger.allowed: use anyone, team_members ou uma lista de usuários do Linear (linha %d)", n.Line)
}

// Anyone diz se qualquer pessoa pode disparar (também quando nada foi configurado).
func (a AllowedList) Anyone() bool {
	return len(a) == 0 || (len(a) == 1 && a[0] == AllowAnyone)
}

// TeamMembers diz se os membros do time dono podem disparar.
func (a AllowedList) TeamMembers() bool {
	for _, x := range a {
		if x == AllowTeamMembers {
			return true
		}
	}
	return false
}

// Users são os usuários do Linear citados (e-mail ou nome).
func (a AllowedList) Users() []string {
	var out []string
	for _, x := range a {
		if x != AllowTeamMembers && x != AllowAnyone {
			out = append(out, x)
		}
	}
	return out
}

func (a AllowedList) String() string {
	if a.Anyone() {
		return AllowAnyone
	}
	return strings.Join(a, ", ")
}

func (a AllowedList) validate() error {
	for _, x := range a {
		if x == AllowAnyone && len(a) > 1 {
			return fmt.Errorf("fix_trigger.allowed: anyone não se combina com outros valores")
		}
		if strings.TrimSpace(x) == "" {
			return fmt.Errorf("fix_trigger.allowed: valor vazio")
		}
	}
	return nil
}

// FixTriggerFor devolve quem pode disparar a correção no time e de onde veio.
func (c *Config) FixTriggerFor(team string) (AllowedList, string) {
	if t := c.Teams[team]; t != nil && t.FixTrigger != nil && len(t.FixTrigger.Allowed) > 0 {
		return t.FixTrigger.Allowed, "teams." + team + ".fix_trigger.allowed"
	}
	if len(c.FixTrigger.Allowed) > 0 {
		return c.FixTrigger.Allowed, "fix_trigger.allowed"
	}
	return nil, "padrão (anyone; fix_trigger não definido)"
}
