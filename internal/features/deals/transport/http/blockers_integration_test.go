package dealshttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dealservice "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
)

func blockBody(d dealservice.Deal, reason string) string {
	return encode(dealservice.ReasonInput{DealVersion: d.DealVersion, Reason: reason})
}
func cancellationDTO(t *testing.T, raw []byte) (dealservice.Deal, dealservice.CancellationRequest) {
	t.Helper()
	var out struct {
		Deal    dealservice.Deal                `json:"deal"`
		Request dealservice.CancellationRequest `json:"cancellation_request"`
	}
	if e := json.Unmarshal(raw, &out); e != nil {
		t.Fatal(e)
	}
	return out.Deal, out.Request
}
func requestCancellation(t *testing.T, f *fixture, c *client, d dealservice.Deal, key string) (dealservice.Deal, dealservice.CancellationRequest, []byte) {
	t.Helper()
	_, raw := f.request(t, c, "POST", "/api/v1/deals/"+d.ID+"/cancellation-requests", key, blockBody(d, "Учебная согласованная отмена"), 201)
	card, request := cancellationDTO(t, raw)
	return card, request, raw
}
func cancellationPath(d dealservice.Deal, r dealservice.CancellationRequest, action string) string {
	return "/api/v1/deals/" + d.ID + "/cancellation-requests/" + r.ID + "/" + action
}
func currentCard(t *testing.T, f *fixture, d dealservice.Deal) dealservice.Deal {
	t.Helper()
	_, raw := f.request(t, f.customer, "GET", "/api/v1/deals/"+d.ID, "", "", 200)
	return dealDTO(t, raw)
}

func TestCancellationDraftNeedsTwoOrganisationsAndPreservesReplay(t *testing.T) {
	f := reserveFixture(t)
	d := reserveDraft(t, f, "cancel-draft", nil)
	balances := f.accountSnapshot(t)
	beforeLedger := count(t, f.pool, "ledger_entries")
	card, request, first := requestCancellation(t, f, f.customer, d, "cancel-draft-key01")
	if card.Status != "CANCELLATION_PENDING" || request.Status != "PENDING" || request.Decision != nil || card.CancellationRequest == nil || request.InitiatorOrganisationID != f.customer.org {
		t.Fatal("request did not preserve one-party consent", string(first))
	}
	audit, outbox := count(t, f.pool, "audit_events"), count(t, f.pool, "outbox")
	_, same, _ := requestCancellation(t, f, f.customer, d, "cancel-draft-key02")
	if same.ID != request.ID || count(t, f.pool, "audit_events") != audit || count(t, f.pool, "outbox") != outbox || count(t, f.pool, "cancellation_decisions") != 0 {
		t.Fatal("new key duplicated a request or added a second vote")
	}
	for _, action := range []string{"accept", "reject"} {
		body := reserveBody(d)
		if action == "reject" {
			body = blockBody(d, "Инициатор не может голосовать повторно")
		}
		_, raw := f.request(t, f.customer, "POST", cancellationPath(d, request, action), "self-cancel-"+action+"-01", body, 409)
		errorCode(t, raw, "CANCELLATION_SELF_APPROVAL")
	}
	_, raw := f.request(t, f.supplier, "POST", "/api/v1/deals/"+d.ID+"/cancellation-requests", "other-cancel-request", blockBody(d, "Не второй голос"), 409)
	errorCode(t, raw, "INVALID_STATE")
	_, accepted := f.request(t, f.supplier, "POST", cancellationPath(d, request, "accept"), "draft-cancel-accept", reserveBody(d), 200)
	card, decision := cancellationDTO(t, accepted)
	if card.Status != "CANCELLED" || card.Reserve != nil || card.CancellationRequest != nil || decision.Status != "ACCEPTED" || decision.Decision == nil || decision.Decision.OrganisationID != f.supplier.org || decision.Decision.Reason != nil {
		t.Fatal("two parties did not cancel the unfunded draft", string(accepted))
	}
	if f.accountSnapshot(t) != balances || count(t, f.pool, "ledger_entries") != beforeLedger {
		t.Fatal("draft cancellation changed money")
	}
	restarted := httptest.NewServer(f.handler(t))
	oldURL := f.server.URL
	f.server.URL = restarted.URL
	_, replay := f.request(t, f.supplier, "POST", cancellationPath(d, request, "accept"), "draft-cancel-accept", reserveBody(d), 200)
	f.server.URL = oldURL
	restarted.Close()
	if string(replay) != string(accepted) {
		t.Fatal("restart lost exact cancellation result")
	}
	_, replay = f.request(t, f.customer, "POST", "/api/v1/deals/"+d.ID+"/cancellation-requests", "cancel-draft-key01", blockBody(d, "Учебная согласованная отмена"), 201)
	if string(replay) != string(first) {
		t.Fatal("old request replay changed historical response")
	}
	f.request(t, f.supplier, "POST", cancellationPath(d, request, "accept"), "new-late-accept01", reserveBody(d), 409)
}

