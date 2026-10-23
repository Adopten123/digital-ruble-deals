package dealshttp_test

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/core/documentfile"
	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	authrepo "github.com/Adopten123/digital-ruble-deals/internal/features/auth/repository/postgres"
	authservice "github.com/Adopten123/digital-ruble-deals/internal/features/auth/service"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/bootstrap"
	dealrepo "github.com/Adopten123/digital-ruble-deals/internal/features/deals/repository/postgres"
	dealservice "github.com/Adopten123/digital-ruble-deals/internal/features/deals/service"
	dealhttp "github.com/Adopten123/digital-ruble-deals/internal/features/deals/transport/http"
	settlementrepo "github.com/Adopten123/digital-ruble-deals/internal/features/settlement/repository/postgres"
	settlementservice "github.com/Adopten123/digital-ruble-deals/internal/features/settlement/service"
	settlementhttp "github.com/Adopten123/digital-ruble-deals/internal/features/settlement/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

const origin = "http://localhost:8080"

type client struct {
	http            *http.Client
	csrf, org, user string
}
type fixture struct {
	pool                                *pgxpool.Pool
	server                              *httptest.Server
	customer, supplier, third, operator *client
	balances                            string
	files                               *documentfile.Store
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{pool: testutil.Database(t)}
	var err error
	f.files, err = documentfile.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.files.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := bootstrap.Run(context.Background(), f.pool); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(f.handler(t))
	t.Cleanup(f.server.Close)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO users(id,email,password_hash) VALUES('third-user','third@example.test','unused'); INSERT INTO organizations(id,name,kind) VALUES('third-org','Третья организация','IP'); INSERT INTO memberships(user_id,organization_id,role) VALUES('third-user','third-org','ORG_OWNER'); INSERT INTO accounts(id,organization_id) VALUES('third-account','third-org')`); err != nil {
		t.Fatal(err)
	}
	f.customer = f.session(t, "customer@demo.poaktu.local")
	f.supplier = f.session(t, "supplier@demo.poaktu.local")
	f.third = f.session(t, "third@example.test")
	f.operator = f.session(t, "operator@demo.poaktu.local")
	f.balances = f.accountSnapshot(t)
	t.Cleanup(func() {
		if snapshot := f.accountSnapshot(t); snapshot != f.balances {
			t.Errorf("drafts changed accounts: before %s after %s", f.balances, snapshot)
		}
	})
	return f
}
func (f *fixture) handler(t *testing.T) http.Handler {
	t.Helper()
	svc, err := authservice.New(authrepo.New(f.pool))
	if err != nil {
		t.Fatal(err)
	}
	csrf := httpapi.NewCSRF(make([]byte, 32), origin, false)
	auth := authhttp.New(svc, csrf)
	mux := http.NewServeMux()
	auth.RegisterRoutes(mux)
	dealhttp.New(dealservice.New(dealrepo.New(f.pool), make([]byte, 32), f.files), csrf).RegisterRoutes(mux, auth.Require)
	settlementhttp.New(settlementservice.New(settlementrepo.New(f.pool), make([]byte, 32)), csrf).RegisterRoutes(mux, auth.Require)
	return httpapi.Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), mux)
}
func (f *fixture) accountSnapshot(t *testing.T) string {
	t.Helper()
	var snapshot string
	if err := f.pool.QueryRow(context.Background(), `SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id)::text FROM accounts a`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
func (f *fixture) session(t *testing.T, email string) *client {
	t.Helper()
	c := &client{}
	if err := f.pool.QueryRow(context.Background(), `SELECT u.id,COALESCE(m.organization_id,'') FROM users u LEFT JOIN memberships m ON m.user_id=u.id WHERE u.email=$1`, email).Scan(&c.user, &c.org); err != nil {
		t.Fatal(err)
	}
	token, err := security.RandomToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 day')`, security.TokenHash(token), c.user); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	c.http = &http.Client{Jar: jar}
	u, _ := url.Parse(f.server.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: httpapi.SessionCookie, Value: token, Path: "/"}})
	_, body := f.request(t, c, "GET", "/api/v1/auth/csrf", "", "", 200)
	var out map[string]string
	if json.Unmarshal(body, &out) != nil {
		t.Fatal("invalid csrf")
	}
	c.csrf = out["csrf_token"]
	return c
}
func (f *fixture) request(t *testing.T, c *client, method, path, key, body string, status int) (*http.Response, []byte) {
	t.Helper()
	response, raw, err := f.raw(c, method, path, key, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s got %d want %d: %s", method, path, response.StatusCode, status, raw)
	}
	return response, raw
}
func (f *fixture) raw(c *client, method, path, key, body string, headers map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", c.csrf)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	return response, raw, err
}
func input(supplier string) map[string]string {
	return map[string]string{"supplier_party_id": supplier, "title": "Печать упаковки", "description": "Учебный макет согласован сторонами", "amount_kopecks": "30000000", "currency": "RUB", "deadline_at": time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond).Format(time.RFC3339Nano)}
}
func encode(value any) string { b, _ := json.Marshal(value); return string(b) }
func object(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func errorCode(t *testing.T, raw []byte, code string) {
	t.Helper()
	if object(t, raw)["error"].(map[string]any)["code"] != code {
		t.Fatalf("want %s: %s", code, raw)
	}
}
func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDraftCreationReplayScopeAndAtomicFacts(t *testing.T) {
	f := setup(t)
	in := input(f.supplier.org)
	key := "draft-create-key-001"
	_, first := f.request(t, f.customer, "POST", "/api/v1/deals", key, encode(in), 201)
	d := object(t, first)
	id := d["id"].(string)
	if d["status"] != "DRAFT" || d["deal_version"] != float64(1) || d["customer_party_id"] != f.customer.org || d["supplier_party_id"] != f.supplier.org || d["amount_kopecks"] != "30000000" {
		t.Fatalf("wrong draft: %s", first)
	}
	for _, field := range []string{"document", "reserve", "payment_id", "dispute", "cancellation_request"} {
		if d[field] != nil {
			t.Fatalf("%s must be null", field)
		}
	}
	for _, field := range []string{"agreements", "signatures"} {
		if values, ok := d[field].([]any); !ok || len(values) != 0 {
			t.Fatalf("%s must be empty array", field)
		}
	}
	if len(d["customer"].(map[string]any)) != 3 || len(d["supplier"].(map[string]any)) != 3 {
		t.Fatal("party summary leaked fields")
	}
	for _, field := range []string{"deadline_at", "created_at", "updated_at"} {
		if !strings.HasSuffix(d[field].(string), "Z") {
			t.Fatal("non-UTC timestamp")
		}
	}
	in["title"] = "  " + in["title"] + "  "
	deadline, _ := time.Parse(time.RFC3339Nano, in["deadline_at"])
	in["deadline_at"] = deadline.In(time.FixedZone("msk", 3*3600)).Format(time.RFC3339Nano)
	_, repeat := f.request(t, f.customer, "POST", "/api/v1/deals", key, encode(in), 201)
	if string(first) != string(repeat) {
		t.Fatal("replay must preserve exact original bytes")
	}
	// A new repository/service/HTTP handler reads the outcome from PostgreSQL.
	restarted := httptest.NewServer(f.handler(t))
	oldURL := f.server.URL
	f.server.URL = restarted.URL
	// Cookies are scoped to host, so the new loopback port retains the session.
	_, repeat = f.request(t, f.customer, "POST", "/api/v1/deals", key, encode(in), 201)
	f.server.URL = oldURL
	restarted.Close()
	if string(first) != string(repeat) {
		t.Fatal("restart replay changed outcome")
	}
	in["amount_kopecks"] = "30000001"
	_, conflict := f.request(t, f.customer, "POST", "/api/v1/deals", key, encode(in), 409)
	errorCode(t, conflict, "IDEMPOTENCY_CONFLICT")
	_, card := f.request(t, f.supplier, "GET", "/api/v1/deals/"+id, "", "", 200)
	if object(t, card)["id"] != id {
		t.Fatal("supplier cannot read")
	}
	f.request(t, f.third, "GET", "/api/v1/deals/"+id, "", "", 404)
	f.request(t, f.operator, "GET", "/api/v1/deals/"+id, "", "", 404)
	f.request(t, f.operator, "GET", "/api/v1/deals", "", "", 403)
	f.request(t, f.operator, "POST", "/api/v1/deals", "operator-draft-key", encode(in), 403)
	_, empty := f.request(t, f.third, "GET", "/api/v1/deals", "", "", 200)
	if len(object(t, empty)["items"].([]any)) != 0 {
		t.Fatal("third party list leaked")
	}
	jar, _ := cookiejar.New(nil)
	anonymous := &client{http: &http.Client{Jar: jar}}
	f.request(t, anonymous, "GET", "/api/v1/deals/"+id, "", "", 401)
	for _, table := range []string{"deals", "deal_versions", "idempotency_keys", "audit_events", "outbox"} {
		if count(t, f.pool, table) != 1 {
			t.Fatalf("duplicate or missing %s", table)
		}
	}
	var actor, org, requestID, event string
	var payload []byte
	if err := f.pool.QueryRow(context.Background(), `SELECT actor_user_id,actor_organization_id,request_id,type,payload FROM audit_events`).Scan(&actor, &org, &requestID, &event, &payload); err != nil {
		t.Fatal(err)
	}
	if actor != f.customer.user || org != f.customer.org || requestID == "" || event != "DEAL_CREATED" || object(t, payload)["id"] != id {
		t.Fatal("audit attribution incorrect")
	}
}

func TestConcurrentCreationAndRollback(t *testing.T) {
	f := setup(t)
	body := encode(input(f.supplier.org))
	key := "concurrent-draft-key"
	const n = 16
	var wg sync.WaitGroup
	results := make(chan string, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, raw, err := f.raw(f.customer, "POST", "/api/v1/deals", key, body, nil)
			if err != nil {
				errs <- err
				return
			}
			if response.StatusCode != 201 {
				errs <- fmt.Errorf("HTTP %d: %s", response.StatusCode, raw)
				return
			}
			results <- string(raw)
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	first := ""
	for body := range results {
		if first == "" {
			first = body
		}
		if body != first {
			t.Fatal("concurrent outcomes differ")
		}
	}
	for _, table := range []string{"deals", "deal_versions", "idempotency_keys", "audit_events", "outbox"} {
		if count(t, f.pool, table) != 1 {
			t.Fatalf("concurrency duplicated %s", table)
		}
	}
	// Fail the final domain insert and prove all preceding writes roll back.
	if _, err := f.pool.Exec(context.Background(), `CREATE FUNCTION reject_outbox() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$; CREATE TRIGGER reject_outbox BEFORE INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION reject_outbox()`); err != nil {
		t.Fatal(err)
	}
	f.request(t, f.customer, "POST", "/api/v1/deals", "rollback-draft-key", body, 503)
	for _, table := range []string{"deals", "deal_versions", "idempotency_keys", "audit_events", "outbox"} {
		if count(t, f.pool, table) != 1 {
			t.Fatalf("failed tx left %s", table)
		}
	}
	if _, err := f.pool.Exec(context.Background(), `DROP TRIGGER reject_outbox ON outbox`); err != nil {
		t.Fatal(err)
	}
	f.request(t, f.customer, "POST", "/api/v1/deals", "rollback-draft-key", body, 201)
	if count(t, f.pool, "deals") != 2 {
		t.Fatal("retry did not succeed once")
	}
}

func TestDraftValidationAndDurableFailures(t *testing.T) {
	f := setup(t)
	base := input(f.supplier.org)
	for i, amount := range []string{"0", "-1", "01", "+1", "1.0", "1e2", "9223372036854775808", " 1 ", ""} {
		in := input(f.supplier.org)
		in["amount_kopecks"] = amount
		key := fmt.Sprintf("invalid-amount-key-%02d", i)
		_, first := f.request(t, f.customer, "POST", "/api/v1/deals", key, encode(in), 400)
		errorCode(t, first, "INVALID_AMOUNT")
		_, repeat := f.request(t, f.customer, "POST", "/api/v1/deals", key, encode(in), 400)
		if string(first) != string(repeat) {
			t.Fatal("semantic error not persisted")
		}
		in["amount_kopecks"] = "1"
		f.request(t, f.customer, "POST", "/api/v1/deals", key, encode(in), 409)
	}
	for _, amount := range []string{"1", "9223372036854775807"} {
		in := input(f.supplier.org)
		in["amount_kopecks"] = amount
		_, raw := f.request(t, f.customer, "POST", "/api/v1/deals", "boundary-amount-"+amount, encode(in), 201)
		if object(t, raw)["amount_kopecks"] != amount {
			t.Fatal("money lost precision")
		}
	}
	invalid := []map[string]string{}
	for _, field := range []string{"supplier_party_id", "title", "description", "currency", "deadline_at"} {
		in := input(f.supplier.org)
		in[field] = ""
		invalid = append(invalid, in)
	}
	for _, change := range []struct{ field, value string }{{"supplier_party_id", f.customer.org}, {"title", strings.Repeat("Я", 201)}, {"description", strings.Repeat("Я", 5001)}, {"title", "A\x00B"}, {"description", "A\x00B"}, {"supplier_party_id", "A\x00B"}, {"currency", "USD"}, {"deadline_at", "tomorrow"}} {
		in := input(f.supplier.org)
		in[change.field] = change.value
		invalid = append(invalid, in)
	}
	for i, in := range invalid {
		_, raw := f.request(t, f.customer, "POST", "/api/v1/deals", fmt.Sprintf("invalid-input-key-%02d", i), encode(in), 400)
		errorCode(t, raw, "INVALID_REQUEST")
	}
	before := count(t, f.pool, "idempotency_keys")
	for i, body := range []string{`null`, `[]`, `{}`, `{"title":null}`, strings.Replace(encode(base), `"amount_kopecks":"30000000"`, `"amount_kopecks":30000000`, 1), strings.TrimSuffix(encode(base), "}") + `,"customer_party_id":"forged"}`, strings.TrimSuffix(encode(base), "}") + `,"system_role":"DEMO_OPERATOR"}`} {
		f.request(t, f.customer, "POST", "/api/v1/deals", fmt.Sprintf("invalid-json-key-%02d", i), body, 400)
	}
	if count(t, f.pool, "idempotency_keys") != before {
		t.Fatal("malformed envelopes must not create domain commands")
	}
	f.request(t, f.customer, "POST", "/api/v1/deals", "", encode(base), 400)
	f.request(t, f.customer, "POST", "/api/v1/deals", "short", encode(base), 400)
	response, _, err := f.raw(f.customer, "POST", "/api/v1/deals", "invalid-origin-key", encode(base), map[string]string{"Origin": "http://foreign.example"})
	if err != nil || response.StatusCode != 403 {
		t.Fatal("Origin bypass", err)
	}
	response, _, err = f.raw(f.customer, "POST", "/api/v1/deals", "invalid-csrf-key-01", encode(base), map[string]string{"X-CSRF-Token": "wrong"})
	if err != nil || response.StatusCode != 403 {
		t.Fatal("CSRF bypass", err)
	}
	f.request(t, f.customer, "POST", "/api/v1/deals", "oversize-body-key-01", `{"title":"`+strings.Repeat("x", 17<<10)+`"}`, 413)
	missing := input("missing-org")
	_, first := f.request(t, f.customer, "POST", "/api/v1/deals", "missing-party-key-01", encode(missing), 404)
	errorCode(t, first, "RESOURCE_NOT_FOUND")
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO organizations(id,name,kind) VALUES('missing-org','Поздний контрагент','COMPANY')`); err != nil {
		t.Fatal(err)
	}
	_, repeat := f.request(t, f.customer, "POST", "/api/v1/deals", "missing-party-key-01", encode(missing), 404)
	if string(first) != string(repeat) {
		t.Fatal("stored rejection changed when organization appeared")
	}
	past := input(f.supplier.org)
	past["deadline_at"] = time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
	_, first = f.request(t, f.customer, "POST", "/api/v1/deals", "past-deadline-key-01", encode(past), 422)
	errorCode(t, first, "DEADLINE_PASSED")
	_, repeat = f.request(t, f.customer, "POST", "/api/v1/deals", "past-deadline-key-01", encode(past), 422)
	if string(first) != string(repeat) {
		t.Fatal("deadline rejection not durable")
	}
	var rejected, commands int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE type='DEAL_CREATE_REJECTED' AND deal_id IS NULL AND deal_version IS NULL AND actor_user_id=$1 AND actor_organization_id=$2 AND request_id<>'' AND payload->>'operation'='CREATE_DEAL' AND payload->>'error_code' IN ('INVALID_REQUEST','INVALID_AMOUNT','RESOURCE_NOT_FOUND','DEADLINE_PASSED')`, f.customer.user, f.customer.org).Scan(&rejected); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM idempotency_keys WHERE status_code>=400`).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if rejected != commands {
		t.Fatalf("semantic failures need exactly one attributed audit each: audit %d commands %d", rejected, commands)
	}
	if count(t, f.pool, "outbox") != 2 {
		t.Fatal("rejections must not enqueue notifications")
	}
}

func TestReadClockAfterPoolWait(t *testing.T) {
	f := setup(t)
	in := input(f.supplier.org)
	in["deadline_at"] = time.Now().UTC().Add(250 * time.Millisecond).Format(time.RFC3339Nano)
	_, raw := f.request(t, f.customer, "POST", "/api/v1/deals", "poolwait-draft-key-01", encode(in), 201)
	id := object(t, raw)["id"].(string)
	config := f.pool.Config()
	config.MaxConns = 1
	config.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	held, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	svc := dealservice.New(dealrepo.New(pool), make([]byte, 32))
	actor := dealservice.Actor{UserID: f.customer.user, OrganizationID: f.customer.org}
	got := make(chan dealservice.Deal, 1)
	failed := make(chan error, 1)
	go func() {
		d, e := svc.Get(context.Background(), actor, id)
		if e != nil {
			failed <- e
			return
		}
		got <- d
	}()
	time.Sleep(350 * time.Millisecond)
	held.Release()
	select {
	case d := <-got:
		if d.Status != "REVIEW_REQUIRED" {
			t.Fatal("read used time before waiting for connection")
		}
	case e := <-failed:
		t.Fatal(e)
	case <-time.After(5 * time.Second):
		t.Fatal("read blocked")
	}
	page, err := svc.List(context.Background(), actor, dealservice.Filters{Status: "REVIEW_REQUIRED", Limit: 20}, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list/card deadline clock mismatch: %v %+v", err, page)
	}
}

func TestDraftSearchPaginationAndDeadlineReads(t *testing.T) {
	f := setup(t)
	for i := 0; i < 5; i++ {
		in := input(f.supplier.org)
		in["title"] = fmt.Sprintf("Тираж %d", i)
		f.request(t, f.customer, "POST", "/api/v1/deals", fmt.Sprintf("page-draft-key-%02d", i), encode(in), 201)
	}
	_, raw := f.request(t, f.customer, "GET", "/api/v1/deals?limit=2&role=customer", "", "", 200)
	page := object(t, raw)
	items := page["items"].([]any)
	next := page["next_cursor"].(string)
	if len(items) != 2 || items[0].(map[string]any)["title"] != "Тираж 4" {
		t.Fatal("wrong descending list")
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.(map[string]any)["id"].(string)] = true
	}
	for next != "" {
		_, raw = f.request(t, f.customer, "GET", "/api/v1/deals?limit=2&role=customer&cursor="+url.QueryEscape(next), "", "", 200)
		page = object(t, raw)
		for _, item := range page["items"].([]any) {
			id := item.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatal("duplicate pagination item")
			}
			seen[id] = true
		}
		next, _ = page["next_cursor"].(string)
	}
	if len(seen) != 5 {
		t.Fatal("pagination lost records")
	}
	_, raw = f.request(t, f.customer, "GET", "/api/v1/deals?limit=2&role=customer", "", "", 200)
	cursor := object(t, raw)["next_cursor"].(string)
	for _, path := range []string{"?limit=3&role=customer&cursor=" + url.QueryEscape(cursor), "?limit=2&role=supplier&cursor=" + url.QueryEscape(cursor), "?limit=2&role=customer&q=x&cursor=" + url.QueryEscape(cursor), "?limit=2&role=customer&cursor=" + url.QueryEscape(cursor+"x")} {
		f.request(t, f.customer, "GET", "/api/v1/deals"+path, "", "", 400)
	}
	f.request(t, f.supplier, "GET", "/api/v1/deals?limit=2&role=customer&cursor="+url.QueryEscape(cursor), "", "", 400)
	for _, q := range []string{"Тираж 3", "Типография", f.supplier.org} {
		_, raw = f.request(t, f.customer, "GET", "/api/v1/deals?q="+url.QueryEscape(q), "", "", 200)
		if len(object(t, raw)["items"].([]any)) == 0 {
			t.Fatal("search missing", q)
		}
	}
	for _, q := range []string{"Заказчик", f.customer.org} {
		_, raw = f.request(t, f.supplier, "GET", "/api/v1/deals?q="+url.QueryEscape(q)+"&role=supplier", "", "", 200)
		if len(object(t, raw)["items"].([]any)) != 5 {
			t.Fatal("supplier counterpart search wrong")
		}
	}
	_, raw = f.request(t, f.supplier, "GET", "/api/v1/deals?role=customer", "", "", 200)
	if len(object(t, raw)["items"].([]any)) != 0 {
		t.Fatal("role filter failed")
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=", "status=UNKNOWN", "role=owner", "created_from=tomorrow", "created_from=2027-01-01T00:00:00Z&created_to=2026-01-01T00:00:00Z", "q=a&q=b", "unknown=x", "q=%zz"} {
		f.request(t, f.customer, "GET", "/api/v1/deals?"+query, "", "", 400)
	}
	created := items[0].(map[string]any)["created_at"].(string)
	_, raw = f.request(t, f.customer, "GET", "/api/v1/deals?created_from="+url.QueryEscape(created)+"&created_to="+url.QueryEscape(created), "", "", 200)
	if len(object(t, raw)["items"].([]any)) != 1 {
		t.Fatal("created bounds must be inclusive")
	}
	in := input(f.supplier.org)
	in["deadline_at"] = time.Now().UTC().Add(350 * time.Millisecond).Format(time.RFC3339Nano)
	_, raw = f.request(t, f.customer, "POST", "/api/v1/deals", "expiring-draft-key-01", encode(in), 201)
	expiringID := object(t, raw)["id"].(string)
	time.Sleep(450 * time.Millisecond)
	_, raw = f.request(t, f.customer, "GET", "/api/v1/deals/"+expiringID, "", "", 200)
	if object(t, raw)["status"] != "REVIEW_REQUIRED" {
		t.Fatal("expired card stale")
	}
	_, raw = f.request(t, f.customer, "GET", "/api/v1/deals?status=REVIEW_REQUIRED", "", "", 200)
	expired := object(t, raw)["items"].([]any)
	if len(expired) != 1 || expired[0].(map[string]any)["id"] != expiringID {
		t.Fatal("deadline list/filter mismatch")
	}
	_, raw = f.request(t, f.customer, "GET", "/api/v1/deals?status=DRAFT", "", "", 200)
	if len(object(t, raw)["items"].([]any)) != 5 {
		t.Fatal("expired draft leaked into DRAFT filter")
	}
	_, raw = f.request(t, f.customer, "POST", "/api/v1/deals", "expiring-draft-key-01", encode(in), 201)
	if object(t, raw)["status"] != "DRAFT" {
		t.Fatal("idempotent replay must return historical response")
	}
}

func TestDraftDatabaseConstraints(t *testing.T) {
	f := setup(t)
	_, raw := f.request(t, f.customer, "POST", "/api/v1/deals", "immutable-draft-key-01", encode(input(f.supplier.org)), 201)
	id := object(t, raw)["id"].(string)
	for _, statement := range []string{`UPDATE deal_versions SET title='changed' WHERE deal_id=$1`, `DELETE FROM deal_versions WHERE deal_id=$1`, `UPDATE audit_events SET type='changed' WHERE deal_id=$1`, `DELETE FROM audit_events WHERE deal_id=$1`, `UPDATE idempotency_keys SET status_code=200 WHERE resource=$1`, `UPDATE deals SET deal_version=2 WHERE id=$1`} {
		arg := id
		if strings.Contains(statement, "resource") {
			arg = "/deals"
		}
		if _, err := f.pool.Exec(context.Background(), statement, arg); err == nil {
			t.Fatal("constraint failed", statement)
		}
	}
	for _, change := range []string{"amount_kopecks", "customer_party_id"} {
		tx, err := f.pool.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(context.Background(), `INSERT INTO deals(id) VALUES('invalid-deal')`); err != nil {
			t.Fatal(err)
		}
		amount := int64(1)
		supplier := f.supplier.org
		if change == "amount_kopecks" {
			amount = 0
		} else {
			supplier = f.customer.org
		}
		_, err = tx.Exec(context.Background(), `INSERT INTO deal_versions(deal_id,deal_version,customer_party_id,supplier_party_id,title,description,amount_kopecks,currency,deadline_at,created_by) VALUES('invalid-deal',1,$1,$2,'Test','Test',$3,'RUB',clock_timestamp()+interval '1 day',$4)`, f.customer.org, supplier, amount, f.customer.user)
		_ = tx.Rollback(context.Background())
		if err == nil {
			t.Fatal("DB accepted invalid", change)
		}
	}
}
