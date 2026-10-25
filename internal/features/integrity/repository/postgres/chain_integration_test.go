package postgres_test

import (
	"bytes"
	"context"
	"fmt"
	format "github.com/Adopten123/digital-ruble-deals/internal/core/integrityformat"
	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	"github.com/Adopten123/digital-ruble-deals/internal/features/bootstrap"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func chainDatabase(t *testing.T, legacy bool) *pgxpool.Pool {
	t.Helper()
	if !legacy {
		return testutil.Database(t)
	}
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL absent")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	id, e := security.ID()
	if e != nil {
		t.Fatal(e)
	}
	schema := "chain_" + strings.ToLower(strings.ReplaceAll(id, "-", "_"))
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+quoted); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	dir := t.TempDir()
	files, e := filepath.Glob(filepath.Join(testutil.MigrationsDir(), "*.sql"))
	if e != nil {
		t.Fatal(e)
	}
	for _, file := range files {
		if filepath.Base(file) >= "010" {
			continue
		}
		raw, e := os.ReadFile(file)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, filepath.Base(file)), raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e = corepg.Migrate(ctx, corepg.UnitOfWork{Pool: pool}, dir); e != nil {
		t.Fatal(e)
	}
	return pool
}
func chainActor(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, e := pool.Exec(context.Background(), `INSERT INTO users(id,email,password_hash) VALUES('actor','chain-actor@example.test','unused')`); e != nil {
		t.Fatal(e)
	}
}
func chainInsert(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, id string) error {
	_, e := q.Exec(ctx, `INSERT INTO audit_events(id,type,actor_user_id,request_id,payload,created_at) VALUES($1,'CHAIN_TEST','actor',$1,'{"b":1.00,"a":"я"}','2000-01-01T00:00:00.123456Z')`, id)
	return e
}
func chainCount(t *testing.T, pool *pgxpool.Pool) (int64, int64) {
	t.Helper()
	var seq, count int64
	if e := pool.QueryRow(context.Background(), `SELECT last_seq,(SELECT count(*) FROM audit_chain_entries) FROM audit_chain_head`).Scan(&seq, &count); e != nil {
		t.Fatal(e)
	}
	return seq, count
}
func chainVerify(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var log string
	var seq, count int64
	var head []byte
	if e := pool.QueryRow(ctx, `SELECT log_id,last_seq,head_hash,(SELECT count(*) FROM audit_events) FROM audit_chain_head`).Scan(&log, &seq, &head, &count); e != nil {
		t.Fatal(e)
	}
	previous := format.Genesis(log)
	rows, e := pool.Query(ctx, `SELECT c.sequence,c.log_id,c.event_hash,c.previous_hash,c.entry_hash,a.id,a.deal_id,a.deal_version,a.type,a.actor_user_id,a.actor_organization_id,a.actor_source,a.request_id,a.payload,a.created_at FROM audit_chain_entries c LEFT JOIN audit_events a ON a.id=c.audit_id ORDER BY c.sequence`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	var n int64
	for rows.Next() {
		var no int64
		var rowLog string
		var evHash, prev, link []byte
		var event format.Event
		if e = rows.Scan(&no, &rowLog, &evHash, &prev, &link, &event.ID, &event.DealID, &event.DealVersion, &event.Type, &event.ActorUserID, &event.ActorOrganizationID, &event.ActorSource, &event.RequestID, &event.Payload, &event.CreatedAt); e != nil {
			t.Fatal(e)
		}
		n++
		got, e := format.EventHash(event)
		if e != nil {
			t.Fatal(e)
		}
		want := format.LinkHash(log, no, previous, got)
		if no != n || rowLog != log || !bytes.Equal(evHash, got[:]) || !bytes.Equal(prev, previous[:]) || !bytes.Equal(link, want[:]) {
			t.Fatalf("broken chain row %d", n)
		}
		previous = want
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	if n != seq || n != count || !bytes.Equal(head, previous[:]) {
		t.Fatalf("head/count mismatch: %d %d %d", n, seq, count)
	}
}
func TestIntegrityChainCrossLanguageCanonicalJSONEventAndLinks(t *testing.T) {
	pool := chainDatabase(t, false)
	chainActor(t, pool)
	ctx := context.Background()
	for _, raw := range []string{`null`, `[true,false,null,"я",0,-0.00]`, `{"long":1.200,"b":1e-4,"a":[{},[],"\"\\\n"]}`, `{"я":9223372036854775807,"a":-9223372036854775808,"é":"é","a long key":123e2}`, `1e1000`, `-1e-1000`} {
		got, e := format.CanonicalJSON([]byte(raw))
		if e != nil {
			t.Fatal(e)
		}
		var sql []byte
		if e = pool.QueryRow(ctx, `SELECT integrity_json_v1($1::jsonb)`, raw).Scan(&sql); e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(got, sql) {
			t.Fatalf("SQL / Go canonical mismatch %s\n%x\n%x", raw, got, sql)
		}
	}
	if e := chainInsert(ctx, pool, "source"); e != nil {
		t.Fatal(e)
	}
	chainVerify(t, pool)
	for _, stamp := range []string{"1960-01-02T03:04:05.123456Z", "2000-01-01T05:30:00.000001+05:30"} {
		var event format.Event
		var sqlHash []byte
		if e := pool.QueryRow(ctx, `SELECT a.id,a.deal_id,a.deal_version,a.type,a.actor_user_id,a.actor_organization_id,a.actor_source,a.request_id,a.payload,$1::timestamptz,integrity_event_hash_v1(ROW(a.id,a.deal_id,a.deal_version,a.type,a.actor_user_id,a.actor_organization_id,a.request_id,a.payload,$1::timestamptz,a.actor_source)::audit_events) FROM audit_events a WHERE a.id='source'`, stamp).Scan(&event.ID, &event.DealID, &event.DealVersion, &event.Type, &event.ActorUserID, &event.ActorOrganizationID, &event.ActorSource, &event.RequestID, &event.Payload, &event.CreatedAt, &sqlHash); e != nil {
			t.Fatal(e)
		}
		goHash, e := format.EventHash(event)
		if e != nil || !bytes.Equal(sqlHash, goHash[:]) {
			t.Fatalf("time hash mismatch %v", e)
		}
	}
	var log string
	var genesis, link []byte
	if e := pool.QueryRow(ctx, `SELECT log_id,integrity_genesis_v1(log_id),integrity_link_hash_v1(log_id,7,head_hash,head_hash) FROM audit_chain_head`).Scan(&log, &genesis, &link); e != nil {
		t.Fatal(e)
	}
	g := format.Genesis(log)
	if !bytes.Equal(genesis, g[:]) {
		t.Fatal("genesis mismatch")
	}
	var head []byte
	if e := pool.QueryRow(ctx, `SELECT head_hash FROM audit_chain_head`).Scan(&head); e != nil {
		t.Fatal(e)
	}
	h := [32]byte(head)
	l := format.LinkHash(log, 7, h, h)
	if !bytes.Equal(link, l[:]) {
		t.Fatal("link mismatch")
	}
}
func TestIntegrityChainDeferredAtomicRollbackAndFailedCommitLeaveNoGaps(t *testing.T) {
	pool := chainDatabase(t, false)
	chainActor(t, pool)
	ctx := context.Background()
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = chainInsert(ctx, tx, "rolled-back"); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM audit_chain_entries`).Scan(&count); e != nil || count != 0 {
		t.Fatal("append was not deferred", e)
	}
	if e = tx.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	chainVerify(t, pool)
	tx, e = pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = chainInsert(ctx, tx, "first-in-failing-tx"); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(id,type,actor_user_id,request_id,payload,created_at) VALUES('invalid-time','CHAIN_TEST','actor','r','{}','10000-01-01T00:00:00Z')`); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e == nil {
		t.Fatal("unencodable source committed")
	}
	if seq, count := chainCount(t, pool); seq != 0 || count != 0 {
		t.Fatal("partial failed append survived")
	}
	chainVerify(t, pool)
	if e = chainInsert(ctx, pool, "committed"); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, `INSERT INTO audit_events SELECT * FROM audit_events WHERE id='committed' ON CONFLICT(id) DO NOTHING`); e != nil {
		t.Fatal(e)
	}
	if seq, count := chainCount(t, pool); seq != 1 || count != 1 {
		t.Fatal("duplicate source advanced head")
	}
	chainVerify(t, pool)
}
func TestIntegrityChainConcurrentIndependentTransactionsSerialize(t *testing.T) {
	pool := chainDatabase(t, false)
	chainActor(t, pool)
	var wg sync.WaitGroup
	errors := make(chan error, 24)
	for i := range 24 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errors <- chainInsert(context.Background(), pool, fmt.Sprintf("parallel-%02d", i))
		}(i)
	}
	wg.Wait()
	close(errors)
	for e := range errors {
		if e != nil {
			t.Fatal(e)
		}
	}
	if seq, count := chainCount(t, pool); seq != 24 || count != 24 {
		t.Fatal("lost sequence")
	}
	chainVerify(t, pool)
}
func TestIntegrityChain009UpgradePreservesOldRowsAndReconstructedBoundary(t *testing.T) {
	pool := chainDatabase(t, true)
	ctx := context.Background()
	if e := bootstrap.Run(ctx, pool); e != nil {
		t.Fatal(e)
	}
	chainActor(t, pool)
	for _, id := range []string{"z", "A", "b"} {
		if e := chainInsert(ctx, pool, id); e != nil {
			t.Fatal(e)
		}
	}
	tables, e := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() AND tablename<>'schema_migrations' ORDER BY tablename`)
	if e != nil {
		t.Fatal(e)
	}
	var names []string
	for tables.Next() {
		var n string
		if e = tables.Scan(&n); e != nil {
			t.Fatal(e)
		}
		names = append(names, n)
	}
	tables.Close()
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, name := range names {
			var raw string
			if e := pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+pgx.Identifier{name}.Sanitize()+` t`).Scan(&raw); e != nil {
				t.Fatal(e)
			}
			out[name] = raw
		}
		return out
	}
	before := snapshot()
	for range 2 {
		if e = corepg.Migrate(ctx, corepg.UnitOfWork{Pool: pool}, testutil.MigrationsDir()); e != nil {
			t.Fatal(e)
		}
		if e = bootstrap.Run(ctx, pool); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(before, snapshot()) {
			t.Fatal("010 rewrote legacy facts")
		}
	}
	var legacy int64
	var order []string
	if e = pool.QueryRow(ctx, `SELECT legacy_count,ARRAY(SELECT audit_id FROM audit_chain_entries ORDER BY sequence) FROM audit_chain_head`).Scan(&legacy, &order); e != nil {
		t.Fatal(e)
	}
	if legacy != 3 || !reflect.DeepEqual(order, []string{"A", "b", "z"}) {
		t.Fatalf("legacy boundary/order %d %v", legacy, order)
	}
	chainVerify(t, pool)
	if _, e = pool.Exec(ctx, `INSERT INTO audit_events(id,type,actor_user_id,request_id,payload,created_at) VALUES('new-old-time','CHAIN_TEST','actor','r','{}','1900-01-01')`); e != nil {
		t.Fatal(e)
	}
	var last string
	if e = pool.QueryRow(ctx, `SELECT audit_id FROM audit_chain_entries ORDER BY sequence DESC LIMIT 1`).Scan(&last); e != nil || last != "new-old-time" {
		t.Fatal("new append reordered historical sequence", e)
	}
	chainVerify(t, pool)
}
func TestIntegrityChainRuntimePrivilegesAndTemporaryShadowResistance(t *testing.T) {
	pool := chainDatabase(t, false)
	chainActor(t, pool)
	ctx := context.Background()
	if _, e := pool.Exec(ctx, `DO $$BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='poaktu_runtime') THEN CREATE ROLE poaktu_runtime NOLOGIN;END IF;END$$`); e != nil {
		t.Fatal(e)
	}
	var schema string
	if e := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); e != nil {
		t.Fatal(e)
	}
	quoted := pgx.Identifier{schema}.Sanitize()
	raw, e := os.ReadFile(filepath.Join(testutil.MigrationsDir(), "..", "deploy", "postgres", "runtime_grants.sql"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, strings.ReplaceAll(string(raw), "SCHEMA public", "SCHEMA "+quoted)); e != nil {
		t.Fatal(e)
	}
	cfg := pool.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error { _, e := c.Exec(ctx, `SET ROLE poaktu_runtime`); return e }
	runtime, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer runtime.Close()
	for _, query := range []string{`UPDATE audit_chain_head SET last_seq=999`, `DELETE FROM audit_chain_entries`, `INSERT INTO audit_chain_entries(sequence,log_id,audit_id,event_hash,previous_hash,entry_hash) SELECT 99,log_id,'x',head_hash,head_hash,head_hash FROM audit_chain_head`, `SELECT append_audit_chain_entry_v1(a) FROM audit_events a`} {
		if _, e = runtime.Exec(ctx, query); e == nil {
			t.Fatalf("runtime gained chain mutation: %s", query)
		}
	}
	conn, e := runtime.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Release()
	if _, e = conn.Exec(ctx, `CREATE TEMP TABLE audit_chain_head(singleton boolean,last_seq bigint,head_hash bytea); CREATE TEMP TABLE audit_chain_entries(sequence bigint)`); e != nil {
		t.Fatal(e)
	}
	if _, e = conn.Exec(ctx, `SELECT `+quoted+`.lock_audit_chain_head()`); e != nil {
		t.Fatal(e)
	}
	if _, e = conn.Exec(ctx, `INSERT INTO `+quoted+`.audit_events(id,type,actor_user_id,request_id,payload) VALUES('runtime-real','CHAIN_TEST','actor','r','{}')`); e != nil {
		t.Fatal(e)
	}
	chainVerify(t, pool)
	if seq, count := chainCount(t, pool); seq != 1 || count != 1 {
		t.Fatal("TEMP tables replaced real head")
	}
	for _, query := range []string{`UPDATE audit_chain_entries SET entry_hash=repeat('a',32)::bytea`, `DELETE FROM audit_chain_entries`, `UPDATE audit_chain_head SET log_id='00000000-0000-0000-0000-000000000000'`, `DELETE FROM audit_chain_head`} {
		if _, e = pool.Exec(ctx, query); e == nil {
			t.Fatalf("unguarded chain mutation: %s", query)
		}
	}
}
func TestIntegrityChainExplicitHeadLockWaitsWithoutPublishingAudit(t *testing.T) {
	pool := chainDatabase(t, false)
	chainActor(t, pool)
	ctx := context.Background()
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT lock_audit_chain_head()`); e != nil {
		t.Fatal(e)
	}
	waiter, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer waiter.Rollback(ctx)
	waitCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, e = waiter.Exec(waitCtx, `SELECT lock_audit_chain_head()`); e == nil {
		t.Fatal("head lock did not wait")
	}
	if seq, count := chainCount(t, pool); seq != 0 || count != 0 {
		t.Fatal("lock mutated audit")
	}
}
