package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func clearCORSEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{EnvCORSAllowOrigin, EnvCORSAllowMethods, EnvCORSAllowHeaders, EnvCORSMaxAge} {
		t.Setenv(key, "")
	}
}

func TestCORSConfigFromEnvDefaults(t *testing.T) {
	clearCORSEnv(t)
	cfg := CORSConfigFromEnv()
	if cfg.AllowOrigin != DefaultCORSAllowOrigin {
		t.Fatalf("allow origin = %q, want %q", cfg.AllowOrigin, DefaultCORSAllowOrigin)
	}
	if cfg.AllowMethods != DefaultCORSAllowMethods {
		t.Fatalf("allow methods = %q, want %q", cfg.AllowMethods, DefaultCORSAllowMethods)
	}
	if cfg.AllowHeaders != DefaultCORSAllowHeaders {
		t.Fatalf("allow headers = %q, want default", cfg.AllowHeaders)
	}
	if cfg.MaxAge != DefaultCORSMaxAge {
		t.Fatalf("max age = %d, want %d", cfg.MaxAge, DefaultCORSMaxAge)
	}
}

func TestCORSConfigFromEnvOverrides(t *testing.T) {
	t.Setenv(EnvCORSAllowOrigin, "https://play.example.com")
	t.Setenv(EnvCORSAllowMethods, "GET, OPTIONS")
	t.Setenv(EnvCORSAllowHeaders, "Authorization, X-Custom")
	t.Setenv(EnvCORSMaxAge, "1200")
	cfg := CORSConfigFromEnv()
	if cfg.AllowOrigin != "https://play.example.com" || cfg.AllowMethods != "GET, OPTIONS" ||
		cfg.AllowHeaders != "Authorization, X-Custom" || cfg.MaxAge != 1200 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestCORSConfigFromEnvIgnoresInvalidMaxAge(t *testing.T) {
	clearCORSEnv(t)
	t.Setenv(EnvCORSMaxAge, "not-a-number")
	if cfg := CORSConfigFromEnv(); cfg.MaxAge != DefaultCORSMaxAge {
		t.Fatalf("max age = %d, want default %d", cfg.MaxAge, DefaultCORSMaxAge)
	}
	t.Setenv(EnvCORSMaxAge, "-5")
	if cfg := CORSConfigFromEnv(); cfg.MaxAge != DefaultCORSMaxAge {
		t.Fatalf("negative max age = %d, want default %d", cfg.MaxAge, DefaultCORSMaxAge)
	}
}

func newCORSTestHandler() http.Handler {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("inner"))
	})
	return CORSConfig{AllowOrigin: "*", AllowMethods: "GET, POST, OPTIONS", AllowHeaders: "Authorization", MaxAge: 600}.Middleware(inner)
}

func TestCORSMiddlewareAddsHeadersOnV1(t *testing.T) {
	handler := newCORSTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
	req.Header.Set("Origin", "https://play.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("allow origin = %q, want *", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, OPTIONS" {
		t.Fatalf("allow methods = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Authorization" {
		t.Fatalf("allow headers = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Fatalf("max age = %q", got)
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("inner handler must still run, status = %d", rec.Code)
	}
}

func TestCORSMiddlewareCoversInternalRoutes(t *testing.T) {
	handler := newCORSTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/internal/battle/result", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("internal route must be covered by CORS")
	}
}

func TestCORSMiddlewarePreflightReturns204(t *testing.T) {
	handler := newCORSTestHandler()
	req := httptest.NewRequest(http.MethodOptions, "/v1/rooms/create", nil)
	req.Header.Set("Origin", "https://play.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight missing allow origin")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("preflight body must be empty, got %q", rec.Body.String())
	}
}

func TestCORSMiddlewareLeavesOtherPathsUntouched(t *testing.T) {
	handler := newCORSTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("non-v1 path must not receive CORS headers")
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("inner handler must still run, status = %d", rec.Code)
	}
}

func TestCORSMiddlewareSpecificOriginEcho(t *testing.T) {
	handler := CORSConfig{AllowOrigin: "https://a.example.com, https://b.example.com"}.Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
	)
	req := httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
	req.Header.Set("Origin", "https://b.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://b.example.com" {
		t.Fatalf("allow origin = %q, want echoed origin", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("disallowed origin must not be echoed, got %q", got)
	}
}

func TestCORSPathApplies(t *testing.T) {
	cases := map[string]bool{
		"/v1":                     true,
		"/v1/bootstrap":           true,
		"/v1/lobby/ws":            true,
		"/internal":               true,
		"/internal/battle/result": true,
		"/health":                 false,
		"/":                       false,
		"/v10":                    false,
	}
	for path, want := range cases {
		if got := corsPathApplies(path); got != want {
			t.Fatalf("corsPathApplies(%q) = %v, want %v", path, got, want)
		}
	}
}
