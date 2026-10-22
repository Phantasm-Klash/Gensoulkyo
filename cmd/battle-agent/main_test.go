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

// assignment 带 mode_id 时必须原样传给对局服（--mode）。缺了它，合作房会被
// 按默认 mvp_boss_race spawn：4 人局 add_player_failed 秒退（agent 重试 3 次
// 放弃），2 人局跑成 race——机制时间表、胜负判定、快照 payload 类型全不对。
// mode_id 为空时不加 flag，保持对局服侧默认值（兼容历史 assignment）。
func TestSpawnArgsPassesModeID(t *testing.T) {
	a := &agent{cfg: config{
		LobbyEndpoint:   "lobby.example:7350",
		BattleServerID:  "bs-104-1",
		DefaultBossHP:   1200,
		DefaultMaxTicks: 7200,
	}}
	args := a.spawnArgs(assignment{
		MatchID:       "match_coop",
		ModeID:        "coop_boss",
		Ruleset:       "coop-boss-s0",
		ServerSeedHex: "0a0b",
		PlayerIDs:     []string{"p-a", "p-b"},
	}, 7401)

	if !strings.Contains(strings.Join(args, " "), "--mode coop_boss") {
		t.Fatalf("spawn args lost the assignment mode_id: %v", args)
	}

	blank := a.spawnArgs(assignment{
		MatchID:       "match_race",
		Ruleset:       "mvp-boss-race-s0",
		ServerSeedHex: "0a0b",
		PlayerIDs:     []string{"p-a", "p-b"},
	}, 7402)
	for _, arg := range blank {
		if arg == "--mode" {
			t.Fatalf("spawn args carry --mode without an assignment mode_id: %v", blank)
		}
	}
}

// 把 agent 真实产出的 signed_result 信封喂进大厅的 SubmitBattleResult：
// 只要 shape / 版本 / hash / 禁止字段任何一项不合格，就会在 "match not found"
// 之前被拒。未知 match 是预期结果——它证明签名与版本已经通过大厅的校验门。
//
// players 必须带 damage_dealt：真实 RESULT 负载带着它，而大厅
// firstForbiddenBattleResultProjectionField 曾因 mode projection 含该字段把
// 整个提交 403 拒掉（对局不结算、客户端不出结算画面）。这条用例就是那次
// 回归的复现：projection 不过滤 damage_dealt 就会在这里 forbidden_field 失败。
func TestAgentSignedPayloadPassesLobbyShapeGate(t *testing.T) {
	raw := rawResult{
		MatchID:        "match_probe",
		ModeID:         "mvp_boss_race",
		RulesetVersion: "ruleset-local-s0",
		MatchSeed:      42,
		WinnerPlayerID: "p-a",
		WinnerTick:     10,
		StateHash:      "sha256:" + strings.Repeat("a", 64),
		Players: []struct {
			PlayerID      string `json:"player_id"`
			DamageDealt   int64  `json:"damage_dealt"`
			BossCurrentHP int64  `json:"boss_current_hp"`
		}{
			{PlayerID: "p-a", DamageDealt: 300, BossCurrentHP: 0},
			{PlayerID: "p-b", DamageDealt: 290, BossCurrentHP: 10},
		},
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
	} else if strings.Contains(err.Error(), "forbidden_field") {
		t.Fatalf("mode projection carries a forbidden field: %v", err)
	} else if !strings.Contains(err.Error(), "match not found") {
		t.Fatalf("agent payload did not clear the lobby gates: %v", err)
	}
}

