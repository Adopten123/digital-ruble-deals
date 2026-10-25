package dealshttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Adopten123/digital-ruble-deals/internal/core/documentfile"
	corepg "github.com/Adopten123/digital-ruble-deals/internal/core/repository/postgres"
	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	authrepo "github.com/Adopten123/digital-ruble-deals/internal/features/auth/repository/postgres"
	authservice "github.com/Adopten123/digital-ruble-deals/internal/features/auth/service"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/bootstrap"
	notificationsrepo "github.com/Adopten123/digital-ruble-deals/internal/features/notifications/repository/postgres"
	notifications "github.com/Adopten123/digital-ruble-deals/internal/features/notifications/service"
	notificationshttp "github.com/Adopten123/digital-ruble-deals/internal/features/notifications/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// old006Database owns a random schema and cleanup never touches public.
func notificationsUpgrade008Fixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := &fixture{pool: old006Database(t)}
	dir := t.TempDir()
	files, err := filepath.Glob(filepath.Join(testutil.MigrationsDir(), "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if filepath.Base(file) >= "009" {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, filepath.Base(file)), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = corepg.Migrate(context.Background(), corepg.UnitOfWork{Pool: f.pool}, dir); err != nil {
		t.Fatal(err)
	}
	var version string
	var later *string
	if err = f.pool.QueryRow(context.Background(), "SELECT max(version),to_regclass('notifications')::text FROM schema_migrations").Scan(&version, &later); err != nil || version != "008_edo_settlement.sql" || later != nil {
		t.Fatalf("fixture must stop at 008: version=%s relation=%v err=%v", version, later, err)
	}
	storeDir := t.TempDir()
	f.files, err = documentfile.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.files.Close(); err != nil {
			t.Error(err)
		}
	})
	if err = bootstrap.Run(context.Background(), f.pool); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(f.handler(t))
	t.Cleanup(f.server.Close)
	f.customer = f.session(t, "customer@demo.poaktu.local")
	f.supplier = f.session(t, "supplier@demo.poaktu.local")
	f.operator = f.session(t, "operator@demo.poaktu.local")
	return f, storeDir
}

func notificationsUpgradeTables(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), "SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname=current_schema() ORDER BY tablename")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err = rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return tables
}

// Include every old table and exact bytea values; only the new migration record
// is excluded. After delivery, outbox.delivered_at is the one expected mutation.
func notificationsUpgradeFacts(t *testing.T, pool *pgxpool.Pool, tables []string, omitDelivery bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range tables {
		expression, filter := "to_jsonb(x)", ""
		if table == "outbox" && omitDelivery {
			expression = "to_jsonb(x)-'delivered_at'"
		}
		if table == "schema_migrations" {
			filter = " WHERE version<'009'"
		}
		var raw string
		query := "SELECT COALESCE(jsonb_agg(" + expression + " ORDER BY (" + expression + ")::text),'[]'::jsonb)::text FROM " + pgx.Identifier{table}.Sanitize() + " x" + filter
		if err := pool.QueryRow(context.Background(), query).Scan(&raw); err != nil {
			t.Fatal(table, err)
		}
		out[table] = raw
	}
	return out
}
func notificationsUpgradeAssertFacts(t *testing.T, before, after map[string]string) {
	t.Helper()
	for table, raw := range before {
		if raw != after[table] {
			t.Errorf("notification migration/worker changed old %s rows", table)
		}
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("old source rows changed")
	}
}
func notificationsUpgradeServer(t *testing.T, f *fixture) *notifications.Service {
	t.Helper()
	authsvc, err := authservice.New(authrepo.New(f.pool))
	if err != nil {
		t.Fatal(err)
	}
	csrf := httpapi.NewCSRF(make([]byte, 32), origin, false)
	auth := authhttp.New(authsvc, csrf)
	svc := notifications.New(notificationsrepo.New(f.pool), make([]byte, 32))
	mux := http.NewServeMux()
	auth.RegisterRoutes(mux)
	notificationshttp.New(svc, csrf).RegisterRoutes(mux, auth.Require)
	mux.Handle("/", f.handler(t))
	server := httptest.NewServer(httpapi.Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), mux))
	old := f.server
	f.server = server
	old.Close()
	t.Cleanup(server.Close)
	return svc
}
func notificationsUpgradeList(t *testing.T, f *fixture, c *client) notifications.Page {
	t.Helper()
	_, raw := f.request(t, c, "GET", "/api/v1/notifications?limit=100", "", "", 200)
	var page notifications.Page
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != nil {
		t.Fatal("fixture unexpectedly exceeds one page")
	}
	return page
}
func notificationsUpgradeDrain(t *testing.T, svc *notifications.Service) {
	t.Helper()
	for i := 0; i < 100; i++ {
		worked, err := svc.DeliverOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			return
		}
	}
	t.Fatal("upgrade backlog did not drain")
}

