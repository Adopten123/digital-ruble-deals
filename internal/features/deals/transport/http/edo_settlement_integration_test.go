package dealshttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	authrepo "github.com/Adopten123/digital-ruble-deals/internal/features/auth/repository/postgres"
	authservice "github.com/Adopten123/digital-ruble-deals/internal/features/auth/service"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	deals "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	edorepo "github.com/Adopten123/digital-ruble-deals/internal/features/edo/repository/postgres"
	edo "github.com/Adopten123/digital-ruble-deals/internal/features/edo/service"
	edohttp "github.com/Adopten123/digital-ruble-deals/internal/features/edo/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func edoServer(t *testing.T, f *fixture, pool *pgxpool.Pool) (*edo.Service, *httptest.Server) {
	t.Helper()
	svc := edo.New(edorepo.New(pool), make([]byte, 32))
	authSvc, err := authservice.New(authrepo.New(pool))
	if err != nil {
		t.Fatal(err)
	}
	csrf := httpapi.NewCSRF(make([]byte, 32), origin, false)
	auth := authhttp.New(authSvc, csrf)
	mux := http.NewServeMux()
	edohttp.New(svc, csrf).RegisterRoutes(mux, auth.Require)
	mux.Handle("/", f.handler(t))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return svc, server
}
func edoReady(t *testing.T, f *fixture, key string) deals.Deal {
	t.Helper()
	d := fundedForSignature(t, f, key, nil)
	sign(t, f, f.customer, d, key+"-customer-sign")
	d, _, _ = sign(t, f, f.supplier, d, key+"-supplier-sign")
	return d
}
func edoBytes(t *testing.T, f *fixture, d deals.Deal) []byte {
	t.Helper()
	var raw []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT raw_body FROM edo_outbox WHERE deal_id=$1`, d.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}
func deliverHTTP(server *httptest.Server, svc *edo.Service, body []byte, h *edo.Headers) (int, []byte, error) {
	headers := svc.Sign(body, time.Now())
	if h != nil {
		headers = *h
	}
	r, _ := http.NewRequest("POST", server.URL+"/internal/edo/events", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-EDO-Key-ID", headers.KeyID)
	r.Header.Set("X-EDO-Timestamp", headers.Timestamp)
	r.Header.Set("X-EDO-Signature", headers.Signature)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	return response.StatusCode, raw, err
}
func receive(t *testing.T, server *httptest.Server, svc *edo.Service, body []byte, want int) []byte {
	t.Helper()
	status, raw, err := deliverHTTP(server, svc, body, nil)
	if err != nil || status != want {
		t.Fatalf("receive %d want %d: %s (%v)", status, want, raw, err)
	}
	return raw
}
func changedEvent(t *testing.T, raw []byte, change func(*edo.Event)) []byte {
	t.Helper()
	event, err := edo.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	change(&event)
	raw, err = json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestEDOHTTPSettlementConcurrentReplayAndNewIDs(t *testing.T) {
	f := reserveFixture(t)
	svc, server := edoServer(t, f, f.pool)
	d := edoReady(t, f, "edo-once-success")
	body := edoBytes(t, f, d)
	before := count(t, f.pool, "ledger_entries")
	results := make([][]byte, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, raw, err := deliverHTTP(server, svc, body, nil)
			if err != nil || status != 200 {
				t.Errorf("delivery %d %s %v", status, raw, err)
			}
			results[i] = raw
		}(i)
	}
	wg.Wait()
	for _, raw := range results {
		if string(raw) != string(results[0]) {
			t.Fatal("same event returned different persisted outcomes")
		}
	}
	if count(t, f.pool, "settlements") != 1 || count(t, f.pool, "ledger_entries") != before+2 || count(t, f.pool, "event_inbox") != 1 {
		t.Fatal("replay duplicated payment or receipts")
	}
	assertBalance(t, f, f.customer.org, 20000000, 0)
	assertBalance(t, f, f.supplier.org, 30000000, 0)
	var payment string
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM settlements WHERE deal_id=$1`, d.ID).Scan(&payment); err != nil {
		t.Fatal(err)
	}
	other := changedEvent(t, body, func(e *edo.Event) { e.EventID = "new-valid-after-settlement" })
	raw := receive(t, server, svc, other, 200)
	if !strings.Contains(string(raw), `"result":"already_settled"`) || !strings.Contains(string(raw), payment) {
		t.Fatal("new event did not reuse payment", string(raw))
	}
	conflict := changedEvent(t, body, func(e *edo.Event) { e.AmountKopecks = "30000001" })
	errorCode(t, receive(t, server, svc, conflict, 409), "EVENT_ID_CONFLICT")
	wrong := changedEvent(t, body, func(e *edo.Event) { e.EventID = "wrong-after-settlement"; e.SupplierPartyID = "unrelated-org" })
	errorCode(t, receive(t, server, svc, wrong, 422), "EVENT_BINDING_MISMATCH")
	if count(t, f.pool, "settlements") != 1 || count(t, f.pool, "ledger_entries") != before+2 {
		t.Fatal("new event duplicated payout")
	}
	_, raw = f.request(t, f.customer, "GET", "/api/v1/deals/"+d.ID, "", "", 200)
	card := dealDTO(t, raw)
	if card.Status != "SETTLED" || card.Reserve.Status != "CONSUMED" {
		t.Fatal("settlement projection did not persist", string(raw))
	}
}

