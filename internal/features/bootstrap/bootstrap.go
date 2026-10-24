// Package bootstrap initializes only the local, explicitly marked test environment.
package bootstrap

import (
	"context"
	"errors"

	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const Name = "foundation_v1"
const Password = "PoaktuDemo2026!"

func Run(ctx context.Context, pool *pgxpool.Pool) error {
	passwordHash, err := security.HashPassword(Password)
	if err != nil {
		return err
	}
	return (corepg.UnitOfWork{Pool: pool}).WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7061657475002)`); err != nil {
			return err
		}
		// Add the fixed test-debit destination on upgrade without reseeding money.
		if _, err := tx.Exec(ctx, `INSERT INTO accounts(id,system_purpose) VALUES('poaktu-demo-debits-v1','DEMO_DEBITS') ON CONFLICT(system_purpose) DO NOTHING`); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM bootstrap_runs WHERE name=$1)`, Name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		newID := func() (string, error) { return security.ID() }
		issuance, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO accounts(id,system_purpose,total_kopecks) VALUES($1,'DEMO_ISSUANCE',0)`, issuance); err != nil {
			return err
		}
		var customerAccount string
		fixtures := []struct{ email, name, role string }{{"customer@demo.poaktu.local", "Демо Заказчик", "USER"}, {"supplier@demo.poaktu.local", "Демо Типография", "USER"}, {"operator@demo.poaktu.local", "", "DEMO_OPERATOR"}}
		for index, f := range fixtures {
			userID, err := newID()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO users(id,email,password_hash,system_role) VALUES($1,$2,$3,$4)`, userID, f.email, passwordHash, f.role); err != nil {
				return err
			}
			if f.role == "DEMO_OPERATOR" {
				continue
			}
			orgID, err := newID()
			if err != nil {
				return err
			}
			accountID, err := newID()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO organizations(id,name,kind,is_demo) VALUES($1,$2,'COMPANY',true)`, orgID, f.name); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO memberships(user_id,organization_id,role) VALUES($1,$2,'ORG_OWNER')`, userID, orgID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO accounts(id,organization_id) VALUES($1,$2)`, accountID, orgID); err != nil {
				return err
			}
			if index == 0 {
				customerAccount = accountID
			}
		}
		batchID, err := newID()
		if err != nil {
			return err
		}
		positiveID, err := newID()
		if err != nil {
			return err
		}
		negativeID, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ledger_batches(id,operation_key,kind,reason) VALUES($1,'bootstrap:foundation_v1:initial-credit','DEMO_SEED','Начальные тестовые средства; не реальные деньги')`, batchID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ledger_entries(id,batch_id,account_id,delta_kopecks) VALUES($1,$2,$3,50000000),($4,$2,$5,-50000000)`, positiveID, batchID, customerAccount, negativeID, issuance); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET total_kopecks=CASE WHEN id=$1 THEN 50000000 ELSE -50000000 END WHERE id IN ($1,$2)`, customerAccount, issuance); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO bootstrap_runs(name) VALUES($1)`, Name)
		return err
	})
}

func Ready(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	var ready bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1) AND EXISTS(SELECT 1 FROM bootstrap_runs WHERE name=$2)
 AND to_regclass('users') IS NOT NULL AND to_regclass('sessions') IS NOT NULL AND to_regclass('accounts') IS NOT NULL
 AND to_regclass('deals') IS NOT NULL AND to_regclass('deal_versions') IS NOT NULL AND to_regclass('idempotency_keys') IS NOT NULL
 AND to_regclass('audit_events') IS NOT NULL AND to_regclass('outbox') IS NOT NULL
 AND to_regclass('documents') IS NOT NULL AND to_regclass('document_versions') IS NOT NULL AND to_regclass('deal_agreements') IS NOT NULL
 AND to_regclass('reserves') IS NOT NULL AND to_regclass('operator_command_results') IS NOT NULL
 AND to_regclass('document_signatures') IS NOT NULL AND to_regclass('edo_outbox') IS NOT NULL
 AND to_regclass('deal_disputes') IS NOT NULL AND to_regclass('cancellation_requests') IS NOT NULL AND to_regclass('cancellation_decisions') IS NOT NULL
 AND to_regclass('event_inbox') IS NOT NULL AND to_regclass('settlements') IS NOT NULL AND to_regclass('edo_deliveries') IS NOT NULL
 AND to_regclass('edo_command_results') IS NOT NULL
 AND EXISTS(SELECT 1 FROM accounts WHERE system_purpose='DEMO_DEBITS')`, corepg.SchemaVersion, Name).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("schema or bootstrap is incomplete")
	}
	return nil
}
