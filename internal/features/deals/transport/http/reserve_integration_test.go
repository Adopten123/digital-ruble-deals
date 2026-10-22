package dealshttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dealrepo "github.com/Adopten123/digital-ruble-deals/internal/features/deals/repository/postgres"
	dealservice "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func reserveFixture(t *testing.T) *fixture {
	t.Helper()
	f := setup(t)
	// The original fixture guards nonmonetary commands. This stage deliberately
	// moves funds; assert the stronger ledger/reserve projections instead.
	t.Cleanup(func() {
		assertMonetaryFacts(t, f)
		f.balances = f.accountSnapshot(t)
	})
	return f
}

func accountID(t *testing.T, f *fixture, organization string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT id FROM accounts WHERE organization_id=$1`, organization).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertBalance(t *testing.T, f *fixture, organization string, total, reserved int64) {
	t.Helper()
	var gotTotal, gotReserved int64
	if err := f.pool.QueryRow(context.Background(), `SELECT total_kopecks,reserved_kopecks FROM accounts WHERE organization_id=$1`, organization).Scan(&gotTotal, &gotReserved); err != nil {
		t.Fatal(err)
	}
	if gotTotal != total || gotReserved != reserved {
		t.Fatalf("organization %s: total/reserved %d/%d, want %d/%d", organization, gotTotal, gotReserved, total, reserved)
	}
}

func assertMonetaryFacts(t *testing.T, f *fixture) {
	t.Helper()
	var invalidAccounts, invalidBatches int
	var globalSum string
	err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM accounts a WHERE a.total_kopecks::numeric<>(SELECT COALESCE(sum(e.delta_kopecks::numeric),0) FROM ledger_entries e WHERE e.account_id=a.id) OR a.reserved_kopecks::numeric<>(SELECT COALESCE(sum(r.amount_kopecks::numeric),0) FROM reserves r WHERE r.account_id=a.id AND r.status='ACTIVE') OR (a.organization_id IS NOT NULL AND (a.reserved_kopecks<0 OR a.total_kopecks<a.reserved_kopecks))`).Scan(&invalidAccounts)
	if err != nil {
		t.Fatal(err)
	}
	err = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_batches b WHERE (SELECT count(*) FROM ledger_entries e WHERE e.batch_id=b.id)<>2 OR (SELECT sum(e.delta_kopecks::numeric) FROM ledger_entries e WHERE e.batch_id=b.id)<>0`).Scan(&invalidBatches)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(context.Background(), `SELECT sum(total_kopecks::numeric)::text FROM accounts`).Scan(&globalSum); err != nil {
		t.Fatal(err)
	}
	if invalidAccounts != 0 || invalidBatches != 0 || globalSum != "0" {
		t.Fatalf("monetary facts disagree: %d account projections, %d batches, global sum %s", invalidAccounts, invalidBatches, globalSum)
	}
}

func reserveDraft(t *testing.T, f *fixture, key string, mutate func(map[string]string)) dealservice.Deal {
	t.Helper()
	in := input(f.supplier.org)
	if mutate != nil {
		mutate(in)
	}
	_, raw := f.request(t, f.customer, "POST", "/api/v1/deals", key+"-create", encode(in), 201)
	return dealDTO(t, raw)
}

func readyForReserve(t *testing.T, f *fixture, key string, mutate func(map[string]string)) dealservice.Deal {
	t.Helper()
	d, _ := generate(t, f, reserveDraft(t, f, key, mutate), key+"-pdf")
	d = agree(t, f, f.customer, d, key+"-customer")
	return agree(t, f, f.supplier, d, key+"-supplier")
}

func reserveBody(d dealservice.Deal) string {
	return encode(map[string]any{"deal_version": d.DealVersion})
}

func moveBody(amount, reason string) string {
	return encode(map[string]string{"amount_kopecks": amount, "reason": reason})
}

func reserveDTO(t *testing.T, raw []byte) (dealservice.Deal, map[string]any) {
	t.Helper()
	var out struct {
		Deal    dealservice.Deal `json:"deal"`
		Reserve map[string]any   `json:"reserve"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out.Deal, out.Reserve
}

