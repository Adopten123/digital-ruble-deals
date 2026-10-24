package ledger

import (
	"context"
	"errors"
	"math"

	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	"github.com/jackc/pgx/v5"
)

// CheckReservedTransfer consumes this deal's own reservation. Available funds
// need not cover it again, but other deals' reservations must remain covered.
func CheckReservedTransfer(from, to Account, amount int64) error {
	if amount <= 0 || from.ID == to.ID || from.OrganizationID == "" || to.OrganizationID == "" {
		return errors.New("invalid reserved movement")
	}
	if from.Reserved < amount || from.Total < amount || from.Total-amount < from.Reserved-amount {
		return ErrInsufficient
	}
	if to.Total > math.MaxInt64-amount {
		return ErrOverflow
	}
	return nil
}

// PostReserved has no commit. The caller already holds the deal, both accounts
// in sorted order, and its exact ACTIVE reserve. The repeated account locks
// therefore introduce no new lock after the reserve was acquired.
func PostReserved(ctx context.Context, tx pgx.Tx, operation, from, to string, amount int64) (string, error) {
	accounts, e := Lock(ctx, tx, from, to)
	if e != nil {
		return "", e
	}
	if e = CheckReservedTransfer(accounts[from], accounts[to], amount); e != nil {
		return "", e
	}
	batch, e := security.ID()
	if e != nil {
		return "", e
	}
	negative, e := security.ID()
	if e != nil {
		return "", e
	}
	positive, e := security.ID()
	if e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO ledger_batches(id,operation_key,kind,reason) VALUES($1,$2,'DEAL_SETTLEMENT','Расчёт по проверенному событию ЭДО')`, batch, operation); e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO ledger_entries(id,batch_id,account_id,delta_kopecks) VALUES($1,$2,$3,$4),($5,$2,$6,$7)`, negative, batch, from, -amount, positive, to, amount); e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, `UPDATE accounts SET total_kopecks=total_kopecks-$2,reserved_kopecks=reserved_kopecks-$2 WHERE id=$1`, from, amount); e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, `UPDATE accounts SET total_kopecks=total_kopecks+$2 WHERE id=$1`, to, amount); e != nil {
		return "", e
	}
	return batch, nil
}
