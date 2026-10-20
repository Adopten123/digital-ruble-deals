package authhttp_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Adopten123/digital-ruble-deals/internal/core/security"
	httpapi "github.com/Adopten123/digital-ruble-deals/internal/core/transport/http"
	authrepo "github.com/Adopten123/digital-ruble-deals/internal/features/auth/repository/postgres"
	authservice "github.com/Adopten123/digital-ruble-deals/internal/features/auth/service"
	authhttp "github.com/Adopten123/digital-ruble-deals/internal/features/auth/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/features/bootstrap"
	orgrepo "github.com/Adopten123/digital-ruble-deals/internal/features/organizations/repository/postgres"
	orgservice "github.com/Adopten123/digital-ruble-deals/internal/features/organizations/service"
	orghttp "github.com/Adopten123/digital-ruble-deals/internal/features/organizations/transport/http"
	"github.com/Adopten123/digital-ruble-deals/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

const origin = "http://localhost:8080"

type fixture struct {
	pool   *pgxpool.Pool
	server *httptest.Server
	client *http.Client
	csrf   string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.Database(t)
	if err := bootstrap.Run(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	svc, err := authservice.New(authrepo.New(pool))
	if err != nil {
		t.Fatal(err)
	}
	guard := httpapi.NewCSRF(make([]byte, 32), origin, false)
	handler := authhttp.New(svc, guard)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	orghttp.New(orgservice.New(orgrepo.New(pool), make([]byte, 32))).RegisterRoutes(mux, handler.Require)
	server := httptest.NewServer(httpapi.Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), mux))
	t.Cleanup(server.Close)
	jar, _ := cookiejar.New(nil)
	f := &fixture{pool: pool, server: server, client: &http.Client{Jar: jar}}
	f.refreshCSRF(t)
	return f
}
func (f *fixture) refreshCSRF(t *testing.T) {
	t.Helper()
	_, body, _ := f.request(t, "GET", "/api/v1/auth/csrf", "", 200)
	f.csrf = body["csrf_token"].(string)
}
func (f *fixture) request(t *testing.T, method, path, body string, status int) (*http.Response, map[string]any, string) {
	t.Helper()
	request, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if method != "GET" {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", origin)
		request.Header.Set("X-CSRF-Token", f.csrf)
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s got %d want %d: %s", method, path, response.StatusCode, status, raw)
	}
	out := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &out) != nil {
		t.Fatalf("invalid response: %s", raw)
	}
	return response, out, string(raw)
}
func registerBody(email string) string {
	body, _ := json.Marshal(map[string]any{"email": email, "password": "RegistrationSecret2026!", "organisation": map[string]string{"name": "Учебная организация", "kind": "COMPANY"}})
	return string(body)
}
func loginBody(email, password string) string {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return string(body)
}
func (f *fixture) login(t *testing.T, email string) {
	t.Helper()
	_, out, _ := f.request(t, "POST", "/api/v1/auth/login", loginBody(email, bootstrap.Password), 200)
	f.csrf = out["csrf_token"].(string)
}

