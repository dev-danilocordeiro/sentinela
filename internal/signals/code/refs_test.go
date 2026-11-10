package code

import (
	"slices"
	"testing"
)

// testdata/monorepo-poliglota-f0b78b1 tem o go.mod e o go/cmd/api/main.go reais do monorepo-poliglota no SHA
// da rodada 12 (o arquivo da evidência das EX-12, EX-14 e EX-15).
const monorepo = "testdata/monorepo-poliglota-f0b78b1"

func TestGoRefsResolvesPackagesCitedByTheFix(t *testing.T) {
	files := []string{"go/cmd/api/main.go"}
	// Correção proposta real da EX-15.
	ex15 := "Usar erros sentinela (ex.: turmas.ErrNaoEncontrada) com errors.Is para 404; os demais casos devem logar o erro com slog e retornar 500 com mensagem genérica."
	if got := goRefs(monorepo, files, ex15, "case err != nil:", "http.Error(w, err.Error(), http.StatusNotFound)"); !slices.Equal(got, []string{"go/internal/turmas"}) {
		t.Errorf("EX-15: refs = %v, quer [go/internal/turmas] (errors, slog e http são da biblioteca padrão)", got)
	}
	// EX-12 (timeouts): só biblioteca padrão.
	if got := goRefs(monorepo, files, "Usar http.Server com ReadHeaderTimeout e chamar srv.ListenAndServe."); len(got) != 0 {
		t.Errorf("EX-12: refs = %v, quer nenhuma", got)
	}
	if got := goRefs(monorepo, files, "matricula.New e financeiro.New e matricula.ErrX"); !slices.Equal(got, []string{"go/internal/matricula", "go/internal/financeiro"}) {
		t.Errorf("refs = %v", got)
	}
	if got := goRefs(monorepo, []string{"java/Foo.java"}, "turmas.ErrNaoEncontrada"); len(got) != 0 {
		t.Errorf("arquivo não Go não tem imports: %v", got)
	}
}
