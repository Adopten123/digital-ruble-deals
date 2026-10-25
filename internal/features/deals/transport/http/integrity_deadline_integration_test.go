package dealshttp_test

import (
	"context"
	"testing"
	"time"
)

// The audit head is the final lock: waiting cannot preserve a stale deadline.
func TestReserveRechecksDeadlineAfterWaitingForAccountAndAuditHead(t *testing.T) {
	f := reserveFixture(t)
	deadline := time.Now().UTC().Add(2 * time.Second).Truncate(time.Microsecond)
	d := readyForReserve(t, f, "reserve-lock-deadline", func(in map[string]string) { in["deadline_at"] = deadline.Format(time.RFC3339Nano) })
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blockerPID int
	if err = tx.QueryRow(context.Background(), `SELECT pg_backend_pid() FROM audit_chain_head FOR UPDATE`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	result := make(chan moneyResponse, 1)
	go func() {
		result <- moneyCommand(f, f.customer, "/api/v1/deals/"+d.ID+"/reserve", "reserve-lock-wait01", reserveBody(d))()
	}()
	blocked := false
	for until := time.Now().Add(time.Second); time.Now().Before(until); {
		if err = f.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("reserve never waited for the locked account")
	}
	if wait := time.Until(deadline) + 50*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-result:
		if response.err != nil || response.status != 422 {
			t.Fatalf("deadline ignored after account wait: %v %d %s", response.err, response.status, response.body)
		}
		errorCode(t, response.body, "DEADLINE_PASSED")
	case <-time.After(5 * time.Second):
		t.Fatal("reserve did not finish after account unlock")
	}
	if count(t, f.pool, "reserves") != 0 {
		t.Fatal("expired command reserved money")
	}
	_, raw := f.request(t, f.customer, "GET", "/api/v1/deals/"+d.ID, "", "", 200)
	if dealDTO(t, raw).Status != "REVIEW_REQUIRED" {
		t.Fatal("expired aggregate did not require review")
	}
}

func TestSignatureRechecksDeadlineAfterAggregateWaitAndReturnsExistingFactsAndAuditHead(t *testing.T) {
	f := reserveFixture(t)
	deadline := time.Now().UTC().Add(3 * time.Second).Truncate(time.Microsecond)
	d := fundedForSignature(t, f, "signature-deadline", func(in map[string]string) { in["deadline_at"] = deadline.Format(time.RFC3339Nano) })
	_, signature, first := sign(t, f, f.customer, d, "signature-before-end")
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blockerPID int
	if err = tx.QueryRow(context.Background(), `SELECT pg_backend_pid() FROM audit_chain_head FOR UPDATE`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	responses := make(chan moneyResponse, 1)
	go func() {
		responses <- moneyCommand(f, f.supplier, "/api/v1/documents/"+d.Document.ID+"/sign", "signature-after-wait", signBody(d))()
	}()
	blocked := false
	for until := time.Now().Add(time.Second); time.Now().Before(until); {
		if err = f.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("signature never waited for aggregate")
	}
	if wait := time.Until(deadline) + 50*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-responses:
		if response.err != nil || response.status != 422 {
			t.Fatalf("signature used stale pre-lock time: %v %d %s", response.err, response.status, response.body)
		}
		errorCode(t, response.body, "DEADLINE_PASSED")
	case <-time.After(5 * time.Second):
		t.Fatal("signature did not finish after unlock")
	}
	_, _, replay := sign(t, f, f.customer, d, "signature-before-end")
	if string(first) != string(replay) {
		t.Fatal("deadline changed the historical successful result")
	}
	current, existing, _ := sign(t, f, f.customer, d, "signature-noop-late")
	if current.Status != "REVIEW_REQUIRED" || existing.ID != signature.ID || count(t, f.pool, "document_signatures") != 1 || count(t, f.pool, "edo_outbox") != 0 {
		t.Fatal("existing signature was reissued or new late signature published")
	}
}

func TestEDORefreshesDeadlineAfterWaitingForAccountAndAuditHead(t *testing.T) {
	f := reserveFixture(t)
	svc, server := edoServer(t, f, f.pool)
	deadline := time.Now().UTC().Add(3 * time.Second).Truncate(time.Microsecond)
	d := fundedForSignature(t, f, "edo-account-deadline", func(in map[string]string) { in["deadline_at"] = deadline.Format(time.RFC3339Nano) })
	sign(t, f, f.customer, d, "edo-deadline-customer")
	sign(t, f, f.supplier, d, "edo-deadline-supplier")
	body := edoBytes(t, f, d)
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blocker int
	if err = tx.QueryRow(context.Background(), `SELECT pg_backend_pid() FROM audit_chain_head FOR UPDATE`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	result := make(chan moneyResponse, 1)
	go func() {
		status, raw, err := deliverHTTP(server, svc, body, nil)
		result <- moneyResponse{status: status, body: raw, err: err}
	}()
	waitForEDOBlock(t, f, blocker)
	if wait := time.Until(deadline) + 40*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-result:
		if response.err != nil || response.status != 422 {
			t.Fatal("payout used stale pre-wait clock", response)
		}
		errorCode(t, response.body, "DEADLINE_PASSED")
	case <-time.After(5 * time.Second):
		t.Fatal("payout did not resume")
	}
	if count(t, f.pool, "settlements") != 0 {
		t.Fatal("expired payout changed money")
	}
	assertBalance(t, f, f.customer.org, 50000000, 30000000)
}
