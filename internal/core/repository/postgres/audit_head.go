package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// LockAuditChainHead is the final shared resource lock, taken after the
// aggregate/accounts and before the last deadline check. Older isolated upgrade
// fixtures have no chain yet; serve readiness always requires the latest schema.
func LockAuditChainHead(ctx context.Context, tx pgx.Tx) error {
	var schema string
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.current_schema()`).Scan(&schema); err != nil {
		return err
	}
	if schema == "" {
		return errors.New("audit schema unavailable")
	}
	relation := pgx.Identifier{schema, "audit_chain_head"}.Sanitize()
	var installed bool
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.to_regclass($1) IS NOT NULL`, relation).Scan(&installed); err != nil {
		return err
	}
	if !installed {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT `+pgx.Identifier{schema, "lock_audit_chain_head"}.Sanitize()+`()`)
	return err
}
