package httpapi

import (
	"net/http"
	"os"
	"strconv"
	"strings"
)

// CORS configuration environment variables.
const (
	// EnvCORSAllowOrigin overrides Access-Control-Allow-Origin. A comma-separated
	// list is accepted; "*" (the default) allows any origin.
	EnvCORSAllowOrigin = "GENSOULKYO_CORS_ALLOW_ORIGIN"
	// EnvCORSAllowMethods overrides Access-Control-Allow-Methods.
	EnvCORSAllowMethods = "GENSOULKYO_CORS_ALLOW_METHODS"
	// EnvCORSAllowHeaders overrides Access-Control-Allow-Headers.
	EnvCORSAllowHeaders = "GENSOULKYO_CORS_ALLOW_HEADERS"
	// EnvCORSMaxAge overrides Access-Control-Max-Age in seconds.
	EnvCORSMaxAge = "GENSOULKYO_CORS_MAX_AGE"
)

// CORS defaults.
const (
	DefaultCORSAllowOrigin  = "*"
	DefaultCORSAllowMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	DefaultCORSAllowHeaders = "Authorization, Content-Type, X-Session-Token, " +
		"X-PhK-Service-Origin, X-PhK-Battle-Callback, X-PhK-Business-Envelope, " +
		"X-PhK-Business-Seq, X-PhK-Business-Timestamp-Ms, X-PhK-Business-Nonce, " +
		"X-PhK-Business-Op, X-PhK-Business-Key-Id, X-PhK-Business-Tag, " +
		"X-PhK-Business-Mode, X-PhK-Business-Body-Hash"
	DefaultCORSMaxAge = 600
)

// CORSConfig configures the cross-origin middleware.
type CORSConfig struct {
	AllowOrigin  string
	AllowMethods string
	AllowHeaders string
	MaxAge       int
}

// CORSConfigFromEnv resolves a CORSConfig from the environment, applying
// defaults for any unset or invalid value.
func CORSConfigFromEnv() CORSConfig {
	cfg := CORSConfig{
		AllowOrigin:  strings.TrimSpace(os.Getenv(EnvCORSAllowOrigin)),
		AllowMethods: strings.TrimSpace(os.Getenv(EnvCORSAllowMethods)),
		AllowHeaders: strings.TrimSpace(os.Getenv(EnvCORSAllowHeaders)),
	}
	if cfg.AllowOrigin == "" {
		cfg.AllowOrigin = DefaultCORSAllowOrigin
	}
	if cfg.AllowMethods == "" {
		cfg.AllowMethods = DefaultCORSAllowMethods
	}
	if cfg.AllowHeaders == "" {
		cfg.AllowHeaders = DefaultCORSAllowHeaders
	}
	cfg.MaxAge = DefaultCORSMaxAge
	if raw := strings.TrimSpace(os.Getenv(EnvCORSMaxAge)); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value >= 0 {
			cfg.MaxAge = value
		}
	}
	return cfg
}

// Middleware wraps next with CORS handling for the /v1/... and /internal/...
// routes. Preflight OPTIONS requests are answered with 204 and never reach the
// wrapped handler.
func (c CORSConfig) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !corsPathApplies(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		if origin, ok := c.allowOriginFor(r.Header.Get("Origin")); ok {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Methods", c.AllowMethods)
		w.Header().Set("Access-Control-Allow-Headers", c.AllowHeaders)
		w.Header().Set("Access-Control-Max-Age", strconv.Itoa(c.MaxAge))
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowOriginFor resolves the Access-Control-Allow-Origin value for a request.
func (c CORSConfig) allowOriginFor(requestOrigin string) (string, bool) {
	configured := strings.TrimSpace(c.AllowOrigin)
	if configured == "" {
		return "", false
	}
	if configured == "*" {
		return "*", true
	}
	for _, candidate := range strings.Split(configured, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if candidate == "*" {
			return "*", true
		}
		if requestOrigin != "" && strings.EqualFold(candidate, requestOrigin) {
			return requestOrigin, true
		}
	}
	return "", false
}

// corsPathApplies reports whether the path is covered by the CORS middleware.
func corsPathApplies(path string) bool {
	return path == "/v1" || strings.HasPrefix(path, "/v1/") ||
		path == "/internal" || strings.HasPrefix(path, "/internal/")
}
