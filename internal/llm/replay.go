package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Replay responde com respostas gravadas em Dir/<agente>/<ReplayKey>.json; se não houver,
// usa Dir/<agente>/default.json. Não chama modelo nenhum (ADR 0013).
type Replay struct {
	Dir      string
	Recorder Recorder
	B        *Budget
	RunID    int64
}

var _ Caller = (*Replay)(nil)

// ErrNoRecording indica que não há resposta gravada para a requisição.
var ErrNoRecording = errors.New("sem resposta gravada")

func (r *Replay) Provider() string { return "replay" }
func (r *Replay) Budget() *Budget  { return r.B }

func (r *Replay) Call(ctx context.Context, req Request) (Response, error) {
	if err := validate(req); err != nil {
		return Response{}, err
	}
	if err := r.B.ReserveCall(); err != nil {
		return Response{}, err
	}
	rec := CallRecord{RunID: r.RunID, Provider: r.Provider(), Agent: req.Agent, Model: req.Model, PromptVersion: req.PromptVersion, Mode: "sync", Status: "succeeded"}
	var data []byte
	var err error
	for _, name := range []string{ReplayKey(req) + ".json", "default.json"} {
		data, err = os.ReadFile(filepath.Join(r.Dir, req.Agent, name)) //nolint:gosec // diretório de gravações do operador
		if err == nil {
			break
		}
	}
	if err != nil {
		rec.Status, rec.Error = "errored", ErrNoRecording.Error()
		_ = r.Recorder.RecordCall(ctx, rec)
		return Response{}, fmt.Errorf("%w: %s/%s", ErrNoRecording, req.Agent, ReplayKey(req))
	}
	if err := r.Recorder.RecordCall(ctx, rec); err != nil {
		return Response{}, err
	}
	return Response{Model: req.Model, Text: string(data), StopReason: "end_turn", Latency: time.Duration(0)}, nil
}

func (r *Replay) Batch(ctx context.Context, items []BatchItem) (map[string]BatchResult, error) {
	out := make(map[string]BatchResult, len(items))
	for _, it := range items {
		resp, err := r.Call(ctx, it.Request)
		if errors.Is(err, ErrBudgetExceeded) {
			return out, err
		}
		out[it.CustomID] = BatchResult{Response: resp, Err: err}
	}
	return out, nil
}
