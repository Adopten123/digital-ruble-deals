package dealshttp_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	edorepo "github.com/Adopten123/digital-ruble-deals/internal/features/edo/repository/postgres"
	edo "github.com/Adopten123/digital-ruble-deals/internal/features/edo/service"
	edohttp "github.com/Adopten123/digital-ruble-deals/internal/features/edo/transport/http"
)

// Unlike the lost-response case, the first claimant never calls Complete.
// A replacement process must reclaim the expired lease without allowing a late
// result from the old process to overwrite the replacement's active/final work.
func TestEDOExpiredLeaseRecoversAfterClaimAndIgnoresStaleCompletion(t *testing.T) {
	f := reserveFixture(t)
	d := edoReady(t, f, "edo-expired-lease")
	svc, server := edoServer(t, f, f.pool)
	repo := edorepo.New(f.pool)
	ctx := context.Background()
	entriesBefore := count(t, f.pool, "ledger_entries")

	first, err := repo.Claim(ctx)
	if err != nil || first == nil {
		t.Fatal("first process could not claim the immutable source", err)
	}
	if first.DealID != d.ID || first.LeaseID == "" {
		t.Fatal("first claim lost its deal or lease")
	}
	if other, err := repo.Claim(ctx); err != nil || other != nil {
		t.Fatal("an active lease allowed another claimant", err)
	}

	// Only the test owner advances this fixture's lease expiry; no wall-clock
	// dependency or public schema mutation is needed to simulate process death.
	tag, err := f.pool.Exec(ctx, "UPDATE edo_deliveries SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1 AND lease_id=$2 AND completed_at IS NULL", first.JobID, first.LeaseID)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatal("could not expire the abandoned fixture lease", err)
	}
	second, err := repo.Claim(ctx)
	if err != nil || second == nil {
		t.Fatal("replacement process did not reclaim expired work", err)
	}
	if second.JobID != first.JobID || second.EventID != first.EventID || !bytes.Equal(second.Raw, first.Raw) || second.LeaseID == "" || second.LeaseID == first.LeaseID {
		t.Fatal("reclaim changed source identity or reused the old lease")
	}

	snapshot := func() string {
		t.Helper()
		var row string
		if err := f.pool.QueryRow(ctx, "SELECT to_jsonb(d)::text FROM edo_deliveries d WHERE id=$1", first.JobID).Scan(&row); err != nil {
			t.Fatal(err)
		}
		return row
	}
	active := snapshot()
	staleBody := []byte("{\"error\":{\"code\":\"STALE_TEST_RESULT\"}}")
	if err = repo.Complete(ctx, *first, 422, staleBody, true); err != nil {
		t.Fatal("late terminal completion should be a harmless no-op", err)
	}
	if err = repo.Complete(ctx, *first, 0, nil, false); err != nil {
		t.Fatal("late retry completion should be a harmless no-op", err)
	}
	if snapshot() != active {
		t.Fatal("stale process overwrote the replacement's active lease")
	}
	current, err := svc.Get(ctx, edo.Actor{UserID: f.operator.user}, second.EventID)
	if err != nil || current.Attempts != 2 || current.Status != "PENDING" || current.LeaseID != second.LeaseID || current.HTTPStatus != nil || current.CompletedAt != nil {
		t.Fatal("replacement lease was lost before delivery", err)
	}

	sender, err := edohttp.NewSender(server.URL + "/internal/edo/events")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := sender.Send(ctx, svc.Sign(second.Raw, time.Now()), second.Raw)
	if err != nil || reply.Status != 200 {
		t.Fatalf("replacement delivery failed: status=%d body=%s err=%v", reply.Status, reply.Body, err)
	}
	if err = repo.Complete(ctx, *second, reply.Status, reply.Body, true); err != nil {
		t.Fatal("replacement could not persist its receipt", err)
	}
	current, err = svc.Get(ctx, edo.Actor{UserID: f.operator.user}, second.EventID)
	if err != nil || current.Status != "DELIVERED" || current.Attempts != 2 || current.HTTPStatus == nil || *current.HTTPStatus != 200 || current.CompletedAt == nil || !bytes.Equal(current.Result, reply.Body) {
		t.Fatal("replacement's final receipt was lost", err)
	}
	final := snapshot()
	if err = repo.Complete(ctx, *first, 422, staleBody, true); err != nil || snapshot() != final {
		t.Fatal("late process changed a completed delivery", err)
	}
	if replay := receive(t, server, svc, first.Raw, 200); !bytes.Equal(replay, reply.Body) {
		t.Fatal("recovered source did not preserve the exact inbox outcome")
	}
	if other, err := repo.Claim(ctx); err != nil || other != nil {
		t.Fatal("completed automatic source was claimed again", err)
	}
	if count(t, f.pool, "settlements") != 1 || count(t, f.pool, "event_inbox") != 1 || count(t, f.pool, "ledger_entries") != entriesBefore+2 {
		t.Fatal("lease recovery duplicated the payment or receipt")
	}
	var paymentBatches int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM ledger_batches WHERE kind='DEAL_SETTLEMENT'").Scan(&paymentBatches); err != nil || paymentBatches != 1 {
		t.Fatal("lease recovery did not publish exactly one payment batch", paymentBatches, err)
	}
	assertBalance(t, f, f.customer.org, 20000000, 0)
	assertBalance(t, f, f.supplier.org, 30000000, 0)
	assertMonetaryFacts(t, f)
}