func TestEDORejectsSourceAndSchemaBeforeDurableReceipt(t *testing.T) {
	f := reserveFixture(t)
	svc, server := edoServer(t, f, f.pool)
	d := edoReady(t, f, "edo-transport-invalid")
	raw := edoBytes(t, f, d)
	for _, change := range []func(*edo.Headers){func(h *edo.Headers) { h.KeyID = "unknown" }, func(h *edo.Headers) { h.Signature = "sha256=" + strings.Repeat("0", 64) }, func(h *edo.Headers) { h.Timestamp = "1" }, func(h *edo.Headers) { h.Signature = strings.ToUpper(h.Signature) }} {
		h := svc.Sign(raw, time.Now())
		change(&h)
		status, body, err := deliverHTTP(server, svc, raw, &h)
		if err != nil || status != 403 {
			t.Fatal("invalid source accepted", status, string(body), err)
		}
	}
	for _, bad := range [][]byte{[]byte(`{}`), append([]byte(`{"event_id":"duplicate",`), raw[1:]...), bytes.Replace(raw, []byte(`"signed_by_customer":true`), []byte(`"signed_by_customer":null`), 1)} {
		receive(t, server, svc, bad, 400)
	}
	if count(t, f.pool, "event_inbox") != 0 || count(t, f.pool, "settlements") != 0 {
		t.Fatal("untrusted or malformed input created domain facts")
	}
}

