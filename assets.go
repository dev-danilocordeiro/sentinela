// Package sentinela embute os prompts versionados e os JSON Schemas dos contratos
// entre etapas, para que o binário seja autocontido.
package sentinela

import "embed"

// Prompts contém prompts/<agente>.md, com a versão no frontmatter.
//
//go:embed prompts/*.md
var Prompts embed.FS

// Schemas contém schemas/<agente>.input.json e schemas/<agente>.output.json.
//
//go:embed schemas/*.json
var Schemas embed.FS
