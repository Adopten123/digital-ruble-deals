package notificationshttp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	authrepo "github.com/Adopten123/digital-ruble-deals/internal/features/auth/repository/postgres"
	authservice "github.com/Adopten123/digital-ruble-deals/internal/features/auth/service"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	repo "github.com/Adopten123/digital-ruble-deals/internal/features/notifications/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/features/notifications/service"
	notificationshttp "github.com/Adopten123/digital-ruble-deals/internal/features/notifications/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const origin = "http://localhost:8080"

type client struct {
	http *http.Client
	csrf string
}
type fixture struct {
	pool    *pgxpool.Pool
	server  *httptest.Server
	clients map[string]*client
	service *service.Service
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{pool: testutil.Database(t), clients: map[string]*client{}}
	ctx := context.Background()
	for _, id := range []string{"customer", "supplier", "replacement", "third", "operator"} {
		role := "USER"
		if id == "operator" {
			role = "DEMO_OPERATOR"
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO users(id,email,password_hash,system_role) VALUES($1,$2,'unused',$3)`, id, id+"@example.test", role); err != nil {
			t.Fatal(err)
		}
		if id != "operator" {
			if _, err := f.pool.Exec(ctx, `INSERT INTO organizations(id,name,kind) VALUES($1,$2,'COMPANY')`, id, id+" company"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `INSERT INTO memberships(user_id,organization_id,role) VALUES($1,$1,'ORG_OWNER');`, id); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `INSERT INTO accounts(id,organization_id) VALUES($1,$2)`, id+"-account", id); err != nil {
				t.Fatal(err)
			}
		}
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deals(id) VALUES('deal'); INSERT INTO deal_versions(deal_id,deal_version,customer_party_id,supplier_party_id,title,description,amount_kopecks,currency,deadline_at,created_by) VALUES('deal',1,'customer','supplier','Private title','Private description',100,'RUB',clock_timestamp()+interval '1 day','customer')`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	authsvc, err := authservice.New(authrepo.New(f.pool))
	if err != nil {
		t.Fatal(err)
	}
	csrf := httpapi.NewCSRF(make([]byte, 32), origin, false)
	auth := authhttp.New(authsvc, csrf)
	mux := http.NewServeMux()
	auth.RegisterRoutes(mux)
	f.service = service.New(repo.New(f.pool), []byte("notification-test-key"))
	notificationshttp.New(f.service, csrf).RegisterRoutes(mux, auth.Require)
	f.server = httptest.NewServer(httpapi.Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), mux))
	t.Cleanup(f.server.Close)
	for _, id := range []string{"customer", "supplier", "replacement", "third", "operator"} {
		token, err := security.RandomToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 day')`, security.TokenHash(token), id); err != nil {
			t.Fatal(err)
		}
		jar, _ := cookiejar.New(nil)
		base, _ := url.Parse(f.server.URL)
		jar.SetCookies(base, []*http.Cookie{{Name: httpapi.SessionCookie, Value: token, Path: "/"}})
		f.clients[id] = &client{http: &http.Client{Jar: jar}}
		_, body := f.request(t, id, "GET", "/api/v1/auth/csrf", "", nil, 200)
		var data map[string]string
		if json.Unmarshal(body, &data) != nil {
			t.Fatal(string(body))
		}
		f.clients[id].csrf = data["csrf_token"]
	}
	return f
}
func (f *fixture) request(t *testing.T, who, method, path, body string, headers map[string]string, status int) (*http.Response, []byte) {
	t.Helper()
	c := f.clients[who]
	transport := http.DefaultClient
	if c != nil {
		transport = c.http
	}
	req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if method != "GET" {
		req.Header.Set("Origin", origin)
		if c != nil {
			req.Header.Set("X-CSRF-Token", c.csrf)
		}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	response, err := transport.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s as %s got%d want%d: %s", method, path, who, response.StatusCode, status, raw)
	}
	return response, raw
}
func (f *fixture) enqueue(t *testing.T, id, kind string, global bool) {
	t.Helper()
	var dealID, version, org any
	actor := "customer"
	payload := `{"status":"DRAFT","amount_kopecks":"100","currency":"RUB","title":"PRIVATE TITLE","password":"SECRET"}`
	if global {
		actor = "operator"
		payload = `{"transaction_id":"transaction-id","account":{"id":"customer-account","total_kopecks":"999999","name":"PRIVATE NAME"}}`
	} else {
		dealID = "deal"
		version = 1
		org = "customer"
	}
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(id,deal_id,deal_version,type,actor_user_id,actor_organization_id,request_id,payload,created_at) VALUES($1,$2,$3,$4,$5,$6,$1,$7,'2000-01-01T00:00:00Z')`, id, dealID, version, kind, actor, org, payload); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox(id,event_id,type,deal_id,payload,created_at) VALUES($1,$2,$3,$4,$5,'2000-01-01T00:00:00Z')`, id+"-job", id, kind, dealID, payload); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 100; i++ {
		worked, err := f.service.DeliverOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			return
		}
	}
	t.Fatal("queue did not drain")
}
func decode(t *testing.T, raw []byte) service.Page {
	t.Helper()
	var p service.Page
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func (f *fixture) list(t *testing.T, who, query string) service.Page {
	t.Helper()
	_, raw := f.request(t, who, "GET", "/api/v1/notifications"+query, "", nil, 200)
	return decode(t, raw)
}
func TestVisibilitySafeProjectionReadAndStrictTransport(t *testing.T) {
	f := setup(t)
	f.enqueue(t, "created", "DEAL_CREATED", false)
	f.enqueue(t, "credit", "DEMO_CREDITED", true)
	f.enqueue(t, "technical", "EDO_EVENT_READY", false)
	f.enqueue(t, "unknown", "FUTURE_EVENT", false)
	if p := f.list(t, "customer", ""); len(p.Items) != 0 || p.UnreadCount != 0 {
		t.Fatal("delivered before worker")
	}
	f.drain(t)
	a, b := f.list(t, "customer", ""), f.list(t, "supplier", "")
	if len(a.Items) != 2 || a.UnreadCount != 2 || len(b.Items) != 1 || b.UnreadCount != 1 {
		t.Fatalf("recipients %+v %+v", a, b)
	}
	for _, n := range a.Items {
		raw, _ := json.Marshal(n)
		if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "999999") || !n.CreatedAt.After(n.Event.CreatedAt) {
			t.Fatalf("unsafe/wrong time %s", raw)
		}
	}
	for _, who := range []string{"third", "operator"} {
		if p := f.list(t, who, ""); len(p.Items) != 0 || p.UnreadCount != 0 {
			t.Fatalf("foreign %+v", p)
		}
	}
	id := a.Items[0].ID
	readPath := "/api/v1/notifications/" + id + "/read"
	f.request(t, "third", "POST", readPath, "", nil, 404)
	f.request(t, "operator", "POST", readPath, "", nil, 404)
	f.request(t, "customer", "POST", readPath, "", map[string]string{"Origin": "http://evil.invalid"}, 403)
	f.request(t, "customer", "POST", readPath, "", map[string]string{"X-CSRF-Token": "wrong"}, 403)
	f.request(t, "customer", "POST", readPath, "", map[string]string{"X-Expected-User-ID": "supplier"}, 409)
	f.request(t, "customer", "POST", readPath+"?unknown=x", "", nil, 400)
	f.request(t, "customer", "POST", readPath, "{}", nil, 400)
	response, raw := f.request(t, "customer", "POST", readPath, "", nil, 204)
	if len(raw) != 0 || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("read response", string(raw))
	}
	var first time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT read_at FROM notifications WHERE id=$1`, id).Scan(&first); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f.request(t, "customer", "POST", readPath, "", nil, 204) }()
	}
	wg.Wait()
	var later time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT read_at FROM notifications WHERE id=$1`, id).Scan(&later); err != nil || !first.Equal(later) {
		t.Fatalf("read time changed %v %v %v", first, later, err)
	}
	if p := f.list(t, "customer", "?unread=false"); len(p.Items) != 1 || p.UnreadCount != 1 {
		t.Fatalf("read filter %+v", p)
	}
	if p := f.list(t, "customer", "?unread=true"); len(p.Items) != 1 || p.UnreadCount != 1 {
		t.Fatal(p)
	}
	for _, q := range []string{"?unknown=x", "?unread=", "?unread=True", "?unread=0", "?unread=true&unread=false", "?limit=01", "?limit=0", "?limit=101", "?cursor=", "?cursor=bad", "?limit=%ZZ"} {
		f.request(t, "customer", "GET", "/api/v1/notifications"+q, "", nil, 400)
	}
	f.request(t, "", "GET", "/api/v1/notifications", "", nil, 401)
}
func TestHistoricalPrivacyIsRepeatedForItemsCountsAndRead(t *testing.T) {
	f := setup(t)
	f.enqueue(t, "old", "DEAL_CREATED", false)
	f.drain(t)
	old := f.list(t, "supplier", "").Items[0].ID
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deal_versions(deal_id,deal_version,customer_party_id,supplier_party_id,title,description,amount_kopecks,currency,deadline_at,created_by) VALUES('deal',2,'customer','replacement','New title','New description',100,'RUB',clock_timestamp()+interval '1 day','customer'); UPDATE deals SET deal_version=2,row_version=row_version+1 WHERE id='deal'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"supplier", "replacement"} {
		if p := f.list(t, who, ""); len(p.Items) != 0 || p.UnreadCount != 0 {
			t.Fatalf("old round leaked to %s: %+v", who, p)
		}
	}
	f.request(t, "supplier", "POST", "/api/v1/notifications/"+old+"/read", "", nil, 404)
	if len(f.list(t, "customer", "").Items) != 1 {
		t.Fatal("customer lost own history")
	}
	// Delivery delayed until after replacement also keeps the new party out.
	f.enqueue(t, "old-delayed", "DEAL_REWORKED", false)
	f.drain(t)
	if len(f.list(t, "replacement", "").Items) != 0 || len(f.list(t, "supplier", "").Items) != 0 || len(f.list(t, "customer", "").Items) != 2 {
		t.Fatal("delayed history scope failed")
	}
}
func TestAtomicDeliveryRollbackConcurrentDrainAndCommitVisibility(t *testing.T) {
	f := setup(t)
	f.enqueue(t, "one", "DEAL_CREATED", false)
	f.enqueue(t, "two", "DISPUTE_OPENED", false)
	if _, err := f.pool.Exec(context.Background(), `CREATE FUNCTION reject_notification_ack() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure before commit'; END $$; CREATE TRIGGER reject_ack BEFORE UPDATE ON outbox FOR EACH ROW EXECUTE FUNCTION reject_notification_ack()`); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.service.DeliverOnce(context.Background()); err == nil || worked {
		t.Fatal("fault did not fail atomic delivery", worked, err)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial notifications committed", count, err)
	}
	if _, err := f.pool.Exec(context.Background(), `DROP TRIGGER reject_ack ON outbox`); err != nil {
		t.Fatal(err)
	}
	// A busy row is skipped, not held across other delivery work.
	tx, err := f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `SELECT id FROM outbox WHERE event_id='one' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.service.DeliverOnce(context.Background()); err != nil || !worked {
		t.Fatal("skip locked", worked, err)
	}
	if err = tx.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc := service.New(repo.New(f.pool), nil)
			for {
				worked, e := svc.DeliverOnce(context.Background())
				if e != nil {
					t.Error(e)
					return
				}
				if !worked {
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications`).Scan(&count); err != nil || count != 4 {
		t.Fatal("concurrent duplicate/missing", count, err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox WHERE delivered_at IS NULL`).Scan(&count); err != nil || count != 0 {
		t.Fatal("unacked", count, err)
	}
	tx, err = f.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `INSERT INTO audit_events(id,deal_id,deal_version,type,actor_user_id,actor_organization_id,request_id,payload) VALUES('uncommitted','deal',1,'DEAL_CREATED','customer','customer','request','{}'); INSERT INTO outbox(id,event_id,type,deal_id,payload) VALUES('uncommitted-job','uncommitted','DEAL_CREATED','deal','{}')`); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.service.DeliverOnce(context.Background()); err != nil || worked {
		t.Fatal("worker saw uncommitted source", worked, err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.drain(t)
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notifications`).Scan(&count); err != nil || count != 6 {
		t.Fatal(count, err)
	}
}
func TestDeliveryPaginationHighwaterAndCursorScope(t *testing.T) {
	f := setup(t)
	for i := 0; i < 4; i++ {
		f.enqueue(t, fmt.Sprintf("event-%d", i), "DEAL_CREATED", false)
	}
	f.drain(t)
	first := f.list(t, "customer", "?limit=1")
	if first.NextCursor == nil {
		t.Fatal("no cursor")
	}
	seen := map[string]bool{first.Items[0].ID: true}
	f.enqueue(t, "newer", "DEAL_CREATED", false)
	f.drain(t)
	cursor := first.NextCursor
	for cursor != nil {
		p := f.list(t, "customer", "?limit=1&cursor="+url.QueryEscape(*cursor))
		for _, n := range p.Items {
			if seen[n.ID] || n.Event.ID == "newer" {
				t.Fatal("duplicate/newer entry", n)
			}
			seen[n.ID] = true
		}
		if p.UnreadCount != 5 {
			t.Fatal("count was page-bound", p)
		}
		cursor = p.NextCursor
	}
	if len(seen) != 4 {
		t.Fatal("lost entries", seen)
	}
	for _, who := range []string{"supplier", "operator"} {
		f.request(t, who, "GET", "/api/v1/notifications?limit=1&cursor="+url.QueryEscape(*first.NextCursor), "", nil, 400)
	}
	f.request(t, "customer", "GET", "/api/v1/notifications?limit=2&cursor="+url.QueryEscape(*first.NextCursor), "", nil, 400)
	f.request(t, "customer", "GET", "/api/v1/notifications?limit=1&unread=true&cursor="+url.QueryEscape(*first.NextCursor), "", nil, 400)
}
func TestRuntimePermissionsAndImmutableDeliveryState(t *testing.T) {
	f := setup(t)
	f.enqueue(t, "runtime", "DEAL_CREATED", false)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='poaktu_runtime') THEN CREATE ROLE poaktu_runtime NOLOGIN; END IF; END $$`); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := f.pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	grants, err := os.ReadFile(filepath.Join(testutil.MigrationsDir(), "..", "deploy", "postgres", "runtime_grants.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, strings.ReplaceAll(string(grants), "SCHEMA public", "SCHEMA "+pgx.Identifier{schema}.Sanitize())); err != nil {
		t.Fatal(err)
	}
	cfg := f.pool.Config().Copy()
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, e := conn.Exec(ctx, `SET ROLE poaktu_runtime`)
		return e
	}
	runtime, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	svc := service.New(repo.New(runtime), nil)
	if worked, err := svc.DeliverOnce(ctx); err != nil || !worked {
		t.Fatal("runtime delivery", worked, err)
	}
	p, err := svc.List(ctx, service.Actor{UserID: "customer", OrganizationID: "customer"}, service.Filters{Limit: 20}, "")
	if err != nil || len(p.Items) != 1 {
		t.Fatal(p, err)
	}
	if err = svc.Read(ctx, service.Actor{UserID: "customer", OrganizationID: "customer"}, p.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`UPDATE notifications SET recipient_user_id='third'`, `UPDATE notifications SET event_id='other'`, `UPDATE notifications SET semantic_key='other'`, `UPDATE notifications SET read_at=NULL WHERE read_at IS NOT NULL`, `UPDATE notifications SET read_at=clock_timestamp() WHERE read_at IS NOT NULL`, `DELETE FROM notifications`, `UPDATE outbox SET delivered_at=NULL`, `UPDATE outbox SET delivered_at=clock_timestamp()`, `UPDATE outbox SET payload='{}'`, `DELETE FROM outbox`} {
		if _, err = runtime.Exec(ctx, sql); err == nil {
			t.Fatal("runtime changed final notification state", sql)
		}
	}
	var wrong int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.proname IN('notification_state_guard','notification_outbox_guard') AND (p.prosecdef OR NOT EXISTS(SELECT 1 FROM unnest(p.proconfig) c WHERE c='search_path=pg_catalog, '||quote_ident(current_schema())||', pg_temp'))`).Scan(&wrong); err != nil || wrong != 0 {
		t.Fatal("untrusted trigger search path", wrong, err)
	}
}