func TestEDOPendingSurvivesPayoutRollbackAndSavedRefusal(t *testing.T) {
	f := reserveFixture(t)
	svc, server := edoServer(t, f, f.pool)
	d := fundedForSignature(t, f, "edo-rollback-state", nil)
	partial := edo.Event{SchemaVersion: 1, EventID: "early-event-refusal", Source: edo.Source, EventType: "document.accepted_by_both", DealID: d.ID, DealVersion: d.DealVersion, DocumentID: d.Document.ID, DocumentVersion: d.Document.DocumentVersion, DocumentSHA256: d.Document.SHA256, CustomerPartyID: d.CustomerPartyID, SupplierPartyID: d.SupplierPartyID, AmountKopecks: d.AmountKopecks, Currency: "RUB", OccurredAt: time.Now().UTC()}
	early, _ := json.Marshal(partial)
	refused := receive(t, server, svc, early, 422)
	errorCode(t, refused, "SIGNATURES_MISSING")
	sign(t, f, f.customer, d, "edo-rollback-customer")
	sign(t, f, f.supplier, d, "edo-rollback-supplier")
	body := edoBytes(t, f, d)
	if got := receive(t, server, svc, early, 422); !bytes.Equal(got, refused) {
		t.Fatal("saved refusal changed after conditions became true")
	}
	before := f.accountSnapshot(t)
	entries := count(t, f.pool, "ledger_entries")
	if _, err := f.pool.Exec(context.Background(), `CREATE FUNCTION reject_payment_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='DEAL_SETTLED' THEN RAISE EXCEPTION 'injected payment outbox failure'; END IF;RETURN NEW;END $$;CREATE TRIGGER reject_payment_job BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION reject_payment_job()`); err != nil {
		t.Fatal(err)
	}
	receive(t, server, svc, body, 503)
	var state string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM event_inbox WHERE event_id=(convert_from($1::bytea,'UTF8')::jsonb->>'event_id')`, body).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "PENDING" || count(t, f.pool, "settlements") != 0 || count(t, f.pool, "ledger_entries") != entries || f.accountSnapshot(t) != before {
		t.Fatal("infrastructure failure partially committed payout")
	}
	if _, err := f.pool.Exec(context.Background(), `DROP TRIGGER reject_payment_job ON outbox`); err != nil {
		t.Fatal(err)
	}
	restartedSvc, restarted := edoServer(t, f, f.pool)
	receive(t, restarted, restartedSvc, body, 200)
	if count(t, f.pool, "settlements") != 1 {
		t.Fatal("pending receipt was not resumed")
	}
}

func TestEDORuntimePaymentAndImmutableResults(t *testing.T) {
	f := reserveFixture(t)
	d := edoReady(t, f, "edo-runtime-role")
	runtime, _ := shadowRuntime(t, f)
	grants, err := os.ReadFile(filepath.Join(testutil.MigrationsDir(), "..", "deploy", "postgres", "edo_runtime_grants.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(context.Background(), string(grants)); err != nil {
		t.Fatal(err)
	}
	svc, server := edoServer(t, f, runtime)
	raw := edoBytes(t, f, d)
	queued, err := svc.Queue(context.Background(), edo.Actor{UserID: f.operator.user, RequestID: "runtime-job"}, "edo-runtime-queue-command", edo.Input{DealID: d.ID, Template: "valid"})
	if err != nil || queued.Status != 202 {
		t.Fatal("runtime cannot create operator delivery", queued, err)
	}
	sender, err := edohttp.NewSender(server.URL + "/internal/edo/events")
	if err != nil {
		t.Fatal(err)
	}
	worker := edo.NewWorker(svc, sender)
	for i := 0; i < 2; i++ {
		worked, err := worker.DeliverOnce(context.Background())
		if err != nil || !worked {
			t.Fatal("runtime cannot deliver durable job", worked, err)
		}
	}
	receive(t, server, svc, raw, 200)
	for _, sql := range []string{`UPDATE event_inbox SET raw_body='bad'::bytea`, `UPDATE event_inbox SET status='PENDING'`, `DELETE FROM event_inbox`, `UPDATE settlements SET amount_kopecks=1`, `DELETE FROM settlements`, `UPDATE reserves SET status='RELEASED' WHERE status='CONSUMED'`, `UPDATE deals SET status='READY_TO_SETTLE' WHERE status='SETTLED'`, `UPDATE edo_deliveries SET http_status=422 WHERE completed_at IS NOT NULL`, `UPDATE edo_deliveries SET raw_body='bad'::bytea`, `DELETE FROM edo_deliveries`} {
		if _, err := runtime.Exec(context.Background(), sql); err == nil {
			t.Fatal("runtime modified a final payment fact", sql)
		}
	}
}

type lostEDOResponse struct {
	sender edo.Sender
	once   bool
}

func (s *lostEDOResponse) Send(ctx context.Context, h edo.Headers, body []byte) (edo.Response, error) {
	out, err := s.sender.Send(ctx, h, body)
	if err == nil && !s.once {
		s.once = true
		return edo.Response{}, errors.New("lost reply after actual receiver commit")
	}
	return out, err
}
func TestEDORealHTTPWorkerRestartsAfterLostReplyWithoutSecondPayment(t *testing.T) {
	f := reserveFixture(t)
	svc, server := edoServer(t, f, f.pool)
	d := edoReady(t, f, "edo-worker-response")
	sender, err := edohttp.NewSender(server.URL + "/internal/edo/events")
	if err != nil {
		t.Fatal(err)
	}
	worker := edo.NewWorker(svc, &lostEDOResponse{sender: sender})
	worked, err := worker.DeliverOnce(context.Background())
	if err != nil || !worked {
		t.Fatal("worker did not discover immutable basis", worked, err)
	}
	if count(t, f.pool, "settlements") != 1 {
		t.Fatal("lost response did not execute actual payment")
	}
	if _, err = f.pool.Exec(context.Background(), `UPDATE edo_deliveries SET next_attempt_at=clock_timestamp() WHERE completed_at IS NULL`); err != nil {
		t.Fatal(err)
	}
	worker = edo.NewWorker(edo.New(edorepo.New(f.pool), make([]byte, 32)), sender)
	worked, err = worker.DeliverOnce(context.Background())
	if err != nil || !worked {
		t.Fatal("restart did not retry durable job", worked, err)
	}
	event, err := edo.Parse(edoBytes(t, f, d))
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.Get(context.Background(), edo.Actor{UserID: f.operator.user}, event.EventID)
	if err != nil || job.Status != "DELIVERED" || job.Attempts != 2 || job.HTTPStatus == nil || *job.HTTPStatus != 200 || count(t, f.pool, "settlements") != 1 {
		t.Fatal("worker replay duplicated or lost payment", job, err)
	}
}

func TestEDOIndependentBlockingFactStopsPayment(t *testing.T) {
	f := reserveFixture(t)
	svc, server := edoServer(t, f, f.pool)
	d := edoReady(t, f, "edo-dispute-stop")
	raw := edoBytes(t, f, d)
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(), `INSERT INTO deal_disputes(id,deal_id,deal_version,organization_id,user_id,reason,created_at) VALUES('edo-test-dispute',$1,$2,$3,$4,'Проверка результата',clock_timestamp())`, d.ID, d.DealVersion, f.customer.org, f.customer.user); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `UPDATE deals SET status='DISPUTED' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	errorCode(t, receive(t, server, svc, raw, 422), "SETTLEMENT_BLOCKED")
	assertBalance(t, f, f.customer.org, 50000000, 30000000)
	assertBalance(t, f, f.supplier.org, 0, 0)
	if count(t, f.pool, "settlements") != 0 {
		t.Fatal("dispute did not block payout")
	}
}

