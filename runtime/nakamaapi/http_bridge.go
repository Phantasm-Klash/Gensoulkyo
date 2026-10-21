package nakamaapi

import (
	"errors"
	"net/http"
	"strings"

	"gensoulkyo/runtime/core"
)

// ErrNakamaIdentityRequired is returned by EnsureRESTSession when the caller
// could not resolve a Nakama user id, so no business session can be bound to
// the request.
var ErrNakamaIdentityRequired = errors.New("nakama user id is required")

// HTTPHandler returns the Gensoulkyo `/v1` REST surface bound to the same
// core.Service and business-envelope guard that HandleRPC dispatches against.
//
// Nakama's `RegisterHttp` hands the runtime a raw *http.Request with an empty
// runtime context: no user id, no session id (verified against
// heroiclabs/nakama:3.41.0). The REST routes therefore authenticate through the
// `Authorization: Bearer` header exactly like the standalone Go lobby did, and
// the caller is responsible for rewriting that header onto a Gensoulkyo session
// token before delegating here — see EnsureRESTSession.
func (handler *Handler) HTTPHandler() http.Handler {
	return handler.restHandler
}

// EnsureRESTSession binds a Nakama-authenticated user onto the Gensoulkyo
// business session model and returns the session token the `/v1` REST surface
// expects in `Authorization: Bearer`.
//
// The token is derived exactly like the HTTP RPC path derives it
// (resolveSessionID), so a caller that mixes `/v2/rpc/*` and `/v1/*` reaches
// one and the same business session instead of superseding itself.
func (handler *Handler) EnsureRESTSession(userID string, displayName string) (string, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", ErrNakamaIdentityRequired
	}
	token := resolveSessionID(userID, "")
	if token == "" {
		return "", ErrNakamaIdentityRequired
	}
	if _, err := handler.service.LoginExternal(core.ExternalSessionRequest{
		UserID:       userID,
		SessionToken: token,
		DisplayName:  strings.TrimSpace(displayName),
		Provider:     "nakama",
	}); err != nil {
		return "", err
	}
	return token, nil
}