func TestCancellationReleasesOnlyItsOwnReserveAndKeepsExistingSignatureReadable(t *testing.T) {
	f := reserveFixture(t)
	d := fundedForSignature(t, f, "cancel-funded", nil)
	other := fundedForSignature(t, f, "cancel-other", func(in map[string]string) { in["amount_kopecks"] = "10000000" })
	_, signature, _ := sign(t, f, f.customer, d, "cancel-existing-sign")
	ledger := count(t, f.pool, "ledger_entries")
	_, request, _ := requestCancellation(t, f, f.supplier, d, "cancel-funded-request")
	assertBalance(t, f, f.customer.org, 50000000, 40000000)
	_, raw := f.request(t, f.customer, "POST", cancellationPath(d, request, "accept"), "cancel-funded-accept", reserveBody(d), 200)
	card, closed := cancellationDTO(t, raw)
	if card.Status != "REFUNDED" || card.Reserve == nil || card.Reserve.ID != d.Reserve.ID || card.Reserve.Status != "RELEASED" || closed.Status != "ACCEPTED" {
		t.Fatal("funded cancellation did not release exactly its reserve", string(raw))
	}
	assertBalance(t, f, f.customer.org, 50000000, 10000000)
	assertBalance(t, f, f.supplier.org, 0, 0)
	if count(t, f.pool, "ledger_entries") != ledger || currentCard(t, f, other).Reserve.Status != "ACTIVE" {
		t.Fatal("release moved money or released another deal")
	}
	audit, outbox := count(t, f.pool, "audit_events"), count(t, f.pool, "outbox")
	current, existing, _ := sign(t, f, f.customer, d, "cancel-noop-sign-01")
	if current.Status != "REFUNDED" || existing.ID != signature.ID || count(t, f.pool, "audit_events") != audit || count(t, f.pool, "outbox") != outbox {
		t.Fatal("existing signature was changed after refund")
	}
	f.request(t, f.supplier, "POST", "/api/v1/documents/"+d.Document.ID+"/sign", "cancel-new-sign-001", signBody(d), 409)
}