func TestReserveAtomicFactsExactReplayAndRestart(t *testing.T) {
	f := reserveFixture(t)
	d := readyForReserve(t, f, "reserve-success-01", nil)
	path, body, key := "/api/v1/deals/"+d.ID+"/reserve", reserveBody(d), "reserve-success-command"
	beforeLedger, beforeVersions := count(t, f.pool, "ledger_entries"), count(t, f.pool, "deal_versions")
	_, first := f.request(t, f.customer, "POST", path, key, body, 201)
	card, reserve := reserveDTO(t, first)
	if card.Status != "FUNDED" || card.DealVersion != d.DealVersion || reserve["status"] != "ACTIVE" || reserve["amount_kopecks"] != "30000000" || reserve["currency"] != "RUB" || reserve["deal_id"] != d.ID || reserve["account_id"] != accountID(t, f, f.customer.org) || card.Reserve == nil {
		t.Fatalf("wrong reserve context: %s", first)
	}
	if count(t, f.pool, "reserves") != 1 || count(t, f.pool, "ledger_entries") != beforeLedger || count(t, f.pool, "deal_versions") != beforeVersions {
		t.Fatal("reserve duplicated facts or moved total funds")
	}
	assertBalance(t, f, f.customer.org, 50000000, 30000000)
	assertBalance(t, f, f.supplier.org, 0, 0)
	var bound bool
	err := f.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM reserves r JOIN deals d ON d.id=r.deal_id JOIN document_versions v ON v.document_id=r.document_id AND v.document_version=r.document_version WHERE d.id=$1 AND r.deal_version=d.deal_version AND d.first_reserved_at=r.created_at AND r.document_sha256=v.sha256 AND r.customer_party_id=$2 AND r.supplier_party_id=$3 AND r.amount_kopecks=30000000 AND d.status='FUNDED')`, d.ID, f.customer.org, f.supplier.org).Scan(&bound)
	if err != nil || !bound {
		t.Fatalf("reserve context not persisted atomically: %v", err)
	}
	beforeAudit, beforeOutbox := count(t, f.pool, "audit_events"), count(t, f.pool, "outbox")
	_, replay := f.request(t, f.customer, "POST", path, key, body, 201)
	if string(first) != string(replay) || count(t, f.pool, "audit_events") != beforeAudit || count(t, f.pool, "outbox") != beforeOutbox {
		t.Fatal("replay changed original result or duplicated audit/outbox")
	}
	// A new handler/repository must read the original outcome from PostgreSQL.
	restarted := httptest.NewServer(f.handler(t))
	oldURL := f.server.URL
	f.server.URL = restarted.URL
	_, replay = f.request(t, f.customer, "POST", path, key, body, 201)
	f.server.URL = oldURL
	restarted.Close()
	if string(first) != string(replay) {
		t.Fatal("restart lost exact reserve outcome")
	}
	_, raw := f.request(t, f.customer, "POST", path, key, encode(map[string]any{"deal_version": d.DealVersion + 1}), 409)
	errorCode(t, raw, "IDEMPOTENCY_CONFLICT")
	_, raw = f.request(t, f.customer, "POST", path, "new-reserve-key-01", body, 409)
	errorCode(t, raw, "INVALID_STATE")
	_, raw = f.request(t, f.supplier, "GET", "/api/v1/deals/"+d.ID, "", "", 200)
	if dealDTO(t, raw).Reserve == nil || strings.Contains(string(raw), "50000000") {
		t.Fatal("participant reserve missing or unrelated customer balance disclosed")
	}
	for i, command := range []struct{ method, suffix, body string }{
		{"PATCH", "", encode(map[string]any{"expected_version": d.DealVersion, "title": "После резерва"})},
		{"POST", "/rework", encode(map[string]any{"deal_version": d.DealVersion, "reason": "Изменить условия"})},
		{"POST", "/documents/generate", encode(map[string]any{"expected_deal_version": d.DealVersion, "template_id": "printing-acceptance-v1"})},
	} {
		_, raw = f.request(t, f.customer, command.method, "/api/v1/deals/"+d.ID+command.suffix, fmt.Sprintf("funded-protection-%02d", i), command.body, 409)
		errorCode(t, raw, "INVALID_STATE")
	}
}

func TestReserveRequiresCurrentAgreementAndCustomerAuthority(t *testing.T) {
	f := reserveFixture(t)
	d := reserveDraft(t, f, "reserve-precondition", nil)
	path := "/api/v1/deals/" + d.ID + "/reserve"
	_, raw := f.request(t, f.customer, "POST", path, "reserve-draft-key01", reserveBody(d), 409)
	errorCode(t, raw, "INVALID_STATE")
	d, _ = generate(t, f, d, "reserve-one-pdf-01")
	d = agree(t, f, f.customer, d, "reserve-one-agree-01")
	_, raw = f.request(t, f.customer, "POST", path, "reserve-one-key001", reserveBody(d), 409)
	errorCode(t, raw, "INVALID_STATE")
	d = agree(t, f, f.supplier, d, "reserve-two-agree-01")
	for _, tc := range []struct {
		c      *client
		status int
		code   string
	}{{f.supplier, 403, "FORBIDDEN"}, {f.third, 404, "RESOURCE_NOT_FOUND"}, {f.operator, 404, "RESOURCE_NOT_FOUND"}} {
		_, raw = f.request(t, tc.c, "POST", path, "reserve-role-key01", reserveBody(d), tc.status)
		errorCode(t, raw, tc.code)
	}
	_, raw = f.request(t, f.customer, "POST", path, "reserve-version01", encode(map[string]any{"deal_version": d.DealVersion + 1}), 409)
	errorCode(t, raw, "VERSION_CONFLICT")
	for i, body := range []string{`{}`, `{"deal_version":null}`, `{"deal_version":"1"}`, `{"deal_version":1,"amount_kopecks":"1"}`} {
		f.request(t, f.customer, "POST", path, fmt.Sprintf("reserve-shape-%03d", i), body, 400)
	}
	response, raw, err := f.raw(f.customer, "POST", path, "reserve-csrf-key01", reserveBody(d), map[string]string{"X-CSRF-Token": "invalid"})
	if err != nil || response.StatusCode != 403 {
		t.Fatalf("reserve CSRF check: %v %s", err, raw)
	}
	if count(t, f.pool, "reserves") != 0 {
		t.Fatal("rejected command created reserve")
	}
	// Simulate inconsistent aggregate state as an administrator: a status alone
	// cannot replace two matching consent facts or a current PDF.
	missing := reserveDraft(t, f, "reserve-missing-pdf", nil)
	if _, err = f.pool.Exec(context.Background(), `UPDATE deals SET status='AGREED' WHERE id=$1`, missing.ID); err != nil {
		t.Fatal(err)
	}
	_, raw = f.request(t, f.customer, "POST", "/api/v1/deals/"+missing.ID+"/reserve", "reserve-no-pdf-key", reserveBody(missing), 409)
	errorCode(t, raw, "DOCUMENT_REQUIRED")
	missing, _ = generate(t, f, reserveDraft(t, f, "reserve-missing-vote", nil), "reserve-no-vote-pdf")
	if _, err = f.pool.Exec(context.Background(), `UPDATE deals SET status='AGREED' WHERE id=$1`, missing.ID); err != nil {
		t.Fatal(err)
	}
	_, raw = f.request(t, f.customer, "POST", "/api/v1/deals/"+missing.ID+"/reserve", "reserve-no-vote-key", reserveBody(missing), 422)
	errorCode(t, raw, "CONSENTS_MISSING")
	// A genuine rework invalidates the old command version and leaves no reserve.
	_, raw = f.request(t, f.supplier, "POST", "/api/v1/deals/"+d.ID+"/rework", "reserve-before-rework", encode(map[string]any{"deal_version": d.DealVersion, "reason": "Изменить согласованный акт"}), 200)
	_, raw = f.request(t, f.customer, "POST", path, "reserve-old-votes-01", reserveBody(d), 409)
	errorCode(t, raw, "VERSION_CONFLICT")
	assertBalance(t, f, f.customer.org, 50000000, 0)
}

type moneyResponse struct {
	status int
	body   []byte
	err    error
}

func concurrentMoney(commands ...func() moneyResponse) []moneyResponse {
	start := make(chan struct{})
	results := make([]moneyResponse, len(commands))
	var wg sync.WaitGroup
	for i, command := range commands {
		wg.Add(1)
		go func(i int, command func() moneyResponse) {
			defer wg.Done()
			<-start
			results[i] = command()
		}(i, command)
	}
	close(start)
	wg.Wait()
	return results
}

func moneyCommand(f *fixture, c *client, path, key, body string) func() moneyResponse {
	return func() moneyResponse {
		r, raw, err := f.raw(c, "POST", path, key, body, nil)
		status := 0
		if r != nil {
			status = r.StatusCode
		}
		return moneyResponse{status, raw, err}
	}
}

func oneSuccessOneInsufficient(t *testing.T, results []moneyResponse) {
	t.Helper()
	successes, rejected := 0, 0
	for _, result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		switch result.status {
		case 201:
			successes++
		case 422:
			errorCode(t, result.body, "INSUFFICIENT_AVAILABLE_FUNDS")
			rejected++
		default:
			t.Fatalf("unexpected concurrent result: %d %s", result.status, result.body)
		}
	}
	if successes != 1 || rejected != 1 {
		t.Fatalf("expected one success and one rejection, got %d/%d", successes, rejected)
	}
}

func TestConcurrentReservesAndDebitsRespectAvailableFunds(t *testing.T) {
	t.Run("two independent deals", func(t *testing.T) {
		f := reserveFixture(t)
		a := readyForReserve(t, f, "competing-reserve-a", nil)
		b := readyForReserve(t, f, "competing-reserve-b", nil)
		results := concurrentMoney(
			moneyCommand(f, f.customer, "/api/v1/deals/"+a.ID+"/reserve", "competing-reserve-a1", reserveBody(a)),
			moneyCommand(f, f.customer, "/api/v1/deals/"+b.ID+"/reserve", "competing-reserve-b1", reserveBody(b)),
		)
		oneSuccessOneInsufficient(t, results)
		assertBalance(t, f, f.customer.org, 50000000, 30000000)
		if count(t, f.pool, "reserves") != 1 {
			t.Fatal("competing reserves overdrew the customer")
		}
	})
	t.Run("reserve and external debit", func(t *testing.T) {
		f := reserveFixture(t)
		d := readyForReserve(t, f, "competing-debit-01", nil)
		results := concurrentMoney(
			moneyCommand(f, f.customer, "/api/v1/deals/"+d.ID+"/reserve", "reserve-with-debit1", reserveBody(d)),
			moneyCommand(f, f.operator, "/api/v1/demo/accounts/"+accountID(t, f, f.customer.org)+"/debits", "debit-with-reserve1", moveBody("30000000", "Конкурирующее учебное списание")),
		)
		oneSuccessOneInsufficient(t, results)
		if results[0].status == 201 {
			assertBalance(t, f, f.customer.org, 50000000, 30000000)
		} else {
			assertBalance(t, f, f.customer.org, 20000000, 0)
		}
	})
	t.Run("same reserve key in parallel", func(t *testing.T) {
		f := reserveFixture(t)
		d := readyForReserve(t, f, "duplicate-reserve01", nil)
		commands := make([]func() moneyResponse, 8)
		for i := range commands {
			commands[i] = moneyCommand(f, f.customer, "/api/v1/deals/"+d.ID+"/reserve", "parallel-same-reserve", reserveBody(d))
		}
		results := concurrentMoney(commands...)
		for _, result := range results {
			if result.err != nil || result.status != 201 || string(result.body) != string(results[0].body) {
				t.Fatalf("concurrent replay changed outcome: %v %d %s", result.err, result.status, result.body)
			}
		}
		if count(t, f.pool, "reserves") != 1 {
			t.Fatal("parallel replay created extra reserve")
		}
		assertBalance(t, f, f.customer.org, 50000000, 30000000)
	})
}

func TestOperatorMovementsReplayIsolationAndReservedFunds(t *testing.T) {
	f := reserveFixture(t)
	id := accountID(t, f, f.customer.org)
	path := "/api/v1/demo/accounts/" + id
	d := readyForReserve(t, f, "operator-reserve01", nil)
	f.request(t, f.customer, "POST", "/api/v1/deals/"+d.ID+"/reserve", "operator-hold-command", reserveBody(d), 201)
	_, refusal := f.request(t, f.operator, "POST", path+"/debits", "insufficient-debit01", moveBody("20000001", "Не затрагивать резерв"), 422)
	errorCode(t, refusal, "INSUFFICIENT_AVAILABLE_FUNDS")
	_, credit := f.request(t, f.operator, "POST", path+"/credits", "operator-credit-01", moveBody("10000000", "  Учебное пополнение  "), 201)
	if object(t, credit)["transaction_id"] == "" || object(t, credit)["account"].(map[string]any)["available_kopecks"] != "30000000" {
		t.Fatalf("incorrect credit response: %s", credit)
	}
	_, replay := f.request(t, f.operator, "POST", path+"/credits", "operator-credit-01", moveBody("10000000", "Учебное пополнение"), 201)
	if string(credit) != string(replay) {
		t.Fatal("normalized credit replay changed response")
	}
	// A terminal refusal remains the original outcome after replenishment.
	_, replay = f.request(t, f.operator, "POST", path+"/debits", "insufficient-debit01", moveBody("20000001", "Не затрагивать резерв"), 422)
	if string(refusal) != string(replay) {
		t.Fatal("saved debit refusal changed after credit")
	}
	_, raw := f.request(t, f.operator, "POST", path+"/credits", "operator-credit-01", moveBody("1", "Учебное пополнение"), 409)
	errorCode(t, raw, "IDEMPOTENCY_CONFLICT")
	_, debit := f.request(t, f.operator, "POST", path+"/debits", "operator-debit-001", moveBody("30000000", "Только доступный остаток"), 201)
	_, replay = f.request(t, f.operator, "POST", path+"/debits", "operator-debit-001", moveBody("30000000", "Только доступный остаток"), 201)
	if string(debit) != string(replay) {
		t.Fatal("debit replay changed exact original response")
	}
	assertBalance(t, f, f.customer.org, 30000000, 30000000)
	if count(t, f.pool, "ledger_batches") != 3 || count(t, f.pool, "ledger_entries") != 6 {
		t.Fatal("operator command replay duplicated ledger")
	}
	for _, c := range []*client{f.customer, f.supplier, f.third} {
		f.request(t, c, "GET", "/api/v1/demo/accounts", "", "", 403)
		f.request(t, c, "POST", path+"/credits", "owner-credit-denied", moveBody("1", "Недопустимый доступ"), 403)
		f.request(t, c, "POST", path+"/debits", "owner-debit-denied1", moveBody("1", "Недопустимый доступ"), 403)
	}
	for _, target := range []string{"missing-account", "poaktu-demo-debits-v1"} {
		_, raw = f.request(t, f.operator, "POST", "/api/v1/demo/accounts/"+target+"/credits", "system-credit-denied", moveBody("1", "Скрытый счёт"), 404)
		errorCode(t, raw, "RESOURCE_NOT_FOUND")
	}
	// The operator identity, operation and account are all part of the key scope.
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO users(id,email,password_hash,system_role) VALUES('second-operator','second-operator@example.test','unused','DEMO_OPERATOR')`); err != nil {
		t.Fatal(err)
	}
	second := f.session(t, "second-operator@example.test")
	f.request(t, second, "POST", path+"/credits", "operator-credit-01", moveBody("1", "Другой оператор"), 201)
	f.request(t, f.operator, "POST", "/api/v1/demo/accounts/"+accountID(t, f, f.supplier.org)+"/credits", "operator-credit-01", moveBody("1", "Другой счёт"), 201)
	assertBalance(t, f, f.customer.org, 30000001, 30000000)
	assertBalance(t, f, f.supplier.org, 1, 0)
}

