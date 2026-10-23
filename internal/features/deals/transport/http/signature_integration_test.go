package dealshttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dealrepo "github.com/Adopten123/digital-ruble-deals/internal/features/deals/repository/postgres"
	dealservice "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
)

func fundedForSignature(t *testing.T, f *fixture, key string, mutate func(map[string]string)) dealservice.Deal {
	t.Helper()
	d := readyForReserve(t, f, key, mutate)
	_, raw := f.request(t, f.customer, "POST", "/api/v1/deals/"+d.ID+"/reserve", key+"-reserve", reserveBody(d), 201)
	d, _ = reserveDTO(t, raw)
	return d
}

func signBody(d dealservice.Deal) string {
	return encode(dealservice.SignInput{DealVersion: d.DealVersion, DocumentVersion: d.Document.DocumentVersion, DocumentSHA256: d.Document.SHA256})
}

func signatureDTO(t *testing.T, raw []byte) (dealservice.Deal, dealservice.Signature) {
	t.Helper()
	var out struct {
		Deal      dealservice.Deal      `json:"deal"`
		Signature dealservice.Signature `json:"signature"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Deal, out.Signature
}

func sign(t *testing.T, f *fixture, c *client, d dealservice.Deal, key string) (dealservice.Deal, dealservice.Signature, []byte) {
	t.Helper()
	_, raw := f.request(t, c, "POST", "/api/v1/documents/"+d.Document.ID+"/sign", key, signBody(d), 201)
	card, signature := signatureDTO(t, raw)
	return card, signature, raw
}

func TestSignaturesPublishOneExactEDOBasisWithoutMovingMoney(t *testing.T) {
	f := reserveFixture(t)
	d := fundedForSignature(t, f, "signature-success", nil)
	balances, ledger := f.accountSnapshot(t), count(t, f.pool, "ledger_entries")
	first, customerSignature, firstRaw := sign(t, f, f.customer, d, "customer-sign-key01")
	if first.Status != "FUNDED" || len(first.Signatures) != 1 || customerSignature.ID == "" || customerSignature.OrganisationID != f.customer.org || customerSignature.UserID != f.customer.user || customerSignature.DocumentSHA256 != d.Document.SHA256 || customerSignature.DealVersion != d.DealVersion || count(t, f.pool, "edo_outbox") != 0 {
		t.Fatalf("first signature changed context or created EDO event: %s", firstRaw)
	}
	audit, outbox := count(t, f.pool, "audit_events"), count(t, f.pool, "outbox")
	_, duplicate, _ := sign(t, f, f.customer, d, "customer-new-key01")
	if duplicate.ID != customerSignature.ID || count(t, f.pool, "audit_events") != audit || count(t, f.pool, "outbox") != outbox {
		t.Fatal("new key duplicated signature or its side effects")
	}
	both, supplierSignature, _ := sign(t, f, f.supplier, d, "supplier-sign-key01")
	if both.Status != "READY_TO_SETTLE" || len(both.Signatures) != 2 || both.DealVersion != d.DealVersion || both.Reserve == nil || both.Reserve.ID != d.Reserve.ID || supplierSignature.OrganisationID != f.supplier.org || both.PaymentID != nil {
		t.Fatalf("two signatures did not publish ready context: %+v", both)
	}
	var rawBody, pendingPayload []byte
	var eventID string
	var occurredAt time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT e.event_id,e.occurred_at,e.raw_body,o.payload FROM edo_outbox e JOIN outbox o ON o.event_id=e.event_id WHERE e.deal_id=$1 AND o.type='EDO_EVENT_READY' AND o.delivered_at IS NULL`, d.ID).Scan(&eventID, &occurredAt, &rawBody, &pendingPayload); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal(rawBody, &event); err != nil {
		t.Fatal(err)
	}
	for k, expected := range map[string]any{"schema_version": float64(1), "event_id": eventID, "source": "edo-emulator", "event_type": "document.accepted_by_both", "deal_id": d.ID, "deal_version": float64(d.DealVersion), "document_id": d.Document.ID, "document_version": float64(d.Document.DocumentVersion), "document_sha256": d.Document.SHA256, "customer_party_id": f.customer.org, "supplier_party_id": f.supplier.org, "amount_kopecks": d.AmountKopecks, "currency": "RUB", "signed_by_customer": true, "signed_by_supplier": true, "customer_signature_id": customerSignature.ID, "supplier_signature_id": supplierSignature.ID, "occurred_at": occurredAt.UTC().Format(time.RFC3339Nano)} {
		if event[k] != expected {
			t.Errorf("event %s=%v, want %v", k, event[k], expected)
		}
	}
	if len(event) != 18 {
		t.Fatal("EDO body has missing or extra fields", event)
	}
	restarted := httptest.NewServer(f.handler(t))
	oldURL := f.server.URL
	f.server.URL = restarted.URL
	_, _, replay := sign(t, f, f.customer, d, "customer-sign-key01")
	f.server.URL = oldURL
	restarted.Close()
	if string(replay) != string(firstRaw) {
		t.Fatal("restart did not preserve the exact first signature result")
	}
	audit, outbox = count(t, f.pool, "audit_events"), count(t, f.pool, "outbox")
	current, existing, _ := sign(t, f, f.customer, d, "customer-ready-key")
	if current.Status != "READY_TO_SETTLE" || existing.ID != customerSignature.ID || count(t, f.pool, "edo_outbox") != 1 || count(t, f.pool, "audit_events") != audit || count(t, f.pool, "outbox") != outbox {
		t.Fatal("ready duplicate changed facts or omitted current state")
	}
	var retained []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT raw_body FROM edo_outbox WHERE deal_id=$1`, d.ID).Scan(&retained); err != nil || string(retained) != string(rawBody) {
		t.Fatal("EDO source bytes changed on replay", err)
	}
	if f.accountSnapshot(t) != balances || count(t, f.pool, "ledger_entries") != ledger {
		t.Fatal("signatures changed money")
	}
	_, raw := f.request(t, f.supplier, "GET", "/api/v1/deals/"+d.ID, "", "", 200)
	if got := dealDTO(t, raw); len(got.Signatures) != 2 || got.Status != "READY_TO_SETTLE" {
		t.Fatal("card lost signatures")
	}
	_, raw = f.request(t, f.customer, "GET", "/api/v1/deals?status=READY_TO_SETTLE", "", "", 200)
	if !strings.Contains(string(raw), customerSignature.ID) || !strings.Contains(string(raw), supplierSignature.ID) {
		t.Fatal("list filter or signature projection failed")
	}
	for i, command := range []struct{ method, suffix, body string }{
		{"PATCH", "", encode(map[string]any{"expected_version": d.DealVersion, "title": "После подписей"})},
		{"POST", "/rework", encode(map[string]any{"deal_version": d.DealVersion, "reason": "Нельзя менять"})},
		{"POST", "/documents/generate", encode(map[string]any{"expected_deal_version": d.DealVersion, "template_id": "printing-acceptance-v1"})},
	} {
		_, raw := f.request(t, f.customer, command.method, "/api/v1/deals/"+d.ID+command.suffix, fmt.Sprintf("signature-locked-%d", i), command.body, 409)
		errorCode(t, raw, "INVALID_STATE")
	}
}

func TestSignatureAuthorityBindingsAndSavedRefusals(t *testing.T) {
	f := reserveFixture(t)
	d := readyForReserve(t, f, "signature-refusals", nil)
	path, body := "/api/v1/documents/"+d.Document.ID+"/sign", signBody(d)
	_, refused := f.request(t, f.customer, "POST", path, "before-reserve-sign", body, 409)
	errorCode(t, refused, "INVALID_STATE")
	_, raw := f.request(t, f.customer, "POST", "/api/v1/deals/"+d.ID+"/reserve", "reserve-after-sign", reserveBody(d), 201)
	d, _ = reserveDTO(t, raw)
	_, repeated := f.request(t, f.customer, "POST", path, "before-reserve-sign", body, 409)
	if string(repeated) != string(refused) || count(t, f.pool, "document_signatures") != 0 {
		t.Fatal("saved refusal became a success after reserve")
	}
	for _, c := range []*client{f.third, f.operator} {
		_, raw = f.request(t, c, "POST", path, "outsider-sign-key1", body, 404)
		errorCode(t, raw, "RESOURCE_NOT_FOUND")
	}
	for i, tc := range []struct {
		input dealservice.SignInput
		code  string
	}{
		{dealservice.SignInput{DealVersion: d.DealVersion + 1, DocumentVersion: d.Document.DocumentVersion, DocumentSHA256: d.Document.SHA256}, "VERSION_CONFLICT"},
		{dealservice.SignInput{DealVersion: d.DealVersion, DocumentVersion: d.Document.DocumentVersion + 1, DocumentSHA256: d.Document.SHA256}, "DOCUMENT_MISMATCH"},
		{dealservice.SignInput{DealVersion: d.DealVersion, DocumentVersion: d.Document.DocumentVersion, DocumentSHA256: strings.Repeat("0", 64)}, "DOCUMENT_MISMATCH"},
	} {
		_, raw = f.request(t, f.customer, "POST", path, fmt.Sprintf("signature-binding-%d", i), encode(tc.input), 409)
		errorCode(t, raw, tc.code)
	}
	_, raw = f.request(t, f.customer, "POST", path, "before-reserve-sign", encode(dealservice.SignInput{DealVersion: d.DealVersion + 1, DocumentVersion: d.Document.DocumentVersion, DocumentSHA256: d.Document.SHA256}), 409)
	errorCode(t, raw, "IDEMPOTENCY_CONFLICT")
	for i, invalid := range []string{`{}`, `{"deal_version":null}`, `{"deal_version":"1"}`, `{"deal_version":2,"document_version":1,"document_sha256":"` + d.Document.SHA256 + `","organisation_id":"` + f.supplier.org + `"}`} {
		f.request(t, f.customer, "POST", path, fmt.Sprintf("signature-invalid-%d", i), invalid, 400)
	}
	response, raw, err := f.raw(f.customer, "POST", path, "signature-no-csrf", body, map[string]string{"X-CSRF-Token": "invalid"})
	if err != nil || response.StatusCode != 403 {
		t.Fatal("signature omitted CSRF enforcement", err, string(raw))
	}
	if count(t, f.pool, "document_signatures") != 0 || count(t, f.pool, "edo_outbox") != 0 {
		t.Fatal("rejected request published signature facts")
	}
}

func TestConcurrentSignaturesAndDuplicateKeysPublishOnlyTwoFacts(t *testing.T) {
	f := reserveFixture(t)
	d := fundedForSignature(t, f, "signature-races", nil)
	path, body := "/api/v1/documents/"+d.Document.ID+"/sign", signBody(d)
	balances, ledger := f.accountSnapshot(t), count(t, f.pool, "ledger_entries")
	results := concurrentMoney(
		moneyCommand(f, f.customer, path, "parallel-customer1", body),
		moneyCommand(f, f.customer, path, "parallel-customer1", body),
		moneyCommand(f, f.customer, path, "parallel-customer2", body),
		moneyCommand(f, f.supplier, path, "parallel-supplier1", body),
		moneyCommand(f, f.supplier, path, "parallel-supplier2", body),
	)
	for _, response := range results {
		if response.err != nil || response.status != 201 {
			t.Fatalf("concurrent sign: %v %d %s", response.err, response.status, response.body)
		}
	}
	if string(results[0].body) != string(results[1].body) || count(t, f.pool, "document_signatures") != 2 || count(t, f.pool, "edo_outbox") != 1 || f.accountSnapshot(t) != balances || count(t, f.pool, "ledger_entries") != ledger {
		t.Fatal("concurrent signing duplicated facts, outcomes or funds")
	}
	var jobs int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE deal_id=$1 AND type='EDO_EVENT_READY'`, d.ID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal("second signatures produced several EDO jobs", err, jobs)
	}
}

