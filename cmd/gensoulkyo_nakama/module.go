//go:build nakama

package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/nakamaapi"
	"gensoulkyo/runtime/storage"

	"github.com/heroiclabs/nakama-common/runtime"
)

var rpcIDs = runtimeRPCIDs()

var serviceOriginRPCIDs = serviceOriginRPCIDSet()

// External battle servers (e.g. the Raksmart battle-agent) cannot satisfy the
// in-process service-origin gate because Nakama only fills RUNTIME_CTX_VARS for
// calls that originate inside a runtime. They authenticate with a shared secret
// instead, supplied either as the X-Gensoulkyo-Service-Key HTTP header or the
// service_key query parameter. Nakama exposes both to HTTP RPC handlers via
// RUNTIME_CTX_HEADERS / RUNTIME_CTX_QUERY_PARAMS, so the gate can be satisfied
// without weakening isServiceOriginRPC for internal callers.
const (
	serviceRuntimeModeKey = core.ServiceCallbackRuntimeModeKey
	serviceOriginVarKey   = core.ServiceCallbackOriginKey
	serviceCallbackVarKey = core.ServiceCallbackFlagKey

	serviceKeyHeader     = "x-gensoulkyo-service-key"
	serviceKeyQueryParam = "service_key"

	// serviceCallbackSecretEnv names the env var holding the shared secret used
	// by out-of-process battle servers to authenticate as a service caller.
	serviceCallbackSecretEnv = "GENSOULKYO_SERVICE_CALLBACK_KEY"
)

func InitModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	handler := nakamaapi.New(nil)
	if db != nil {
		// Pushes travel as Nakama notifications: the realtime protocol is a
		// closed enum, so this is the only server-initiated channel the socket
		// carries. Without it the lobby still works, clients just poll.
		dbHandler, err := nakamaapi.NewWithDatabaseWithNotifier(db, nakamaapi.NewNakamaNotifier(nk, logger), roomCreationLimiter(logger))
		if err != nil {
			return err
		}
		handler = dbHandler
	}
	for _, id := range rpcIDs {
		rpcID := id
		if err := initializer.RegisterRpc(rpcID, func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
			response := handler.HandleRPC(nakamaapi.RPCRequest{
				ID:           rpcID,
				SessionID:    runtimeCtxString(ctx, runtime.RUNTIME_CTX_SESSION_ID),
				UserID:       runtimeCtxString(ctx, runtime.RUNTIME_CTX_USER_ID),
				DisplayName:  runtimeCtxString(ctx, runtime.RUNTIME_CTX_USERNAME),
				Service:      isServiceCall(ctx, rpcID),
				Payload:      decodedPayload(payload),
				PayloadError: payloadError(payload),
			})
			return encodeResponse(response)
		}); err != nil {
			return fmt.Errorf("register rpc %s: %w", rpcID, err)
		}
	}
	// The `/v1` business surface the client and the battle servers call. Nakama
	// answers every other `/v1/*` path with 404, so the routes are mounted one
	// by one and bridged onto the Gensoulkyo service (see rest_bridge.go).
	if err := newRESTBridge(handler, logger).register(initializer); err != nil {
		return err
	}
	// The lobby health route is mounted on the Nakama client API so that
	// operators can probe the unified business server on one port.
	if err := initializer.RegisterHttp("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"service":"gensoulkyo-nakama","rpc_count":` + strconv.Itoa(len(rpcIDs)) + `}`))
	}); err != nil {
		return fmt.Errorf("register http health: %w", err)
	}

	if logger != nil {
		logger.Info("Gensoulkyo Nakama runtime registered %d RPC handlers", len(rpcIDs))
	}
	return nil
}

