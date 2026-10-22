package postgres

import (
	"context"
	"encoding/json"
	"errors"
	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"github.com/Adopten123/digital-ruble-deals/internal/features/settlement/ledger"
	"github.com/Adopten123/digital-ruble-deals/internal/features/settlement/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

type querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func operator(ctx context.Context, q querier, id string) error {
	var ok bool
	if e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND system_role='DEMO_OPERATOR')`, id).Scan(&ok); e != nil {
		return e
	}
	if !ok {
		return service.ErrForbidden
	}
	return nil
}

const accountColumns = `a.id,o.id,o.name,o.kind,a.currency,a.total_kopecks::text,a.reserved_kopecks::text,(a.total_kopecks-a.reserved_kopecks)::text,a.created_at`
const accountFrom = ` FROM accounts a JOIN organizations o ON o.id=a.organization_id `

type scanner interface{ Scan(...any) error }

func scan(row scanner) (a service.Account, e error) {
	e = row.Scan(&a.ID, &a.Organisation.ID, &a.Organisation.Name, &a.Organisation.Kind, &a.Currency, &a.Total, &a.Reserved, &a.Available, &a.CreatedAt)
	a.CreatedAt = a.CreatedAt.UTC()
	return
}
func (r *Repository) List(ctx context.Context, q service.Query) ([]service.Account, error) {
	if e := operator(ctx, r.pool, q.Actor.UserID); e != nil {
		return nil, e
	}
	rows, e := r.pool.Query(ctx, `SELECT `+accountColumns+accountFrom+` WHERE ($1='' OR strpos(lower(a.id),lower($1))>0 OR strpos(lower(o.id),lower($1))>0 OR strpos(lower(o.name),lower($1))>0) AND($2::timestamptz IS NULL OR(a.created_at,a.id)<($2,$3::text)) ORDER BY a.created_at DESC,a.id DESC LIMIT $4`, q.Search, q.BeforeTime, q.BeforeID, q.Limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []service.Account{}
	for rows.Next() {
		a, e := scan(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (r *Repository) Move(ctx context.Context, c service.Command) (out deals.Result, err error) {
	err = (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		if e := operator(ctx, tx, c.Actor.UserID); e != nil {
			return e
		}
		resource := "/demo/accounts/" + c.ID
		scope, _ := json.Marshal([]string{"operator", c.Actor.UserID, c.Operation, resource, c.Key})
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(scope)); e != nil {
			return e
		}
		var oldHash string
		e := tx.QueryRow(ctx, `SELECT request_hash,status_code,response_body FROM operator_command_results WHERE user_id=$1 AND operation=$2 AND resource=$3 AND key=$4`, c.Actor.UserID, c.Operation, resource, c.Key).Scan(&oldHash, &out.Status, &out.Body)
		if e == nil {
			if oldHash != c.Hash {
				out = deals.Failure{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "Ключ использован с другим телом"}.Result(c.Actor.RequestID)
			}
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		var now time.Time
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		now = now.UTC()
		persist := func(success bool) error {
			event := c.Operation + "_REJECTED"
			if success {
				if c.Operation == "DEMO_CREDIT" {
					event = "DEMO_CREDITED"
				} else {
					event = "DEMO_DEBITED"
				}
			}
			eventID, e := security.ID()
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,type,actor_user_id,request_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6)`, eventID, event, c.Actor.UserID, c.Actor.RequestID, out.Body, now); e != nil {
				return e
			}
			if success {
				job, e := security.ID()
				if e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `INSERT INTO outbox(id,event_id,type,payload,created_at) VALUES($1,$2,$3,$4,$5)`, job, eventID, event, out.Body, now); e != nil {
					return e
				}
			}
			_, e = tx.Exec(ctx, `INSERT INTO operator_command_results(user_id,operation,resource,key,request_hash,status_code,response_body) VALUES($1,$2,$3,$4,$5,$6,$7)`, c.Actor.UserID, c.Operation, resource, c.Key, c.Hash, out.Status, out.Body)
			return e
		}
		reject := func(status int, code, msg string) error {
			out = deals.Failure{Status: status, Code: code, Message: msg}.Result(c.Actor.RequestID)
			return persist(false)
		}
		var clientID string
		e = tx.QueryRow(ctx, `SELECT a.id `+accountFrom+` WHERE a.id=$1`, c.ID).Scan(&clientID)
		if errors.Is(e, pgx.ErrNoRows) {
			return reject(404, "RESOURCE_NOT_FOUND", "Клиентский счёт не найден")
		}
		if e != nil {
			return e
		}
		if c.Invalid != nil {
			out = c.Invalid.Result(c.Actor.RequestID)
			return persist(false)
		}
		purpose := "DEMO_ISSUANCE"
		if c.Operation == "DEMO_DEBIT" {
			purpose = "DEMO_DEBITS"
		}
		var systemID string
		if e = tx.QueryRow(ctx, `SELECT id FROM accounts WHERE system_purpose=$1`, purpose).Scan(&systemID); e != nil {
			return e
		}
		_, e = ledger.Lock(ctx, tx, clientID, systemID)
		if e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		now = now.UTC()
		from, to := systemID, clientID
		if c.Operation == "DEMO_DEBIT" {
			from, to = clientID, systemID
		}
		batch, e := ledger.Post(ctx, tx, string(scope), c.Operation, c.Input.Reason, from, to, c.Amount)
		if errors.Is(e, ledger.ErrInsufficient) {
			return reject(422, "INSUFFICIENT_AVAILABLE_FUNDS", "Недостаточно доступных тестовых средств")
		}
		if errors.Is(e, ledger.ErrOverflow) {
			return reject(422, "AMOUNT_OVERFLOW", "Операция выходит за диапазон целых копеек")
		}
		if e != nil {
			return e
		}
		account, e := scan(tx.QueryRow(ctx, `SELECT `+accountColumns+accountFrom+` WHERE a.id=$1`, clientID))
		if e != nil {
			return e
		}
		out.Status = 201
		out.Body, e = json.Marshal(map[string]any{"transaction_id": batch, "account": account})
		if e != nil {
			return e
		}
		return persist(true)
	})
	if err != nil {
		return deals.Result{}, err
	}
	return out, nil
}