func TestRegistrationSessionRotationAndLogout(t *testing.T) {
	f := setup(t)
	preauth := f.csrf
	response, identity, _ := f.request(t, "POST", "/api/v1/auth/register", registerBody("Owner@Example.test"), 201)
	f.csrf = identity["csrf_token"].(string)
	if f.csrf == preauth || identity["user"].(map[string]any)["system_role"] != "USER" {
		t.Fatal("identity or CSRF rotation wrong")
	}
	if !strings.HasSuffix(identity["session_expires_at"].(string), "Z") {
		t.Fatal("session time is not UTC")
	}
	orgID := identity["organisation"].(map[string]any)["id"].(string)
	var sessionCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == httpapi.SessionCookie {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !security.ValidToken(sessionCookie.Value) || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode || sessionCookie.Secure || sessionCookie.Path != "/" || sessionCookie.MaxAge != 86400 {
		t.Fatalf("bad local session policy: %+v", sessionCookie)
	}
	ctx := context.Background()
	var hash, password string
	var lifetime float64
	if err := f.pool.QueryRow(ctx, `SELECT s.token_hash,u.password_hash,extract(epoch FROM(s.expires_at-s.created_at)) FROM sessions s JOIN users u ON u.id=s.user_id WHERE u.email='owner@example.test'`).Scan(&hash, &password, &lifetime); err != nil {
		t.Fatal(err)
	}
	if hash == sessionCookie.Value || hash != security.TokenHash(sessionCookie.Value) || !strings.HasPrefix(password, "$argon2id$") || !security.VerifyPassword(password, "RegistrationSecret2026!") || lifetime < 86390 || lifetime > 86405 {
		t.Fatal("stored session/password policy wrong")
	}
	_, own, _ := f.request(t, "GET", "/api/v1/organisations/me", "", 200)
	account := own["account"].(map[string]any)
	if own["organisation"].(map[string]any)["id"] != orgID || account["total_kopecks"] != "0" || account["reserved_kopecks"] != "0" || account["available_kopecks"] != "0" {
		t.Fatal("registration did not create zero owner account")
	}
	f.request(t, "GET", "/api/v1/auth/me", "", 200)
	_, after, _ := f.request(t, "POST", "/api/v1/auth/login", loginBody("owner@example.test", "RegistrationSecret2026!"), 200)
	f.csrf = after["csrf_token"].(string)
	var revoked bool
	if err := f.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM sessions WHERE token_hash=$1`, hash).Scan(&revoked); err != nil || !revoked {
		t.Fatal("old session was not revoked", err)
	}
	current := f.csrf
	f.csrf = preauth
	f.request(t, "POST", "/api/v1/auth/logout", "", 403)
	f.csrf = current
	f.request(t, "POST", "/api/v1/auth/logout", "", 204)
	f.request(t, "GET", "/api/v1/auth/me", "", 401)
	var active int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, identity["user"].(map[string]any)["id"]).Scan(&active); err != nil || active != 0 {
		t.Fatal("logout did not revoke session", err)
	}
}

func TestSecurityValidationAndGenericLogin(t *testing.T) {
	f := setup(t)
	for _, body := range []string{`null`, `[]`, `{"email":null,"password":"long-password"}`, `{"email":"a@example.test","password":null}`, `{"email":"a@example.test","password":"long-password","system_role":"DEMO_OPERATOR"}`, `{"email":"a@example.test","password":"long-password"} {}`} {
		f.request(t, "POST", "/api/v1/auth/login", body, 400)
	}
	_, existing, _ := f.request(t, "POST", "/api/v1/auth/login", loginBody("customer@demo.poaktu.local", "incorrect-password"), 401)
	_, missing, _ := f.request(t, "POST", "/api/v1/auth/login", loginBody("absent@example.test", "incorrect-password"), 401)
	if existing["error"].(map[string]any)["code"] != missing["error"].(map[string]any)["code"] || existing["error"].(map[string]any)["message"] != missing["error"].(map[string]any)["message"] {
		t.Fatal("login reveals whether account exists")
	}
	for _, badOrigin := range []string{"", "https://attacker.example"} {
		request, _ := http.NewRequest("POST", f.server.URL+"/api/v1/auth/register", strings.NewReader(registerBody("attacker@example.test")))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", badOrigin)
		request.Header.Set("X-CSRF-Token", f.csrf)
		response, err := f.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatal("origin bypass", response.StatusCode)
		}
	}
	request, _ := http.NewRequest("POST", f.server.URL+"/api/v1/auth/register", strings.NewReader(registerBody("attacker@example.test")))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("missing CSRF accepted")
	}
	f.request(t, "POST", "/api/v1/auth/register", `{"email":"privileged@example.test","password":"RegistrationSecret2026!","organisation":{"name":"Company","kind":"COMPANY","id":"existing"}}`, 400)
	f.request(t, "POST", "/api/v1/auth/register", registerBody("customer@demo.poaktu.local"), 409)
	f.request(t, "POST", "/api/v1/auth/login", `{"email":"`+strings.Repeat("x", 17<<10)+`"}`, 413)
}

func TestOrganizationScopeCatalogPaginationAndOperator(t *testing.T) {
	f := setup(t)
	f.login(t, "customer@demo.poaktu.local")
	_, own, _ := f.request(t, "GET", "/api/v1/organisations/me?organization_id=other", "", 200)
	if own["account"].(map[string]any)["total_kopecks"] != "50000000" {
		t.Fatal("scope or initial account wrong")
	}
	_, catalog, raw := f.request(t, "GET", "/api/v1/organisations?limit=1", "", 200)
	if len(catalog["items"].([]any)) != 1 || catalog["next_cursor"] == nil {
		t.Fatal("pagination missing")
	}
	for _, private := range []string{"email", "account", "total_kopecks", "members", "password"} {
		if strings.Contains(raw, private) {
			t.Fatal("catalog private field", private)
		}
	}
	first := catalog["items"].([]any)[0].(map[string]any)
	if len(first) != 4 {
		t.Fatal("catalog projection contains extra fields")
	}
	next := catalog["next_cursor"].(string)
	_, page2, _ := f.request(t, "GET", "/api/v1/organisations?limit=1&cursor="+next, "", 200)
	if page2["items"].([]any)[0].(map[string]any)["id"] == first["id"] {
		t.Fatal("cursor repeats item")
	}
	f.request(t, "GET", "/api/v1/organisations?cursor="+next+"x", "", 400)
	f.request(t, "GET", "/api/v1/organisations?limit=101", "", 400)
	_, empty, _ := f.request(t, "GET", "/api/v1/organisations?q=%25", "", 200)
	if len(empty["items"].([]any)) != 0 {
		t.Fatal("search interpreted wildcard")
	}
	f.login(t, "operator@demo.poaktu.local")
	_, operator, _ := f.request(t, "GET", "/api/v1/auth/me", "", 200)
	if operator["organisation"] != nil || operator["user"].(map[string]any)["system_role"] != "DEMO_OPERATOR" {
		t.Fatal("operator has organization")
	}
	f.request(t, "GET", "/api/v1/organisations/me", "", 403)
}

func TestRegistrationAtomicFailure(t *testing.T) {
	pool := testutil.Database(t)
	ctx := context.Background()
	repo := authrepo.New(pool)
	in := authservice.Registration{UserID: "rollback-user", OrganizationID: "rollback-org", AccountID: "rollback-account", Email: "rollback@example.test", PasswordHash: "unused-in-this-persistence-test", Name: "Rollback Company", Kind: "COMPANY", Session: authservice.Session{Hash: strings.Repeat("a", 64), UserID: "rollback-user", ExpiresAt: time.Now().Add(-time.Hour)}}
	if _, err := repo.Register(ctx, in); err == nil {
		t.Fatal("late session constraint failure missing")
	}
	for _, table := range []string{"users", "organizations", "memberships", "accounts", "sessions"} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial registration in %s: %d %v", table, count, err)
		}
	}
}

func TestLoginRateLimit(t *testing.T) {
	f := setup(t)
	for range 10 {
		f.request(t, "POST", "/api/v1/auth/login", loginBody("missing@example.test", "incorrect-password"), 401)
	}
	response, _, _ := f.request(t, "POST", "/api/v1/auth/login", loginBody("missing@example.test", "incorrect-password"), 429)
	if response.Header.Get("Retry-After") == "" {
		t.Fatal("retry hint missing")
	}
}
