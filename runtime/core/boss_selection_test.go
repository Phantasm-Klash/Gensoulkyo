package core

import (
	"encoding/json"
	"testing"
)

// A1/A2：房间 boss 选择 + 每玩家自机 loadout（线程 A「游戏内容」）。
//
// 本文件锁定三件事：
//   1. Go 侧 boss 权威目录数值与 C++ 侧
//      PhK-BattleServer/src/danmaku/boss_catalog.cpp 逐字段一致（跨端一致性）；
//   2. 建房 boss_id 仅房主可设、非法值 invalid_request、guest 加入不可改，且
//      boss 的 HP 从目录灌入 match；
//   3. signed ticket 覆盖新字段 boss_id / character_id / shot_type / bomb_type，
//      篡改任一字段即破签。

// TestBossCatalogMatchesPhKBattleServer 钉住 Go 目录数值。
//
// 对照来源：PhK-BattleServer/src/danmaku/boss_catalog.cpp（三引擎唯一权威）。
// 若此测试与 C++ 不一致，说明两端口径漂移，必须先对齐再改。
func TestBossCatalogMatchesPhKBattleServer(t *testing.T) {
	entries := BossCatalogEntries()
	if len(entries) != 2 {
		t.Fatalf("boss catalog size = %d, want 2 (tian, hui)", len(entries))
	}

	tian := FindBossCatalogEntry("tian")
	if tian == nil {
		t.Fatal("tian missing from boss catalog")
	}
	if tian.Display != "天" || tian.BossMaxHP != 20000 {
		t.Fatalf("tian display/hp = %q/%d, want 天/20000", tian.Display, tian.BossMaxHP)
	}
	if tian.Movement.Kind != BossMovementSwayX || tian.Movement.AmplitudeMilli != 60000 ||
		tian.Movement.PeriodTicks != 600 || tian.Movement.HalfTicks != 300 {
		t.Fatalf("tian movement = %+v, want sway_x/60000/600/300", tian.Movement)
	}
	wantTianPhases := []BossPatternPhase{
		{PatternID: "kome_ring", Waves: 4},
		{PatternID: "arrow_aimed_fan", Waves: 3},
		{PatternID: "ring_spiral", Waves: 4},
		{PatternID: "kome_homing", Waves: 3},
		{PatternID: "chou_four_dir", Waves: 4},
	}
	assertPhases(t, "tian", tian.Phases, wantTianPhases)

	hui := FindBossCatalogEntry("hui")
	if hui == nil {
		t.Fatal("hui missing from boss catalog")
	}
	if hui.Display != "辉" || hui.BossMaxHP != 24000 {
		t.Fatalf("hui display/hp = %q/%d, want 辉/24000", hui.Display, hui.BossMaxHP)
	}
	if hui.Movement.Kind != BossMovementBobY || hui.Movement.AmplitudeMilli != 30000 ||
		hui.Movement.PeriodTicks != 480 || hui.Movement.HalfTicks != 240 {
		t.Fatalf("hui movement = %+v, want bob_y/30000/480/240", hui.Movement)
	}
	wantHuiPhases := []BossPatternPhase{
		{PatternID: "kusabi_gap_ring", Waves: 3},
		{PatternID: "sizuku_sine_radius", Waves: 4},
		{PatternID: "kunai_bounce", Waves: 3},
		{PatternID: "heart_sine_speed", Waves: 4},
		{PatternID: "stama_mirror", Waves: 3},
	}
	assertPhases(t, "hui", hui.Phases, wantHuiPhases)

	if !IsKnownBossID("") || !IsKnownBossID("tian") || IsKnownBossID("bogus") {
		t.Fatalf("IsKnownBossID must accept empty/tian and reject bogus")
	}
}

func assertPhases(t *testing.T, bossID string, got, want []BossPatternPhase) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s phase count = %d, want %d (%+v)", bossID, len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s phase[%d] = %+v, want %+v", bossID, i, got[i], want[i])
		}
	}
}

func TestCreateRoomRejectsUnknownBoss(t *testing.T) {
	service := NewService(Config{})
	host := mustLogin(t, service, "Boss Reject Host")

	_, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "host_deck",
		DeckSnapshot: validDeck("host_deck"),
		BossID:       "bogus",
	})
	if err == nil || ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("unknown boss must be invalid_request, got %v", err)
	}
}