// mode projection 只留识别字段：damage_dealt 是大厅的结算权威字段，出现在
// mode_result_json 里会被 403 forbidden_field 拒。过滤后 winner/seed/state_hash
// 与每玩家的 player_id/boss_current_hp 必须原样保留，result_hash 也不能变
// （它只覆盖 match/mode/winner/tick/state_hash，与 projection 无关）。
func TestModeProjectionDropsForbiddenDamageField(t *testing.T) {
	raw := rawResult{
		MatchID:        "match_probe",
		ModeID:         "mvp_boss_race",
		RulesetVersion: "ruleset-local-s0",
		MatchSeed:      42,
		WinnerPlayerID: "p-a",
		WinnerTick:     10,
		StateHash:      "sha256:" + strings.Repeat("a", 64),
		Players: []struct {
			PlayerID      string `json:"player_id"`
			DamageDealt   int64  `json:"damage_dealt"`
			BossCurrentHP int64  `json:"boss_current_hp"`
		}{
			{PlayerID: "p-a", DamageDealt: 300, BossCurrentHP: 0},
			{PlayerID: "p-b", DamageDealt: 290, BossCurrentHP: 10},
		},
	}
	body := signedResultPayload(
		assignment{MatchID: "match_probe", ModeID: "mvp_boss_race", PlayerIDs: []string{"p-a", "p-b"}},
		raw,
		"bs-104-1",
		time.Now().UTC().UnixMilli(),
	)

	result, ok := body["signed_result"].(map[string]any)["result"].(map[string]any)
	if !ok {
		t.Fatalf("signed result envelope shape changed: %+v", body)
	}
	if got := result["result_hash"]; got != resultHashOf(raw) {
		t.Fatalf("result_hash = %v, want %v (hash must stay independent of the projection)", got, resultHashOf(raw))
	}

	var projection struct {
		WinnerPlayerID string `json:"winner_player_id"`
		WinnerTick     int64  `json:"winner_tick"`
		MatchSeed      int64  `json:"match_seed"`
		StateHash      string `json:"state_hash"`
		Players        []struct {
			PlayerID      string `json:"player_id"`
			BossCurrentHP int64  `json:"boss_current_hp"`
			DamageDealt   *int64 `json:"damage_dealt"`
		} `json:"players"`
	}
	if err := json.Unmarshal([]byte(result["mode_result_json"].(string)), &projection); err != nil {
		t.Fatalf("decode mode projection: %v", err)
	}
	if projection.WinnerPlayerID != raw.WinnerPlayerID || projection.WinnerTick != raw.WinnerTick ||
		projection.MatchSeed != raw.MatchSeed || projection.StateHash != raw.StateHash {
		t.Fatalf("projection lost authoritative match fields: %+v", projection)
	}
	if len(projection.Players) != len(raw.Players) {
		t.Fatalf("projection players = %d, want %d", len(projection.Players), len(raw.Players))
	}
	for index, player := range projection.Players {
		if player.DamageDealt != nil {
			t.Fatalf("projection player %d still carries damage_dealt=%d (lobby 403)", index, *player.DamageDealt)
		}
		if player.PlayerID != raw.Players[index].PlayerID || player.BossCurrentHP != raw.Players[index].BossCurrentHP {
			t.Fatalf("projection player %d = %+v, want id %q boss_current_hp %d", index, player, raw.Players[index].PlayerID, raw.Players[index].BossCurrentHP)
		}
	}
}

// TestSpawnArgsPrefersTheRoomMatchLength: a room that picked a length wins over
// the host default, and a room that did not keeps the host backstop -- both
// matter, because the second is the only thing that kills an abandoned process.
func TestSpawnArgsPrefersTheRoomMatchLength(t *testing.T) {
	a := &agent{cfg: config{
		LobbyEndpoint:   "lobby.example:7350",
		BattleServerID:  "bs-104-1",
		DefaultMaxTicks: 7200,
	}}
	withRoom := a.spawnArgs(assignment{
		MatchID:       "match_short",
		ModeID:        "mvp_boss_race",
		Ruleset:       "ruleset-local-s0",
		ServerSeedHex: "0a0b",
		PlayerIDs:     []string{"p-a"},
		MaxTicks:      1800,
	}, 7402)
	if !strings.Contains(strings.Join(withRoom, " "), "--max-ticks 1800") {
		t.Fatalf("spawn args ignored the room match length: %v", withRoom)
	}

	withoutRoom := a.spawnArgs(assignment{
		MatchID:       "match_default",
		ModeID:        "mvp_boss_race",
		Ruleset:       "ruleset-local-s0",
		ServerSeedHex: "0a0b",
		PlayerIDs:     []string{"p-a"},
	}, 7403)
	if !strings.Contains(strings.Join(withoutRoom, " "), "--max-ticks 7200") {
		t.Fatalf("spawn args lost the host backstop: %v", withoutRoom)
	}
}

// TestSpawnArgsPrefersTheRoomBossHealth: the room's choice wins over the host
// default, and a silent room keeps the host's value -- which is the only thing
// that gives a boss a defined size when nobody asked for one.
func TestSpawnArgsPrefersTheRoomBossHealth(t *testing.T) {
	a := &agent{cfg: config{
		LobbyEndpoint:  "lobby.example:7350",
		BattleServerID: "bs-104-1",
		DefaultBossHP:  1200,
	}}
	withRoom := a.spawnArgs(assignment{
		MatchID:       "match_boss",
		ModeID:        "mvp_boss_race",
		Ruleset:       "ruleset-local-s0",
		ServerSeedHex: "0a0b",
		PlayerIDs:     []string{"p-a"},
		BossHP:        12000,
	}, 7404)
	if !strings.Contains(strings.Join(withRoom, " "), "--boss-hp 12000") {
		t.Fatalf("spawn args ignored the room boss health: %v", withRoom)
	}

	withoutRoom := a.spawnArgs(assignment{
		MatchID:       "match_default_boss",
		ModeID:        "mvp_boss_race",
		Ruleset:       "ruleset-local-s0",
		ServerSeedHex: "0a0b",
		PlayerIDs:     []string{"p-a"},
	}, 7405)
	if !strings.Contains(strings.Join(withoutRoom, " "), "--boss-hp 1200") {
		t.Fatalf("spawn args lost the host default boss health: %v", withoutRoom)
	}
}
