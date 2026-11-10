package repo

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Imports de TypeScript e JavaScript que não são relativos: o alias de compilerOptions.paths e o
// baseUrl do tsconfig (ou jsconfig) mais próximo do arquivo, com extends, e os pacotes de
// workspace (npm, yarn e pnpm) pelo nome do package.json. Segue a ordem do tsc: paths, baseUrl e,
// por último, o pacote (que o gerenciador liga em node_modules). Semântica conferida em
// https://www.typescriptlang.org/tsconfig/#paths, #baseUrl e #extends e em
// https://nodejs.org/api/packages.html#package-entry-points (exports). Sem compilar e sem
// node_modules: tudo sai dos manifestos do checkout.

// tsResolver resolve os imports não relativos de um checkout. Guarda o tsconfig por diretório.
type tsResolver struct {
	root       string
	configs    map[string]*tsConfig // diretório do arquivo -> config efetiva (nil: nenhuma)
	workspaces map[string]string    // nome do package.json -> diretório do pacote
}

// tsConfig é o que importa do tsconfig efetivo (já com extends) para resolver imports.
type tsConfig struct {
	baseURL   string // relativo à raiz; "" sem baseUrl
	paths     map[string][]string
	pathsBase string // de onde os alvos de paths partem: o baseUrl ou o diretório do tsconfig que define paths
}

func newTSResolver(root string) *tsResolver {
	r := &tsResolver{root: root, configs: map[string]*tsConfig{}, workspaces: map[string]string{}}
	for _, u := range BuildUnits(root) {
		if !u.JS() {
			continue
		}
		data, err := ReadFile(root, path.Join(u.Dir, "package.json"))
		if err != nil {
			continue
		}
		var pkg struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &pkg) == nil && pkg.Name != "" {
			r.workspaces[pkg.Name] = u.Dir
		}
	}
	return r
}

// resolve devolve o caminho, relativo à raiz, que o import aponta (arquivo sem extensão ou
// diretório), ou "" quando ele não é do repo (dependência externa, node:, URL).
func (r *tsResolver) resolve(file, spec string) string {
	if strings.HasPrefix(spec, ".") {
		return cleanRel(path.Join(path.Dir(file), spec))
	}
	if strings.Contains(spec, ":") {
		return "" // node:fs, https://...
	}
	if cfg := r.config(path.Dir(file)); cfg != nil {
		if p := r.fromPaths(cfg, spec); p != "" {
			return p
		}
		if cfg.baseURL != "" {
			if p := cleanRel(path.Join(cfg.baseURL, spec)); p != "" && r.exists(p) {
				return p
			}
		}
	}
	return r.fromWorkspace(spec)
}

// fromPaths aplica compilerOptions.paths: vence o padrão exato e, entre os com *, o de prefixo
// mais longo; o primeiro alvo que existe no checkout é o escolhido.
func (r *tsResolver) fromPaths(cfg *tsConfig, spec string) string {
	best, star, bestLen := "", "", -1
	for pattern := range cfg.paths {
		prefix, suffix, wild := strings.Cut(pattern, "*")
		if !wild {
			if pattern == spec {
				best, star = pattern, ""
				break
			}
			continue
		}
		if len(spec) >= len(prefix)+len(suffix) && strings.HasPrefix(spec, prefix) && strings.HasSuffix(spec, suffix) && len(prefix) > bestLen {
			best, star, bestLen = pattern, spec[len(prefix):len(spec)-len(suffix)], len(prefix)
		}
	}
	if best == "" {
		return ""
	}
	for _, target := range cfg.paths[best] {
		p := cleanRel(path.Join(cfg.pathsBase, strings.Replace(target, "*", star, 1)))
		if p != "" && r.exists(p) {
			return p
		}
	}
	return ""
}

