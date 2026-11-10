package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/acme/monorepo-poliglota/go/internal/financeiro"
	"github.com/acme/monorepo-poliglota/go/internal/matricula"
	"github.com/acme/monorepo-poliglota/go/internal/turmas"
)

func main() {
	t := turmas.New()
	m := matricula.New(t)
	f := financeiro.New()
	m.AoConfirmar(f.AoConfirmarMatricula)

	http.HandleFunc("POST /turmas/{id}", func(w http.ResponseWriter, r *http.Request) {
		capacidade, err := strconv.Atoi(r.URL.Query().Get("capacidade"))
		if err != nil {
			http.Error(w, "capacidade inválida", http.StatusBadRequest)
			return
		}
		t.AbrirTurma(r.PathValue("id"), capacidade)
		w.WriteHeader(http.StatusCreated)
	})

	http.HandleFunc("POST /turmas/{id}/matriculas/{estudante}", func(w http.ResponseWriter, r *http.Request) {
		err := m.Matricular(r.PathValue("estudante"), r.PathValue("id"), 2027)
		switch {
		case errors.Is(err, turmas.ErrSemVaga):
			http.Error(w, err.Error(), http.StatusConflict)
		case err != nil:
			http.Error(w, err.Error(), http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusCreated)
		}
	})

	slog.Info("escutando", "endereco", ":8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		slog.Error("servidor parou", "erro", err)
		os.Exit(1)
	}
}
