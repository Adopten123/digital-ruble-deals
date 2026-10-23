package dealshttp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	dealrepo "github.com/Adopten123/digital-ruble-deals/internal/features/deals/repository/postgres"
	dealservice "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
)

func draft(t *testing.T, f *fixture) dealservice.Deal {
	t.Helper()
	_, b := f.request(t, f.customer, "POST", "/api/v1/deals", "agreement-create-key-01", encode(input(f.supplier.org)), 201)
	return dealDTO(t, b)
}
func dealDTO(t *testing.T, b []byte) (d dealservice.Deal) {
	t.Helper()
	if e := json.Unmarshal(b, &d); e != nil {
		t.Fatal(e)
	}
	return
}
func generate(t *testing.T, f *fixture, d dealservice.Deal, key string) (dealservice.Deal, dealservice.Document) {
	t.Helper()
	_, b := f.request(t, f.customer, "POST", "/api/v1/deals/"+d.ID+"/documents/generate", key, encode(map[string]any{"expected_deal_version": d.DealVersion, "template_id": "printing-acceptance-v1"}), 201)
	var out struct {
		Deal     dealservice.Deal     `json:"deal"`
		Document dealservice.Document `json:"document"`
	}
	if e := json.Unmarshal(b, &out); e != nil {
		t.Fatal(e)
	}
	return out.Deal, out.Document
}
func acceptance(d dealservice.Deal) dealservice.AgreeInput {
	return dealservice.AgreeInput{DealVersion: d.DealVersion, DocumentID: d.Document.ID, DocumentVersion: d.Document.DocumentVersion, DocumentSHA256: d.Document.SHA256}
}
func agree(t *testing.T, f *fixture, c *client, d dealservice.Deal, key string) dealservice.Deal {
	t.Helper()
	_, b := f.request(t, c, "POST", "/api/v1/deals/"+d.ID+"/agree", key, encode(acceptance(d)), 200)
	return dealDTO(t, b)
}

func TestAgreementVersionHistoryAndOldReplay(t *testing.T) {
	f := setup(t)
	d := draft(t, f)
	path := "/api/v1/deals/" + d.ID
	_, b := f.request(t, f.customer, "POST", path+"/agree", "missing-document-key", encode(dealservice.AgreeInput{DealVersion: 1, DocumentID: "missing", DocumentVersion: 1, DocumentSHA256: strings.Repeat("0", 64)}), 409)
	errorCode(t, b, "DOCUMENT_REQUIRED")
	d, doc := generate(t, f, d, "generate-document-01")
	if d.DealVersion != 2 || doc.DocumentVersion != 1 || doc.DealVersion != 2 || d.Document == nil {
		t.Fatalf("wrong document context: %+v %+v", d, doc)
	}
	wrong := acceptance(d)
	wrong.DocumentSHA256 = strings.Repeat("0", 64)
	_, b = f.request(t, f.supplier, "POST", path+"/agree", "wrong-document-hash", encode(wrong), 409)
	errorCode(t, b, "DOCUMENT_MISMATCH")
	first := agree(t, f, f.customer, d, "customer-agree-key01")
	if first.Status != "DRAFT" || len(first.Agreements) != 1 {
		t.Fatalf("first agreement released phase: %+v", first)
	}
	duplicate := agree(t, f, f.customer, d, "customer-agree-key02")
	if len(duplicate.Agreements) != 1 {
		t.Fatal("same organization counted twice")
	}
	both := agree(t, f, f.supplier, d, "supplier-agree-key01")
	if both.Status != "AGREED" || len(both.Agreements) != 2 || both.DealVersion != 2 {
		t.Fatalf("two agreements: %+v", both)
	}
	_, b = f.request(t, f.customer, "PATCH", path, "patch-agreed-key001", `{"expected_version":2,"title":"blocked"}`, 409)
	errorCode(t, b, "INVALID_STATE")
	_, historical := f.request(t, f.supplier, "POST", path+"/rework", "supplier-rework-key", `{"deal_version":2,"reason":"Уточнить тираж"}`, 200)
	reworked := dealDTO(t, historical)
	if reworked.DealVersion != 3 || reworked.Document != nil || len(reworked.Agreements) != 0 || reworked.Status != "DRAFT" {
		t.Fatalf("rework: %s", historical)
	}
	_, repeat := f.request(t, f.supplier, "POST", path+"/rework", "supplier-rework-key", `{"deal_version":2,"reason":"Уточнить тираж"}`, 200)
	if !bytes.Equal(historical, repeat) {
		t.Fatal("rework replay changed exact response shape")
	}
	_, b = f.request(t, f.customer, "POST", path+"/agree", "customer-agree-key01", encode(acceptance(d)), 200)
	if len(dealDTO(t, b).Agreements) != 1 {
		t.Fatal("old response did not retain historical result")
	}
	_, b = f.request(t, f.supplier, "GET", path, "", "", 200)
	current := dealDTO(t, b)
	if current.DealVersion != 3 || len(current.Agreements) != 0 {
		t.Fatal("replay reactivated agreement")
	}
	_, b = f.request(t, f.customer, "PATCH", path, "edit-reworked-key01", `{"expected_version":3,"description":"Уточнённые условия"}`, 200)
	edited := dealDTO(t, b)
	if edited.DealVersion != 4 || edited.Document != nil {
		t.Fatal("patch did not invalidate PDF")
	}
	edited, next := generate(t, f, edited, "generate-document-02")
	if next.ID != doc.ID || next.DocumentVersion != 2 || edited.DealVersion != 5 {
		t.Fatal("document ID changed or generation version drifted")
	}
	if count(t, f.pool, "deal_versions") != 5 || count(t, f.pool, "document_versions") != 2 || count(t, f.pool, "deal_agreements") != 2 {
		t.Fatal("history overwritten")
	}
	var reason string
	if e := f.pool.QueryRow(context.Background(), `SELECT payload->>'reason' FROM audit_events WHERE deal_id=$1 AND type='DEAL_REWORKED'`, d.ID).Scan(&reason); e != nil || reason != "Уточнить тираж" {
		t.Fatal("rework reason lost", e, reason)
	}
	for _, statement := range []string{`UPDATE document_versions SET file_name='changed' WHERE deal_id=$1`, `DELETE FROM document_versions WHERE deal_id=$1`, `UPDATE documents SET id='changed' WHERE deal_id=$1`, `UPDATE deal_agreements SET user_id='changed' WHERE deal_id=$1`, `DELETE FROM deal_agreements WHERE deal_id=$1`} {
		if _, e := f.pool.Exec(context.Background(), statement, d.ID); e == nil {
			t.Fatal("immutable fact changed", statement)
		}
	}
}