func TestOperatorAccountCatalogStrictPaginationAndPrivacy(t *testing.T) {
	f := reserveFixture(t)
	path := "/api/v1/demo/accounts"
	_, raw := f.request(t, f.operator, "GET", path+"?limit=1", "", "", 200)
	first := object(t, raw)
	items := first["items"].([]any)
	if len(items) != 1 || first["next_cursor"] == nil {
		t.Fatalf("missing account pagination: %s", raw)
	}
	seen := map[string]bool{items[0].(map[string]any)["id"].(string): true}
	cursor := first["next_cursor"].(string)
	for cursor != "" {
		_, raw = f.request(t, f.operator, "GET", path+"?limit=1&cursor="+url.QueryEscape(cursor), "", "", 200)
		page := object(t, raw)
		for _, item := range page["items"].([]any) {
			id := item.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatal("account repeated across pages")
			}
			seen[id] = true
		}
		cursor, _ = page["next_cursor"].(string)
	}
	if len(seen) != 3 {
		t.Fatal("catalog must expose only three organization accounts", len(seen))
	}
	for _, query := range []string{"?unknown=1", "?limit=1&limit=2", "?limit=0", "?limit=101", "?cursor=forged", "?q=%zz", "?limit=1&q=changed&cursor=" + url.QueryEscape(first["next_cursor"].(string))} {
		f.request(t, f.operator, "GET", path+query, "", "", 400)
	}
	_, raw = f.request(t, f.operator, "GET", path+"?q="+url.QueryEscape(f.customer.org), "", "", 200)
	page := object(t, raw)
	if len(page["items"].([]any)) != 1 || strings.Contains(string(raw), "password") || strings.Contains(string(raw), "email") || strings.Contains(string(raw), "DEMO_ISSUANCE") || strings.Contains(string(raw), "DEMO_DEBITS") {
		t.Fatalf("operator account scope or privacy failed: %s", raw)
	}
}

