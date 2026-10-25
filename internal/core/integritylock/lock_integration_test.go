package integritylock_test

import (
	"context"
	"errors"
	"github.com/Adopten123/digital-ruble-deals/internal/core/integritylock"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
)

func exclusive(t *testing.T, p *pgxpool.Pool, want error) {
	t.Helper()
	tx, err := p.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	err = integritylock.TryExclusive(context.Background(), tx)
	if !errors.Is(err, want) {
		t.Fatalf("exclusive %v want %v", err, want)
	}
}
func TestLeaseRequiresAllServeSessionsToStopAndIsSchemaScoped(t *testing.T) {
	p := testutil.Database(t)
	ctx := context.Background()
	a, err := integritylock.HoldShared(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	b, err := integritylock.HoldShared(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer b()
	exclusive(t, p, integritylock.ErrBusy)
	a()
	a()
	exclusive(t, p, integritylock.ErrBusy)
	// A separate disposable schema does not block this application's checkpoint.
	q := testutil.Database(t)
	exclusive(t, q, nil)
	b()
	exclusive(t, p, nil)
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = integritylock.TryExclusive(ctx, tx); err != nil {
		t.Fatal(err)
	}
	timed, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if release, err := integritylock.HoldShared(timed, p); err == nil {
		release()
		t.Fatal("serve started during checkpoint")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	release, err := integritylock.HoldShared(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	release()
	exclusive(t, p, nil)
}

func TestReplacementServeConnectionReacquiresLease(t *testing.T) {
	p := testutil.Database(t)
	ctx := context.Background()
	cfg := p.Config()
	cfg.AfterConnect = integritylock.HoldConnection
	live, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	c, err := live.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pid := c.Conn().PgConn().PID()
	exclusive(t, p, integritylock.ErrBusy)
	var terminated bool
	if err = p.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatal("terminate isolated serve connection", err)
	}
	if err = c.Ping(ctx); err == nil {
		t.Fatal("terminated connection still usable")
	}
	c.Release()
	var one int
	if err = live.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatal(err)
	}
	exclusive(t, p, integritylock.ErrBusy)
	live.Close()
	// Closing a socket does not acknowledge backend exit; wait for that bounded
	// cleanup instead of assuming another session has already processed EOF.
	for until := time.Now().Add(time.Second); ; {
		tx, e := p.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		e = integritylock.TryExclusive(ctx, tx)
		_ = tx.Rollback(ctx)
		if e == nil {
			break
		}
		if !errors.Is(e, integritylock.ErrBusy) || time.Now().After(until) {
			t.Fatal(e)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