func TestDocumentAccessIntegrityAndHistoricalScope(t *testing.T) {
	f := setup(t)
	d, doc := generate(t, f, draft(t, f), "private-document-01")
	path := "/api/v1/documents/" + doc.ID
	response, b := f.request(t, f.supplier, "GET", path+"/view?version=1", "", "", 200)
	hash := sha256.Sum256(b)
	if hex.EncodeToString(hash[:]) != doc.SHA256 || int64(len(b)) != doc.SizeBytes || !strings.HasPrefix(string(b), "%PDF-") || !strings.Contains(response.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("PDF integrity or isolation headers")
	}
	response, download := f.request(t, f.customer, "GET", path+"/download?version=1", "", "", 200)
	if !bytes.Equal(b, download) || !strings.Contains(response.Header.Get("Content-Disposition"), "attachment") {
		t.Fatal("download differs")
	}
	f.request(t, f.third, "GET", path, "", "", 404)
	f.request(t, f.operator, "GET", path+"/view?version=1", "", "", 404)
	for _, suffix := range []string{"/view", "/view?version=0", "/download?version=1&version=1", "?version=garbage", "?unknown=x"} {
		f.request(t, f.customer, "GET", path+suffix, "", "", 400)
	}
	_, raw := f.request(t, f.customer, "PATCH", "/api/v1/deals/"+d.ID, "replace-supplier-key", encode(map[string]any{"expected_version": 2, "supplier_party_id": f.third.org}), 200)
	d = dealDTO(t, raw)
	for _, c := range []*client{f.supplier, f.third} {
		f.request(t, c, "GET", path+"/download?version=1", "", "", 404)
	}
	f.request(t, f.customer, "GET", path+"/download?version=1", "", "", 200)
	d, next := generate(t, f, d, "private-document-02")
	if next.ID != doc.ID {
		t.Fatal("document ID not stable")
	}
	f.request(t, f.third, "GET", path+"/view?version=2", "", "", 200)
	f.request(t, f.supplier, "GET", path+"/view?version=2", "", "", 404)
	// Metadata can never disclose a server storage key.
	_, raw = f.request(t, f.customer, "GET", path, "", "", 200)
	if strings.Contains(string(raw), "storage") || strings.Contains(string(raw), "key") {
		t.Fatal("private path leaked")
	}
}