func TestUpgrade008NotificationsPreservesOldRowsBacklogReadAndMoneyAcrossRestart(t *testing.T) {
	f, storeDir := notificationsUpgrade008Fixture(t)
	ctx := context.Background()

	// Real 008 sources: payout, independent blocked hold, draft cancellation and
	// rework with repeated consent commands and a historical PDF.
	paid := edoReady(t, f, "notification-upgrade-paid")
	edoService, edoHTTP := edoServer(t, f, f.pool)
	receipt := receive(t, edoHTTP, edoService, edoBytes(t, f, paid), 200)
	if !bytes.Contains(receipt, []byte("\"result\":\"settled\"")) {
		t.Fatal("old payout failed", string(receipt))
	}
	held := fundedForSignature(t, f, "notification-upgrade-held", func(in map[string]string) { in["amount_kopecks"] = "10000000" })
	f.request(t, f.customer, "POST", "/api/v1/deals/"+held.ID+"/disputes", "notification-upgrade-dispute", blockBody(held, "Учебный спор до обновления"), 201)
	_, rejectedRequest, _ := requestCancellation(t, f, f.customer, held, "notification-upgrade-request")
	f.request(t, f.supplier, "POST", cancellationPath(held, rejectedRequest, "reject"), "notification-upgrade-reject", blockBody(held, "Сторона отказала до обновления"), 200)
	cancelled := reserveDraft(t, f, "notification-upgrade-cancelled", nil)
	_, cancelledRequest, _ := requestCancellation(t, f, f.customer, cancelled, "notification-upgrade-cancel-draft")
	f.request(t, f.supplier, "POST", cancellationPath(cancelled, cancelledRequest, "accept"), "notification-upgrade-accept", reserveBody(cancelled), 200)
	history := readyForReserve(t, f, "notification-upgrade-history", nil)
	agree(t, f, f.customer, history, "notification-upgrade-repeat-a")
	agree(t, f, f.supplier, history, "notification-upgrade-repeat-b")
	f.request(t, f.supplier, "POST", "/api/v1/deals/"+history.ID+"/rework", "notification-upgrade-rework", blockBody(history, "Старая причина\nДругая версия документа"), 200)
	moneyPath := "/api/v1/demo/accounts/" + accountID(t, f, f.customer.org)
	f.request(t, f.operator, "POST", moneyPath+"/credits", "notification-upgrade-credit", moveBody("5000", "Учебное пополнение до обновления"), 201)
	f.request(t, f.operator, "POST", moneyPath+"/debits", "notification-upgrade-debit", moveBody("1000", "Учебное списание до обновления"), 201)
	_, signatureBefore := f.request(t, f.customer, "POST", "/api/v1/documents/"+paid.Document.ID+"/sign", "notification-upgrade-paid-customer-sign", signBody(paid), 201)
	_, pdfBefore := f.request(t, f.customer, "GET", "/api/v1/documents/"+paid.Document.ID+"/download?version=1", "", "", 200)
	assertMonetaryFacts(t, f)
	assertBalance(t, f, f.customer.org, 20004000, 10000000)
	assertBalance(t, f, f.supplier.org, 30000000, 0)

	tables := notificationsUpgradeTables(t, f.pool)
	before := notificationsUpgradeFacts(t, f.pool, tables, false)
	stable := notificationsUpgradeFacts(t, f.pool, tables, true)
	moneyBefore := f.accountSnapshot(t)
	if len(tables) < 27 {
		t.Fatal("old table snapshot incomplete", tables)
	}
	var pending int
	if err := f.pool.QueryRow(ctx, "SELECT count(*) FROM outbox WHERE delivered_at IS NULL").Scan(&pending); err != nil || pending == 0 {
		t.Fatal("missing old backlog", pending, err)
	}
	for i := 0; i < 2; i++ {
		if err := corepg.Migrate(ctx, corepg.UnitOfWork{Pool: f.pool}, testutil.MigrationsDir()); err != nil {
			t.Fatal(err)
		}
		if err := bootstrap.Run(ctx, f.pool); err != nil {
			t.Fatal(err)
		}
		notificationsUpgradeAssertFacts(t, before, notificationsUpgradeFacts(t, f.pool, tables, false))
		if count(t, f.pool, "notifications") != 0 {
			t.Fatal("migration delivered notifications before worker")
		}
	}
	if err := bootstrap.Ready(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	svc := notificationsUpgradeServer(t, f)
	if page := notificationsUpgradeList(t, f, f.customer); len(page.Items) != 0 || page.UnreadCount != 0 {
		t.Fatal("delivery before worker")
	}
	notificationsUpgradeDrain(t, svc)
	customer := notificationsUpgradeList(t, f, f.customer)
	supplier := notificationsUpgradeList(t, f, f.supplier)
	if len(customer.Items) != 23 || customer.UnreadCount != 23 || len(supplier.Items) != 21 || supplier.UnreadCount != 21 || count(t, f.pool, "notifications") != 44 {
		t.Fatalf("backlog duplicates/missing: customer=%d/%d supplier=%d/%d total=%d", len(customer.Items), customer.UnreadCount, len(supplier.Items), supplier.UnreadCount, count(t, f.pool, "notifications"))
	}
	var paymentNotice string
	for _, notice := range customer.Items {
		if notice.Event.Type == "DEAL_SETTLED" {
			paymentNotice = notice.ID
			if notice.Event.ActorSource == nil || *notice.Event.ActorSource != "edo-emulator" {
				t.Fatal("historical payment source lost")
			}
		}
		if strings.Contains(notice.Event.Type, "EDO_") || notice.Event.Type == "GENERATE_DOCUMENT" || notice.Event.Type == "PATCH_DEAL" {
			t.Fatal("technical notification", notice.Event.Type)
		}
	}
	if paymentNotice == "" {
		t.Fatal("historical payment notification missing")
	}
	f.request(t, f.customer, "POST", "/api/v1/notifications/"+paymentNotice+"/read", "", "", 204)
	customerRead := notificationsUpgradeList(t, f, f.customer)
	if customerRead.UnreadCount != 22 || notificationsUpgradeList(t, f, f.supplier).UnreadCount != 21 {
		t.Fatal("read changed another recipient")
	}
	notificationsUpgradeAssertFacts(t, stable, notificationsUpgradeFacts(t, f.pool, tables, true))
	deliveryState := notificationsUpgradeFacts(t, f.pool, []string{"notifications", "outbox"}, false)
	if f.accountSnapshot(t) != moneyBefore {
		t.Fatal("delivery changed money")
	}

	// An ordinary restart retains the schema and storage and rebuilds every
	// connection/service object; source commands must remain exact historical reads.
	edoHTTP.Close()
	f.server.Close()
	cfg := f.pool.Config().Copy()
	f.pool.Close()
	reopened, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	f.pool = reopened
	if err = f.files.Close(); err != nil {
		t.Fatal(err)
	}
	f.files, err = documentfile.NewStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = corepg.Migrate(ctx, corepg.UnitOfWork{Pool: f.pool}, testutil.MigrationsDir()); err != nil {
		t.Fatal(err)
	}
	if err = bootstrap.Run(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	restarted := notificationsUpgradeServer(t, f)
	notificationsUpgradeDrain(t, restarted)
	if got := notificationsUpgradeList(t, f, f.customer); !reflect.DeepEqual(customerRead, got) {
		t.Fatal("restart lost identities/order/read_at")
	}
	if got := notificationsUpgradeList(t, f, f.supplier); !reflect.DeepEqual(supplier, got) {
		t.Fatal("restart changed another recipient")
	}
	f.request(t, f.customer, "POST", "/api/v1/notifications/"+paymentNotice+"/read", "", "", 204)
	notificationsUpgradeAssertFacts(t, deliveryState, notificationsUpgradeFacts(t, f.pool, []string{"notifications", "outbox"}, false))
	notificationsUpgradeAssertFacts(t, stable, notificationsUpgradeFacts(t, f.pool, tables, true))
	_, signatureAfter := f.request(t, f.customer, "POST", "/api/v1/documents/"+paid.Document.ID+"/sign", "notification-upgrade-paid-customer-sign", signBody(paid), 201)
	if !bytes.Equal(signatureBefore, signatureAfter) {
		t.Fatal("restart changed old exact response")
	}
	_, pdfAfter := f.request(t, f.customer, "GET", "/api/v1/documents/"+paid.Document.ID+"/download?version=1", "", "", 200)
	if !bytes.Equal(pdfBefore, pdfAfter) {
		t.Fatal("restart changed historical PDF")
	}
	if f.accountSnapshot(t) != moneyBefore || count(t, f.pool, "settlements") != 1 {
		t.Fatal("restart paid again/changed held funds")
	}
	assertMonetaryFacts(t, f)
	var heldState string
	if err := f.pool.QueryRow(ctx, "SELECT status FROM reserves WHERE deal_id=$1", held.ID).Scan(&heldState); err != nil || heldState != "ACTIVE" {
		t.Fatal("independent hold changed", heldState, err)
	}
}
