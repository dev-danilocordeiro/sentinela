package scan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

// Scanners de Node (ADR 0060): eslint com a config neutra do Sentinela e tsc --noEmit, os dois das
// ferramentas do próprio Sentinela (discovery.node_tools: tools/node, instalado com
// `npm ci --ignore-scripts`), nunca do repo analisado. Nada do repo é instalado e nenhum script de
// postinstall roda. Flags conferidas na v10 do eslint (https://eslint.org/docs/latest/use/command-line-interface,
// formato em https://eslint.org/docs/latest/use/formatters/#json) e no tsc 5.9
// (https://www.typescriptlang.org/docs/handbook/compiler-options.html).

// nodeTools é o diretório das ferramentas de Node do Sentinela.
type nodeTools string

func (t nodeTools) file(rel string) string { return filepath.Join(string(t), filepath.FromSlash(rel)) }

// missing é o motivo do skipped quando as ferramentas não estão instaladas.
func (t nodeTools) missing(bin string) string {
	if _, err := os.Stat(t.file(bin)); err == nil {
		return ""
	}
	return "as ferramentas de Node do Sentinela não estão instaladas em " + string(t) + ": rode `npm ci --ignore-scripts --prefix " + string(t) + "`"
}

// jsUnit é o perUnit dos scanners de Node: um pacote por package.json (npm, yarn ou pnpm).
type jsUnit struct{}

func (jsUnit) runsIn(u repo.BuildUnit) bool { return u.JS() }
func (jsUnit) noUnits() string              { return "nenhum package.json no repo" }

// ---- eslint ----

// ESLint roda o eslint com a config neutra do Sentinela (tools/node/eslint.config.mjs) em cada
// pacote JS, sem a config nem os plugins do repo. Num monorepo, o pacote da raiz não repete os
// arquivos dos workspaces, que rodam como unidade própria.
type ESLint struct {
	Tools string // discovery.node_tools, absoluto
}

const eslintBin = "node_modules/eslint/bin/eslint.js"

func (ESLint) Name() string { return "eslint" }
func (ESLint) Applies(langs map[string]bool) bool {
	return langs["typescript"] || langs["javascript"]
}
func (ESLint) runsIn(u repo.BuildUnit) bool { return jsUnit{}.runsIn(u) }
func (ESLint) noUnits() string              { return jsUnit{}.noUnits() }

func (e ESLint) preflight(string) string { return nodeTools(e.Tools).missing(eslintBin) }

func (e ESLint) Command(dir string) (string, []string) {
	t := nodeTools(e.Tools)
	args := []string{t.file(eslintBin), "--config", t.file("eslint.config.mjs"), "--format", "json",
		"--no-warn-ignored", "--no-error-on-unmatched-pattern"}
	for _, u := range repo.BuildUnits(dir) {
		if u.JS() && u.Dir != "." {
			args = append(args, "--ignore-pattern", u.Dir+"/**")
		}
	}
	return "node", append(args, ".")
}

type eslintFile struct {
	FilePath string `json:"filePath"`
	Messages []struct {
		RuleID   *string `json:"ruleId"`
		Severity int     `json:"severity"` // 1 aviso, 2 erro
		Fatal    bool    `json:"fatal"`
		Message  string  `json:"message"`
		Line     int     `json:"line"`
	} `json:"messages"`
}

// Parse lê o relatório JSON. Os avisos sobre a config do repo (regra de um plugin que a config
// neutra não carrega, citada num eslint-disable) não são sinal sobre o código e ficam de fora.
func (ESLint) Parse(out []byte, dir string) ([]Signal, error) {
	var files []eslintFile
	if err := json.Unmarshal(out, &files); err != nil {
		return nil, err
	}
	var sigs []Signal
	for _, f := range files {
		for _, m := range f.Messages {
			if strings.HasPrefix(m.Message, "Definition for rule ") || strings.HasPrefix(m.Message, "Unused eslint-disable directive") {
				continue
			}
			rule, sev := "", "low"
			if m.RuleID != nil {
				rule = *m.RuleID
			}
			switch {
			case m.Fatal || rule == "":
				rule = "parse-error"
			case m.Severity == 2:
				sev = "medium"
			}
			sigs = append(sigs, Signal{Scanner: "eslint", Rule: rule, File: rel(dir, f.FilePath), Line: m.Line, Severity: sev, Message: m.Message})
		}
	}
	return sigs, nil
}

