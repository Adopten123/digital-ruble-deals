package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL, HTTPAddr, AppOrigin, KeyFile, MigrationsDir string
	DemoMode, SecureCookies                                  bool
}

func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), HTTPAddr: env("HTTP_ADDR", ":8080"), AppOrigin: env("APP_ORIGIN", "http://localhost:8080"), KeyFile: env("APP_KEY_FILE", "/var/lib/poaktu/keys/app.key"), MigrationsDir: env("MIGRATIONS_DIR", "./migrations")}
	var err error
	c.DemoMode, err = strconv.ParseBool(env("DEMO_MODE", "false"))
	if err != nil {
		return c, errors.New("DEMO_MODE must be true or false")
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	// This binary is the test prototype. Never seed public demo credentials into a production deployment.
	if !c.DemoMode {
		return c, errors.New("this test prototype requires explicit DEMO_MODE=true")
	}
	u, err := url.Parse(c.AppOrigin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return c, errors.New("APP_ORIGIN must be an absolute origin")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return c, errors.New("APP_ORIGIN scheme must be http or https")
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return c, errors.New("HTTP demo origin must use localhost or a loopback address")
		}
	}
	c.SecureCookies = u.Scheme == "https"
	c.AppOrigin = strings.TrimSuffix(c.AppOrigin, "/")
	return c, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