func TestDisputeCancellationAndRejectedReviewRemainIndependent(t *testing.T) {
	f := reserveFixture(t)
	d := readyForReserve(t, f, "blocker-priority", nil)
	_, raw := f.request(t, f.supplier, "POST", "/api/v1/deals/"+d.ID+"/disputes", "priority-dispute01", blockBody(d, "Нужна совместная проверка"), 201)
	var opened struct {
		Dispute dealservice.Dispute `json:"dispute"`
		Deal    dealservice.Deal    `json:"deal"`
	}
	if e := json.Unmarshal(raw, &opened); e != nil {
		t.Fatal(e)
	}
	if opened.Deal.Status != "DISPUTED" || opened.Dispute.OrganisationID != f.supplier.org {
		t.Fatal("dispute did not block", string(raw))
	}
	card, request, _ := requestCancellation(t, f, f.customer, d, "priority-cancel01")
	if card.Status != "CANCELLATION_PENDING" || card.Dispute == nil {
		t.Fatal("cancellation hid the dispute fact")
	}
	_, raw = f.request(t, f.supplier, "POST", cancellationPath(d, request, "reject"), "priority-reject01", blockBody(d, "Пока не согласен с отменой"), 200)
	card, closed := cancellationDTO(t, raw)
	if card.Status != "REVIEW_REQUIRED" || card.Dispute == nil || card.CancellationRequest != nil || closed.Status != "REJECTED" || len(card.ReviewReasons) != 1 || card.ReviewReasons[0] != "CANCELLATION_REJECTED" {
		t.Fatal("rejection erased a fact or reopened payment", string(raw))
	}
	card, next, _ := requestCancellation(t, f, f.supplier, d, "priority-next-cancel")
	if next.ID == request.ID || card.Status != "REVIEW_REQUIRED" || card.CancellationRequest == nil || card.Dispute == nil {
		t.Fatal("new request removed sticky review")
	}
	f.request(t, f.customer, "POST", cancellationPath(d, request, "accept"), "old-request-accept", reserveBody(d), 409)
	_, raw = f.request(t, f.customer, "GET", "/api/v1/deals?status=REVIEW_REQUIRED", "", "", 200)
	if !strings.Contains(string(raw), d.ID) || !strings.Contains(string(raw), next.ID) {
		t.Fatal("list projection differs from card priority")
	}
	_, raw = f.request(t, f.customer, "POST", cancellationPath(d, next, "accept"), "priority-final-accept", reserveBody(d), 200)
	card, _ = cancellationDTO(t, raw)
	if card.Status != "CANCELLED" || card.Dispute == nil || len(card.ReviewReasons) != 1 {
		t.Fatal("terminal completion lost facts")
	}
}

func TestBlockerAuthorityShapesBindingsAndNoOrdinaryBypass(t *testing.T) {
	f := reserveFixture(t)
	d := readyForReserve(t, f, "blocker-authority", nil)
	_, request, _ := requestCancellation(t, f, f.customer, d, "authority-cancel-key")
	for _, actor := range []*client{f.third, f.operator} {
		for i, path := range []string{"/api/v1/deals/" + d.ID + "/disputes", "/api/v1/deals/" + d.ID + "/cancellation-requests", cancellationPath(d, request, "accept"), cancellationPath(d, request, "reject")} {
			body := blockBody(d, "Нет прав")
			if strings.HasSuffix(path, "/accept") {
				body = reserveBody(d)
			}
			_, raw := f.request(t, actor, "POST", path, fmt.Sprintf("blocker-private-%d", i), body, 404)
			errorCode(t, raw, "RESOURCE_NOT_FOUND")
		}
	}
	for i, command := range []struct{ method, suffix, body string }{
		{"PATCH", "", encode(map[string]any{"expected_version": d.DealVersion, "title": "Запрещено"})},
		{"POST", "/agree", encode(acceptance(d))}, {"POST", "/reserve", reserveBody(d)},
		{"POST", "/rework", blockBody(d, "Запрещено")},
		{"POST", "/documents/generate", encode(map[string]any{"expected_deal_version": d.DealVersion, "template_id": "printing-acceptance-v1"})},
	} {
		_, raw := f.request(t, f.customer, command.method, "/api/v1/deals/"+d.ID+command.suffix, fmt.Sprintf("blocker-bypass-%d", i), command.body, 409)
		errorCode(t, raw, "INVALID_STATE")
	}
	path := "/api/v1/deals/" + d.ID + "/disputes"
	_, raw := f.request(t, f.supplier, "POST", path, "stale-blocker-key1", encode(dealservice.ReasonInput{DealVersion: d.DealVersion + 1, Reason: "Старая версия"}), 409)
	errorCode(t, raw, "VERSION_CONFLICT")
	_, raw = f.request(t, f.supplier, "POST", path, "stale-blocker-key1", blockBody(d, "Изменённое тело"), 409)
	errorCode(t, raw, "IDEMPOTENCY_CONFLICT")
	for i, body := range []string{`{}`, `{"deal_version":2,"reason":" "}`, encode(dealservice.ReasonInput{DealVersion: d.DealVersion, Reason: strings.Repeat("я", 2001)}), `{"deal_version":2,"reason":"x","organization_id":"forged"}`} {
		f.request(t, f.supplier, "POST", path, fmt.Sprintf("blocker-invalid-%d", i), body, 400)
	}
	response, raw, e := f.raw(f.supplier, "POST", path, "blocker-csrf-key01", blockBody(d, "Нет CSRF"), map[string]string{"X-CSRF-Token": "invalid"})
	if e != nil || response.StatusCode != 403 {
		t.Fatal("blocker skipped CSRF", e, string(raw))
	}
	f.request(t, f.supplier, "POST", "/api/v1/deals/"+d.ID+"/cancellation-requests/unknown-request/accept", "unknown-request-key", reserveBody(d), 404)
}

