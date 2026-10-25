package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/features/bootstrap"
	dealrepo "github.com/Adopten123/digital-ruble-deals/internal/features/deals/repository/postgres"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	repo "github.com/Adopten123/digital-ruble-deals/internal/features/demorun/repository/postgres"
	demo "github.com/Adopten123/digital-ruble-deals/internal/features/demorun/service"
	integrity "github.com/Adopten123/digital-ruble-deals/internal/features/integrity/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/features/settlement/ledger"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const password = "DemoRunOnly2026!"

var ctx = context.Background()

func fixture(t *testing.T) (*pgxpool.Pool, *demo.Service) {
	t.Helper()
	p := testutil.Database(t)
	if err := bootstrap.Run(ctx, p); err != nil {
		t.Fatal(err)
	}
	return p, demo.New(repo.New(p), deals.New(dealrepo.New(p), make([]byte, 32)), make([]byte, 32))
}
func state(t *testing.T, p *pgxpool.Pool) string {
	t.Helper()
	s, err := integrity.New(p, nil).Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !s.ChainValid {
		t.Fatal(s.ChainIssues)
	}
	return s.StateHash
}
func scalar(t *testing.T, p *pgxpool.Pool, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := p.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func monetary(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	if scalar(t, p, `SELECT count(*) FROM accounts a WHERE a.total_kopecks::numeric<>(SELECT COALESCE(sum(e.delta_kopecks::numeric),0) FROM ledger_entries e WHERE e.account_id=a.id) OR a.reserved_kopecks::numeric<>(SELECT COALESCE(sum(r.amount_kopecks::numeric),0) FROM reserves r WHERE r.account_id=a.id AND r.status='ACTIVE')`) != 0 || scalar(t, p, `SELECT sum(total_kopecks::numeric)::bigint FROM accounts`) != 0 {
		t.Fatal("monetary projections changed")
	}
}
func TestDemoFreshReplayAndSeparateRuns(t *testing.T) {
	p, s := fixture(t)
	var original string
	if err := p.QueryRow(ctx, `SELECT id FROM accounts WHERE organization_id=(SELECT organization_id FROM memberships WHERE user_id=(SELECT id FROM users WHERE email='customer@demo.poaktu.local'))`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	a, err := s.Create(ctx, "first-show", password)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "DEMO_READY" || a.Resumed || len(a.Identities) != 3 || len(a.Deals) != 4 || a.SeededAt.IsZero() || a.DeadlineAt.Sub(a.SeededAt).Hours() != 336 {
		t.Fatalf("bad manifest: %+v", a)
	}
	if scalar(t, p, `SELECT total_kopecks FROM accounts WHERE id=$1`, original) != 50000000 || scalar(t, p, `SELECT total_kopecks FROM accounts WHERE id=$1`, a.Identities[0].AccountID) != 50000000 || scalar(t, p, `SELECT total_kopecks FROM accounts WHERE id=$1`, a.Identities[1].AccountID) != 0 {
		t.Fatal("shared or new balances wrong")
	}
	if scalar(t, p, `SELECT count(*) FROM reserves`)+scalar(t, p, `SELECT count(*) FROM document_signatures`)+scalar(t, p, `SELECT count(*) FROM deal_agreements`)+scalar(t, p, `SELECT count(*) FROM settlements`)+scalar(t, p, `SELECT count(*) FROM document_versions`) != 0 {
		t.Fatal("setup impersonated a business action")
	}
	before := state(t, p)
	again, err := s.Create(ctx, "first-show", password)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Resumed || !reflect.DeepEqual(a.Deals, again.Deals) || !reflect.DeepEqual(a.Identities, again.Identities) || !a.SeededAt.Equal(again.SeededAt) || before != state(t, p) {
		t.Fatal("retry changed persisted facts")
	}
	b, err := s.Create(ctx, "second-show", password)
	if err != nil {
		t.Fatal(err)
	}
	if b.Identities[0].AccountID == a.Identities[0].AccountID || b.Deals[0].ID == a.Deals[0].ID {
		t.Fatal("runs are not separate")
	}
	actor := deals.Actor{UserID: a.Identities[0].UserID, OrganizationID: a.Identities[0].OrganizationID}
	if _, err = dealrepo.New(p).Get(ctx, actor, b.Deals[0].ID); !errors.Is(err, deals.ErrNotFound) {
		t.Fatal("another run became readable", err)
	}
	for _, d := range a.Deals {
		card, err := dealrepo.New(p).Get(ctx, actor, d.ID)
		if err != nil || card.Status != "DRAFT" || card.AmountKopecks != d.InitialAmountKopecks || card.Document != nil {
			t.Fatal("draft not created through domain rules", err)
		}
	}
	if scalar(t, p, `SELECT count(*) FROM audit_events WHERE type='DEMO_RUN_SEEDED'`) != 2 || scalar(t, p, `SELECT count(*) FROM ledger_batches WHERE kind='DEMO_SEED'`) != 3 {
		t.Fatal("duplicate initial seed")
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), password) || strings.Contains(string(raw), "password_hash") {
		t.Fatal("manifest exposes credentials")
	}
	monetary(t, p)
}
func TestDemoConflictDoesNotWrite(t *testing.T) {
	p, s := fixture(t)
	if _, err := s.Create(ctx, "same-show", password); err != nil {
		t.Fatal(err)
	}
	before := state(t, p)
	if _, err := s.Create(ctx, "same-show", "OtherDemoOnly2026!"); !errors.Is(err, demo.ErrConflict) {
		t.Fatal(err)
	}
	changed := demo.New(repo.New(p), deals.New(dealrepo.New(p), make([]byte, 32)), []byte(strings.Repeat("x", 32)))
	if _, err := changed.Create(ctx, "same-show", password); !errors.Is(err, demo.ErrConflict) {
		t.Fatal("changed app key reseeded", err)
	}
	if state(t, p) != before {
		t.Fatal("conflict wrote facts")
	}
	monetary(t, p)
}
func TestDemoConcurrentReplayCreditsOnce(t *testing.T) {
	p, s := fixture(t)
	const n = 6
	results := make([]demo.Manifest, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], errs[i] = s.Create(ctx, "parallel-show", password) }()
	}
	wg.Wait()
	created := 0
	for i, m := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if !m.Resumed {
			created++
		}
		if !reflect.DeepEqual(m.Deals, results[0].Deals) {
			t.Fatal("different concurrent deals")
		}
	}
	if created != 1 || scalar(t, p, `SELECT count(*) FROM deals`) != 4 || scalar(t, p, `SELECT count(*) FROM audit_events WHERE type='DEMO_RUN_SEEDED'`) != 1 || scalar(t, p, `SELECT count(*) FROM ledger_batches WHERE operation_key='demo_seed:v1:parallel-show'`) != 1 {
		t.Fatal("concurrent setup duplicated facts")
	}
	monetary(t, p)
}
func TestDemoCollisionAndIssuanceOverflowRollback(t *testing.T) {
	t.Run("email_collision", func(t *testing.T) {
		p, s := fixture(t)
		email := demo.Identities("collision-show")[1].Email
		if _, err := p.Exec(ctx, `INSERT INTO users(id,email,password_hash) VALUES('preexisting',$1,'unused')`, email); err != nil {
			t.Fatal(err)
		}
		before := state(t, p)
		if _, err := s.Create(ctx, "collision-show", password); !errors.Is(err, demo.ErrConflict) {
			t.Fatal(err)
		}
		if state(t, p) != before {
			t.Fatal("partial setup survived email collision")
		}
		monetary(t, p)
	})
	t.Run("issuer_overflow", func(t *testing.T) {
		p, s := fixture(t)
		err := (corepg.UnitOfWork{Pool: p}).WithinTx(ctx, func(tx pgx.Tx) error {
			var from, to string
			if err := tx.QueryRow(ctx, `SELECT id FROM accounts WHERE system_purpose='DEMO_ISSUANCE'`).Scan(&from); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT id FROM accounts WHERE organization_id=(SELECT organization_id FROM memberships WHERE user_id=(SELECT id FROM users WHERE email='customer@demo.poaktu.local'))`).Scan(&to); err != nil {
				return err
			}
			_, err := ledger.Post(ctx, tx, "prepare-overflow", "DEMO_CREDIT", "Test int64 limit", from, to, math.MaxInt64-demo.InitialKopecks)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		before := state(t, p)
		if _, err = s.Create(ctx, "overflow-show", password); !errors.Is(err, ledger.ErrOverflow) {
			t.Fatal(err)
		}
		if before != state(t, p) {
			t.Fatal("overflow left identities or funding")
		}
		monetary(t, p)
	})
}

type failingDrafts struct {
	real  demo.DraftCreator
	calls int
}

func (f *failingDrafts) Create(c context.Context, a deals.Actor, k string, in deals.Input) (deals.Result, error) {
	f.calls++
	if f.calls == 3 {
		return deals.Result{}, errors.New("simulated interruption")
	}
	return f.real.Create(c, a, k, in)
}
func TestDemoResumesAfterPartialDraftPreparation(t *testing.T) {
	p, healthy := fixture(t)
	broken := demo.New(repo.New(p), &failingDrafts{real: deals.New(dealrepo.New(p), make([]byte, 32))}, make([]byte, 32))
	if _, err := broken.Create(ctx, "interrupted-show", password); !errors.Is(err, demo.ErrIncomplete) {
		t.Fatal(err)
	}
	if scalar(t, p, `SELECT count(*) FROM deals`) != 2 {
		t.Fatal("fixture did not stop midway")
	}
	a, err := healthy.Create(ctx, "interrupted-show", password)
	if err != nil || !a.Resumed || len(a.Deals) != 4 {
		t.Fatal("did not resume", err)
	}
	if scalar(t, p, `SELECT count(*) FROM audit_events WHERE type='DEMO_RUN_SEEDED'`) != 1 || scalar(t, p, `SELECT count(*) FROM deals`) != 4 || scalar(t, p, `SELECT count(*) FROM audit_events WHERE payload::text LIKE '%'||$1||'%'`, password) != 0 {
		t.Fatal("retry recredited, duplicated drafts or exposed password")
	}
	monetary(t, p)
}

func TestConcurrentDifferentPasswordsConflictWithoutSecondFunding(t *testing.T) {
	p, s := fixture(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, secret := range []string{password, "OtherDemoOnly2026!"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[i] = s.Create(ctx, "conflicting-show", secret) }()
	}
	wg.Wait()
	success, conflicts := 0, 0
	for _, err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, demo.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 || scalar(t, p, `SELECT count(*) FROM deals`) != 4 || scalar(t, p, `SELECT count(*) FROM ledger_batches WHERE operation_key='demo_seed:v1:conflicting-show'`) != 1 {
		t.Fatal("different body concurrency reseeded")
	}
	monetary(t, p)
}
func TestRestrictedRoleCannotPrepareDemo(t *testing.T) {
	p, _ := fixture(t)
	var schema string
	if err := p.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	role := "demo_role_" + strings.TrimPrefix(schema, "test_")
	quoted := pgx.Identifier{role}.Sanitize()
	if _, err := p.Exec(ctx, "CREATE ROLE "+quoted+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = p.Exec(ctx, "DROP OWNED BY "+quoted)
		if _, err := p.Exec(ctx, "DROP ROLE "+quoted); err != nil {
			t.Error(err)
		}
	})
	if _, err := p.Exec(ctx, "GRANT USAGE ON SCHEMA "+pgx.Identifier{schema}.Sanitize()+" TO "+quoted); err != nil {
		t.Fatal(err)
	}
	cfg := p.Config()
	cfg.AfterConnect = func(c context.Context, conn *pgx.Conn) error { _, err := conn.Exec(c, "SET ROLE "+quoted); return err }
	restricted, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()
	before := state(t, p)
	s := demo.New(repo.New(restricted), deals.New(dealrepo.New(restricted), make([]byte, 32)), make([]byte, 32))
	if _, err = s.Create(ctx, "restricted-show", password); !errors.Is(err, demo.ErrOwnerRequired) {
		t.Fatal("restricted role prepared seed", err)
	}
	if before != state(t, p) {
		t.Fatal("restricted role changed data")
	}
}
