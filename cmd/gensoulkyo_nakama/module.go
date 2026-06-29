//go:build nakama

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"gensoulkyo/runtime/nakamaapi"

	"github.com/heroiclabs/nakama-common/runtime"
)

var rpcIDs = []string{
	"auth.anonymous",
	"bootstrap",
	"inventory.get",
	"cards.upgrade",
	"decks.list",
	"decks.save",
	"chests.list",
	"chests.open",
	"presence.heartbeat",
	"matchmaking.join",
	"matchmaking.ticket",
	"matchmaking.cancel",
	"rooms.create",
	"rooms.list",
	"rooms.get",
	"rooms.rules",
	"rooms.join",
	"rooms.leave",
	"rooms.message",
	"rooms.chat",
	"rooms.announcement",
	"match.ready",
	"match.disconnect",
	"match.reconnect",
	"activity.claim",
	"battle.servers.register",
	"battle.servers.heartbeat",
	"battle.servers.offline",
	"battle.servers",
	"business.envelope.audit.status",
	"battle.audit.status",
	"lobby.audit.status",
	"battle.allocation",
	"battle.ticket",
	"replay.get",
	"battle.result.submit",
}

var serviceOriginRPCIDs = map[string]struct{}{
	"battle.servers.register":  {},
	"battle.servers.heartbeat": {},
	"battle.servers.offline":   {},
	"battle.result.submit":     {},
}

func InitModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	handler := nakamaapi.New(nil)
	if db != nil {
		dbHandler, err := nakamaapi.NewWithDatabase(db)
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
				Service:      isServiceOriginRPC(ctx, rpcID),
				Payload:      decodedPayload(payload),
				PayloadError: payloadError(payload),
			})
			return encodeResponse(response)
		}); err != nil {
			return fmt.Errorf("register rpc %s: %w", rpcID, err)
		}
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
	mode := strings.ToLower(strings.TrimSpace(runtimeCtxString(ctx, runtime.RUNTIME_CTX_MODE)))
	return mode != "" && mode != "client"
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