func TestConcurrentAgreementCommandsAndAtomicRollback(t *testing.T) {
	f := setup(t)
	d := draft(t, f)
	path := "/api/v1/deals/" + d.ID
	statuses := make(chan int, 12)
	failures := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, _, e := f.raw(f.customer, "PATCH", path, "same-edit-command-01", `{"expected_version":1,"title":"Одна новая редакция"}`, nil)
			if e != nil {
				failures <- e
				return
			}
			statuses <- response.StatusCode
		}()
	}
	wg.Wait()
	close(failures)
	close(statuses)
	for e := range failures {
		t.Error(e)
	}
	for status := range statuses {
		if status != 200 {
			t.Fatal("same command did not replay", status)
		}
	}
	if count(t, f.pool, "deal_versions") != 2 {
		t.Fatal("concurrent replay duplicated version")
	}
	_, b := f.request(t, f.customer, "GET", path, "", "", 200)
	d = dealDTO(t, b)
	d, _ = generate(t, f, d, "concurrent-document")
	statuses = make(chan int, 2)
	failures = make(chan error, 2)
	for i, c := range []*client{f.customer, f.supplier} {
		wg.Add(1)
		go func(c *client, i int) {
			defer wg.Done()
			response, _, e := f.raw(c, "POST", path+"/agree", fmt.Sprintf("parallel-agree-%02d", i), encode(acceptance(d)), nil)
			if e != nil {
				failures <- e
				return
			}
			statuses <- response.StatusCode
		}(c, i)
	}
	wg.Wait()
	close(statuses)
	close(failures)
	for e := range failures {
		t.Error(e)
	}
	for status := range statuses {
		if status != 200 {
			t.Fatal("parallel agreement failed", status)
		}
	}
	_, b = f.request(t, f.customer, "GET", path, "", "", 200)
	d = dealDTO(t, b)
	if d.Status != "AGREED" || len(d.Agreements) != 2 {
		t.Fatal("parallel agreement lost fact")
	}
	if _, e := f.pool.Exec(context.Background(), `CREATE FUNCTION fail_agreement_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='DEAL_REWORKED' THEN RAISE EXCEPTION 'test rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER test_fail_agreement_outbox BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION fail_agreement_outbox()`); e != nil {
		t.Fatal(e)
	}
	before := count(t, f.pool, "deal_versions")
	f.request(t, f.supplier, "POST", path+"/rework", "rollback-rework-key1", encode(map[string]any{"deal_version": d.DealVersion, "reason": "Тест атомарности"}), 503)
	if count(t, f.pool, "deal_versions") != before {
		t.Fatal("outbox failure committed deal version")
	}
	_, b = f.request(t, f.customer, "GET", path, "", "", 200)
	if dealDTO(t, b).Status != "AGREED" {
		t.Fatal("failed transaction changed status")
	}
	if _, e := f.pool.Exec(context.Background(), `DROP TRIGGER test_fail_agreement_outbox ON outbox`); e != nil {
		t.Fatal(e)
	}
	f.request(t, f.supplier, "POST", path+"/rework", "rollback-rework-key1", encode(map[string]any{"deal_version": d.DealVersion, "reason": "Тест атомарности"}), 200)
}