// fromWorkspace resolve "@escopo/pacote/sub" ou "pacote/sub" pelo nome de um pacote do workspace:
// exports, depois types, main ou module, e a fonte em src/ quando a entrada aponta o build.
func (r *tsResolver) fromWorkspace(spec string) string {
	segs := strings.SplitN(spec, "/", 3)
	name, sub := segs[0], segs[1:]
	if strings.HasPrefix(spec, "@") && len(segs) >= 2 {
		name, sub = segs[0]+"/"+segs[1], segs[2:]
	}
	dir, ok := r.workspaces[name]
	if !ok {
		return ""
	}
	subpath := "."
	if len(sub) > 0 {
		subpath = "./" + sub[0]
	}
	var pkg struct {
		Exports json.RawMessage `json:"exports"`
		Types   string          `json:"types"`
		Typings string          `json:"typings"`
		Main    string          `json:"main"`
		Module  string          `json:"module"`
	}
	if data, err := ReadFile(r.root, path.Join(dir, "package.json")); err == nil {
		_ = json.Unmarshal(data, &pkg)
	}
	var entries []string
	if e := exportTarget(pkg.Exports, subpath); e != "" {
		entries = append(entries, e)
	}
	if subpath == "." {
		for _, e := range []string{pkg.Types, pkg.Typings, pkg.Module, pkg.Main} {
			if e != "" {
				entries = append(entries, e)
			}
		}
		entries = append(entries, "src/index", "index")
	} else {
		entries = append(entries, subpath, "src/"+strings.TrimPrefix(subpath, "./"))
	}
	for _, e := range entries {
		p := cleanRel(path.Join(dir, e))
		if p == "" {
			continue
		}
		if r.exists(p) {
			return p
		}
		// "dist/index.js" é o build; a fonte costuma estar em src/ com o mesmo caminho.
		if first, rest, ok := strings.Cut(strings.TrimPrefix(path.Clean(e), "./"), "/"); ok && buildOutputDirs[first] {
			if src := cleanRel(path.Join(dir, "src", strings.TrimSuffix(rest, path.Ext(rest)))); src != "" && r.exists(src) {
				return src
			}
		}
	}
	if r.exists(path.Join(dir, "src")) {
		return path.Join(dir, "src")
	}
	return dir
}

var buildOutputDirs = map[string]bool{"dist": true, "build": true, "lib": true, "out": true, "esm": true, "cjs": true}

// exportTarget lê a entrada de "exports" para o subcaminho: string, mapa de subcaminhos (com um
// * no padrão), mapa de condições (types, import, default...) ou lista com alternativas.
func exportTarget(raw json.RawMessage, subpath string) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if subpath == "." {
			return s
		}
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return conditionTarget(raw)
	}
	subpaths := false
	for k := range m {
		if strings.HasPrefix(k, ".") {
			subpaths = true
			break
		}
	}
	if !subpaths {
		if subpath == "." {
			return conditionTarget(raw)
		}
		return ""
	}
	if v, ok := m[subpath]; ok {
		return conditionTarget(v)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		prefix, suffix, wild := strings.Cut(k, "*")
		if wild && strings.HasPrefix(subpath, prefix) && strings.HasSuffix(subpath, suffix) {
			if t := conditionTarget(m[k]); t != "" {
				return strings.ReplaceAll(t, "*", subpath[len(prefix):len(subpath)-len(suffix)])
			}
		}
	}
	return ""
}

// conditionTarget escolhe o alvo de um valor de exports, preferindo a fonte de tipos.
func conditionTarget(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		for _, v := range list {
			if t := conditionTarget(v); t != "" {
				return t
			}
		}
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, c := range []string{"types", "source", "import", "default", "require", "node", "browser"} {
		if v, ok := m[c]; ok {
			if t := conditionTarget(v); t != "" {
				return t
			}
		}
	}
	return ""
}

// config devolve o tsconfig efetivo do diretório: o tsconfig.json (ou jsconfig.json) mais
// próximo, subindo até a raiz.
func (r *tsResolver) config(dir string) *tsConfig {
	if c, ok := r.configs[dir]; ok {
		return c
	}
	var c *tsConfig
	for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
		if f := path.Join(dir, name); r.isFile(f) {
			c = r.load(f, 0)
			break
		}
	}
	if c == nil && dir != "." {
		c = r.config(path.Dir(dir))
	}
	r.configs[dir] = c
	return c
}

// merge aplica por cima o que o tem: o filho vence o pai, e paths substitui (não soma).
func (r *tsConfig) merge(o *tsConfig) {
	if o.baseURL != "" {
		r.baseURL = o.baseURL
	}
	if o.paths != nil {
		r.paths, r.pathsBase = o.paths, o.pathsBase
	}
}