func TestOperatorCanonicalAmountsAndInt64Overflow(t *testing.T) {
	f := reserveFixture(t)
	path := "/api/v1/demo/accounts/" + accountID(t, f, f.customer.org) + "/credits"
	for i, amount := range []string{"0", "-1", "+1", "01", "1.5", "1e2", " 1", "9223372036854775808"} {
		_, raw := f.request(t, f.operator, "POST", path, fmt.Sprintf("bad-money-format-%02d", i), moveBody(amount, "Проверка формата"), 400)
		errorCode(t, raw, "INVALID_AMOUNT")
	}
	for i, body := range []string{`{}`, `{"amount_kopecks":1,"reason":"Причина"}`, `{"amount_kopecks":"1","reason":null}`, moveBody("1", " "), moveBody("1", strings.Repeat("я", 2001)), `{"amount_kopecks":"1","reason":"Причина","extra":true}`} {
		f.request(t, f.operator, "POST", path, fmt.Sprintf("bad-money-body-%03d", i), body, 400)
	}
	_, raw := f.request(t, f.operator, "POST", path, "credit-up-to-max01", moveBody("9223372036804775807", "Граница int64"), 201)
	if object(t, raw)["account"].(map[string]any)["total_kopecks"] != "9223372036854775807" {
		t.Fatalf("exact int64 balance lost: %s", raw)
	}
	_, rejected := f.request(t, f.operator, "POST", path, "credit-overflow001", moveBody("1", "Переполнение"), 422)
	errorCode(t, rejected, "AMOUNT_OVERFLOW")
	_, replay := f.request(t, f.operator, "POST", path, "credit-overflow001", moveBody("1", "Переполнение"), 422)
	if string(rejected) != string(replay) || count(t, f.pool, "ledger_batches") != 2 {
		t.Fatal("overflow retry changed outcome or posted money")
	}
	assertBalance(t, f, f.customer.org, 9223372036854775807, 0)
	// The destination may fit while the shared issuance source would underflow.
	_, raw = f.request(t, f.operator, "POST", "/api/v1/demo/accounts/"+accountID(t, f, f.supplier.org)+"/credits", "issuance-overflow01", moveBody("2", "Переполнение источника"), 422)
	errorCode(t, raw, "AMOUNT_OVERFLOW")
	assertBalance(t, f, f.supplier.org, 0, 0)
}