func TestAgreementDeadlineAfterLock(t *testing.T) {
	f := setup(t)
	d, doc := generate(t, f, draft(t, f), "deadline-document01")
	_ = doc
	// Admin fixture shortens the immutable deadline by using a new legitimate version.
	deadline := time.Now().UTC().Add(500 * time.Millisecond).Truncate(time.Microsecond).Format(time.RFC3339Nano)
	_, b := f.request(t, f.customer, "PATCH", "/api/v1/deals/"+d.ID, "shorten-deadline-key", encode(map[string]any{"expected_version": d.DealVersion, "deadline_at": deadline}), 200)
	d = dealDTO(t, b)
	// A blocked mutation must check the clock after it finally owns the row lock.
	tx, e := f.pool.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(context.Background(), `SELECT id FROM deals WHERE id=$1 FOR UPDATE`, d.ID); e != nil {
		t.Fatal(e)
	}
	result := make(chan dealservice.Result, 1)
	failed := make(chan error, 1)
	svc := dealservice.New(dealrepo.New(f.pool), make([]byte, 32), f.files)
	actor := dealservice.Actor{UserID: f.customer.user, OrganizationID: f.customer.org, RequestID: "deadline-lock"}
	title := "После ожидания"
	go func() {
		out, e := svc.Patch(context.Background(), actor, d.ID, "wait-deadline-key01", dealservice.PatchInput{ExpectedVersion: d.DealVersion, Title: &title})
		if e != nil {
			failed <- e
		} else {
			result <- out
		}
	}()
	time.Sleep(650 * time.Millisecond)
	if e := tx.Commit(context.Background()); e != nil {
		t.Fatal(e)
	}
	select {
	case out := <-result:
		if out.Status != 422 {
			t.Fatal("deadline ignored", out.Status, string(out.Body))
		}
		errorCode(t, out.Body, "DEADLINE_PASSED")
	case e := <-failed:
		t.Fatal(e)
	case <-time.After(5 * time.Second):
		t.Fatal("mutation blocked")
	}
	_, b = f.request(t, f.customer, "GET", "/api/v1/deals/"+d.ID, "", "", 200)
	if dealDTO(t, b).Status != "REVIEW_REQUIRED" {
		t.Fatal("read deadline mismatch")
	}
	// Retrying a newer deadline cannot revive an already expired aggregate.
	_, b = f.request(t, f.customer, "PATCH", "/api/v1/deals/"+d.ID, "revive-deadline-key", encode(map[string]any{"expected_version": d.DealVersion, "deadline_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}), 422)
	errorCode(t, b, "DEADLINE_PASSED")
}

func TestDifferentKeysCompeteOnOneVersion(t *testing.T) {
	f := setup(t)
	d := draft(t, f)
	path := "/api/v1/deals/" + d.ID
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	failed := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			response, _, err := f.raw(f.customer, "PATCH", path, fmt.Sprintf("competing-patch-%02d", i), encode(map[string]any{"expected_version": 1, "title": fmt.Sprintf("Редакция %d", i)}), nil)
			if err != nil {
				failed <- err
			} else {
				statuses <- response.StatusCode
			}
		}(i)
	}
	wg.Wait()
	close(statuses)
	close(failed)
	for err := range failed {
		t.Error(err)
	}
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("two edits committed from one expected version", counts)
	}
	_, b := f.request(t, f.customer, "GET", path, "", "", 200)
	d = dealDTO(t, b)
	if d.DealVersion != 2 {
		t.Fatal("race lost version", d.DealVersion)
	}
	d, _ = generate(t, f, d, "competing-document1")
	agree(t, f, f.customer, d, "competing-agree-c1")
	d = agree(t, f, f.supplier, d, "competing-agree-s1")
	statuses = make(chan int, 2)
	failed = make(chan error, 2)
	for i, c := range []*client{f.customer, f.supplier} {
		wg.Add(1)
		go func(c *client, i int) {
			defer wg.Done()
			response, _, err := f.raw(c, "POST", path+"/rework", fmt.Sprintf("competing-rework-%02d", i), encode(map[string]any{"deal_version": d.DealVersion, "reason": "Нужна доработка"}), nil)
			if err != nil {
				failed <- err
			} else {
				statuses <- response.StatusCode
			}
		}(c, i)
	}
	wg.Wait()
	close(statuses)
	close(failed)
	for err := range failed {
		t.Error(err)
	}
	counts = map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("two reworks committed from one expected version", counts)
	}
	if count(t, f.pool, "deal_versions") != 4 {
		t.Fatal("rework race duplicated version")
	}
	var events int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE deal_id=$1 AND type='DEAL_REWORKED'`, d.ID).Scan(&events); err != nil || events != 1 {
		t.Fatal("rework race duplicated event", events, err)
	}
}

func TestAfterFirstReserveTermsAndPDFCannotChange(t *testing.T) {
	f := setup(t)
	d, _ := generate(t, f, draft(t, f), "reserved-document01")
	agree(t, f, f.customer, d, "reserved-agree-c01")
	d = agree(t, f, f.supplier, d, "reserved-agree-s01")
	path := "/api/v1/deals/" + d.ID
	// A fixture sets the future reserve marker, without implementing a reserve or touching money.
	if _, err := f.pool.Exec(context.Background(), `UPDATE deals SET first_reserved_at=clock_timestamp(),status='FUNDED' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	before := count(t, f.pool, "deal_versions")
	for i, command := range []struct{ method, suffix, body string }{{"PATCH", "", encode(map[string]any{"expected_version": d.DealVersion, "title": "Forbidden"})}, {"POST", "/rework", encode(map[string]any{"deal_version": d.DealVersion, "reason": "Forbidden"})}, {"POST", "/documents/generate", encode(map[string]any{"expected_deal_version": d.DealVersion, "template_id": "printing-acceptance-v1"})}, {"POST", "/agree", encode(acceptance(d))}} {
		_, b := f.request(t, f.customer, command.method, path+command.suffix, fmt.Sprintf("after-reserve-%03d", i), command.body, 409)
		errorCode(t, b, "INVALID_STATE")
	}
	if count(t, f.pool, "deal_versions") != before || count(t, f.pool, "document_versions") != 1 || count(t, f.pool, "deal_agreements") != 2 {
		t.Fatal("reserved context changed")
	}
}