// load lê um tsconfig e os que ele estende (string ou lista, como no TS 5): os campos do filho
// vencem; baseUrl é relativo ao arquivo que o define, e paths sem baseUrl também.
func (r *tsResolver) load(file string, depth int) *tsConfig {
	out := &tsConfig{}
	data, err := ReadFile(r.root, file)
	if err != nil || depth > 10 {
		return out
	}
	var raw struct {
		Extends         json.RawMessage `json:"extends"`
		CompilerOptions struct {
			BaseURL *string             `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if json.Unmarshal(stripJSONC(data), &raw) != nil {
		return out
	}
	var parents []string
	var one string
	if json.Unmarshal(raw.Extends, &one) == nil {
		parents = []string{one}
	} else {
		_ = json.Unmarshal(raw.Extends, &parents)
	}
	dir := path.Dir(file)
	for _, p := range parents {
		if f := r.extendsFile(dir, p); f != "" {
			out.merge(r.load(f, depth+1))
		}
	}
	own := &tsConfig{paths: raw.CompilerOptions.Paths, pathsBase: dir}
	if raw.CompilerOptions.BaseURL != nil {
		own.baseURL = cleanRel(path.Join(dir, *raw.CompilerOptions.BaseURL))
	}
	out.merge(own)
	if out.baseURL != "" {
		out.pathsBase = out.baseURL
	}
	return out
}

// extendsFile acha o tsconfig de "extends": caminho relativo (com ou sem .json) ou um pacote do
// workspace (pacote/arquivo.json; sem arquivo, o tsconfig.json dele). Pacote de fora do repo
// (ex.: @tsconfig/node20) fica sem resolver: estaria em node_modules.
func (r *tsResolver) extendsFile(dir, spec string) string {
	var cands []string
	if strings.HasPrefix(spec, ".") {
		base := cleanRel(path.Join(dir, spec))
		cands = []string{base, base + ".json", path.Join(base, "tsconfig.json")}
	} else {
		for name, wsDir := range r.workspaces {
			if spec == name {
				cands = append(cands, path.Join(wsDir, "tsconfig.json"))
			} else if rest, ok := strings.CutPrefix(spec, name+"/"); ok {
				cands = append(cands, path.Join(wsDir, rest), path.Join(wsDir, rest)+".json")
			}
		}
	}
	for _, c := range cands {
		if c != "" && r.isFile(c) {
			return c
		}
	}
	return ""
}

// tsExts são as extensões que o import omite.
var tsExts = []string{".ts", ".tsx", ".d.ts", ".js", ".jsx", ".mjs", ".cjs", ".json"}

// exists diz se o caminho é um arquivo (com ou sem a extensão omitida) ou um diretório do checkout.
func (r *tsResolver) exists(p string) bool {
	if r.isFile(p) || r.isDir(p) {
		return true
	}
	for _, ext := range tsExts {
		if r.isFile(p + ext) {
			return true
		}
	}
	return false
}

// packageOf devolve o diretório (pacote do grafo) do caminho resolvido: o próprio diretório,
// quando o import aponta um (index.ts), e o diretório do arquivo nos outros casos.
func (r *tsResolver) packageOf(p string) string {
	if r.isFile(p) {
		return path.Dir(p)
	}
	for _, ext := range tsExts {
		if r.isFile(p + ext) {
			return path.Dir(p)
		}
	}
	if r.isDir(p) {
		return p
	}
	return path.Dir(p)
}

func (r *tsResolver) isFile(p string) bool {
	st, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(p)))
	return err == nil && st.Mode().IsRegular()
}

func (r *tsResolver) isDir(p string) bool {
	st, err := os.Stat(filepath.Join(r.root, filepath.FromSlash(p)))
	return err == nil && st.IsDir()
}

// cleanRel limpa um caminho relativo à raiz; "" quando ele sai do repo.
func cleanRel(p string) string {
	p = path.Clean(p)
	if p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(p) {
		return ""
	}
	return p
}

// stripJSONC tira comentários (// e /* */) e vírgulas finais, que o tsconfig aceita e o JSON não.
func stripJSONC(in []byte) []byte { return stripTrailingCommas(stripComments(in)) }

// jsonString devolve o fim (exclusivo) da string JSON que começa em in[i] == '"'.
func jsonString(in []byte, i int) int {
	for j := i + 1; j < len(in); j++ {
		switch in[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(in)
}

func stripComments(in []byte) []byte {
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); {
		switch {
		case in[i] == '"':
			j := jsonString(in, i)
			out = append(out, in[i:j]...)
			i = j
		case in[i] == '/' && i+1 < len(in) && in[i+1] == '/':
			for i < len(in) && in[i] != '\n' {
				i++
			}
		case in[i] == '/' && i+1 < len(in) && in[i+1] == '*':
			end := strings.Index(string(in[i+2:]), "*/")
			if end < 0 {
				return out
			}
			i += end + 4
			out = append(out, ' ')
		default:
			out = append(out, in[i])
			i++
		}
	}
	return out
}

func stripTrailingCommas(in []byte) []byte {
	out := make([]byte, 0, len(in))
	for i := 0; i < len(in); {
		switch in[i] {
		case '"':
			j := jsonString(in, i)
			out = append(out, in[i:j]...)
			i = j
		case ',':
			j := i + 1
			for j < len(in) && strings.ContainsRune(" \t\r\n", rune(in[j])) {
				j++
			}
			if j >= len(in) || (in[j] != '}' && in[j] != ']') {
				out = append(out, ',')
			}
			i++
		default:
			out = append(out, in[i])
			i++
		}
	}
	return out
}
