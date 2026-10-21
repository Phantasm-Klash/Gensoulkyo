//go:build nakama

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"gensoulkyo/runtime/nakamaapi"

	"github.com/heroiclabs/nakama-common/runtime"
)

// restRoutes is the `/v1` business surface the LayaAir client and the battle
// servers call. Nakama's `RegisterHttp` only matches exact patterns (a
// `/v1/` prefix mount does not cascade), so every route is registered one by
// one; gorilla-style `{name}` placeholders cover the path parameters.
//
// `/v1/matches/{matchID}/input` and `/v1/matches/{matchID}/settle` are
// deliberately absent: they are listed in the client's REST_ROUTES but have no
// caller (battle input goes over KCP straight to the battle server, settlement
// is server-side), and they are the two entries the operation contract forbids
// on the lobby transports for exactly that reason.
var restRoutes = []restRoute{
	// --- session bootstrap (no session required) ---
	{http.MethodPost, "/v1/auth/anonymous"},
	{http.MethodPost, "/v1/auth/register"},
	{http.MethodPost, "/v1/auth/login"},
	{http.MethodPost, "/v1/auth/logout"},
	{http.MethodPost, "/v1/auth/email/code"},
	{http.MethodGet, "/v1/auth/config"},
	// --- profile / meta ---
	{http.MethodGet, "/v1/bootstrap"},
	{http.MethodGet, "/v1/inventory"},
	{http.MethodGet, "/v1/records"},
	{http.MethodPost, "/v1/cards/upgrade"},
	{http.MethodGet, "/v1/decks"},
	{http.MethodPost, "/v1/decks/save"},
	{http.MethodGet, "/v1/chests"},
	{http.MethodPost, "/v1/chests/open"},
	{http.MethodPost, "/v1/presence/heartbeat"},
	{http.MethodGet, "/v1/checkin"},
	{http.MethodPost, "/v1/checkin/claim"},
	{http.MethodPost, "/v1/activity/claim"},
	{http.MethodGet, "/v1/shop"},
	{http.MethodGet, "/v1/shop/catalog"},
	{http.MethodPost, "/v1/shop/purchase"},
	// --- gacha ---
	{http.MethodGet, "/v1/gacha/pools"},
	{http.MethodPost, "/v1/gacha/pull"},
	{http.MethodGet, "/v1/gacha/roster"},
	{http.MethodPost, "/v1/gacha/faith-exchange"},
	// --- rooms ---
	{http.MethodPost, "/v1/rooms/create"},
	{http.MethodGet, "/v1/rooms"},
	{http.MethodGet, "/v1/rooms/{roomCode}"},
	{http.MethodGet, "/v1/rooms/{roomCode}/rules"},
	{http.MethodPost, "/v1/rooms/{roomCode}/join"},
	{http.MethodPost, "/v1/rooms/{roomCode}/leave"},
	{http.MethodPost, "/v1/rooms/{roomCode}/messages"},
	// --- matchmaking ---
	{http.MethodPost, "/v1/matchmaking/join"},
	{http.MethodGet, "/v1/matchmaking/tickets/{ticketID}"},
	{http.MethodPost, "/v1/matchmaking/tickets/{ticketID}/cancel"},
	// --- matches ---
	{http.MethodPost, "/v1/matches/{matchID}/ready"},
	{http.MethodGet, "/v1/matches/{matchID}/snapshot"},
	{http.MethodGet, "/v1/matches/{matchID}/events"},
	{http.MethodGet, "/v1/matches/{matchID}/battle-allocation"},
	{http.MethodPost, "/v1/matches/{matchID}/battle-ticket"},
	{http.MethodPost, "/v1/matches/{matchID}/mode-action"},
	{http.MethodPost, "/v1/matches/{matchID}/disconnect"},
	{http.MethodPost, "/v1/matches/{matchID}/reconnect"},
	{http.MethodPost, "/v1/matches/{matchID}/rematch"},
	// --- replays ---
	{http.MethodGet, "/v1/replays/{replayID}"},
	{http.MethodGet, "/v1/replays/{replayID}/dem"},
	// --- business / security ops surface ---
	{http.MethodGet, "/v1/business/contract"},
	{http.MethodPost, "/v1/business/events"},
	{http.MethodPost, "/v1/business/events/settlement"},
	{http.MethodGet, "/v1/security/business-envelope"},
	{http.MethodGet, "/v1/security/service-callback"},
	{http.MethodGet, "/v1/security/battle-audit"},
	{http.MethodGet, "/v1/security/lobby-audit"},
	// --- battle server service callbacks (shared-secret HMAC, no session) ---
	{http.MethodGet, "/v1/battle/servers"},
	{http.MethodPost, "/v1/battle/servers/register"},
	{http.MethodPost, "/v1/battle/servers/heartbeat"},
	{http.MethodPost, "/v1/battle/servers/offline"},
	{http.MethodPost, "/v1/battle/tickets/consume"},
	{http.MethodPost, "/v1/battle/results/submit"},
	{http.MethodPost, "/v1/battle/events"},
	{http.MethodPost, "/v1/battle/replays/dem"},
}

