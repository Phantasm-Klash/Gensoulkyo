package main

import (
	"encoding/hex"
	"testing"
)

// The lobby's validateSignedBattleResultShape (runtime/core/service.go) rejects
// any signature that is not exactly 128 hex characters. The agent is the
// producer on the wire, so its dev signature must have that shape or every
// real match ends in "battle result signature shape is invalid" and never
// settles.
func TestDevSignatureMatchesLobbyShape(t *testing.T) {
	result := map[string]any{
		"match_id":   "match_probe",
		"mode_id":    "mvp_boss_race",
		"replay_id":  "replay-match_probe",
		"player_ids": []string{"p-a", "p-b"},
	}
	sig := devSignature(result, "bs-104-1")
	if len(sig) != 128 {
		t.Fatalf("signature length = %d, want 128 hex chars: %q", len(sig), sig)
	}
	if _, err := hex.DecodeString(sig); err != nil {
		t.Fatalf("signature is not hex: %v", err)
	}
	if sig != devSignature(result, "bs-104-1") {
		t.Fatal("signature is not deterministic")
	}
	if sig == devSignature(result, "bs-104-2") {
		t.Fatal("signature does not bind the key id")
	}
}

func TestResultHashCarriesSha256Prefix(t *testing.T) {
	raw := rawResult{MatchID: "match_probe", ModeID: "mvp_boss_race", WinnerPlayerID: "p-a", WinnerTick: 10, StateHash: "abc"}
	hash := resultHashOf(raw)
	if len(hash) != len("sha256:")+64 {
		t.Fatalf("result hash length = %d, want 71: %q", len(hash), hash)
	}
	if hash[:7] != "sha256:" {
		t.Fatalf("result hash = %q, want sha256: prefix", hash)
	}
	if _, err := hex.DecodeString(hash[7:]); err != nil {
		t.Fatalf("result hash digest is not hex: %v", err)
	}
}