// TestRoomBossIsHostOnlyAndFlowsToMatch：房主设 boss，guest 加入继承且改不了，
// boss HP 从目录灌入 match。
func TestRoomBossIsHostOnlyAndFlowsToMatch(t *testing.T) {
	service := NewService(Config{})
	host := mustLogin(t, service, "Boss Host")
	guest := mustLogin(t, service, "Boss Guest")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "boss_host_deck",
		DeckSnapshot: validDeck("boss_host_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze", "character_id": "wide"},
		BossID:       "tian",
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if created.BossID != "tian" {
		t.Fatalf("create response boss_id = %q, want tian", created.BossID)
	}

	snapshot, err := service.Room(guest.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("room snapshot: %v", err)
	}
	if snapshot.BossID != "tian" {
		t.Fatalf("room snapshot boss_id = %q, want tian", snapshot.BossID)
	}

	// guest 加入：JoinRoomRequest 无 boss 字段，房间 boss 不变。
	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "boss_guest_deck",
		DeckSnapshot: validDeck("boss_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	if joined.BossID != "tian" {
		t.Fatalf("guest join boss_id = %q, want inherited tian", joined.BossID)
	}
	if joined.MatchID == "" {
		t.Fatalf("expected match after second player joined: %+v", joined)
	}

	service.mu.Lock()
	match := service.matches[joined.MatchID]
	service.mu.Unlock()
	if match == nil {
		t.Fatal("match missing after room filled")
	}
	if match.BossID != "tian" {
		t.Fatalf("match boss_id = %q, want tian", match.BossID)
	}
	if match.BossMaxHP != 20000 || match.BossHP != 20000 {
		t.Fatalf("match boss hp = %d/%d, want 20000/20000", match.BossHP, match.BossMaxHP)
	}
	snapshot, err = service.Room(guest.SessionToken, created.RoomCode)
	if err != nil {
		t.Fatalf("room snapshot after join: %v", err)
	}
	if snapshot.BossID != "tian" {
		t.Fatalf("room boss changed by guest join: %q", snapshot.BossID)
	}
}

// TestDefaultRoomHasEmptyBoss：不设 boss 时 boss_id 为空（复现现状）。
func TestDefaultRoomHasEmptyBoss(t *testing.T) {
	service := NewService(Config{})
	host := mustLogin(t, service, "Default Boss Host")
	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "default_host_deck",
		DeckSnapshot: validDeck("default_host_deck"),
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	if created.BossID != "" {
		t.Fatalf("default room boss_id = %q, want empty", created.BossID)
	}
	payload, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("marshal queue response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal queue response: %v", err)
	}
	if _, present := decoded["boss_id"]; present {
		t.Fatalf("empty boss_id must be omitted from the wire (got %v)", decoded["boss_id"])
	}
}

// TestValidateLoadoutShotAndBombWhitelist：自机选择白名单；缺省留空复现现状。
func TestValidateLoadoutShotAndBombWhitelist(t *testing.T) {
	loadout, err := validateLoadout("mvp_boss_race", map[string]any{
		"stage_id":     "lunar_maze",
		"character_id": "wide",
		"shot_type":    "MarisaA1",
		"bomb_type":    "Marisa",
	}, CertificationProfile{})
	if err != nil {
		t.Fatalf("valid shot/bomb rejected: %v", err)
	}
	if loadout.ShotType != "MarisaA1" || loadout.BombType != "Marisa" {
		t.Fatalf("loadout shot/bomb = %q/%q, want MarisaA1/Marisa", loadout.ShotType, loadout.BombType)
	}

	// 缺省：留空且可序列化省略（缺省必须复现现状）。
	defaulted, err := validateLoadout("mvp_boss_race", map[string]any{"stage_id": "lunar_maze"}, CertificationProfile{})
	if err != nil {
		t.Fatalf("default loadout rejected: %v", err)
	}
	if defaulted.ShotType != "" || defaulted.BombType != "" {
		t.Fatalf("default shot/bomb = %q/%q, want empty", defaulted.ShotType, defaulted.BombType)
	}

	if _, err := validateLoadout("mvp_boss_race", map[string]any{"shot_type": "BogusA9"}, CertificationProfile{}); err == nil || ErrorCode(err) != codeInvalidMode {
		t.Fatalf("unknown shot must be invalid_mode, got %v", err)
	}
	if _, err := validateLoadout("mvp_boss_race", map[string]any{"bomb_type": "Bogus"}, CertificationProfile{}); err == nil || ErrorCode(err) != codeInvalidMode {
		t.Fatalf("unknown bomb must be invalid_mode, got %v", err)
	}
	// 52 条 shot + 13 条 bomb 全部命中白名单。
	if len(allowedShotTypes) != 52 {
		t.Fatalf("allowedShotTypes size = %d, want 52 (PlayerShotTable)", len(allowedShotTypes))
	}
	if len(allowedBombTypes) != 13 {
		t.Fatalf("allowedBombTypes size = %d, want 13 (PlayerBombTable)", len(allowedBombTypes))
	}
}

