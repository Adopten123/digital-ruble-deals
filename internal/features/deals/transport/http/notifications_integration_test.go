package dealshttp_test

import (
	"context"
	"testing"
	"time"

	edo "github.com/Adopten123/digital-ruble-deals/internal/features/edo/service"
	edohttp "github.com/Adopten123/digital-ruble-deals/internal/features/edo/transport/http"
	notificationsrepo "github.com/Adopten123/digital-ruble-deals/internal/features/notifications/repository/postgres"
	notifications "github.com/Adopten123/digital-ruble-deals/internal/features/notifications/service"
)

func drainNotifications(t *testing.T, f *fixture) *notifications.Service {
	t.Helper()
	svc := notifications.New(notificationsrepo.New(f.pool), make([]byte, 32))
	for i := 0; i < 100; i++ {
		worked, err := svc.DeliverOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			return svc
		}
	}
	t.Fatal("notification queue did not drain")
	return nil
}
func notificationCount(t *testing.T, f *fixture, kind string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications n JOIN audit_events a ON a.id=n.event_id WHERE a.type=$1`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestNotificationsRealAgreementSignatureSettlementAndAccountEvents(t *testing.T) {
	f := reserveFixture(t)
	d := readyForReserve(t, f, "notification-full-flow", nil)
	// Same-key and new-key reads of existing consent are not another consent.
	for i, key := range []string{"notification-agree-repeat-a", "notification-agree-repeat-b", "notification-agree-repeat-c"} {
		d = agree(t, f, f.customer, d, key)
		d = agree(t, f, f.supplier, d, key+"-supplier")
		if i == 0 {
			d = agree(t, f, f.customer, d, key)
		}
	}
	drainNotifications(t, f)
	if notificationCount(t, f, "DEAL_CREATED") != 2 || notificationCount(t, f, "AGREE_DEAL") != 4 || notificationCount(t, f, "GENERATE_DOCUMENT") != 0 {
		t.Fatal("agreement replay or PDF created duplicate notification")
	}
	var mismatch int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications n JOIN audit_events a ON a.id=n.event_id JOIN deal_agreements g ON g.deal_id=a.deal_id AND g.deal_version=a.deal_version AND g.organization_id=a.actor_organization_id WHERE a.type='AGREE_DEAL' AND (a.actor_user_id<>g.user_id OR a.created_at<>g.created_at OR n.semantic_key<>'agreement:'||g.id)`).Scan(&mismatch); err != nil || mismatch != 0 {
		t.Fatal("agreement did not refer to original immutable fact", mismatch, err)
	}
	_, raw := f.request(t, f.customer, "POST", "/api/v1/deals/"+d.ID+"/reserve", "notification-full-reserve", reserveBody(d), 201)
	d, _ = reserveDTO(t, raw)
	d, _, _ = sign(t, f, f.customer, d, "notification-full-sign-customer")
	d, _, _ = sign(t, f, f.supplier, d, "notification-full-sign-supplier")
	// A new key only reads the saved signature, even after the second signature.
	sign(t, f, f.customer, d, "notification-full-sign-repeat-new")
	drainNotifications(t, f)
	if notificationCount(t, f, "DEAL_RESERVED") != 2 || notificationCount(t, f, "DOCUMENT_SIGNED") != 4 || notificationCount(t, f, "EDO_EVENT_READY") != 0 {
		t.Fatal("reserve/signature notification cardinality")
	}
	var processed bool
	if err := f.pool.QueryRow(context.Background(), `SELECT delivered_at IS NOT NULL FROM outbox WHERE deal_id=$1 AND type='EDO_EVENT_READY'`, d.ID).Scan(&processed); err != nil || !processed {
		t.Fatal("technical job was not acknowledged", processed, err)
	}
	if count(t, f.pool, "settlements") != 0 {
		t.Fatal("notification processing executed a payment")
	}
	assertBalance(t, f, f.customer.org, 50000000, 30000000)
	// General outbox acknowledgement must not suppress or invalidate specialized
	// EDO delivery, nor the deferred guards established by migration 006.
	edoSvc, server := edoServer(t, f, f.pool)
	sender, err := edohttp.NewSender(server.URL + "/internal/edo/events")
	if err != nil {
		t.Fatal(err)
	}
	worker := edo.NewWorker(edoSvc, sender)
	if worked, err := worker.DeliverOnce(context.Background()); err != nil || !worked {
		t.Fatal("EDO could not deliver acknowledged basis", worked, err)
	}
	drainNotifications(t, f)
	if count(t, f.pool, "settlements") != 1 || notificationCount(t, f, "DEAL_SETTLED") != 2 {
		t.Fatal("payment notification was missing/duplicated")
	}
	assertBalance(t, f, f.customer.org, 20000000, 0)
	assertBalance(t, f, f.supplier.org, 30000000, 0)
	receive(t, server, edoSvc, edoBytes(t, f, d), 200)
	drainNotifications(t, f)
	if worked, err := worker.DeliverOnce(context.Background()); err != nil || worked {
		t.Fatal("specialized delivery repeated", worked, err)
	}
	if count(t, f.pool, "settlements") != 1 || notificationCount(t, f, "DEAL_SETTLED") != 2 {
		t.Fatal("replay repeated payment notification")
	}
	account := accountID(t, f, f.customer.org)
	path := "/api/v1/demo/accounts/" + account
	f.request(t, f.operator, "POST", path+"/credits", "notification-credit-command", moveBody("100", "Собственный учебный счёт"), 201)
	f.request(t, f.operator, "POST", path+"/credits", "notification-credit-command", moveBody("100", "Собственный учебный счёт"), 201)
	f.request(t, f.operator, "POST", path+"/debits", "notification-debit-command", moveBody("100", "Возврат учебных средств"), 201)
	svc := drainNotifications(t, f)
	if notificationCount(t, f, "DEMO_CREDITED") != 1 || notificationCount(t, f, "DEMO_DEBITED") != 1 {
		t.Fatal("global account event not addressed once")
	}
	page, err := svc.List(context.Background(), notifications.Actor{UserID: f.customer.user, OrganizationID: f.customer.org}, notifications.Filters{Limit: 100}, "")
	if err != nil {
		t.Fatal(err)
	}
	accountNotices := 0
	for _, n := range page.Items {
		if n.Event.Type == "DEMO_CREDITED" || n.Event.Type == "DEMO_DEBITED" {
			accountNotices++
			if n.Event.DealID != nil || n.Event.Details.AccountID != account || n.Event.Details.TransactionID == "" || n.Event.ActorUserID == nil || *n.Event.ActorUserID != f.operator.user || n.Event.Details.AmountKopecks != "" {
				t.Fatalf("account projection %+v", n)
			}
		}
	}
	if accountNotices != 2 {
		t.Fatal("owner did not see personal account events", accountNotices)
	}
	page, err = svc.List(context.Background(), notifications.Actor{UserID: f.supplier.user, OrganizationID: f.supplier.org}, notifications.Filters{Limit: 100}, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range page.Items {
		if n.Event.DealID == nil {
			t.Fatal("counterparty received private account event")
		}
	}
	assertMonetaryFacts(t, f)
}
func TestNotificationAgreementKeepsOriginalActorWhenRepeatJobWins(t *testing.T) {
	f := reserveFixture(t)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO users(id,email,password_hash) VALUES('second-owner','second-owner@example.test','unused');`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO memberships(user_id,organization_id,role) VALUES('second-owner',$1,'ORG_OWNER')`, f.customer.org); err != nil {
		t.Fatal(err)
	}
	second := f.session(t, "second-owner@example.test")
	d := readyForReserve(t, f, "notification-actor-race", nil)
	var originalEvent, originalJob, agreementID string
	if err := f.pool.QueryRow(context.Background(), `SELECT a.id,o.id,g.id FROM deal_agreements g JOIN audit_events a ON a.deal_id=g.deal_id AND a.deal_version=g.deal_version AND a.actor_user_id=g.user_id AND a.created_at=g.created_at AND a.type='AGREE_DEAL' JOIN outbox o ON o.event_id=a.id WHERE g.deal_id=$1 AND g.organization_id=$2`, d.ID, f.customer.org).Scan(&originalEvent, &originalJob, &agreementID); err != nil {
		t.Fatal(err)
	}
	// A different owner reads the same organization consent under a new key.
	agree(t, f, second, d, "notification-second-owner-repeat")
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `SELECT id FROM outbox WHERE id=$1 FOR UPDATE`, originalJob); err != nil {
		t.Fatal(err)
	}
	svc := drainNotifications(t, f)
	var wrong int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications WHERE semantic_key=$1`, "agreement:"+agreementID).Scan(&wrong); err != nil || wrong != 0 {
		t.Fatal("repeat job stole original consent attribution", wrong, err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc = drainNotifications(t, f)
	var total int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications WHERE semantic_key=$1 AND event_id=$2`, "agreement:"+agreementID, originalEvent).Scan(&total); err != nil || total != 3 {
		t.Fatal("original consent did not reach all real recipients", total, err)
	}
	page, err := svc.List(context.Background(), notifications.Actor{UserID: second.user, OrganizationID: second.org}, notifications.Filters{Limit: 100}, "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range page.Items {
		if n.Event.ID == originalEvent {
			found = true
			if n.Event.ActorUserID == nil || *n.Event.ActorUserID != f.customer.user {
				t.Fatal("consent actor changed", n)
			}
		}
	}
	if !found {
		t.Fatal("second owner cannot read the original organization fact")
	}
}
func TestNotificationsDeadlineProjectionDoesNotInventAnEventOrRelease(t *testing.T) {
	f := reserveFixture(t)
	now := time.Now().UTC()
	d := fundedForSignature(t, f, "notification-deadline-fact", func(input map[string]string) {
		input["deadline_at"] = now.Add(5 * time.Second).Format(time.RFC3339Nano)
	})
	drainNotifications(t, f)
	before := count(t, f.pool, "notifications")
	// Reading after the real deadline changes only the projected status.
	wait := time.Until(d.DeadlineAt) + 20*time.Millisecond
	if wait > 0 {
		time.Sleep(wait)
	}
	_, raw := f.request(t, f.customer, "GET", "/api/v1/deals/"+d.ID, "", "", 200)
	if dealDTO(t, raw).Status != "REVIEW_REQUIRED" {
		t.Fatal("deadline projection missing")
	}
	drainNotifications(t, f)
	if count(t, f.pool, "notifications") != before || count(t, f.pool, "settlements") != 0 {
		t.Fatal("deadline read invented a notification/payment")
	}
	assertBalance(t, f, f.customer.org, 50000000, 30000000)
}