func TestReserveRechecksDeadlineAfterWaitingForAccount(t *testing.T) {
	f := reserveFixture(t)
	deadline := time.Now().UTC().Add(2 * time.Second).Truncate(time.Microsecond)
	d := readyForReserve(t, f, "reserve-lock-deadline", func(in map[string]string) { in["deadline_at"] = deadline.Format(time.RFC3339Nano) })
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blockerPID int
	if err = tx.QueryRow(context.Background(), `SELECT pg_backend_pid() FROM accounts WHERE organization_id=$1 FOR UPDATE`, f.customer.org).Scan(&blockerPID); err != nil {
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

func TestReworkAndReserveSerializeOnTheSameDeal(t *testing.T) {
	f := reserveFixture(t)
	d := readyForReserve(t, f, "reserve-rework-race", nil)
	path := "/api/v1/deals/" + d.ID
	results := concurrentMoney(
		moneyCommand(f, f.customer, path+"/reserve", "race-reserve-key01", reserveBody(d)),
		moneyCommand(f, f.supplier, path+"/rework", "race-rework-key001", encode(map[string]any{"deal_version": d.DealVersion, "reason": "Изменить условия"})),
	)
	for _, r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
	}
	_, raw := f.request(t, f.customer, "GET", path, "", "", 200)
	current := dealDTO(t, raw)
	switch {
	case results[0].status == 201 && results[1].status == 409:
		errorCode(t, results[1].body, "INVALID_STATE")
		if current.Status != "FUNDED" || current.DealVersion != d.DealVersion || count(t, f.pool, "reserves") != 1 {
			t.Fatal("reserve winner lost immutable context")
		}
		assertBalance(t, f, f.customer.org, 50000000, 30000000)
	case results[0].status == 409 && results[1].status == 200:
		errorCode(t, results[0].body, "VERSION_CONFLICT")
		if current.Status != "DRAFT" || current.DealVersion != d.DealVersion+1 || current.Document != nil || len(current.Agreements) != 0 || count(t, f.pool, "reserves") != 0 {
			t.Fatal("rework winner retained old consent or reserved money")
		}
		assertBalance(t, f, f.customer.org, 50000000, 0)
	default:
		t.Fatalf("incompatible race outcomes: reserve %d %s, rework %d %s", results[0].status, results[0].body, results[1].status, results[1].body)
	}
}

func TestReserveAndOperatorOutboxFailuresRollBackEveryFact(t *testing.T) {
	for _, operation := range []string{"reserve", "credits", "debits"} {
		t.Run(operation, func(t *testing.T) {
			f := reserveFixture(t)
			d := readyForReserve(t, f, "rollback-money-"+operation, nil)
			if _, err := f.pool.Exec(context.Background(), `CREATE FUNCTION reject_money_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type IN ('DEAL_RESERVED','DEMO_CREDITED','DEMO_DEBITED') THEN RAISE EXCEPTION 'injected outbox failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_money_outbox BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION reject_money_outbox()`); err != nil {
				t.Fatal(err)
			}
			before := map[string]int{}
			for _, table := range []string{"reserves", "ledger_batches", "ledger_entries", "audit_events", "outbox", "idempotency_keys", "operator_command_results"} {
				before[table] = count(t, f.pool, table)
			}
			c, path, body := f.operator, "/api/v1/demo/accounts/"+accountID(t, f, f.customer.org)+"/"+operation, moveBody("100", "Проверка атомарности")
			if operation == "reserve" {
				c, path, body = f.customer, "/api/v1/deals/"+d.ID+"/reserve", reserveBody(d)
			}
			key := "rollback-money-command-" + operation
			f.request(t, c, "POST", path, key, body, 503)
			for table, expected := range before {
				if actual := count(t, f.pool, table); actual != expected {
					t.Errorf("%s committed after failed outbox: got %d want %d", table, actual, expected)
				}
			}
			assertBalance(t, f, f.customer.org, 50000000, 0)
			_, raw := f.request(t, f.customer, "GET", "/api/v1/deals/"+d.ID, "", "", 200)
			if dealDTO(t, raw).Status != "AGREED" {
				t.Fatal("outbox failure committed deal state")
			}
			if _, err := f.pool.Exec(context.Background(), `DROP TRIGGER reject_money_outbox ON outbox`); err != nil {
				t.Fatal(err)
			}
			f.request(t, c, "POST", path, key, body, 201)
		})
	}
}

func TestReserveDatabaseFactsAndRuntimeGrants(t *testing.T) {
	f := reserveFixture(t)
	var roleExists bool
	if err := f.pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='poaktu_runtime')`).Scan(&roleExists); err != nil {
		t.Fatal(err)
	}
	if !roleExists {
		t.Skip("poaktu_runtime role absent: runtime-role check requires the disposable deployment roles")
	}
	var schema string
	if err := f.pool.QueryRow(context.Background(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	grants, err := os.ReadFile(filepath.Join(testutil.MigrationsDir(), "..", "deploy", "postgres", "runtime_grants.sql"))
	if err != nil {
		t.Fatal(err)
	}
	// Apply the actual deployment grants only to this random test schema.
	sql := strings.ReplaceAll(string(grants), "SCHEMA public", "SCHEMA "+pgx.Identifier{schema}.Sanitize())
	if _, err = f.pool.Exec(context.Background(), sql); err != nil {
		t.Fatal(err)
	}
	config := f.pool.Config().Copy()
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET ROLE poaktu_runtime`)
		return err
	}
	runtimePool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimePool.Close()
	runtimeFixture := *f
	runtimeFixture.pool = runtimePool
	server := httptest.NewServer(runtimeFixture.handler(t))
	defer server.Close()
	d := readyForReserve(t, f, "runtime-reserve-01", nil)
	oldURL := f.server.URL
	f.server.URL = server.URL
	defer func() { f.server.URL = oldURL }()
	response, raw, requestErr := f.raw(f.customer, "POST", "/api/v1/deals/"+d.ID+"/reserve", "runtime-reserve-key", reserveBody(d), nil)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	if response.StatusCode != 201 {
		svc := dealservice.New(dealrepo.New(runtimePool), make([]byte, 32), f.files)
		_, err := svc.Reserve(context.Background(), dealservice.Actor{UserID: f.customer.user, OrganizationID: f.customer.org, RequestID: "runtime-diagnostic"}, d.ID, "runtime-reserve-key", dealservice.ReserveInput{DealVersion: d.DealVersion})
		t.Fatalf("runtime reserve got %d: %s; repository error: %v", response.StatusCode, raw, err)
	}
	path := "/api/v1/demo/accounts/" + accountID(t, f, f.customer.org)
	f.request(t, f.operator, "POST", path+"/credits", "runtime-credit-key", moveBody("100", "Рабочая роль"), 201)
	f.request(t, f.operator, "POST", path+"/debits", "runtime-debit-key1", moveBody("100", "Рабочая роль"), 201)
	for _, statement := range []string{
		`UPDATE users SET system_role='DEMO_OPERATOR' WHERE email='customer@demo.poaktu.local'`,
		`UPDATE accounts SET total_kopecks=total_kopecks+1 WHERE organization_id IS NOT NULL`,
		`UPDATE accounts SET reserved_kopecks=0 WHERE reserved_kopecks>0`,
		`UPDATE reserves SET amount_kopecks=1`,
		`DELETE FROM reserves`,
		`UPDATE ledger_entries SET delta_kopecks=1`,
		`DELETE FROM ledger_batches`,
		`UPDATE operator_command_results SET status_code=200`,
		`DELETE FROM idempotency_keys`,
		`UPDATE deals SET status='SETTLED' WHERE status='FUNDED'`,
	} {
		if _, err := runtimePool.Exec(context.Background(), statement); err == nil {
			t.Errorf("runtime unexpectedly allowed %s", statement)
		}
	}
	// Immutability also applies to a schema owner, independently of role grants.
	for _, statement := range []string{`UPDATE reserves SET amount_kopecks=amount_kopecks`, `DELETE FROM reserves`, `UPDATE operator_command_results SET response_body=response_body`, `UPDATE ledger_entries SET delta_kopecks=delta_kopecks`} {
		if _, err := f.pool.Exec(context.Background(), statement); err == nil {
			t.Errorf("owner could mutate published fact: %s", statement)
		}
	}
	assertBalance(t, f, f.customer.org, 50000000, 30000000)
}
