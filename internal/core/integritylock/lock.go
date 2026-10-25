// Package integritylock keeps checkpoint reads separate from live application writes.
package integritylock

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrBusy = errors.New("integrity check requires the API and workers to be stopped")

// HoldShared owns one physical session until release. A connection with an
// unconfirmed session unlock is discarded, never returned to the pool.
func HoldShared(ctx context.Context, pool *pgxpool.Pool) (func(), error) {
	c, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	discard := func() {
		raw := c.Hijack()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = raw.Close(closeCtx)
	}
	var key int64
	if err = c.QueryRow(ctx, `SELECT pg_catalog.hashtextextended('poaktu:integrity-lease:v1:'||current_schema(),0)`).Scan(&key); err != nil {
		discard()
		return nil, err
	}
	// Do not leave a blocking session lock queued when startup is cancelled.
	for {
		var acquired bool
		if err = c.QueryRow(ctx, `SELECT pg_catalog.pg_try_advisory_lock_shared($1)`, key).Scan(&acquired); err != nil {
			discard()
			return nil, err
		}
		if acquired {
			break
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			c.Release()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var unlocked bool
			if err := c.QueryRow(releaseCtx, `SELECT pg_catalog.pg_advisory_unlock_shared($1)`, key).Scan(&unlocked); err != nil || !unlocked {
				discard()
				return
			}
			c.Release()
		})
	}, nil
}

// TryExclusive is transaction scoped and never waits for an active serve lease.
func TryExclusive(ctx context.Context, tx pgx.Tx) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.pg_try_advisory_xact_lock(pg_catalog.hashtextextended('poaktu:integrity-lease:v1:'||current_schema(),0))`).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrBusy
	}
	return nil
}