func isServiceOriginRPC(ctx context.Context, rpcID string) bool {
	if _, ok := serviceOriginRPCIDs[rpcID]; !ok {
		return false
	}
	if runtimeCtxString(ctx, runtime.RUNTIME_CTX_SESSION_ID) != "" || runtimeCtxString(ctx, runtime.RUNTIME_CTX_USER_ID) != "" {
		return false
	}
	expectedMode, ok := serviceCallbackContextExpected(serviceRuntimeModeKey)
	if !ok {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(runtimeCtxString(ctx, runtime.RUNTIME_CTX_MODE)))
	if mode != expectedMode {
		return false
	}
	expectedOrigin, ok := serviceCallbackContextExpected(serviceOriginVarKey)
	if !ok {
		return false
	}
	vars := runtimeCtxStringMap(ctx, runtime.RUNTIME_CTX_VARS)
	if strings.ToLower(strings.TrimSpace(vars[serviceOriginVarKey])) != expectedOrigin {
		return false
	}
	_, ok = serviceCallbackContextExpected(serviceCallbackVarKey)
	if !ok {
		return false
	}
	callbackValue := strings.ToLower(strings.TrimSpace(vars[serviceCallbackVarKey]))
	for _, accepted := range core.ServiceCallbackAcceptedValues() {
		if callbackValue == strings.ToLower(strings.TrimSpace(accepted)) {
			return true
		}
	}
	return false
}

// isServiceCall reports whether an RPC handler invocation may act with
// service-to-service authority. It accepts either:
//
//  1. an in-process service-origin call (trusted RUNTIME_CTX_VARS), or
//  2. an out-of-process call from a trusted battle server that presented the
//     shared service secret via header or query parameter.
//
// Only operations listed in core.ServiceCallbackOperations() are eligible; the
// shared secret never widens the operation allow-list.
func isServiceCall(ctx context.Context, rpcID string) bool {
	if _, ok := serviceOriginRPCIDs[rpcID]; !ok {
		return false
	}
	if isServiceOriginRPC(ctx, rpcID) {
		return true
	}
	return externalServiceKeyMatches(ctx)
}

// externalServiceKeyMatches validates the shared secret presented by an
// out-of-process battle server. Requests that carry player session or user
// context are always rejected so a leaked secret cannot be replayed as a
// player. The configured secret comes from GENSOULKYO_SERVICE_CALLBACK_KEY
// (Nakama env), falling back to the runtime HTTP key so single-host
// development keeps working without extra configuration.
func externalServiceKeyMatches(ctx context.Context) bool {
	if runtimeCtxString(ctx, runtime.RUNTIME_CTX_SESSION_ID) != "" || runtimeCtxString(ctx, runtime.RUNTIME_CTX_USER_ID) != "" {
		return false
	}
	expected := strings.TrimSpace(serviceCallbackSecret())
	if expected == "" {
		return false
	}
	presented := strings.TrimSpace(serviceCallbackPresentedKey(ctx))
	if presented == "" {
		return false
	}
	return subtleConstantTimeEqual(presented, expected)
}

func serviceCallbackPresentedKey(ctx context.Context) string {
	if headers := runtimeCtxHeaderMap(ctx, runtime.RUNTIME_CTX_HEADERS); len(headers) > 0 {
		if value := headers[serviceKeyHeader]; value != "" {
			return value
		}
	}
	if params := runtimeCtxStringMap(ctx, runtime.RUNTIME_CTX_QUERY_PARAMS); len(params) > 0 {
		if value := params[serviceKeyQueryParam]; value != "" {
			return value
		}
	}
	return ""
}

func runtimeCtxHeaderMap(ctx context.Context, key string) map[string]string {
	value := ctx.Value(key)
	switch typed := value.(type) {
	case map[string]string:
		out := make(map[string]string, len(typed))
		for headerKey, headerValue := range typed {
			out[strings.ToLower(strings.TrimSpace(headerKey))] = headerValue
		}
		return out
	case map[string][]string:
		out := make(map[string]string, len(typed))
		for headerKey, headerValues := range typed {
			if len(headerValues) > 0 {
				out[strings.ToLower(strings.TrimSpace(headerKey))] = headerValues[0]
			}
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(typed))
		for headerKey, headerValue := range typed {
			switch typedValue := headerValue.(type) {
			case string:
				out[strings.ToLower(strings.TrimSpace(headerKey))] = typedValue
			case []string:
				if len(typedValue) > 0 {
					out[strings.ToLower(strings.TrimSpace(headerKey))] = typedValue[0]
				}
			}
		}
		return out
	default:
		return nil
	}
}

