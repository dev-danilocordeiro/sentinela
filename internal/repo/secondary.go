package repo

import (
	"path"
	"slices"
	"sort"
)

// Secondary é um time que um achado também afeta: as evidências tocam módulos dele, mas o dono
// continua sendo um só, pela precedência do roteamento (ADR 0044).
type Secondary struct {
	Team    string   `json:"team"`
	Modules []string `json:"modules"` // o módulo de cada caminho: glob da config ou padrão do CODEOWNERS
	Paths   []string `json:"paths"`   // os caminhos (com linha, se houver) que tocam o time
}

// SecondaryTeams resolve cada caminho e devolve os times diferentes do dono, na ordem dos nomes.
// refs são os caminhos como aparecem no achado (ex.: "modules/contas/login.go:12"); files, os
// arquivos correspondentes, para o roteamento. Caminhos ignorados ou sem time ficam de fora.
func SecondaryTeams(resolve func(string) (Resolution, error), owner string, files, refs []string) []Secondary {
	byTeam := map[string]*Secondary{}
	for i, f := range files {
		res, err := resolve(f)
		if err != nil || res.Ignored || res.Team == "" || res.Team == owner {
			continue
		}
		s := byTeam[res.Team]
		if s == nil {
			s = &Secondary{Team: res.Team}
			byTeam[res.Team] = s
		}
		mod := res.Pattern
		if mod == "" {
			mod = path.Dir(f) + "/"
		}
		if !slices.Contains(s.Modules, mod) {
			s.Modules = append(s.Modules, mod)
		}
		ref := f
		if i < len(refs) && refs[i] != "" {
			ref = refs[i]
		}
		if !slices.Contains(s.Paths, ref) {
			s.Paths = append(s.Paths, ref)
		}
	}
	out := make([]Secondary, 0, len(byTeam))
	for _, s := range byTeam {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Team < out[j].Team })
	return out
}