type restRoute struct {
	method  string
	pattern string
}

// restBridge mounts the Gensoulkyo `/v1` REST surface on Nakama's client API.
//
// Nakama does not inject any runtime context into RegisterHttp handlers — no
// user id, no session id — so the caller's identity has to be recovered from
// the `Authorization: Bearer` header. The header carries a Nakama JWT, which
// Nakama itself validates: the bridge asks Nakama's own `/v2/account` endpoint
// (loopback) and, on success, rewrites the header onto the Gensoulkyo session
// token that core.Service knows. Nothing is taken on trust from the JWT claims.
type restBridge struct {
	handler *nakamaapi.Handler
	client  *http.Client
	logger  runtime.Logger
}

func newRESTBridge(handler *nakamaapi.Handler, logger runtime.Logger) *restBridge {
	return &restBridge{
		handler: handler,
		client:  &http.Client{Timeout: 5 * time.Second},
		logger:  logger,
	}
}

func (bridge *restBridge) register(initializer runtime.Initializer) error {
	for _, route := range restRoutes {
		// OPTIONS is registered alongside the business method so browser
		// preflights are answered; Nakama adds the CORS headers itself.
		err := initializer.RegisterHttp(route.pattern, bridge.serve, route.method, http.MethodOptions)
		if err != nil {
			return fmt.Errorf("register http %s %s: %w", route.method, route.pattern, err)
		}
	}
	return nil
}

func (bridge *restBridge) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if token := bearerToken(r); token != "" && isNakamaJWT(token) {
		sessionToken, ok := bridge.sessionTokenFor(r, token)
		if !ok {
			writeRESTError(w, http.StatusUnauthorized, "unauthorized", "nakama session token is not valid")
			return
		}
		r.Header.Set("Authorization", "Bearer "+sessionToken)
	}
	bridge.handler.HTTPHandler().ServeHTTP(w, r)
}

// sessionTokenFor resolves the Gensoulkyo session token behind a Nakama JWT.
//
// Nakama owns the token, so Nakama is asked to validate it: the loopback call
// to `/v2/account` is authenticated with the very same bearer credential and
// answers the account (and therefore the user id) only for a live session. A
// caller that forged or replayed an expired JWT is rejected here and never
// reaches the business service.
func (bridge *restBridge) sessionTokenFor(r *http.Request, jwt string) (string, bool) {
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, bridge.accountURL(r), nil)
	if err != nil {
		return "", false
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	response, err := bridge.client.Do(request)
	if err != nil {
		bridge.warn("nakama account lookup failed: %v", err)
		return "", false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", false
	}
	var account struct {
		User struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := json.NewDecoder(response.Body).Decode(&account); err != nil {
		return "", false
	}
	sessionToken, err := bridge.handler.EnsureRESTSession(account.User.ID, account.User.Username)
	if err != nil {
		bridge.warn("gensoulkyo session binding failed for %q: %v", account.User.ID, err)
		return "", false
	}
	return sessionToken, true
}

// accountURL points the loopback validation call at this very Nakama instance.
// Only the port is taken from the incoming request; the host is always
// loopback so a deployment behind a reverse proxy still reaches itself.
func (bridge *restBridge) accountURL(r *http.Request) string {
	port := "7350"
	if _, value, err := net.SplitHostPort(r.Host); err == nil && value != "" {
		port = value
	}
	return "http://127.0.0.1:" + port + "/v2/account"
}

func (bridge *restBridge) warn(format string, args ...any) {
	if bridge.logger != nil {
		bridge.logger.Warn(format, args...)
	}
}

func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

// isNakamaJWT distinguishes a Nakama session token from an already-Gensoulkyo
// session token. Only Nakama JWTs are exchanged; anything else is passed
// through untouched so callers that already hold a business session (and the
// standalone lobby during a rollback) keep working — core.Service still
// validates the token before any operation runs.
func isNakamaJWT(token string) bool {
	return strings.Count(token, ".") == 2
}

func writeRESTError(w http.ResponseWriter, status int, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": code, "message": message})
}
