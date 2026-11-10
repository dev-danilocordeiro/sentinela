// Package agentstest tem utilitários de teste dos agentes: golden files e respostas gravadas.
package agentstest

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

var update = flag.Bool("update", false, "regrava os golden files")

// Golden compara got (serializado como JSON indentado) com testdata/<name>.golden.json.
// Rode `go test ./internal/agents/... -update` para regravar depois de mudar prompt ou contrato.
func Golden(t *testing.T, name string, got any) {
	t.Helper()
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixture de teste
	if err != nil {
		t.Fatalf("golden ausente (rode com -update): %v", err)
	}
	if !bytes.Equal(want, data) {
		t.Errorf("%s difere do golden; rode com -update se a mudança for intencional\n--- got:\n%s", path, data)
	}
}

// RequestView é a parte estável de uma llm.Request que vale fixar em golden.
type RequestView struct {
	Agent         string          `json:"agent"`
	PromptVersion string          `json:"prompt_version"`
	Model         string          `json:"model"`
	MaxTokens     int64           `json:"max_tokens"`
	Effort        string          `json:"effort"`
	HasSchema     bool            `json:"has_schema"`
	User          json.RawMessage `json:"user"`
}

// View extrai a parte estável da requisição.
func View(r llm.Request) RequestView {
	return RequestView{
		Agent: r.Agent, PromptVersion: r.PromptVersion, Model: r.Model, MaxTokens: r.MaxTokens,
		Effort: r.Effort, HasSchema: r.Schema != nil, User: json.RawMessage(r.User),
	}
}

// Recorded lê testdata/<name>.response.json: o texto que o modelo devolveu.
func Recorded(t *testing.T, name string) llm.Response {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".response.json")) //nolint:gosec // fixture de teste
	if err != nil {
		t.Fatal(err)
	}
	return llm.Response{Text: string(data), StopReason: "end_turn"}
}
