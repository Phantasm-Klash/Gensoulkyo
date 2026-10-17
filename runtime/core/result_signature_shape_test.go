package core

import (
	"strings"
	"testing"
	"time"
)

// validateSignedBattleResultShape is the gate that rejected a real match with
// "battle result signature shape is invalid". The C++ battle server emits 128
// hex chars (src/result.cpp DevBattleResultSignatureHex; probe output pinned
// below), and the Go battle agent must emit the same shape. Keep the correct
// shape accepted and keep 64 hex chars rejected, so the producer cannot drift
// back and the check cannot be weakened to compensate.
func TestValidateSignedBattleResultShapeSignatureShape(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	signedWith := func(signatureHex string) SignedBattleResult {
		return SignedBattleResult{
			Result: BattleResult{
				Version:     currentVersionStamp(),
				MatchID:     "match_shape",
				ModeID:      "mvp_boss_race",
				ResultHash:  "sha256:" + strings.Repeat("0", 64),
				ReplayID:    "replay-match_shape",
				PlayerIDs:   []string{"p-a", "p-b"},
				SettledAtMS: now.UnixMilli(),
			},
			SignatureAlg:        "ED25519",
			KeyID:               "bs-104-1",
			SignatureHex:        signatureHex,
			ServerAuthoritative: true,
		}
	}

	// Produced by the C++ code path for a probe result (64 bytes / 128 chars).
	const cppDevSignature = "c917cb283663c005aa1d041f2b7475e4070d593a4c425447e812923141530a264d2caf040aa697812e31e7faffb74d608b223d1620852bc36c27760d1595e1a2"
	if len(cppDevSignature) != 128 {
		t.Fatalf("fixture is %d chars, want 128", len(cppDevSignature))
	}
	if err := validateSignedBattleResultShape(signedWith(cppDevSignature), now); err != nil {
		t.Fatalf("C++-shaped signature rejected: %v", err)
	}

	if err := validateSignedBattleResultShape(signedWith(strings.Repeat("a5", 64)), now); err != nil {
		t.Fatalf("128 hex chars rejected: %v", err)
	}

	// The pre-fix agent signature was 32 bytes / 64 chars and must stay invalid.
	err := validateSignedBattleResultShape(signedWith(strings.Repeat("a5", 32)), now)
	if err == nil {
		t.Fatal("64 hex chars must stay rejected")
	}
	if !strings.Contains(err.Error(), "signature shape is invalid") {
		t.Fatalf("unexpected error: %v", err)
	}
}