func TestEDOOperatorTemplatesDurableJobsAndAuthority(t *testing.T) {
	f := reserveFixture(t)
	svc, server := edoServer(t, f, f.pool)
	d := edoReady(t, f, "edo-operator-templates")
	f.server = server
	sender, err := edohttp.NewSender(server.URL + "/internal/edo/events")
	if err != nil {
		t.Fatal(err)
	}
	worker := edo.NewWorker(svc, sender)
	drain := func() {
		t.Helper()
		for i := 0; i < 30; i++ {
			worked, e := worker.DeliverOnce(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if !worked {
				return
			}
		}
		t.Fatal("unexpected unbounded delivery work")
	}
	// The automatic job also starts from a durable 006 basis.
	drain()
	basis, err := edo.Parse(edoBytes(t, f, d))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		template string
		want     int
		code     string
	}{{"valid", 200, ""}, {"single_signature", 422, "SIGNATURES_MISSING"}, {"wrong_amount", 422, "EVENT_BINDING_MISMATCH"}, {"wrong_parties", 422, "EVENT_BINDING_MISMATCH"}, {"wrong_document", 422, "EVENT_BINDING_MISMATCH"}, {"wrong_document_version", 422, "EVENT_BINDING_MISMATCH"}, {"wrong_document_hash", 422, "EVENT_BINDING_MISMATCH"}, {"wrong_deal_version", 422, "EVENT_BINDING_MISMATCH"}, {"invalid_hmac", 403, "EDO_SOURCE_INVALID"}, {"replay", 200, ""}, {"same_id_different_body", 409, "EVENT_ID_CONFLICT"}} {
		input := edo.Input{DealID: d.ID, Template: tc.template}
		if tc.template == "replay" || tc.template == "same_id_different_body" {
			input.ReplayEventID = &basis.EventID
		}
		key := "edo-template-" + tc.template
		_, raw := f.request(t, f.operator, "POST", "/api/v1/demo/edo/events", key, encode(input), 202)
		_, repeat := f.request(t, f.operator, "POST", "/api/v1/demo/edo/events", key, encode(input), 202)
		if !bytes.Equal(raw, repeat) {
			t.Fatal("operator key created another job")
		}
		var queued struct {
			JobID   string `json:"job_id"`
			EventID string `json:"event_id"`
		}
		if json.Unmarshal(raw, &queued) != nil {
			t.Fatal("invalid job response")
		}
		drain()
		_, raw = f.request(t, f.operator, "GET", "/api/v1/demo/edo/events/"+queued.EventID, "", "", 200)
		var result edo.Delivery
		if json.Unmarshal(raw, &result) != nil || result.JobID != queued.JobID || result.Status != "DELIVERED" || result.HTTPStatus == nil || *result.HTTPStatus != tc.want {
			t.Fatalf("template %s: %s", tc.template, raw)
		}
		if tc.code != "" && (result.ErrorCode == nil || *result.ErrorCode != tc.code) {
			t.Fatalf("template %s wrong result: %s", tc.template, raw)
		}
		if strings.Contains(string(raw), "raw_body") || strings.Contains(string(raw), "sha256=") {
			t.Fatal("operator status exposed source transport bytes")
		}
	}
	f.request(t, f.customer, "POST", "/api/v1/demo/edo/events", "forbidden-edo-command", encode(edo.Input{DealID: d.ID, Template: "valid"}), 403)
	f.request(t, f.customer, "GET", "/api/v1/demo/edo/events/"+basis.EventID, "", "", 403)
	commands := count(t, f.pool, "edo_command_results")
	f.request(t, f.operator, "POST", "/api/v1/demo/edo/events", "edo-explicit-null-replay", `{"deal_id":"`+d.ID+`","template":"valid","replay_event_id":null}`, 400)
	if count(t, f.pool, "edo_command_results") != commands {
		t.Fatal("explicit null replay reached domain command")
	}
	if count(t, f.pool, "settlements") != 1 {
		t.Fatal("operator templates bypassed single-payment invariant")
	}
}

