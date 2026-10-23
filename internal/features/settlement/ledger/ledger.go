// Package ledger provides transaction-local monetary operations. Callers own the
// transaction and acquire deal locks before account locks, never the reverse.
package ledger

import (
	"context"
	"errors"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	"github.com/jackc/pgx/v5"
	"math"
	"sort"
)

type Account struct {
	ID, OrganizationID, Purpose string
	Total, Reserved             int64
}

var ErrInsufficient = errors.New("insufficient available funds")
var ErrOverflow = errors.New("monetary amount overflow")

func CanReserve(a Account, amount int64) error {
	if amount <= 0 {
		return errors.New("nonpositive reserve")
	}
	if a.Total-a.Reserved < amount {
		return ErrInsufficient
	}
	return nil
}
func CheckTransfer(from, to Account, amount int64) error {
	if amount <= 0 || from.ID == to.ID {
		return errors.New("invalid monetary movement")
	}
	if from.Purpose == "DEMO_ISSUANCE" {
		if from.Total < math.MinInt64+amount {
			return ErrOverflow
		}
	} else if from.Total-from.Reserved < amount {
		return ErrInsufficient
	}
	if to.Total > math.MaxInt64-amount {
		return ErrOverflow
	}
	return nil
}

func Lock(ctx context.Context, tx pgx.Tx, ids ...string) (map[string]Account, error) {
	sort.Strings(ids)
	rows, e := tx.Query(ctx, `SELECT id FROM accounts WHERE id=ANY($1) ORDER BY id FOR UPDATE`, ids)
	if e != nil {
		return nil, e
	}
	n := 0
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		n++
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if n != len(ids) {
		return nil, errors.New("account missing")
	}
	out := map[string]Account{}
	for _, id := range ids {
		var a Account
		e = tx.QueryRow(ctx, `SELECT id,COALESCE(organization_id,''),COALESCE(system_purpose,''),total_kopecks,reserved_kopecks FROM accounts WHERE id=$1`, id).Scan(&a.ID, &a.OrganizationID, &a.Purpose, &a.Total, &a.Reserved)
		if e != nil {
			return nil, e
		}
		out[id] = a
	}
	return out, nil
}
func Post(ctx context.Context, tx pgx.Tx, operation, kind, reason, from, to string, amount int64) (string, error) {
	if amount <= 0 || from == to {
		return "", errors.New("invalid monetary movement")
	}
	accounts, err := Lock(ctx, tx, from, to)
	if err != nil {
		return "", err
	}
	if err = CheckTransfer(accounts[from], accounts[to], amount); err != nil {
		return "", err
	}
	batch, e := security.ID()
	if e != nil {
		return "", e
	}
	positive, e := security.ID()
	if e != nil {
		return "", e
	}
	negative, e := security.ID()
	if e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO ledger_batches(id,operation_key,kind,reason) VALUES($1,$2,$3,$4)`, batch, operation, kind, reason); e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO ledger_entries(id,batch_id,account_id,delta_kopecks) VALUES($1,$2,$3,$4),($5,$2,$6,$7)`, negative, batch, from, -amount, positive, to, amount); e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, `UPDATE accounts SET total_kopecks=total_kopecks-$2 WHERE id=$1`, from, amount); e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, `UPDATE accounts SET total_kopecks=total_kopecks+$2 WHERE id=$1`, to, amount); e != nil {
		return "", e
	}
	return batch, nil
}