// TestBattleTicketCoversBossAndLoadoutBySignature：signed ticket 覆盖新字段。
func TestBattleTicketCoversBossAndLoadoutBySignature(t *testing.T) {
	service := NewService(Config{})
	host := mustLogin(t, service, "Ticket Boss Host")
	guest := mustLogin(t, service, "Ticket Boss Guest")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "ticket_host_deck",
		DeckSnapshot: validDeck("ticket_host_deck"),
		ModeParams: map[string]any{
			"stage_id": "lunar_maze", "character_id": "precision",
			"shot_type": "ReimuB1", "bomb_type": "Reimu",
		},
		BossID: "hui",
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "ticket_guest_deck",
		DeckSnapshot: validDeck("ticket_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	if joined.MatchID == "" {
		t.Fatal("expected match id")
	}

	ticket, err := service.BattleTicket(host.SessionToken, joined.MatchID)
	if err != nil {
		t.Fatalf("battle ticket: %v", err)
	}
	if ticket.Ticket.BossID != "hui" {
		t.Fatalf("ticket boss_id = %q, want hui", ticket.Ticket.BossID)
	}
	if ticket.Ticket.CharacterID != "precision" || ticket.Ticket.ShotType != "ReimuB1" || ticket.Ticket.BombType != "Reimu" {
		t.Fatalf("ticket loadout = %q/%q/%q, want precision/ReimuB1/Reimu",
			ticket.Ticket.CharacterID, ticket.Ticket.ShotType, ticket.Ticket.BombType)
	}
	if !verifySignedBattleTicket(t, ticket) {
		t.Fatal("issued ticket signature must verify")
	}

	// 篡改任一新字段必须破签。
	tampered := *ticket
	tampered.Ticket.BossID = "tian"
	if verifySignedBattleTicket(t, &tampered) {
		t.Fatal("boss_id tampering must invalidate the ticket signature")
	}
	tampered = *ticket
	tampered.Ticket.ShotType = "MarisaB2"
	if verifySignedBattleTicket(t, &tampered) {
		t.Fatal("shot_type tampering must invalidate the ticket signature")
	}
	tampered = *ticket
	tampered.Ticket.BombType = "Marisa"
	if verifySignedBattleTicket(t, &tampered) {
		t.Fatal("bomb_type tampering must invalidate the ticket signature")
	}
	tampered = *ticket
	tampered.Ticket.CharacterID = "wide"
	if verifySignedBattleTicket(t, &tampered) {
		t.Fatal("character_id tampering must invalidate the ticket signature")
	}
}

// TestBattleAgentAssignmentCarriesBossAndLoadouts：agent 视图带 boss + 每玩家 loadout。
func TestBattleAgentAssignmentCarriesBossAndLoadouts(t *testing.T) {
	service := NewService(Config{})
	host := mustLogin(t, service, "Agent Boss Host")
	guest := mustLogin(t, service, "Agent Boss Guest")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "agent_host_deck",
		DeckSnapshot: validDeck("agent_host_deck"),
		ModeParams:   map[string]any{"stage_id": "lunar_maze", "character_id": "wide", "shot_type": "SakuyaA1", "bomb_type": "Sakuya"},
		BossID:       "tian",
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "agent_guest_deck",
		DeckSnapshot: validDeck("agent_guest_deck"),
		ModeParams:   map[string]any{"shot_type": "YoumuA1", "bomb_type": "Youmu"},
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	if joined.BattleAllocation == nil {
		t.Fatalf("join response missing battle allocation: %+v", joined)
	}

	resp, err := service.BattleAgentAssignments(joined.BattleAllocation.BattleServerID)
	if err != nil {
		t.Fatalf("assignments: %v", err)
	}
	var assignment *BattleAgentAssignment
	for i := range resp.Assignments {
		if resp.Assignments[i].MatchID == joined.MatchID {
			assignment = &resp.Assignments[i]
		}
	}
	if assignment == nil {
		t.Fatalf("assignment for match %q missing: %+v", joined.MatchID, resp.Assignments)
	}
	if assignment.BossID != "tian" {
		t.Fatalf("assignment boss_id = %q, want tian", assignment.BossID)
	}
	if len(assignment.Loadouts) != 2 {
		t.Fatalf("assignment loadouts = %d, want 2 (%+v)", len(assignment.Loadouts), assignment.Loadouts)
	}
	byID := map[string]BattleAgentLoadout{}
	for _, loadout := range assignment.Loadouts {
		byID[loadout.PlayerID] = loadout
	}
	hostPlayerID := playerIDForUser(joined.MatchID, host.UserID)
	guestPlayerID := playerIDForUser(joined.MatchID, guest.UserID)
	if got := byID[hostPlayerID]; got.CharacterID != "wide" || got.ShotType != "SakuyaA1" || got.BombType != "Sakuya" {
		t.Fatalf("host loadout = %+v, want wide/SakuyaA1/Sakuya", got)
	}
	if got := byID[guestPlayerID]; got.ShotType != "YoumuA1" || got.BombType != "Youmu" {
		t.Fatalf("guest loadout = %+v, want YoumuA1/Youmu", got)
	}
}
