package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCommitUnknown means commit was attempted but its outcome cannot be proven.
// A caller must reconcile the original operation, never assume it was rolled back.
var ErrCommitUnknown = errors.New("commit outcome is unknown")

type UnitOfWork struct{ Pool *pgxpool.Pool }

func (u UnitOfWork) WithinTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := u.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	commitAttempted := false
	defer func() {
		if !commitAttempted {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tx.Rollback(rollbackCtx)
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	commitAttempted = true
	if err := tx.Commit(ctx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return err
		}
		var rejected *pgconn.PgError
		if errors.As(err, &rejected) {
			return err
		} // PostgreSQL explicitly rejected COMMIT.
		return fmt.Errorf("%w: %v", ErrCommitUnknown, err)
	}
	return nil
}