func TestEDOValidBeforeSecondSignaturePersistsMissingFact(t *testing.T) {
	f := reserveFixture(t)
	svc, server := edoServer(t, f, f.pool)
	d := fundedForSignature(t, f, "edo-single-fact-job", nil)
	sign(t, f, f.customer, d, "edo-single-fact-customer")
	f.server = server
	_, raw := f.request(t, f.operator, "POST", "/api/v1/demo/edo/events", "edo-before-second-key", encode(edo.Input{DealID: d.ID, Template: "valid"}), 202)
	var queued struct {
		EventID string `json:"event_id"`
	}
	json.Unmarshal(raw, &queued)
	sender, err := edohttp.NewSender(server.URL + "/internal/edo/events")
	if err != nil {
		t.Fatal(err)
	}
	worker := edo.NewWorker(svc, sender)
	if _, err = worker.DeliverOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, raw = f.request(t, f.operator, "GET", "/api/v1/demo/edo/events/"+queued.EventID, "", "", 200)
	var job edo.Delivery
	json.Unmarshal(raw, &job)
	if job.HTTPStatus == nil || *job.HTTPStatus != 422 || job.ErrorCode == nil || *job.ErrorCode != "SIGNATURES_MISSING" {
		t.Fatal("operator invented another party's signature", string(raw))
	}
	if count(t, f.pool, "settlements") != 0 {
		t.Fatal("one signature paid")
	}
}

