package nakamaapi

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// This file gives the standalone Gensoulkyo lobby the same HTTP RPC face that
// Nakama exposes, so one process can serve both callers:
//
//   - the Windows client, through `POST /v1/...` (httpapi) or
//     `POST /v2/rpc/{id}` (NakamaLobbyTransport);
//   - an out-of-process battle agent (the RAKsmart `phk-battle-agent`), which
//     speaks Nakama RPC only: it registers, heartbeats and polls for match
//     assignments through `POST /v2/rpc/{id}?unwrap=true`.
//
// Without this face the agent's every call lands on the lobby's `/v1` mux and
// comes back `404 route not found`, so remote battle servers never register and
// matches are always spawned locally instead.

// HTTPRPCOptions configures the Nakama-compatible HTTP RPC face.
type HTTPRPCOptions struct {
	// ServiceCallbackKey is the shared secret an out-of-process battle server
	// presents (X-Gensoulkyo-Service-Key header or `service_key` query
	// parameter) to act with service-to-service authority. It mirrors
	// GENSOULKYO_SERVICE_CALLBACK_KEY. An empty value disables the service
	// origin entirely, which fails closed: service-only RPCs then answer
	// `service_origin_required`.
	ServiceCallbackKey string
	// RuntimeHTTPKey mirrors Nakama's `runtime.http_key`. It is accepted either
	// as `?http_key=` or as HTTP basic auth (`<key>:`), which is exactly how
	// Nakama's own runtime HTTP calls authenticate. An empty value disables the
	// check so single-host development keeps working.
	RuntimeHTTPKey string
	// MaxBodyBytes caps the request body. Zero selects the default.
	MaxBodyBytes int64
}

// DefaultHTTPRPCMaxBodyBytes bounds a single RPC request body.
const DefaultHTTPRPCMaxBodyBytes = 1 << 20

// HTTP RPC request/response identifiers.
const (
	headerServiceKey     = "X-Gensoulkyo-Service-Key"
	queryServiceKey      = "service_key"
	queryRuntimeHTTPKey  = "http_key"
	httpRPCPathPrefix    = "/v2/rpc/"
	httpRPCErrorUnauthor = "unauthorized"
)

// HTTPRPCHandler serves `POST /v2/rpc/{id}`.
//
// Response shape: the body is always the `nakamaapi.Response` envelope
// (`{ok,status,error_code,message,payload}`) with HTTP 200, whatever the
// business outcome. That is deliberately identical to what the Nakama plugin
// produces: `InitModule`'s handler never returns a Go error, so Nakama passes
// the encoded envelope straight through even when the caller asks for
// `?unwrap=true`. Both the battle agent and the client's NakamaLobbyTransport
// rely on that envelope, so this face must not "unwrap" anything.
type HTTPRPCHandler struct {
	handler *Handler
	options HTTPRPCOptions
}

// NewHTTPRPCHandler wires a handler onto the Nakama-compatible HTTP face.
func NewHTTPRPCHandler(handler *Handler, options HTTPRPCOptions) *HTTPRPCHandler {
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = DefaultHTTPRPCMaxBodyBytes
	}
	return &HTTPRPCHandler{handler: handler, options: options}
}

// PathPrefix reports the URL prefix this handler owns, so the caller can mount
// it on a mux without duplicating the literal.
func (h *HTTPRPCHandler) PathPrefix() string { return httpRPCPathPrefix }

// Matches reports whether the request path belongs to the HTTP RPC face.
func (h *HTTPRPCHandler) Matches(path string) bool {
	return strings.HasPrefix(path, httpRPCPathPrefix)
}

// ServeHTTP implements http.Handler.
func (h *HTTPRPCHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeRPCEnvelope(w, http.StatusMethodNotAllowed, errorResponse(http.StatusMethodNotAllowed, CodeInvalidRequest, "http rpc requires POST"))
		return
	}
	rpcID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, httpRPCPathPrefix))
	if rpcID == "" {
		writeRPCEnvelope(w, http.StatusBadRequest, errorResponse(http.StatusBadRequest, CodeInvalidRequest, "rpc id is required"))
		return
	}
	if !h.runtimeKeyAccepted(r) {
		writeRPCEnvelope(w, http.StatusUnauthorized, errorResponse(http.StatusUnauthorized, httpRPCErrorUnauthor, "runtime http key is required"))
		return
	}
	payload, payloadError := h.decodePayload(r)
	response := h.handler.HandleRPC(RPCRequest{
		ID:           rpcID,
		SessionID:    bearerToken(r),
		Service:      h.serviceOrigin(r),
		Payload:      payload,
		PayloadError: payloadError,
	})
	// A dispatched call is always HTTP 200, whatever the business outcome: the
	// plugin never returns a Go error, so Nakama passes the encoded envelope
	// through verbatim. Only the transport-level rejections above use a real
	// status code.
	writeRPCResponse(w, response)
}

// decodePayload reads the JSON object body. An empty body is an empty payload;
// a body that is not a JSON object is reported as a payload error so the
// handler answers `invalid_request` instead of panicking on a nil map.
func (h *HTTPRPCHandler) decodePayload(r *http.Request) (map[string]any, string) {
	if r.Body == nil {
		return map[string]any{}, ""
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, h.options.MaxBodyBytes))
	if err != nil {
		return nil, err.Error()
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return map[string]any{}, ""
	}
	var payload map[string]any
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return nil, err.Error()
	}
	if payload == nil {
		return map[string]any{}, ""
	}
	return payload, ""
}

// runtimeKeyAccepted mirrors Nakama's runtime HTTP authentication: the caller
// presents `runtime.http_key` either as `?http_key=` or as basic auth.
func (h *HTTPRPCHandler) runtimeKeyAccepted(r *http.Request) bool {
	expected := strings.TrimSpace(h.options.RuntimeHTTPKey)
	if expected == "" {
		return true
	}
	presented := strings.TrimSpace(r.URL.Query().Get(queryRuntimeHTTPKey))
	if presented == "" {
		if user, _, ok := r.BasicAuth(); ok {
			presented = strings.TrimSpace(user)
		}
	}
	if presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// serviceOrigin reports whether the call may act with service-to-service
// authority.
//
// It mirrors the Nakama plugin's `externalServiceKeyMatches`: the shared secret
// must match, and a request that carries player context is never treated as a
// service caller — so a leaked secret cannot be replayed as a player, and a
// player cannot borrow service authority by adding a header.
func (h *HTTPRPCHandler) serviceOrigin(r *http.Request) bool {
	if bearerToken(r) != "" {
		return false
	}
	presented := strings.TrimSpace(r.Header.Get(headerServiceKey))
	if presented == "" {
		presented = strings.TrimSpace(r.URL.Query().Get(queryServiceKey))
	}
	expected := strings.TrimSpace(h.options.ServiceCallbackKey)
	if presented == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// bearerToken extracts the player session token, if the caller presented one.
func bearerToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

// writeRPCResponse encodes a dispatched call's envelope with HTTP 200.
func writeRPCResponse(w http.ResponseWriter, response Response) {
	writeRPCEnvelope(w, http.StatusOK, response)
}

// writeRPCEnvelope encodes the envelope with an explicit HTTP status. It is used
// for transport-level rejections, which happen before dispatch and therefore do
// carry a real status.
func writeRPCEnvelope(w http.ResponseWriter, status int, response Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
