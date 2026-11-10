package config

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Perfis de modelos (ADR 0051). O sentinela.yaml continua com o bloco models explícito; o perfil
// é o ponto de partida que o init e o config bootstrap escrevem nele, e o `config models` mostra
// a que perfil cada papel corresponde.
const (
	ProfileQuality = "qualidade"
	ProfileEconomy = "economia"
	ProfileCustom  = "personalizado"
	// DefaultProfile é o perfil de quem instala do zero.
	DefaultProfile = ProfileQuality
)

// ModelProfile é um conjunto de modelos por papel e os ajustes da descoberta que andam com ele.
type ModelProfile struct {
	Name            string
	Roles           ModelRoles
	RefusalFallback string
	// JudgeVotes > 1 liga a votação do Judge perto do limiar (discovery.judge_votes).
	JudgeVotes int
	// Tetos de contexto por chamada (discovery.triage_context_tokens e analysis_context_tokens).
	// Com o Triager em Haiku, o da triagem fica abaixo de 100k, onde o preço do Haiku muda de faixa.
	TriageContextTokens, AnalysisContextTokens int
	// ClaudeCodeConcurrency e ClaudeCodeTimeout: chamadas claude -p em paralelo e o teto de cada
	// uma (llm.claude_code).
	ClaudeCodeConcurrency int
	ClaudeCodeTimeout     time.Duration
	// BudgetPerRunUSD é o teto da rodada (budget.per_run_usd; só vale com anthropic_api).
	BudgetPerRunUSD float64
}

const (
	opus   = "claude-opus-5-5"
	sonnet = "claude-sonnet-5-5"
	haiku  = "claude-haiku-5-5"
)

// profiles: qualidade põe modelo forte onde há julgamento que não se recupera depois (triagem,
// análise, arquitetura, revisão de segurança e aprovação); economia é o conjunto anterior, para
// quem roda com orçamento ou cota limitados.
var profiles = map[string]ModelProfile{
	ProfileQuality: {
		Name: ProfileQuality,
		Roles: ModelRoles{
			Triage: opus, Analysis: opus, Architect: opus, Judge: sonnet,
			Curator: sonnet, Notes: sonnet, Orchestrator: sonnet,
			SpecWriter: sonnet, Executor: sonnet,
			ReviewerSecurity: opus, ReviewerCode: sonnet, ReviewerReliability: sonnet, ReviewerDomain: sonnet,
			Approver: opus, Shepherd: sonnet,
		},
		// Um modelo diferente do principal: a recusa do Opus tenta o Sonnet.
		RefusalFallback: sonnet,
		JudgeVotes:      3,
		// O Opus não tem a faixa de 100k do Haiku e tem janela de 1M: mais contexto dos vizinhos
		// na triagem e na análise. Chamadas mais longas: uma a mais em paralelo e teto maior.
		TriageContextTokens: 150_000, AnalysisContextTokens: 300_000,
		ClaudeCodeConcurrency: 3, ClaudeCodeTimeout: 15 * time.Minute,
		BudgetPerRunUSD: 10,
	},
	ProfileEconomy: {
		Name: ProfileEconomy,
		Roles: ModelRoles{
			Triage: haiku, Analysis: sonnet, Architect: sonnet, Judge: sonnet,
			Curator: haiku, Notes: sonnet, Orchestrator: sonnet,
			SpecWriter: sonnet, Executor: opus,
			ReviewerSecurity: opus, ReviewerCode: sonnet, ReviewerReliability: sonnet, ReviewerDomain: sonnet,
			Approver: opus, Shepherd: sonnet,
		},
		RefusalFallback:     opus,
		JudgeVotes:          1,
		TriageContextTokens: 90_000, AnalysisContextTokens: 150_000,
		ClaudeCodeConcurrency: 2, ClaudeCodeTimeout: 10 * time.Minute,
		BudgetPerRunUSD: 5,
	},
}