func TestEDOConcurrentDifferentIDsAndOrdinaryDebitProtectRemainingReserve(t *testing.T) {
	f := reserveFixture(t)
	svc, server := edoServer(t, f, f.pool)
	// Another 10M reservation must survive payout of this deal's 30M. The
	// remaining 10M available may be consumed by an unrelated operator debit.
	other := fundedForSignature(t, f, "edo-other-reserve", func(in map[string]string) { in["amount_kopecks"] = "10000000" })
	d := edoReady(t, f, "edo-cross-money-race")
	body := edoBytes(t, f, d)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			changed := changedEvent(t, body, func(e *edo.Event) { e.EventID = "parallel-new-id-" + strconv.Itoa(i) })
			status, raw, err := deliverHTTP(server, svc, changed, nil)
			if err != nil || status != 200 {
				t.Errorf("new-id concurrent delivery %d: %s %v", status, raw, err)
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		path := "/api/v1/demo/accounts/" + accountID(t, f, f.customer.org) + "/debits"
		response, raw, err := f.raw(f.operator, "POST", path, "concurrent-payout-debit", moveBody("10000000", "Параллельное доступное списание"), nil)
		if err != nil || response.StatusCode != 201 {
			t.Errorf("debit %v %s", err, raw)
		}
	}()
	close(start)
	wg.Wait()
	if count(t, f.pool, "settlements") != 1 {
		t.Fatal("new event IDs created another payment")
	}
	assertBalance(t, f, f.customer.org, 10000000, 10000000)
	assertBalance(t, f, f.supplier.org, 30000000, 0)
	var reserveState string
	if err := f.pool.QueryRow(context.Background(), `SELECT status FROM reserves WHERE deal_id=$1`, other.ID).Scan(&reserveState); err != nil || reserveState != "ACTIVE" {
		t.Fatal("payout consumed another deal's reservation", reserveState, err)
	}
}