func serviceCallbackSecret() string {
	for _, key := range []string{serviceCallbackSecretEnv, "NAKAMA_RUNTIME_HTTP_KEY"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func subtleConstantTimeEqual(a string, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func serviceOriginRPCIDSet() map[string]struct{} {
	out := map[string]struct{}{}
	for _, id := range core.ServiceCallbackOperations() {
		out[id] = struct{}{}
	}
	return out
}

// rpcPreAuthIDs are client operations that run before the caller owns a
// session. They cannot present a signed business envelope, so they are
// deliberately kept out of the business operation contract (whose published
// guard fields require BusinessEnvelopeRequired) and registered here instead.
var rpcPreAuthIDs = []string{
	"auth.anonymous",
	"auth.emailCode",
	"auth.register",
	"auth.login",
}

// rpcAliasIDs are alternate spellings of contract operations that the client's
// REST route table also publishes. Registering them keeps the two transports
// interchangeable instead of answering a 404 on the RPC one.
var rpcAliasIDs = []string{
	"chests.get",
	"rooms.messages",
}

func runtimeRPCIDs() []string {
	ids := append([]string{}, core.ContractClientRPCOperations()...)
	ids = append(ids, rpcPreAuthIDs...)
	ids = append(ids, rpcAliasIDs...)
	seen := map[string]struct{}{}
	for _, id := range ids {
		seen[id] = struct{}{}
	}
	for _, id := range core.ServiceCallbackOperations() {
		if _, ok := seen[id]; ok {
			continue
		}
		ids = append(ids, id)
		seen[id] = struct{}{}
	}
	return ids
}

func serviceCallbackContextValue(key string) string {
	value, _ := serviceCallbackContextExpected(key)
	return value
}

func serviceCallbackContextExpected(key string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(core.ServiceCallbackContext()[key]))
	return value, value != ""
}

func decodePayload(payload string) map[string]any {
	if payload == "" {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		return map[string]any{"body": map[string]any{"raw_payload": payload}}
	}
	return out
}

func decodedPayload(payload string) map[string]any {
	if err := payloadJSONError(payload); err != nil {
		return map[string]any{}
	}
	return decodePayload(payload)
}

func payloadError(payload string) string {
	if err := payloadJSONError(payload); err != nil {
		return "invalid JSON payload: " + err.Error()
	}
	return ""
}

func payloadJSONError(payload string) error {
	if strings.TrimSpace(payload) == "" {
		return nil
	}
	out := map[string]any{}
	return json.Unmarshal([]byte(payload), &out)
}

func encodeResponse(response nakamaapi.Response) (string, error) {
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func runtimeCtxString(ctx context.Context, key string) string {
	value := ctx.Value(key)
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func runtimeCtxStringMap(ctx context.Context, key string) map[string]string {
	value := ctx.Value(key)
	switch typed := value.(type) {
	case map[string]string:
		return typed
	case map[string]any:
		out := make(map[string]string, len(typed))
		for mapKey, mapValue := range typed {
			if text, ok := mapValue.(string); ok {
				out[mapKey] = text
			}
		}
		return out
	default:
		return map[string]string{}
	}
}

// roomCreationLimiter prefers Redis so one player's budget is shared across every
// lobby replica, and falls back to the in-process limiter when Redis is not
// configured or not reachable.
//
// Falling back rather than failing is deliberate and matches what the limiter is
// for: its own failure mode has to stay "allowed", so that a Redis outage costs
// the abuse window reopening rather than people being unable to create rooms. The
// client is never closed on purpose -- it holds the connection pool and lives as
// long as the process.
func roomCreationLimiter(logger runtime.Logger) core.RoomCreationLimiter {
	fallback := core.NewMemoryRoomCreationLimiter(core.DefaultRoomCreationLimit, core.DefaultRoomCreationWindow)
	config := storage.RedisConfigFromEnv()
	if !config.Enabled() {
		return fallback
	}
	client, err := storage.NewRedisClient(config)
	if err != nil {
		if logger != nil {
			logger.Warn("room creation limiter: falling back to in-process, redis client: %v", err)
		}
		return fallback
	}
	limiter, err := storage.NewRedisRoomCreationLimiter(client, core.DefaultRoomCreationLimit, core.DefaultRoomCreationWindow)
	if err != nil {
		if logger != nil {
			logger.Warn("room creation limiter: falling back to in-process, redis limiter: %v", err)
		}
		return fallback
	}
	return limiter
}
