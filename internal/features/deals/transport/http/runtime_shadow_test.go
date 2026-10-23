package dealshttp_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func shadowRuntime(t *testing.T, f *fixture) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	// The disposable CI cluster is owned by the test superuser. Keep an existing
	// deployment role unchanged, including its login and password attributes.
	_, err := f.pool.Exec(ctx, `DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='poaktu_runtime') THEN CREATE ROLE poaktu_runtime NOLOGIN; END IF; END $$`)
	if err != nil {
		t.Fatal(err)
	}
	var schema string
	if err = f.pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	grants, err := os.ReadFile(filepath.Join(testutil.MigrationsDir(), "..", "deploy", "postgres", "runtime_grants.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, strings.ReplaceAll(string(grants), "SCHEMA public", "SCHEMA "+pgx.Identifier{schema}.Sanitize())); err != nil {
		t.Fatal(err)
	}
	cfg := f.pool.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, e := conn.Exec(ctx, `SET ROLE poaktu_runtime`)
		return e
	}
	runtime, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return runtime, pgx.Identifier{schema}.Sanitize()
}

func TestFinancialGuardsIgnoreTemporaryShadowTables(t *testing.T) {
	for _, probe := range []string{"balance projection", "unbalanced ledger batch", "funded without reserve"} {
		t.Run(probe, func(t *testing.T) {
			f := reserveFixture(t)
			runtime, schema := shadowRuntime(t, f)
			customerID := accountID(t, f, f.customer.org)
			var dealID string
			var dealVersion int64
			if probe == "funded without reserve" {
				deal := readyForReserve(t, f, "shadow-ready-key1", nil)
				dealID, dealVersion = deal.ID, deal.DealVersion
			}
			tx, err := runtime.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			exec := func(query string, args ...any) {
				t.Helper()
				if _, e := tx.Exec(context.Background(), query, args...); e != nil {
					t.Fatal(e)
				}
			}
			var mutation string
			switch probe {
			case "balance projection":
				exec(`CREATE TEMP TABLE ledger_entries ON COMMIT DROP AS SELECT * FROM ` + schema + `.ledger_entries`)
				exec(`INSERT INTO pg_temp.ledger_entries(id,batch_id,account_id,delta_kopecks) VALUES('shadow-extra','not-a-real-batch',$1,1)`, customerID)
				exec(`CREATE TEMP TABLE reserves ON COMMIT DROP AS SELECT * FROM ` + schema + `.reserves`)
				mutation = `UPDATE ` + schema + `.accounts SET total_kopecks=total_kopecks+1 WHERE id=$1`
			case "unbalanced ledger batch":
				var issuanceID string
				if err = f.pool.QueryRow(context.Background(), `SELECT id FROM accounts WHERE system_purpose='DEMO_ISSUANCE'`).Scan(&issuanceID); err != nil {
					t.Fatal(err)
				}
				exec(`CREATE TEMP TABLE ledger_entries ON COMMIT DROP AS SELECT * FROM ` + schema + `.ledger_entries`)
				// Fake a balanced batch while preserving the apparent per-account sum.
				exec(`INSERT INTO pg_temp.ledger_entries(id,batch_id,account_id,delta_kopecks) VALUES('fake-pos','shadow-batch',$1,10),('fake-neg','shadow-batch',$2,-10),('fake-adjust','not-a-real-batch',$2,9)`, customerID, issuanceID)
				exec(`INSERT INTO ` + schema + `.ledger_batches(id,operation_key,kind,reason) VALUES('shadow-batch','shadow-operation','DEMO_CREDIT','security regression')`)
				exec(`INSERT INTO `+schema+`.ledger_entries(id,batch_id,account_id,delta_kopecks) VALUES('real-pos','shadow-batch',$1,10),('real-neg','shadow-batch',$2,-1)`, customerID, issuanceID)
				exec(`UPDATE `+schema+`.accounts SET total_kopecks=total_kopecks+CASE WHEN id=$1 THEN 10 ELSE -1 END WHERE id IN($1,$2)`, customerID, issuanceID)
			case "funded without reserve":
				exec(`CREATE TEMP TABLE reserves(id text,deal_id text,deal_version bigint,status text) ON COMMIT DROP`)
				exec(`INSERT INTO pg_temp.reserves VALUES('fake-reserve',$1,$2,'ACTIVE')`, dealID, dealVersion)
				mutation = fmt.Sprintf(`UPDATE %s.deals SET status='FUNDED',first_reserved_at=clock_timestamp() WHERE id=$1`, schema)
				customerID = dealID
			}
			if mutation != "" {
				_, err = tx.Exec(context.Background(), mutation, customerID)
			}
			if err == nil {
				_, err = tx.Exec(context.Background(), `SET CONSTRAINTS ALL IMMEDIATE`)
			}
			if err == nil {
				t.Fatal("runtime bypassed a financial guard using temporary shadow tables")
			}
			var pgErr interface{ SQLState() string }
			if !errors.As(err, &pgErr) || (pgErr.SQLState() != "23514" && pgErr.SQLState() != "P0001") {
				t.Fatalf("expected a financial constraint failure, got %v", err)
			}
		})
	}
}