func TestCancellationAllowsSecondPartyAfterDeadlineAndSerializesDecisionRace(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			f := reserveFixture(t)
			deadline := time.Now().UTC().Add(2 * time.Second).Truncate(time.Microsecond)
			d := fundedForSignature(t, f, "cancel-decision-race", func(in map[string]string) {
				if expired {
					in["deadline_at"] = deadline.Format(time.RFC3339Nano)
				}
			})
			_, request, _ := requestCancellation(t, f, f.customer, d, "decision-race-request")
			if expired {
				if wait := time.Until(deadline) + 50*time.Millisecond; wait > 0 {
					time.Sleep(wait)
				}
				if currentCard(t, f, d).Status != "REVIEW_REQUIRED" {
					t.Fatal("deadline was not projected")
				}
			}
			commands := []func() moneyResponse{moneyCommand(f, f.supplier, cancellationPath(d, request, "accept"), "decision-race-accept", reserveBody(d))}
			if !expired {
				commands = append(commands, moneyCommand(f, f.supplier, cancellationPath(d, request, "reject"), "decision-race-reject", blockBody(d, "Проверить спор")))
			}
			results := concurrentMoney(commands...)
			success := 0
			for _, result := range results {
				if result.err != nil {
					t.Fatal(result.err)
				}
				if result.status == 200 {
					success++
				} else if result.status != 409 {
					t.Fatal("invalid decision race outcome", result.status, string(result.body))
				}
			}
			if success != 1 || count(t, f.pool, "cancellation_decisions") != 1 {
				t.Fatal("two second-party decisions committed")
			}
			card := currentCard(t, f, d)
			if card.Status == "REFUNDED" {
				assertBalance(t, f, f.customer.org, 50000000, 0)
				if card.Reserve.Status != "RELEASED" {
					t.Fatal("refund retained active reserve")
				}
			} else if !expired && card.Status == "REVIEW_REQUIRED" {
				assertBalance(t, f, f.customer.org, 50000000, 30000000)
			} else {
				t.Fatal("decision race produced invalid state", card.Status)
			}
		})
	}
}

func TestBlockingOutboxFailureRollsBackAllFactsAndReserveRelease(t *testing.T) {
	for _, operation := range []string{"dispute", "request", "accept", "reject"} {
		t.Run(operation, func(t *testing.T) {
			f := reserveFixture(t)
			d := fundedForSignature(t, f, "blocker-rollback", nil)
			actor := f.customer
			path := "/api/v1/deals/" + d.ID + "/disputes"
			body := blockBody(d, "Проверка отката")
			if operation == "request" {
				path = "/api/v1/deals/" + d.ID + "/cancellation-requests"
			}
			if operation == "accept" || operation == "reject" {
				_, request, _ := requestCancellation(t, f, f.customer, d, "rollback-cancel-request")
				actor = f.supplier
				path = cancellationPath(d, request, operation)
				if operation == "accept" {
					body = reserveBody(d)
				}
			}
			if _, e := f.pool.Exec(context.Background(), `CREATE FUNCTION reject_blocker_job() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type IN ('DISPUTE_OPENED','CANCELLATION_REQUESTED','CANCELLATION_ACCEPTED','CANCELLATION_REJECTED') THEN RAISE EXCEPTION 'injected blocker outbox failure';END IF;RETURN NEW;END $$;CREATE TRIGGER reject_blocker_job BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION reject_blocker_job()`); e != nil {
				t.Fatal(e)
			}
			before := map[string]int{}
			for _, table := range []string{"deal_disputes", "cancellation_requests", "cancellation_decisions", "reserves", "audit_events", "outbox", "idempotency_keys", "ledger_entries"} {
				before[table] = count(t, f.pool, table)
			}
			balances := f.accountSnapshot(t)
			status := currentCard(t, f, d).Status
			f.request(t, actor, "POST", path, "rollback-blocker-key", body, 503)
			for table, want := range before {
				if count(t, f.pool, table) != want {
					t.Errorf("%s changed on rollback", table)
				}
			}
			if f.accountSnapshot(t) != balances || currentCard(t, f, d).Status != status || currentCard(t, f, d).Reserve.Status != "ACTIVE" {
				t.Fatal("failed outbox published blocking state or released money")
			}
			if _, e := f.pool.Exec(context.Background(), `DROP TRIGGER reject_blocker_job ON outbox`); e != nil {
				t.Fatal(e)
			}
			expected := 201
			if operation == "accept" || operation == "reject" {
				expected = 200
			}
			f.request(t, actor, "POST", path, "rollback-blocker-key", body, expected)
		})
	}
}

