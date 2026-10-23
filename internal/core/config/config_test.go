package config

import "testing"

func TestLocalDemoIsExplicit(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test-only/unused")
	t.Setenv("APP_ORIGIN", "http://localhost:8080")
	t.Setenv("DEMO_MODE", "false")
	if _, err := Load(); err == nil {
		t.Fatal("prototype allowed non-demo mode")
	}
	t.Setenv("DEMO_MODE", "true")
	cfg, err := Load()
	if err != nil || cfg.SecureCookies {
		t.Fatal("local explicit demo rejected", err)
	}
	t.Setenv("APP_ORIGIN", "http://example.test:8080")
	if _, err := Load(); err == nil {
		t.Fatal("non-loopback HTTP accepted")
	}
	t.Setenv("APP_ORIGIN", "https://localhost:8080")
	cfg, err = Load()
	if err != nil || !cfg.SecureCookies {
		t.Fatal("HTTPS cookie policy missing", err)
	}
	for _, origin := range []string{"https://example.test/path", "https://user@example.test", "https://example.test?q=1"} {
		t.Setenv("APP_ORIGIN", origin)
		if _, err := Load(); err == nil {
			t.Fatalf("non-origin accepted: %s", origin)
		}
	}
}
