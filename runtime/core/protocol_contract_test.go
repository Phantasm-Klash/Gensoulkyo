package core

import (
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
