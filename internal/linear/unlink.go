package linear

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// ErrNotUnlinkable: o achado não está aberto ou não está ligado à issue pedida.
var ErrNotUnlinkable = errors.New("achado não pode ser desligado")

// Unlink desliga um achado aberto da issue a que ficou ligado por engano (ex.: o caso da ADR 0047, uma
// issue arquivada de outra instalação): apaga a ligação, grava o registro em
// finding_unlinks com o motivo e deixa o achado pendente de publicação de novo. Não mexe no
// Linear, em outcomes nem em supressões: não é decisão sobre o achado. identifier vazio desliga
// todas as issues do achado.
func Unlink(ctx context.Context, st *store.Store, findingID int64, identifier, reason, actor string) ([]db.LinearIssue, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: o motivo é obrigatório", ErrNotUnlinkable)
	}
	var out []db.LinearIssue
	err := pgx.BeginFunc(ctx, st.Pool, func(tx pgx.Tx) error {
		q := st.Q.WithTx(tx)
		f, err := q.GetFinding(ctx, findingID)
		if err != nil {
			return err
		}
		if f.State != "open" {
			return fmt.Errorf("%w: achado %d está %s (só um achado open volta a pendente)", ErrNotUnlinkable, findingID, f.State)
		}
		rows, err := q.LinearIssuesOfFinding(ctx, findingID)
		if err != nil {
			return err
		}
		for _, li := range rows {
			if identifier != "" && !strings.EqualFold(li.Identifier, identifier) {
				continue
			}
			if err := q.InsertFindingUnlink(ctx, db.InsertFindingUnlinkParams{FindingID: findingID, LinearID: li.LinearID,
				Identifier: li.Identifier, Url: li.Url, Role: li.Role, Reason: reason, Actor: actor}); err != nil {
				return err
			}
			if err := q.DeleteLinearIssue(ctx, li.ID); err != nil {
				return err
			}
			if li.Role == "primary" {
				if err := q.ClearFindingLinearIssue(ctx, findingID); err != nil {
					return err
				}
			}
			out = append(out, li)
		}
		if len(out) == 0 {
			return fmt.Errorf("%w: achado %d não está ligado a %s", ErrNotUnlinkable, findingID, orDash(identifier))
		}
		return nil
	})
	return out, err
}
