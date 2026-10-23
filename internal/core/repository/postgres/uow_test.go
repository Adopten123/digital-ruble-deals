package postgres_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5"
)

func TestUnitOfWorkCommitAndRollback(t *testing.T) {
	pool := testutil.Database(t)
	uow := corepg.UnitOfWork{Pool: pool}
	ctx := context.Background()
	if err := uow.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO organizations(id,name,kind) VALUES('committed','Committed','COMPANY')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("abort whole operation")
	err := uow.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO organizations(id,name,kind) VALUES('rolled-back','Rolled Back','COMPANY')`); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatal("callback failure lost", err)
	}
	var committed, rolledback bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM organizations WHERE id='committed'),EXISTS(SELECT 1 FROM organizations WHERE id='rolled-back')`).Scan(&committed, &rolledback); err != nil {
		t.Fatal(err)
	}
	if !committed || rolledback {
		t.Fatal("transaction was partial")
	}
}

func TestAppliedMigrationCannotChange(t *testing.T) {
	pool := testutil.Database(t)
	body, err := os.ReadFile(filepath.Join(testutil.MigrationsDir(), corepg.SchemaVersion))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	body = append(body, []byte("\n-- changed after application\n")...)
	if err := os.WriteFile(filepath.Join(dir, corepg.SchemaVersion), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := corepg.Migrate(context.Background(), corepg.UnitOfWork{Pool: pool}, dir); err == nil {
		t.Fatal("changed migration was silently accepted")
	}
}