// Coverage conta os arquivos do relatório: o eslint lista cada arquivo que analisou, com ou sem
// mensagem.
func (ESLint) Coverage(out []byte, _ string) (int, string) {
	var files []json.RawMessage
	_ = json.Unmarshal(out, &files)
	return len(files), "arquivos"
}

// ---- tsc ----

// TSC roda o tsc --noEmit do Sentinela no tsconfig de cada pacote. Só com node_modules (no pacote
// ou num diretório acima, até a raiz do checkout): sem as dependências, todo import de pacote
// externo vira erro. A descoberta não instala nada, então lá ele aparece pulado com o motivo; roda
// onde as dependências já estão instaladas, como o worktree do job depois do setup.
type TSC struct {
	Tools string // discovery.node_tools, absoluto
}

const tscBin = "node_modules/typescript/bin/tsc"

func (TSC) Name() string                       { return "tsc" }
func (TSC) Applies(langs map[string]bool) bool { return langs["typescript"] }
func (TSC) runsIn(u repo.BuildUnit) bool       { return jsUnit{}.runsIn(u) }
func (TSC) noUnits() string                    { return jsUnit{}.noUnits() }

func (c TSC) preflight(string) string { return nodeTools(c.Tools).missing(tscBin) }

// skipUnit pula o pacote sem tsconfig.json e o sem node_modules.
func (TSC) skipUnit(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "tsconfig.json")); err != nil {
		return "sem tsconfig.json"
	}
	for d := dir; ; {
		if st, err := os.Stat(filepath.Join(d, "node_modules")); err == nil && st.IsDir() {
			return ""
		}
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return "sem node_modules (o tsc precisa das dependências instaladas, e a descoberta não instala nada do repo; roda no job, depois do setup)"
}

// Command não emite nada nem grava .tsbuildinfo no checkout; --listFiles é a prova de análise.
func (c TSC) Command(string) (string, []string) {
	return "node", []string{nodeTools(c.Tools).file(tscBin), "--noEmit", "-p", ".", "--pretty", "false",
		"--incremental", "false", "--composite", "false", "--listFiles"}
}

var tscDiag = regexp.MustCompile(`^(.+?)\((\d+),\d+\): (error|warning) (TS\d+): (.*)$`)
var tscGlobal = regexp.MustCompile(`^(error|warning) (TS\d+): (.*)$`)

// Parse lê os diagnósticos do tsc. Os de dentro de node_modules ficam de fora; os sem arquivo
// (config) apontam o tsconfig.json.
func (TSC) Parse(out []byte, dir string) ([]Signal, error) {
	var sigs []Signal
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := tscDiag.FindStringSubmatch(line); m != nil {
			file := rel(dir, m[1])
			if strings.Contains(file, "node_modules/") {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			sigs = append(sigs, Signal{Scanner: "tsc", Rule: m[4], File: file, Line: n, Severity: "medium", Message: m[5]})
		} else if m := tscGlobal.FindStringSubmatch(line); m != nil {
			sigs = append(sigs, Signal{Scanner: "tsc", Rule: m[2], File: "tsconfig.json", Line: 1, Severity: "medium", Message: m[3]})
		}
	}
	return sigs, sc.Err()
}

// Coverage conta os arquivos do pacote na lista do --listFiles (sem node_modules nem a lib do TS).
func (TSC) Coverage(out []byte, dir string) (int, string) {
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !filepath.IsAbs(line) || strings.Contains(line, "/node_modules/") {
			continue
		}
		if r, err := filepath.Rel(dir, line); err == nil && !strings.HasPrefix(r, "..") {
			n++
		}
	}
	return n, "arquivos"
}