// Profile devolve o perfil pelo nome. Personalizado parte do qualidade: o bloco sai completo para
// a pessoa trocar o que quiser.
func Profile(name string) (ModelProfile, error) {
	if name == ProfileCustom {
		p := profiles[ProfileQuality]
		p.Name = ProfileCustom
		return p, nil
	}
	p, ok := profiles[name]
	if !ok {
		return ModelProfile{}, fmt.Errorf("perfil de modelos %q não existe: use %s, %s ou %s", name, ProfileQuality, ProfileEconomy, ProfileCustom)
	}
	return p, nil
}

// ModelsYAML é o bloco models completo do perfil, com o perfil de origem num comentário.
func (p ModelProfile) ModelsYAML() string {
	var b strings.Builder
	switch p.Name {
	case ProfileCustom:
		b.WriteString("# Modelos: perfil personalizado, a partir do qualidade; troque cada papel como quiser (docs/custos.md).\n")
	case ProfileEconomy:
		b.WriteString("# Modelos: perfil economia, para orçamento ou cota limitados (docs/custos.md). O padrão é qualidade.\n")
	default:
		b.WriteString("# Modelos: perfil qualidade (padrão): modelo forte onde há julgamento que não se recupera depois.\n" +
			"# Para orçamento ou cota limitados, gere com --models economia (docs/custos.md).\n")
	}
	b.WriteString("models:\n")
	for _, r := range p.Roles.roles() {
		fmt.Fprintf(&b, "  %s: %s\n", r.key, r.id)
	}
	fmt.Fprintf(&b, "  refusal:\n    action: fallback_model\n    fallback_model: %s\n", p.RefusalFallback)
	return b.String()
}

// Apply escreve o perfil na config: papéis, fallback de recusa, votos do Judge, tetos de
// contexto, concorrência e teto da rodada.
func (p ModelProfile) Apply(c *Config) {
	c.Models.ModelRoles = p.Roles
	c.Models.Refusal = RefusalPolicy{Action: RefusalFallbackModel, FallbackModel: p.RefusalFallback}
	c.Discovery.JudgeVotes = p.JudgeVotes
	c.Discovery.TriageContextTokens, c.Discovery.AnalysisContextTokens = p.TriageContextTokens, p.AnalysisContextTokens
	c.LLM.ClaudeCode.Concurrency, c.LLM.ClaudeCode.Timeout = p.ClaudeCodeConcurrency, p.ClaudeCodeTimeout
	c.Budget.PerRunUSD = p.BudgetPerRunUSD
}

// haikuLongContextLimit é onde o preço do Haiku muda de faixa (tokens de prompt).
const haikuLongContextLimit = 100_000

// IsHaiku diz se o ID é de um Haiku, o único com preço maior acima de 100k tokens de prompt.
func IsHaiku(id string) bool { return strings.HasPrefix(id, "claude-haiku") }

// triageUsesHaiku diz se a triagem roda em Haiku em algum lugar: no models global, num repo, num
// time ou num repo com um time.
func (c *Config) triageUsesHaiku() bool {
	teams := []string{""}
	for t := range c.Teams {
		teams = append(teams, t)
	}
	repos := []string{""}
	for _, r := range c.Repos {
		repos = append(repos, r.Name)
	}
	for _, r := range repos {
		for _, t := range teams {
			if IsHaiku(c.ModelsFor(r, t).Triage) {
				return true
			}
		}
	}
	return false
}

// ProfilesOf diz a que perfis o modelo de um papel corresponde (qualidade, economia, os dois ou
// nenhum), para o `config models`.
func ProfilesOf(role, model string) []string {
	var out []string
	for name, p := range profiles {
		for _, r := range p.Roles.explained() {
			if r.key == role && r.id == model {
				out = append(out, name)
			}
		}
		if role == "refusal_fallback" && p.RefusalFallback == model {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ModelsProfile diz de que perfil é o bloco models global: qualidade ou economia, se todos os
// papéis batem com ele, ou personalizado.
func (c *Config) ModelsProfile() string {
	for _, name := range []string{ProfileQuality, ProfileEconomy} {
		if c.Models.sameRoles(profiles[name].Roles) {
			return name
		}
	}
	return ProfileCustom
}

func (m ModelRoles) sameRoles(o ModelRoles) bool {
	a, b := m.roles(), o.roles()
	for i := range a {
		if a[i].id != b[i].id {
			return false
		}
	}
	return true
}
