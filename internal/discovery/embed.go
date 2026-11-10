package discovery

import (
	"hash/fnv"
	"math"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

// Dims é a dimensão da coluna findings.embedding.
const Dims = 1024

// Embedder transforma um achado num vetor para busca de similares no pgvector.
type Embedder interface {
	Embed(f signals.Finding, problemKey string) []float32
}

// HashEmbedder é um embedding léxico local (feature hashing de palavras e bigramas,
// com sinal, normalizado). Não chama API nenhuma: a Anthropic não oferece embeddings,
// e um provedor externo (ex.: Voyage) exigiria outra chave. Pega reformulações com
// vocabulário parecido; paráfrases distantes ficam para o Judge, que vê os similares (ADR 0011).
type HashEmbedder struct{}

func (HashEmbedder) Embed(f signals.Finding, problemKey string) []float32 {
	vec := make([]float32, Dims)
	add := func(text string, weight float32) {
		words := strings.Fields(signals.NormalizeProblem(text))
		for i, w := range words {
			bump(vec, w, weight)
			if i > 0 {
				bump(vec, words[i-1]+" "+w, weight*0.5)
			}
		}
	}
	add(f.Title, 1.0)
	add(problemKey, 1.5)
	add(f.Context, 0.3)
	bump(vec, "category:"+f.Category, 1.0)
	if len(f.Evidence) > 0 {
		bump(vec, "file:"+f.Evidence[0].File, 1.5)
	}
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm == 0 {
		return vec
	}
	inv := float32(1 / math.Sqrt(norm))
	for i := range vec {
		vec[i] *= inv
	}
	return vec
}

func bump(vec []float32, token string, weight float32) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(token))
	sum := h.Sum64()
	idx := sum % Dims
	if sum>>63 == 1 {
		weight = -weight
	}
	vec[idx] += weight
}

// Cosine calcula a similaridade de cosseno entre vetores normalizados.
func Cosine(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}