func TestSignatureRechecksDeadlineAfterAggregateWaitAndReturnsExistingFacts(t *testing.T) {
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
	if err = tx.QueryRow(context.Background(), `SELECT pg_backend_pid() FROM deals WHERE id=$1 FOR UPDATE`, d.ID).Scan(&blockerPID); err != nil {
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

func TestSignatureOutboxFailureRollsBackFirstAndSecondFacts(t *testing.T) {
	for _, phase := range []string{"first", "second-edo", "second-notification"} {
		t.Run(phase, func(t *testing.T) {
			f := reserveFixture(t)
			d := fundedForSignature(t, f, "signature-rollback", nil)
			actor := f.customer
			if phase != "first" {
				sign(t, f, f.customer, d, "rollback-first-sign")
				actor = f.supplier
			}
			rejectedType := "DOCUMENT_SIGNED"
			if phase == "second-edo" {
				rejectedType = "EDO_EVENT_READY"
			}
			if _, err := f.pool.Exec(context.Background(), `CREATE FUNCTION reject_signature_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='`+rejectedType+`' THEN RAISE EXCEPTION 'injected signature outbox failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_signature_job BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION reject_signature_job()`); err != nil {
				t.Fatal(err)
			}
			balances := f.accountSnapshot(t)
			before := map[string]int{}
			for _, table := range []string{"document_signatures", "edo_outbox", "reserves", "ledger_batches", "ledger_entries", "audit_events", "outbox", "idempotency_keys"} {
				before[table] = count(t, f.pool, table)
			}
			path := "/api/v1/documents/" + d.Document.ID + "/sign"
			f.request(t, actor, "POST", path, "signature-rollback-key", signBody(d), 503)
			for table, expected := range before {
				if count(t, f.pool, table) != expected {
					t.Errorf("%s changed after outbox rollback", table)
				}
			}
			if f.accountSnapshot(t) != balances {
				t.Fatal("signature failure changed balances")
			}
			_, raw := f.request(t, f.customer, "GET", "/api/v1/deals/"+d.ID, "", "", 200)
			if dealDTO(t, raw).Status != "FUNDED" {
				t.Fatal("failed signature published ready state")
			}
			if _, err := f.pool.Exec(context.Background(), `DROP TRIGGER reject_signature_job ON outbox`); err != nil {
				t.Fatal(err)
			}
			sign(t, f, actor, d, "signature-rollback-key")
		})
	}
}

func TestSignatureDoesNotCommitWithoutExactStoredPDFBytes(t *testing.T) {
	f := reserveFixture(t)
	d := fundedForSignature(t, f, "signature-filecheck", nil)
	var key string
	if err := f.pool.QueryRow(context.Background(), `SELECT storage_key FROM document_versions WHERE document_id=$1 AND document_version=$2`, d.Document.ID, d.Document.DocumentVersion).Scan(&key); err != nil {
		t.Fatal(err)
	}
	// Locate the private test store through the immutable store interface rather
	// than exposing its root. A wrapper injects missing/corrupt reads only here.
	original, err := f.files.Read(key)
	if err != nil {
		t.Fatal(err)
	}
	store := &signatureReadStore{data: original}
	svc := dealservice.New(dealrepo.New(f.pool), make([]byte, 32), store)
	a := dealservice.Actor{UserID: f.customer.user, OrganizationID: f.customer.org, RequestID: "signature-file-integrity"}
	input := dealservice.SignInput{DealVersion: d.DealVersion, DocumentVersion: d.Document.DocumentVersion, DocumentSHA256: d.Document.SHA256}
	balances := f.accountSnapshot(t)
	for _, mode := range []string{"missing", "corrupt"} {
		store.missing = mode == "missing"
		store.data = append([]byte(nil), original...)
		if mode == "corrupt" {
			store.data[0] ^= 1
		}
		out, err := svc.Sign(context.Background(), a, d.Document.ID, "signature-file-key01", input)
		if err == nil || out.Status != 0 || count(t, f.pool, "document_signatures") != 0 || count(t, f.pool, "edo_outbox") != 0 || f.accountSnapshot(t) != balances {
			t.Fatal("missing/corrupt PDF committed signature", mode, err)
		}
	}
	store.missing, store.data = false, original
	out, err := svc.Sign(context.Background(), a, d.Document.ID, "signature-file-key01", input)
	if err != nil || out.Status != 201 {
		t.Fatal("transient file failure became saved refusal", err, string(out.Body))
	}
}

type signatureReadStore struct {
	data    []byte
	missing bool
}

func (s *signatureReadStore) Put([]byte) (string, error) { return "unused", nil }
func (s *signatureReadStore) Read(string) ([]byte, error) {
	if s.missing {
		return nil, &os.PathError{Op: "open", Path: filepath.Base("private-pdf"), Err: os.ErrNotExist}
	}
	return append([]byte(nil), s.data...), nil
}

func TestSignatureRuntimeGuardsAndImmutableFacts(t *testing.T) {
	f := reserveFixture(t)
	d := fundedForSignature(t, f, "signature-runtime", nil)
	runtime, schema := shadowRuntime(t, f)
	runtimeFixture := *f
	runtimeFixture.pool = runtime
	server := httptest.NewServer(runtimeFixture.handler(t))
	defer server.Close()
	oldURL := f.server.URL
	f.server.URL = server.URL
	defer func() { f.server.URL = oldURL }()
	for _, statement := range []string{
		`UPDATE deals SET status='READY_TO_SETTLE' WHERE id=$1`,
		`UPDATE deals SET status='SETTLED' WHERE id=$1`,
		`UPDATE deals SET deal_version=deal_version+1 WHERE id=$1`,
		`UPDATE deals SET current_document_id=NULL,current_document_version=NULL WHERE id=$1`,
	} {
		if _, err := runtime.Exec(context.Background(), statement, d.ID); err == nil {
			t.Fatal("runtime bypassed funded/ready guard", statement)
		}
	}
	if _, err := runtime.Exec(context.Background(), `INSERT INTO document_signatures(id,deal_id,deal_version,organization_id,user_id,document_id,document_version,document_sha256,created_at) VALUES('wrong-org-signature',$1,$2,$3,$4,$5,$6,$7,clock_timestamp())`, d.ID, d.DealVersion, f.third.org, f.third.user, d.Document.ID, d.Document.DocumentVersion, d.Document.SHA256); err == nil {
		t.Fatal("runtime signature guard accepted a nonparticipant organisation")
	}
	// A TEMP-capable runtime role must not turn fake signatures/EDO rows into
	// authority for the real aggregate's transition.
	tx, err := runtime.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(context.Background(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TEMP TABLE document_signatures ON COMMIT DROP AS SELECT * FROM ` + schema + `.document_signatures`)
	exec(`INSERT INTO pg_temp.document_signatures(id,deal_id,deal_version,organization_id,user_id,document_id,document_version,document_sha256,created_at) VALUES('fake-customer',$1,$2,$3,$4,$5,$6,$7,clock_timestamp()),('fake-supplier',$1,$2,$8,$9,$5,$6,$7,clock_timestamp())`, d.ID, d.DealVersion, f.customer.org, f.customer.user, d.Document.ID, d.Document.DocumentVersion, d.Document.SHA256, f.supplier.org, f.supplier.user)
	exec(`CREATE TEMP TABLE edo_outbox(event_id text,deal_id text,deal_version bigint,document_id text,document_version bigint) ON COMMIT DROP`)
	exec(`INSERT INTO pg_temp.edo_outbox VALUES('fake-event',$1,$2,$3,$4)`, d.ID, d.DealVersion, d.Document.ID, d.Document.DocumentVersion)
	_, err = tx.Exec(context.Background(), `UPDATE `+schema+`.deals SET status='READY_TO_SETTLE' WHERE id=$1`, d.ID)
	var constraint interface{ SQLState() string }
	if !errors.As(err, &constraint) || constraint.SQLState() != "P0001" {
		t.Fatal("temporary signature/EDO shadows bypassed the READY guard", err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	sign(t, f, f.customer, d, "runtime-customer-sign")
	sign(t, f, f.supplier, d, "runtime-supplier-sign")
	for _, statement := range []string{
		`UPDATE document_signatures SET document_sha256=repeat('0',64)`,
		`DELETE FROM document_signatures`,
		`UPDATE edo_outbox SET raw_body='changed'::bytea`,
		`DELETE FROM edo_outbox`,
		`UPDATE outbox SET payload='{}'::jsonb WHERE type='EDO_EVENT_READY'`,
		`UPDATE deals SET status='FUNDED' WHERE status='READY_TO_SETTLE'`,
	} {
		if _, err := runtime.Exec(context.Background(), statement); err == nil {
			t.Fatal("runtime modified a published signature/EDO fact", statement)
		}
	}
	var wrongPaths int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.proname IN ('agreement_runtime_transition','check_signature_context','check_edo_outbox_context','check_signature_published_state') AND (p.prosecdef OR NOT EXISTS(SELECT 1 FROM unnest(p.proconfig) c WHERE c='search_path=pg_catalog, '||quote_ident(current_schema())||', pg_temp'))`).Scan(&wrongPaths); err != nil || wrongPaths != 0 {
		t.Fatal("signature guards do not use trusted invoker search path", err, wrongPaths)
	}
}
