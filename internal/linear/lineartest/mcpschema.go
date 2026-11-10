package lineartest

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/mcpcall"
)

// linearMCPTools é o schema das ferramentas do MCP do Linear que o Sentinela usa, como o servidor
// as expõe (veja o campo _fonte do arquivo).
//
//go:embed linear-mcp-tools.json
var linearMCPTools []byte

// MCPTool é o que importa do schema de uma ferramenta para conferir os passos do Sentinela.
type MCPTool struct {
	Properties []string `json:"properties"`
	Required   []string `json:"required"`
	LimitMax   *float64 `json:"limitMax"`
	Cursor     bool     `json:"cursor"`
}

// LinearMCPTools devolve o schema gravado das ferramentas.
func LinearMCPTools() map[string]MCPTool {
	var doc struct {
		Tools map[string]MCPTool `json:"tools"`
	}
	if err := json.Unmarshal(linearMCPTools, &doc); err != nil {
		panic(err)
	}
	return doc.Tools
}

// CheckSteps confere cada passo contra o schema: a ferramenta existe, os argumentos são
// propriedades dela (o servidor recusa as outras), os obrigatórios estão lá, o limit cabe no
// máximo e, numa listagem paginada, a instrução manda seguir o cursor.
func CheckSteps(tools map[string]MCPTool, steps []mcpcall.Step) error {
	var errs []string
	for _, s := range steps {
		name := s.Tool[strings.LastIndex(s.Tool, "__")+2:]
		tool, ok := tools[name]
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: ferramenta fora do schema gravado", name))
			continue
		}
		raw, err := json.Marshal(s.Args)
		if err != nil {
			return err
		}
		var args map[string]any
		if err := json.Unmarshal(raw, &args); err != nil {
			return err
		}
		allowed := map[string]bool{}
		for _, p := range tool.Properties {
			allowed[p] = true
		}
		var keys []string
		for k := range args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !allowed[k] {
				errs = append(errs, fmt.Sprintf("%s: argumento %q não existe na ferramenta", name, k))
			}
		}
		for _, r := range tool.Required {
			if _, ok := args[r]; !ok {
				errs = append(errs, fmt.Sprintf("%s: falta o argumento obrigatório %q", name, r))
			}
		}
		if l, ok := args["limit"].(float64); ok {
			if tool.LimitMax != nil && l > *tool.LimitMax {
				errs = append(errs, fmt.Sprintf("%s: limit %v acima do máximo %v", name, l, *tool.LimitMax))
			}
			if tool.Cursor && !strings.Contains(s.Note, "cursor") {
				errs = append(errs, fmt.Sprintf("%s: listagem com limit sem instrução de seguir o cursor", name))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("passos fora do schema do MCP do Linear: %s", strings.Join(errs, "; "))
	}
	return nil
}
