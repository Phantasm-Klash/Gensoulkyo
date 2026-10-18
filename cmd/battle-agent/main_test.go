package main

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gensoulkyo/runtime/core"
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

// 结算只能有一条路径。agent 必须在 spawn 参数里关掉对局服的 result POST，
// 否则两条路径竞争：C++ 先到会写 replay_ 前缀的原始 hash，agent 的签名
// submit 后到直接 409，严格路径永远不结算。
func TestSpawnArgsDisableResultSubmit(t *testing.T) {
	a := &agent{cfg: config{
		LobbyEndpoint:   "lobby.example:7350",
		BattleServerID:  "bs-104-1",
		DefaultBossHP:   1200,
		DefaultMaxTicks: 7200,
	}}
	args := a.spawnArgs(assignment{
		MatchID:       "match_probe",
		Ruleset:       "mvp-boss-race-s0",
		ServerSeedHex: "0a0b",
		PlayerIDs:     []string{"p-a", "p-b"},
	}, 7401)

	count := 0
	for _, arg := range args {
		if arg == "--no-result-submit" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("spawn args contain --no-result-submit %d times, want 1: %v", count, args)
	}
	if !strings.Contains(strings.Join(args, " "), "--lobby lobby.example:7350") {
		t.Fatalf("spawn args lost the lobby endpoint: %v", args)
	}
}

// 把 agent 真实产出的 signed_result 信封喂进大厅的 SubmitBattleResult：
// 只要 shape / 版本 / hash 任何一项不合格，就会在 "match not found" 之前
// 被拒。未知 match 是预期结果——它证明签名与版本已经通过大厅的校验门。
func TestAgentSignedPayloadPassesLobbyShapeGate(t *testing.T) {
	raw := rawResult{
		MatchID:        "match_probe",
		ModeID:         "mvp_boss_race",
		RulesetVersion: "ruleset-local-s0",
		MatchSeed:      42,
		WinnerPlayerID: "p-a",
		WinnerTick:     10,
		StateHash:      "sha256:" + strings.Repeat("a", 64),
	}
	body := signedResultPayload(
		assignment{MatchID: "match_probe", ModeID: "mvp_boss_race", PlayerIDs: []string{"p-a", "p-b"}},
		raw,
		"bs-104-1",
		time.Now().UTC().UnixMilli(),
	)

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal signed result: %v", err)
	}
	var req core.BattleResultSubmitRequest
	if err := json.Unmarshal(encoded, &req); err != nil {
		t.Fatalf("lobby cannot decode the agent payload: %v", err)
	}

	service := core.NewService(core.Config{})
	if _, err := service.SubmitBattleResult(req); err == nil {
		t.Fatal("expected match-not-found for an unknown match")
	} else if strings.Contains(err.Error(), "signature shape is invalid") {
		t.Fatalf("lobby rejected the agent signature shape: %v", err)
	} else if !strings.Contains(err.Error(), "match not found") {
		t.Fatalf("agent payload did not clear the lobby gates: %v", err)
	}
}
