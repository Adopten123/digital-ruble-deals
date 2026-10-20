package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

const SchemaVersion = "001_foundation.sql"

func Migrate(ctx context.Context, uow UnitOfWork, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return errors.New("no SQL migrations found")
	}
	return uow.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7061657475001)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
			return err
		}
		for _, path := range files {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			version := filepath.Base(path)
			sum := sha256.Sum256(body)
			checksum := hex.EncodeToString(sum[:])
			var previous string
			err = tx.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE version=$1`, version).Scan(&previous)
			if err == nil {
				if previous != checksum {
					return fmt.Errorf("applied migration %s checksum differs", version)
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if strings.TrimSpace(string(body)) == "" {
				return fmt.Errorf("empty migration %s", version)
			}
			if _, err = tx.Exec(ctx, string(body)); err != nil {
				return fmt.Errorf("migration %s: %w", version, err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)`, version, checksum); err != nil {
				return err
			}
		}
		return nil
	})
}
