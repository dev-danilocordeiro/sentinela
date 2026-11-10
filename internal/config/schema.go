package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// Schema é o JSON Schema do sentinela.yaml, gerado das structs desta package (ADR 0041). Os
// editores usam para completar e validar; o teste TestSchemaFilesAreUpToDate falha se o arquivo
// em schema/ ficar diferente do código.
func Schema() ([]byte, error) {
	return schemaOf(reflect.TypeFor[Config](), "sentinela.yaml", "Configuração do Sentinela (arquivo principal). Referência: docs/configuracao.md.")
}

// TeamSchema é o JSON Schema de config.d/teams/<time>.yaml.
func TeamSchema() ([]byte, error) {
	return schemaOf(reflect.TypeFor[TeamFile](), "config.d/teams/<time>.yaml",
		"Arquivo de um time (o nome do arquivo é o time). Política da organização só vale no sentinela.yaml principal. Referência: docs/guia-configuracao.md.")
}

func schemaOf(t reflect.Type, title, desc string) ([]byte, error) {
	s := typeSchema(t)
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["title"] = title
	s["description"] = desc
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

var (
	durationType    = reflect.TypeFor[time.Duration]()
	timeType        = reflect.TypeFor[time.Time]()
	allowedListType = reflect.TypeFor[AllowedList]()
)

func typeSchema(t reflect.Type) map[string]any {
	switch t {
	case durationType:
		return map[string]any{"type": "string", "pattern": `^-?([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`, "description": "duração do Go, ex.: 15m, 2h, 168h"}
	case timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case allowedListType:
		return map[string]any{"description": "anyone, team_members ou uma lista de usuários do Linear (e-mail ou nome), que pode incluir team_members",
			"oneOf": []any{map[string]any{"type": "string", "enum": []string{AllowAnyone, AllowTeamMembers}}, map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return typeSchema(t.Elem())
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": typeSchema(t.Elem())}
	case reflect.Struct:
		props := map[string]any{}
		addFields(t, props)
		return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	}
	return map[string]any{}
}

func addFields(t reflect.Type, props map[string]any) {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		name, opts, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if strings.Contains(opts, "inline") {
			addFields(f.Type, props)
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		props[name] = typeSchema(f.Type)
	}
}
