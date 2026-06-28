package core

import (
	"encoding/json"
	"testing"

	phkv1 "github.com/phantasm-klash/phk-protocol/gen/go/phk/v1"
)

func TestCoreVersionConstantsFollowPhKProtocolManifest(t *testing.T) {
	if ProtocolVersion != phkv1.ProtocolVersion {
		t.Fatalf("protocol version drift: core=%d manifest=%d", ProtocolVersion, phkv1.ProtocolVersion)
	}
	if BusinessAPIVersion != phkv1.BusinessAPIVersion {
		t.Fatalf("business api version drift: core=%s manifest=%s", BusinessAPIVersion, phkv1.BusinessAPIVersion)
	}
	if BattleAPIVersion != phkv1.BattleAPIVersion {
		t.Fatalf("battle api version drift: core=%s manifest=%s", BattleAPIVersion, phkv1.BattleAPIVersion)
	}
	if RulesetVersion != phkv1.RulesetVersion {
		t.Fatalf("ruleset version drift: core=%s manifest=%s", RulesetVersion, phkv1.RulesetVersion)
	}
}

func TestCoreDependsOnRequiredProtocolFields(t *testing.T) {
	required := map[string][]string{
		"BattleTicket":           {"match_id", "user_id", "player_id", "battle_server_id", "endpoint", "deck_snapshot_hash", "ruleset_version", "expires_at_ms", "business_session_id"},
		"SignedBattleTicket":     {"ticket", "signature_alg", "key_id", "signature"},
		"BattleResult":           {"version", "match_id", "mode_id", "result_hash", "replay_id", "player_ids", "settled_at_ms"},
		"BattleModeAction":       {"version", "match_id", "player_id", "tick", "seq", "action_id", "action_type", "payload_json", "client_result_authoritative"},
		"SignedBattleResult":     {"result", "signature_alg", "key_id", "signature"},
		"BattleServerAllocation": {"match_id", "mode_id", "battle_server_id", "endpoint", "players", "server_seed", "mode_config_hash", "allocated_at_ms"},
		"BusinessSecureEnvelope": {"version", "session_id", "seq", "timestamp_ms", "nonce", "op_code", "key_id", "auth_tag"},
	}
	for messageName, fields := range required {
		for _, field := range fields {
			if !phkv1.HasMessageField(messageName, field) {
				t.Fatalf("protocol manifest missing %s.%s", messageName, field)
			}
		}
	}
}

func TestBattleModeActionFixtureContract(t *testing.T) {
	action := struct {
		Version                   int    `json:"version"`
		MatchID                   string `json:"match_id"`
		PlayerID                  string `json:"player_id"`
		Tick                      int    `json:"tick"`
		Seq                       int    `json:"seq"`
		ActionID                  string `json:"action_id"`
		ActionType                string `json:"action_type"`
		PayloadJSON               string `json:"payload_json"`
		ClientResultAuthoritative bool   `json:"client_result_authoritative"`
	}{
		Version:                   phkv1.ProtocolVersion,
		MatchID:                   phkv1.BattleModeActionMatchID,
		PlayerID:                  phkv1.BattleModeActionPlayerID,
		Tick:                      phkv1.BattleModeActionTick,
		Seq:                       phkv1.BattleModeActionSeq,
		ActionID:                  phkv1.BattleModeActionActionID,
		ActionType:                phkv1.BattleModeActionActionType,
		PayloadJSON:               phkv1.BattleModeActionPayloadJSON,
		ClientResultAuthoritative: false,
	}
	if action.MatchID == "" || action.PlayerID == "" || action.ActionID == "" || action.ActionType == "" {
		t.Fatalf("battle mode action fixture missing identity fields: %+v", action)
	}
	if action.Tick <= 0 || action.Seq <= 0 {
		t.Fatalf("battle mode action fixture must carry positive tick/seq: %+v", action)
	}
	if action.ClientResultAuthoritative {
		t.Fatalf("battle mode action fixture must not allow client-authored results: %+v", action)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(action.PayloadJSON), &payload); err != nil {
		t.Fatalf("battle mode action payload_json must be parseable JSON: %v", err)
	}
	if payload["card_id"] == "" || payload["round_index"] == nil {
		t.Fatalf("battle mode action payload missing expected mode-action fields: %+v", payload)
	}
	encoded, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("marshal battle mode action fixture: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("round-trip battle mode action fixture: %v", err)
	}
	if got, ok := roundTrip["client_result_authoritative"].(bool); !ok || got {
		t.Fatalf("wire action must include client_result_authoritative=false, got %#v", roundTrip["client_result_authoritative"])
	}
	payloadJSON, ok := roundTrip["payload_json"].(string)
	if !ok {
		t.Fatalf("wire action payload_json must be a JSON string field, got %#v", roundTrip["payload_json"])
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("wire action payload_json string must parse as JSON: %v", err)
	}
}