func TestBlockerRuntimeGuardsKeepIdentityAndRequireRealSecondParty(t *testing.T) {
	f := reserveFixture(t)
	d := fundedForSignature(t, f, "blocker-runtime", nil)
	runtime, schema := shadowRuntime(t, f)
	runtimeFixture := *f
	runtimeFixture.pool = runtime
	server := httptest.NewServer(runtimeFixture.handler(t))
	defer server.Close()
	oldURL := f.server.URL
	f.server.URL = server.URL
	defer func() { f.server.URL = oldURL }()
	if _, e := runtime.Exec(context.Background(), `UPDATE reserves SET status='RELEASED' WHERE deal_id=$1`, d.ID); e == nil {
		t.Fatal("unilateral reserve release succeeded")
	}
	_, request, _ := requestCancellation(t, f, f.customer, d, "runtime-cancel-request")
	if _, e := runtime.Exec(context.Background(), `INSERT INTO cancellation_decisions(request_id,organization_id,user_id,decision,created_at) VALUES($1,$2,$3,'ACCEPTED',clock_timestamp())`, request.ID, f.customer.org, f.customer.user); e == nil {
		t.Fatal("requester created second vote")
	}
	// Fake temp acceptance and empty blocker tables must not authorize the
	// real reserve release or a return to a payable phase.
	tx, e := runtime.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	for _, sql := range []string{`CREATE TEMP TABLE cancellation_decisions(request_id text,organization_id text,user_id text,decision text) ON COMMIT DROP`, `CREATE TEMP TABLE cancellation_requests ON COMMIT DROP AS SELECT * FROM ` + schema + `.cancellation_requests`, `CREATE TEMP TABLE deal_disputes ON COMMIT DROP AS SELECT * FROM ` + schema + `.deal_disputes`} {
		if _, e = tx.Exec(context.Background(), sql); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = tx.Exec(context.Background(), `INSERT INTO pg_temp.cancellation_decisions VALUES($1,$2,$3,'ACCEPTED')`, request.ID, f.supplier.org, f.supplier.user); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(context.Background(), `UPDATE `+schema+`.reserves SET status='RELEASED' WHERE deal_id=$1`, d.ID); e == nil {
		t.Fatal("TEMP acceptance bypassed reserve guard")
	}
	if e = tx.Rollback(context.Background()); e != nil {
		t.Fatal(e)
	}
	for _, statement := range []string{`UPDATE deals SET status='FUNDED' WHERE id=$1`, `UPDATE deals SET status='READY_TO_SETTLE' WHERE id=$1`, `UPDATE reserves SET amount_kopecks=1 WHERE deal_id=$1`, `DELETE FROM cancellation_requests WHERE deal_id=$1`} {
		if _, e = runtime.Exec(context.Background(), statement, d.ID); e == nil {
			t.Fatal("runtime bypassed blocker/identity guard", statement)
		}
	}
	f.request(t, f.supplier, "POST", cancellationPath(d, request, "accept"), "runtime-cancel-accept", reserveBody(d), 200)
	if currentCard(t, f, d).Status != "REFUNDED" {
		t.Fatal("runtime could not execute real two-party release")
	}
	if _, e = runtime.Exec(context.Background(), `UPDATE reserves SET status='ACTIVE' WHERE deal_id=$1`, d.ID); e == nil {
		t.Fatal("terminal reserve reactivated")
	}
}
