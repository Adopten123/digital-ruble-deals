package audithttp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	auditrepo "github.com/Adopten123/digital-ruble-deals/internal/features/audit/repository/postgres"
	"github.com/Adopten123/digital-ruble-deals/internal/features/audit/service"
	audithttp "github.com/Adopten123/digital-ruble-deals/internal/features/audit/transport/http"
	authrepo "github.com/Adopten123/digital-ruble-deals/internal/features/auth/repository/postgres"
	authservice "github.com/Adopten123/digital-ruble-deals/internal/features/auth/service"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	pool   *pgxpool.Pool
	server *httptest.Server
	tokens map[string]string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{pool: testutil.Database(t), tokens: map[string]string{}}
	ctx := context.Background()
	for _, id := range []string{"customer", "former", "replacement", "third", "operator"} {
		role := "USER"
		if id == "operator" {
			role = "DEMO_OPERATOR"
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO users(id,email,password_hash,system_role) VALUES($1,$2,'unused',$3)`, id, id+"@example.test", role); err != nil {
			t.Fatal(err)
		}
		if id != "operator" {
			if _, err := f.pool.Exec(ctx, `INSERT INTO organizations(id,name,kind) VALUES($1,$2,'COMPANY');`, id, id+" company"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `INSERT INTO memberships(user_id,organization_id,role) VALUES($1,$1,'ORG_OWNER')`, id); err != nil {
				t.Fatal(err)
			}
		}
		token, err := security.RandomToken()
		if err != nil {
			t.Fatal(err)
		}
		f.tokens[id] = token
		if _, err := f.pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 day')`, security.TokenHash(token), id); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO deals(id,deal_version) VALUES('deal',2); INSERT INTO deal_versions(deal_id,deal_version,customer_party_id,supplier_party_id,title,description,amount_kopecks,currency,deadline_at,created_by) VALUES('deal',1,'customer','former','Old private title','Old private description',100,'RUB',clock_timestamp()+interval '1 day','customer'),('deal',2,'customer','replacement','Current title','Current description',100,'RUB',clock_timestamp()+interval '1 day','customer')`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO audit_events(id,deal_id,deal_version,type,actor_user_id,actor_organization_id,request_id,payload,created_at) VALUES
 ('a','deal',1,'DEAL_CREATED','customer','customer','request-a','{"title":"PRIVATE TITLE","description":"PRIVATE DESCRIPTION","status":"DRAFT","amount_kopecks":"100","currency":"RUB","password":"SECRET"}','2026-10-24T12:00:00Z'),
 ('b','deal',1,'DEAL_REWORKED','customer','customer','request-b','{"deal":{"status":"DRAFT"},"reason":"PRIVATE OLD REASON"}','2026-10-24T12:00:00Z'),
 ('c','deal',2,'PATCH_DEAL','customer','customer','request-c','{"status":"DRAFT","amount_kopecks":"100","currency":"RUB"}','2026-10-24T12:00:00Z'),
 ('d','deal',2,'CANCELLATION_REJECTED','replacement','replacement','request-d','{"deal":{"status":"REVIEW_REQUIRED"},"cancellation_request":{"id":"cancel","reason":"initial reason","decision":{"decision":"REJECTED","reason":"Actual decision reason"}}}','2026-10-24T12:00:01Z');
 INSERT INTO audit_events(id,type,actor_user_id,request_id,payload,created_at) VALUES('global','DEAL_CREATE_REJECTED','customer','request-global','{"operation":"CREATE_DEAL","error_code":"INVALID_AMOUNT"}','2026-10-24T12:00:02Z');
 INSERT INTO audit_events(id,deal_id,deal_version,type,actor_source,request_id,payload,created_at) VALUES('source','deal',2,'EDO_EVENT_PROCESSED','edo-emulator','request-source','{"event_id":"event-id","result":{"result":"ALREADY_SETTLED","payment_id":"payment-id"}}','2026-10-24T12:00:03Z')`); err != nil {
		t.Fatal(err)
	}
	authsvc, err := authservice.New(authrepo.New(f.pool))
	if err != nil {
		t.Fatal(err)
	}
	auth := authhttp.New(authsvc, httpapi.NewCSRF(make([]byte, 32), "http://localhost:8080", false))
	mux := http.NewServeMux()
	auth.RegisterRoutes(mux)
	audithttp.New(service.New(auditrepo.New(f.pool), []byte("fixed-test-only-key"))).RegisterRoutes(mux, auth.Require)
	f.server = httptest.NewServer(httpapi.Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), mux))
	t.Cleanup(f.server.Close)
	return f
}
func (f *fixture) get(t *testing.T, actor, path string, status int) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest("GET", f.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if actor != "" {
		req.AddCookie(&http.Cookie{Name: httpapi.SessionCookie, Value: f.tokens[actor]})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("GET %s as %s: %d want %d: %s", path, actor, resp.StatusCode, status, body)
	}
	return resp, body
}
func page(t *testing.T, body []byte) service.Page {
	t.Helper()
	var p service.Page
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestHistoricalScopeAndSafeReadOnlyProjection(t *testing.T) {
	f := setup(t)
	path := "/api/v1/deals/deal/audit"
	_, body := f.get(t, "customer", path, 200)
	p := page(t, body)
	if len(p.Items) != 5 {
		t.Fatalf("customer items %d: %s", len(p.Items), body)
	}
	if strings.Contains(string(body), "PRIVATE TITLE") || strings.Contains(string(body), "SECRET") {
		t.Fatalf("unsafe body %s", body)
	}
	_, body = f.get(t, "replacement", path, 200)
	p = page(t, body)
	if len(p.Items) != 3 || strings.Contains(string(body), "PRIVATE OLD") {
		t.Fatalf("replacement: %s", body)
	}
	if p.Items[0].ActorSource == nil || *p.Items[0].ActorSource != "edo-emulator" || p.Items[0].ActorUserID != nil || p.Items[0].Details.PaymentID != "payment-id" {
		t.Fatalf("source %+v", p.Items[0])
	}
	if p.Items[1].Result != "SUCCEEDED" || p.Items[1].Details.Reason != "Actual decision reason" || p.Items[1].ErrorCode != nil {
		t.Fatalf("decision %+v", p.Items[1])
	}
	for _, actor := range []string{"former", "third"} {
		f.get(t, actor, path, 404)
		f.get(t, actor, path+"/export?format=json", 404)
	}
	f.get(t, "operator", path, 404)
	f.get(t, "third", path+"?type=UNKNOWN_EVENT", 404)
	f.get(t, "customer", "/api/v1/deals/unknown/audit?type=UNKNOWN_EVENT", 404)
	f.get(t, "operator", path+"/export?format=json", 404)
	f.get(t, "", path, 401)
	f.get(t, "customer", "/api/v1/demo/audit", 403)
	_, body = f.get(t, "operator", "/api/v1/demo/audit", 200)
	if len(page(t, body).Items) != 6 || !strings.Contains(string(body), "INVALID_AMOUNT") {
		t.Fatalf("operator %s", body)
	}
	_, body = f.get(t, "operator", "/api/v1/demo/audit?deal_id=deal", 200)
	if len(page(t, body).Items) != 5 || strings.Contains(string(body), "request-global") {
		t.Fatalf("filtered operator %s", body)
	}
	_, body = f.get(t, "replacement", path+"/export?format=json", 200)
	var exported service.Export
	if json.Unmarshal(body, &exported) != nil || len(exported.Items) != 3 || strings.Contains(string(body), "PRIVATE OLD") {
		t.Fatalf("export %s", body)
	}
	// No read route creates a domain fact, result, outbox or ledger entry.
	for _, table := range []string{"outbox", "idempotency_keys", "ledger_entries"} {
		var n int
		if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("read changed %s count %d err %v", table, n, err)
		}
	}
}
func TestPaginationTiesHighwaterScopeAndInclusiveFilters(t *testing.T) {
	f := setup(t)
	path := "/api/v1/deals/deal/audit?limit=1"
	_, body := f.get(t, "customer", path, 200)
	p := page(t, body)
	if p.NextCursor == nil || p.Items[0].ID != "source" {
		t.Fatalf("first %s", body)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO audit_events(id,deal_id,deal_version,type,actor_user_id,actor_organization_id,request_id,payload,created_at) VALUES('new','deal',2,'PATCH_DEAL','customer','customer','new-request','{}','2026-10-24T12:01:00Z')`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{"source": true}
	cursor := p.NextCursor
	for cursor != nil {
		_, body = f.get(t, "customer", path+"&cursor="+url.QueryEscape(*cursor), 200)
		p = page(t, body)
		for _, e := range p.Items {
			if seen[e.ID] || e.ID == "new" {
				t.Fatalf("repeat/new entry %s", body)
			}
			seen[e.ID] = true
		}
		cursor = p.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("lost tied events: %+v", seen)
	}
	_, body = f.get(t, "customer", "/api/v1/deals/deal/audit?limit=100&from=2026-10-24T15%3A00%3A00%2B03%3A00&to=2026-10-24T12%3A00%3A00Z", 200)
	if len(page(t, body).Items) != 3 {
		t.Fatalf("inclusive %s", body)
	}

	for _, bounds := range []string{
		"from=2026-10-24T12:00:00.000000001Z&to=2026-10-24T12:00:00.999999999Z",
		"from=2026-10-24T12:00:00.000000001Z&to=2026-10-24T12:00:00.000000002Z",
	} {
		_, body = f.get(t, "customer", "/api/v1/deals/deal/audit?"+bounds, 200)
		if len(page(t, body).Items) != 0 {
			t.Fatalf("nanosecond bounds widened: %s", body)
		}
	}
	_, body = f.get(t, "customer", path, 200)
	p = page(t, body)
	f.get(t, "replacement", path+"&cursor="+url.QueryEscape(*p.NextCursor), 400)
	f.get(t, "customer", path+"&type=DEAL_CREATED&cursor="+url.QueryEscape(*p.NextCursor), 400)
	f.get(t, "customer", "/api/v1/deals/deal/audit?limit=2&cursor="+url.QueryEscape(*p.NextCursor), 400)
	f.get(t, "customer", path+"&cursor="+url.QueryEscape(*p.NextCursor+"x"), 400)
	// Rechecking current participation on every cursor use revokes old access.
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM memberships WHERE user_id='replacement'`); err != nil {
		t.Fatal(err)
	}
	f.get(t, "replacement", path, 403)
}
func TestStrictQueriesExportHeadersAndBound(t *testing.T) {
	f := setup(t)
	path := "/api/v1/deals/deal/audit"
	for _, q := range []string{"?unknown=x", "?type=", "?type=DEAL_CREATED&type=PATCH_DEAL", "?type=lowercase", "?limit=01", "?limit=+1", "?limit=0", "?limit=101", "?cursor=", "?from=2026-10-24", "?from=2026-10-25T00:00:00Z&to=2026-10-24T00:00:00Z", "?from=%ZZ", "?format=json", "?deal_id=deal"} {
		f.get(t, "customer", path+q, 400)
	}
	for _, q := range []string{"", "?format=", "?format=xml", "?format=csv&limit=1", "?format=json&cursor=abc", "?format=json&format=csv"} {
		f.get(t, "customer", path+"/export"+q, 400)
	}
	resp, body := f.get(t, "customer", path+"/export?format=csv&type=CANCELLATION_REJECTED", 200)
	if resp.Header.Get("Content-Type") != "text/csv; charset=utf-8" || resp.Header.Get("Content-Disposition") != `attachment; filename="poaktu-audit-deal.csv"` || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(string(body), "\xef\xbb\xbf") || !strings.Contains(string(body), "Actual decision reason") {
		t.Fatalf("headers %v body %q", resp.Header, body)
	}
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO audit_events(id,deal_id,deal_version,type,actor_user_id,actor_organization_id,request_id,payload) SELECT 'bulk-'||i,'deal',2,'PATCH_DEAL','customer','customer','bulk-request','{}'::jsonb FROM generate_series(1,5001) i`); err != nil {
		t.Fatal(err)
	}
	_, body = f.get(t, "customer", path+"/export?format=json", 422)
	if !strings.Contains(string(body), "EXPORT_TOO_LARGE") || strings.Contains(string(body), "bulk-") {
		t.Fatalf("not atomic %s", body)
	}
	_, body = f.get(t, "customer", path+"/export?format=json&type=CANCELLATION_REJECTED", 200)
	if strings.Contains(string(body), "bulk-") {
		t.Fatal(string(body))
	}
	_, body = f.get(t, "operator", "/api/v1/demo/audit?type=UNKNOWN_EVENT", 200)
	if len(page(t, body).Items) != 0 {
		t.Fatal(string(body))
	}
}