func TestEDORefreshesDeadlineAfterWaitingForAccount(t *testing.T) {
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
	if err = tx.QueryRow(context.Background(), `SELECT pg_backend_pid() FROM accounts WHERE organization_id=$1 FOR UPDATE`, f.customer.org).Scan(&blocker); err != nil {
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

func TestEDORecipientOverflowPreservesReserveAndRefusal(t *testing.T) {
	f := reserveFixture(t)
	svc, server := edoServer(t, f, f.pool)
	d := fundedForSignature(t, f, "edo-recipient-overflow", func(in map[string]string) { in["amount_kopecks"] = "50000000" })
	sign(t, f, f.customer, d, "edo-overflow-customer")
	sign(t, f, f.supplier, d, "edo-overflow-supplier")
	raw := edoBytes(t, f, d)
	account := accountID(t, f, f.supplier.org)
	credit := strconv.FormatInt(math.MaxInt64-50000000+1, 10)
	f.request(t, f.operator, "POST", "/api/v1/demo/accounts/"+account+"/credits", "edo-overflow-target-credit", moveBody(credit, "Проверка верхней границы получателя"), 201)
	before := f.accountSnapshot(t)
	entries := count(t, f.pool, "ledger_entries")
	refusal := receive(t, server, svc, raw, 422)
	errorCode(t, refusal, "AMOUNT_OVERFLOW")
	if before != f.accountSnapshot(t) || entries != count(t, f.pool, "ledger_entries") || count(t, f.pool, "settlements") != 0 {
		t.Fatal("overflow partially changed funds")
	}
	assertBalance(t, f, f.customer.org, 50000000, 50000000)
	f.request(t, f.operator, "POST", "/api/v1/demo/accounts/"+account+"/debits", "edo-overflow-target-debit", moveBody("1", "Освободить одну копейку диапазона"), 201)
	if repeated := receive(t, server, svc, raw, 422); !bytes.Equal(repeated, refusal) {
		t.Fatal("saved overflow became payment after balance changed")
	}
	newEvent := changedEvent(t, raw, func(e *edo.Event) { e.EventID = "new-event-after-recipient-overflow" })
	receive(t, server, svc, newEvent, 200)
	assertBalance(t, f, f.supplier.org, math.MaxInt64, 0)
	assertBalance(t, f, f.customer.org, 0, 0)
}
func waitForEDOBlock(t *testing.T, f *fixture, pid int) {
	t.Helper()
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until); {
		var blocked bool
		if err := f.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("payout never reached the expected database lock")
}

func TestEDOPaymentAndBlockingRacesSerializeOnDeal(t *testing.T) {
	for _, action := range []struct{ path, table string }{{"disputes", "deal_disputes"}, {"cancellation-requests", "cancellation_requests"}} {
		for _, first := range []string{"payment", "block"} {
			t.Run(action.path+"/"+first, func(t *testing.T) {
				f := reserveFixture(t)
				svc, server := edoServer(t, f, f.pool)
				d := edoReady(t, f, "edo-race-"+first)
				raw := edoBytes(t, f, d)
				tx, err := f.pool.Begin(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				var blocker int
				if err = tx.QueryRow(context.Background(), `SELECT pg_backend_pid() FROM deals WHERE id=$1 FOR UPDATE`, d.ID).Scan(&blocker); err != nil {
					t.Fatal(err)
				}
				responses := make(chan moneyResponse, 2)
				payment := func() {
					status, body, e := deliverHTTP(server, svc, raw, nil)
					responses <- moneyResponse{status: status, body: body, err: e}
				}
				dispute := func() {
					response, body, e := f.raw(f.customer, "POST", "/api/v1/deals/"+d.ID+"/"+action.path, "edo-race-blocking-key", encode(map[string]any{"deal_version": d.DealVersion, "reason": "Проверить выполнение"}), nil)
					status := 0
					if response != nil {
						status = response.StatusCode
					}
					responses <- moneyResponse{status: status, body: body, err: e}
				}
				if first == "payment" {
					go payment()
				} else {
					go dispute()
				}
				waitForEDOBlock(t, f, blocker)
				if first == "payment" {
					go dispute()
				} else {
					go payment()
				}
				if err = tx.Commit(context.Background()); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					select {
					case r := <-responses:
						if r.err != nil {
							t.Fatal(r.err)
						}
						if r.status != 200 && r.status != 201 && r.status != 409 && r.status != 422 {
							t.Fatalf("unexpected race result %d %s", r.status, r.body)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("race did not finish")
					}
				}
				if first == "payment" {
					if count(t, f.pool, "settlements") != 1 || count(t, f.pool, action.table) != 0 {
						t.Fatal("late dispute changed settled payment")
					}
					assertBalance(t, f, f.customer.org, 20000000, 0)
				} else {
					if count(t, f.pool, "settlements") != 0 || count(t, f.pool, action.table) != 1 {
						t.Fatal("earlier committed dispute did not stop payment")
					}
					assertBalance(t, f, f.customer.org, 50000000, 30000000)
					if action.path == "cancellation-requests" {
						var requestID string
						if err = f.pool.QueryRow(context.Background(), `SELECT id FROM cancellation_requests WHERE deal_id=$1`, d.ID).Scan(&requestID); err != nil {
							t.Fatal(err)
						}
						f.request(t, f.supplier, "POST", "/api/v1/deals/"+d.ID+"/cancellation-requests/"+requestID+"/accept", "edo-race-cancel-accept", encode(map[string]any{"deal_version": d.DealVersion}), 200)
						assertBalance(t, f, f.customer.org, 50000000, 0)
						assertBalance(t, f, f.supplier.org, 0, 0)
						late := changedEvent(t, raw, func(e *edo.Event) { e.EventID = "edo-after-refund-new-id" })
						errorCode(t, receive(t, server, svc, late, 422), "SETTLEMENT_BLOCKED")
						if count(t, f.pool, "settlements") != 0 {
							t.Fatal("released reserve was also paid")
						}
					}
				}
			})
		}
	}
}

// Compile-time assertion keeps real HTTP and the worker on the same transport.
var _ edo.Sender = (*edohttp.HTTPSender)(nil)
