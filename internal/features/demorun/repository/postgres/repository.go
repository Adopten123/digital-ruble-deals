package postgres

import (
	"context"
	"encoding/json"
	"errors"
	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	"github.com/Adopten123/digital-ruble-deals/internal/features/demorun/service"
	"github.com/Adopten123/digital-ruble-deals/internal/features/settlement/ledger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }
func (r *Repository) Seed(ctx context.Context, c service.SeedCommand) (out service.SeedResult, err error) {
	err = (corepg.UnitOfWork{Pool: r.pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		var owner bool
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.pg_has_role(current_user,c.relowner,'USAGE') FROM pg_catalog.pg_class c WHERE c.oid='bootstrap_runs'::regclass`).Scan(&owner); err != nil {
			return err
		}
		if !owner {
			return service.ErrOwnerRequired
		}
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('poaktu:demo:v1:'||current_schema()||':'||$1,0))`, c.RunID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT name,completed_at FROM bootstrap_runs WHERE left(name,length($1))=$1`, c.Prefix())
		if err != nil {
			return err
		}
		names := []string{}
		for rows.Next() {
			var name string
			if err = rows.Scan(&name, &out.SeededAt); err != nil {
				rows.Close()
				return err
			}
			names = append(names, name)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(names) > 0 {
			if len(names) != 1 || names[0] != c.Marker() {
				return service.ErrConflict
			}
			if err = validateSeed(ctx, tx, c); err != nil {
				return err
			}
			out.Resumed = true
			return nil
		}
		for i, a := range c.Identities {
			role := "USER"
			if a.Role == "operator" {
				role = "DEMO_OPERATOR"
			}
			if _, err = tx.Exec(ctx, `INSERT INTO users(id,email,password_hash,system_role) VALUES($1,$2,$3,$4)`, a.UserID, a.Email, c.PasswordHashes[i], role); err != nil {
				return conflict(err)
			}
			if a.OrganizationID != "" {
				if _, err = tx.Exec(ctx, `INSERT INTO organizations(id,name,kind,is_demo) VALUES($1,$2,'COMPANY',true)`, a.OrganizationID, a.OrganizationName); err != nil {
					return conflict(err)
				}
				if _, err = tx.Exec(ctx, `INSERT INTO memberships(user_id,organization_id,role) VALUES($1,$2,'ORG_OWNER')`, a.UserID, a.OrganizationID); err != nil {
					return conflict(err)
				}
				if _, err = tx.Exec(ctx, `INSERT INTO accounts(id,organization_id) VALUES($1,$2)`, a.AccountID, a.OrganizationID); err != nil {
					return conflict(err)
				}
			}
		}
		var issuance string
		if err = tx.QueryRow(ctx, `SELECT id FROM accounts WHERE system_purpose='DEMO_ISSUANCE'`).Scan(&issuance); err != nil {
			return err
		}
		batch, err := ledger.Post(ctx, tx, c.Operation(), "DEMO_SEED", "Новый изолированный учебный показ; не реальные деньги", issuance, c.Identities[0].AccountID, service.InitialKopecks)
		if err != nil {
			return conflict(err)
		}
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&out.SeededAt); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"run_id": c.RunID, "profile": service.Profile, "batch_id": batch, "amount_kopecks": "50000000", "origin": "demo-cli"})
		if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,type,actor_user_id,request_id,payload,created_at) VALUES($1,'DEMO_RUN_SEEDED',$2,$3,$4,$5)`, service.ID(c.RunID, "seed-audit"), c.Identities[2].UserID, service.ID(c.RunID, "seed-request"), payload, out.SeededAt); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO bootstrap_runs(name,completed_at) VALUES($1,$2)`, c.Marker(), out.SeededAt)
		return err
	})
	return out, err
}
func conflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return service.ErrConflict
	}
	return err
}
func validateSeed(ctx context.Context, tx pgx.Tx, c service.SeedCommand) error {
	for _, a := range c.Identities {
		var email, role, hash string
		err := tx.QueryRow(ctx, `SELECT email,system_role,password_hash FROM users WHERE id=$1`, a.UserID).Scan(&email, &role, &hash)
		expected := "USER"
		if a.Role == "operator" {
			expected = "DEMO_OPERATOR"
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return service.ErrChanged
		}
		if err != nil {
			return err
		}
		if email != a.Email || role != expected || !security.VerifyPassword(hash, c.Password) {
			return service.ErrChanged
		}
		var valid bool
		if a.OrganizationID != "" {
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN organizations o ON o.id=m.organization_id JOIN accounts a ON a.organization_id=o.id WHERE m.user_id=$1 AND m.organization_id=$2 AND m.role='ORG_OWNER' AND o.name=$3 AND o.kind='COMPANY' AND o.is_demo AND a.id=$4 AND a.system_purpose IS NULL AND a.currency='RUB')`, a.UserID, a.OrganizationID, a.OrganizationName, a.AccountID).Scan(&valid); err != nil {
				return err
			}
		} else {
			if err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM memberships WHERE user_id=$1)`, a.UserID).Scan(&valid); err != nil {
				return err
			}
		}
		if !valid {
			return service.ErrChanged
		}
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT count(*)=2 AND count(*) FILTER(WHERE e.account_id=$2 AND e.delta_kopecks=50000000)=1 AND count(*) FILTER(WHERE a.system_purpose='DEMO_ISSUANCE' AND e.delta_kopecks=-50000000)=1 FROM ledger_batches b JOIN ledger_entries e ON e.batch_id=b.id JOIN accounts a ON a.id=e.account_id WHERE b.operation_key=$1 AND b.kind='DEMO_SEED'`, c.Operation(), c.Identities[0].AccountID).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return service.ErrChanged
	}
	return nil
}
