package bootstrap_test

import (
	"context"
	"testing"

	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/features/bootstrap"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5"
)

func TestBootstrapRestartAndLedgerConstraints(t *testing.T) {
	pool := testutil.Database(t)
	ctx := context.Background()
	if err := bootstrap.Ready(ctx, pool); err == nil {
		t.Fatal("unseeded database ready")
	}
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- bootstrap.Run(ctx, pool) }()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := bootstrap.Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Ready(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := corepg.Migrate(ctx, corepg.UnitOfWork{Pool: pool}, testutil.MigrationsDir()); err != nil {
		t.Fatal(err)
	}
	var users, orgs, entries, batches, members int
	var customer, supplier, sum int64
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM organizations),(SELECT count(*) FROM ledger_entries),(SELECT count(*) FROM ledger_batches),(SELECT count(*) FROM memberships)`).Scan(&users, &orgs, &entries, &batches, &members); err != nil {
		t.Fatal(err)
	}
	if users != 3 || orgs != 2 || entries != 2 || batches != 1 || members != 2 {
		t.Fatalf("seed repeated or roles wrong: %d %d %d %d %d", users, orgs, entries, batches, members)
	}
	if err := pool.QueryRow(ctx, `SELECT (SELECT a.total_kopecks FROM accounts a JOIN memberships m ON m.organization_id=a.organization_id JOIN users u ON u.id=m.user_id WHERE u.email='customer@demo.poaktu.local'),(SELECT a.total_kopecks FROM accounts a JOIN memberships m ON m.organization_id=a.organization_id JOIN users u ON u.id=m.user_id WHERE u.email='supplier@demo.poaktu.local'),(SELECT sum(delta_kopecks) FROM ledger_entries)`).Scan(&customer, &supplier, &sum); err != nil {
		t.Fatal(err)
	}
	if customer != 50000000 || supplier != 0 || sum != 0 {
		t.Fatalf("bad seed money: %d %d %d", customer, supplier, sum)
	}
	for _, sql := range []string{`INSERT INTO accounts(id) VALUES('invalid-owner')`, `UPDATE accounts SET reserved_kopecks=total_kopecks+1 WHERE organization_id IS NOT NULL`, `UPDATE ledger_entries SET delta_kopecks=1`, `DELETE FROM ledger_entries`} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatalf("invalid operation accepted: %s", sql)
		}
	}
	for _, mode := range []string{"unbalanced", "three-entries"} {
		t.Run(mode, func(t *testing.T) {
			err := (corepg.UnitOfWork{Pool: pool}).WithinTx(ctx, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `INSERT INTO ledger_batches(id,operation_key,kind,reason) VALUES($1,$1,'DEMO_SEED','constraint test')`, mode); err != nil {
					return err
				}
				if mode == "unbalanced" {
					_, err := tx.Exec(ctx, `INSERT INTO ledger_entries(id,batch_id,account_id,delta_kopecks) SELECT 'bad-'||id,$1,id,1 FROM accounts LIMIT 2`, mode)
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO ledger_entries(id,batch_id,account_id,delta_kopecks) SELECT 'bad-'||id,$1,id,CASE row_number() OVER () WHEN 1 THEN 1 WHEN 2 THEN 1 ELSE -2 END FROM accounts`, mode)
				return err
			})
			if err == nil {
				t.Fatal("invalid batch committed")
			}
		})
	}
}

func TestReadinessRequiresTheCurrentSettlementSchemaAndAllFeatureTables(t *testing.T) {
	pool := testutil.Database(t)
	ctx := context.Background()
	if err := bootstrap.Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Ready(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var checksum string
	if err := pool.QueryRow(ctx, "DELETE FROM schema_migrations WHERE version=$1 RETURNING checksum", corepg.SchemaVersion).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Ready(ctx, pool); err == nil {
		t.Fatal("old migration markers allowed payment API readiness")
	}
	if _, err := pool.Exec(ctx, "INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)", corepg.SchemaVersion, checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "ALTER TABLE edo_deliveries RENAME TO unavailable_edo_deliveries"); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Ready(ctx, pool); err == nil {
		t.Fatal("missing delivery table allowed readiness")
	}
	if _, err := pool.Exec(ctx, "ALTER TABLE unavailable_edo_deliveries RENAME TO edo_deliveries"); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Ready(ctx, pool); err != nil {
		t.Fatal(err)
	}
}