type slowDocumentStore struct {
	delegate dealservice.DocumentStore
	delay    time.Duration
}

func (s slowDocumentStore) Put(b []byte) (string, error) {
	key, err := s.delegate.Put(b)
	time.Sleep(s.delay)
	return key, err
}
func (s slowDocumentStore) Read(key string) ([]byte, error) { return s.delegate.Read(key) }
func TestDeadlineDuringPDFPreparationDoesNotPublishVersion(t *testing.T) {
	f := setup(t)
	in := input(f.supplier.org)
	in["deadline_at"] = time.Now().UTC().Add(600 * time.Millisecond).Truncate(time.Microsecond).Format(time.RFC3339Nano)
	_, b := f.request(t, f.customer, "POST", "/api/v1/deals", "slow-document-deal1", encode(in), 201)
	d := dealDTO(t, b)
	svc := dealservice.New(dealrepo.New(f.pool), make([]byte, 32), slowDocumentStore{delegate: f.files, delay: 700 * time.Millisecond})
	actor := dealservice.Actor{UserID: f.customer.user, OrganizationID: f.customer.org, RequestID: "pdf-preparation-deadline"}
	out, err := svc.Generate(context.Background(), actor, d.ID, "slow-document-key01", dealservice.GenerateInput{ExpectedDealVersion: 1, TemplateID: "printing-acceptance-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != 422 {
		t.Fatal("deadline during PDF preparation ignored", out.Status, string(out.Body))
	}
	errorCode(t, out.Body, "DEADLINE_PASSED")
	if count(t, f.pool, "deal_versions") != 1 || count(t, f.pool, "document_versions") != 0 || count(t, f.pool, "documents") != 0 {
		t.Fatal("failed PDF preparation published context")
	}
	repeat, err := svc.Generate(context.Background(), actor, d.ID, "slow-document-key01", dealservice.GenerateInput{ExpectedDealVersion: 1, TemplateID: "printing-acceptance-v1"})
	if err != nil || !bytes.Equal(out.Body, repeat.Body) {
		t.Fatal("deadline rejection not saved", err)
	}
}

func TestAgreementUploadStrictPartsAndInvalidPDF(t *testing.T) {
	f := setup(t)
	d := draft(t, f)
	path := "/api/v1/deals/" + d.ID + "/documents"
	upload := func(parts []struct{ name, value, filename, mime string }, status int) {
		t.Helper()
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for _, part := range parts {
			header := make(textproto.MIMEHeader)
			disposition := fmt.Sprintf(`form-data; name=%q`, part.name)
			if part.filename != "" {
				disposition += fmt.Sprintf(`; filename=%q`, part.filename)
			}
			header.Set("Content-Disposition", disposition)
			if part.mime != "" {
				header.Set("Content-Type", part.mime)
			}
			p, e := writer.CreatePart(header)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = io.WriteString(p, part.value)
		}
		writer.Close()
		request, e := http.NewRequest("POST", f.server.URL+path, &body)
		if e != nil {
			t.Fatal(e)
		}
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.Header.Set("Origin", origin)
		request.Header.Set("X-CSRF-Token", f.customer.csrf)
		request.Header.Set("Idempotency-Key", "upload-invalid-key01")
		response, e := f.customer.http.Do(request)
		if e != nil {
			t.Fatal(e)
		}
		defer response.Body.Close()
		raw, _ := io.ReadAll(response.Body)
		if response.StatusCode != status {
			t.Fatal("upload status", response.StatusCode, string(raw))
		}
	}
	type part = struct{ name, value, filename, mime string }
	upload([]part{{"expected_deal_version", "1", "", ""}, {"file", "%PDF-malformed", "invalid.pdf", "application/pdf"}}, 422)
	upload([]part{{"expected_deal_version", "1", "", ""}, {"expected_deal_version", "1", "", ""}}, 400)
	upload([]part{{"expected_deal_version", "1", "", ""}, {"unknown", "x", "", ""}}, 400)
	upload([]part{{"expected_deal_version", "1", "", ""}, {"file", "text", "bad.pdf", "text/plain"}}, 422)
	for _, body := range []string{`{"expected_version":1,"title":null}`, `{"expected_version":1,"amount_kopecks":12}`, `{"expected_version":1,"unknown":true}`} {
		f.request(t, f.customer, "PATCH", "/api/v1/deals/"+d.ID, "strict-patch-key001", body, 400)
	}
	if count(t, f.pool, "deal_versions") != 1 || count(t, f.pool, "document_versions") != 0 {
		t.Fatal("invalid upload changed aggregate")
	}
}
