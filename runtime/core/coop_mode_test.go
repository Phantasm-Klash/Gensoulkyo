package core

import (
	"testing"
)

// 合作模式（coop_boss）大厅侧契约测试（COOP_MODE_CONTRACT.md §1）：
//   1. 房主建房可选合作人数 2/4（mode_params.coop_players），RequiredPlayers 跟随；
//   2. 非法人数（3/垃圾值）invalid_request；默认（缺省）= 2；
//   3. 满「房主选择人数」才成局（2p 房第 2 人成局、4p 房第 4 人成局，第 3 人还在等）；
//   4. guest 加入继承房间人数（JoinRoomRequest 无该字段），成局 match.mode_id = coop_boss；
//   5. 其他模式 RequiredPlayers 仍 = MinPlayers（回归现状）。
func TestCoopRoomRequiredPlayersAndFill(t *testing.T) {
	service := NewService(Config{})

	host, err := service.CreateRoom(mustLogin(t, service, "Coop Host").SessionToken, CreateRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "coop_host_deck",
		DeckSnapshot: validDeck("coop_host_deck"),
		ModeParams:   map[string]any{"coop_players": "4"},
	})
	if err != nil {
		t.Fatalf("create coop room: %v", err)
	}
	if host.RequiredPlayers != 4 {
		t.Fatalf("required players = %d, want 4", host.RequiredPlayers)
	}
	if host.CurrentPlayers != 1 {
		t.Fatalf("current players = %d, want 1", host.CurrentPlayers)
	}

	snapshot, err := service.Room(mustLogin(t, service, "Coop Peek").SessionToken, host.RoomCode)
	if err != nil {
		t.Fatalf("room snapshot: %v", err)
	}
	if snapshot.RequiredPlayers != 4 || snapshot.MaxPlayers != 4 || snapshot.CurrentPlayers != 1 {
		t.Fatalf("room snapshot = %d/%d/%d, want 4/4/1", snapshot.RequiredPlayers, snapshot.MaxPlayers, snapshot.CurrentPlayers)
	}

	// 第 2、3 人加入：4p 房不成局，RequiredPlayers 仍是 4。
	var third *QueueResponse
	for i := 2; i <= 3; i++ {
		guest, err := service.JoinRoom(mustLogin(t, service, "Coop Guest").SessionToken, host.RoomCode, JoinRoomRequest{
			ModeID:       "coop_boss",
			ActiveDeckID: "coop_guest_deck",
			DeckSnapshot: validDeck("coop_guest_deck"),
		})
		if err != nil {
			t.Fatalf("join #%d: %v", i, err)
		}
		if guest.MatchID != "" {
			t.Fatalf("match created too early at player %d", i)
		}
		if guest.RequiredPlayers != 4 {
			t.Fatalf("join #%d required = %d, want 4", i, guest.RequiredPlayers)
		}
		if i == 3 {
			third = guest
		}
	}

	// 第 4 人加入：成局，match.mode_id = coop_boss，roster = 4。
	fourth, err := service.JoinRoom(mustLogin(t, service, "Coop Fourth").SessionToken, host.RoomCode, JoinRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "coop_fourth_deck",
		DeckSnapshot: validDeck("coop_fourth_deck"),
	})
	if err != nil {
		t.Fatalf("join #4: %v", err)
	}
	if fourth.MatchID == "" {
		t.Fatalf("expected match at 4 players: %+v", fourth)
	}
	if fourth.RequiredPlayers != 4 {
		t.Fatalf("filled room required = %d, want 4", fourth.RequiredPlayers)
	}
	_ = third
	service.mu.Lock()
	match := service.matches[fourth.MatchID]
	service.mu.Unlock()
	if match == nil {
		t.Fatal("match missing after room filled")
	}
	if match.ModeID != "coop_boss" {
		t.Fatalf("match mode = %q, want coop_boss", match.ModeID)
	}
	if len(match.PlayerIDs) != 4 {
		t.Fatalf("match roster = %d, want 4", len(match.PlayerIDs))
	}
}

// 2 人房：第 2 人即成局（默认人数也是 2）。
func TestCoopRoomDefaultsToTwoPlayers(t *testing.T) {
	service := NewService(Config{})
	created, err := service.CreateRoom(mustLogin(t, service, "Coop2 Host").SessionToken, CreateRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "coop2_host_deck",
		DeckSnapshot: validDeck("coop2_host_deck"),
	})
	if err != nil {
		t.Fatalf("create coop room: %v", err)
	}
	if created.RequiredPlayers != 2 {
		t.Fatalf("default required = %d, want 2", created.RequiredPlayers)
	}
	joined, err := service.JoinRoom(mustLogin(t, service, "Coop2 Guest").SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "coop2_guest_deck",
		DeckSnapshot: validDeck("coop2_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if joined.MatchID == "" {
		t.Fatalf("2p coop room must fill at 2: %+v", joined)
	}
}

// 拒绝路径：coop_players=3 与垃圾值 → invalid_request；满员后再 join → 房间不可用。
func TestCoopRoomRejectsInvalidPlayerCount(t *testing.T) {
	service := NewService(Config{})
	for _, bad := range []any{"3", "abc", 5} {
		_, err := service.CreateRoom(mustLogin(t, service, "Coop Bad").SessionToken, CreateRoomRequest{
			ModeID:       "coop_boss",
			ActiveDeckID: "coop_bad_deck",
			DeckSnapshot: validDeck("coop_bad_deck"),
			ModeParams:   map[string]any{"coop_players": bad},
		})
		if err == nil || ErrorCode(err) != codeInvalidRequest {
			t.Fatalf("coop_players=%v must be invalid_request, got %v", bad, err)
		}
	}

	created, err := service.CreateRoom(mustLogin(t, service, "Coop Full Host").SessionToken, CreateRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "coop_full_host_deck",
		DeckSnapshot: validDeck("coop_full_host_deck"),
		ModeParams:   map[string]any{"coop_players": "2"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := service.JoinRoom(mustLogin(t, service, "Coop Full G1").SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "coop_full_g1_deck",
		DeckSnapshot: validDeck("coop_full_g1_deck"),
	}); err != nil {
		t.Fatalf("join #1: %v", err)
	}
	_, err = service.JoinRoom(mustLogin(t, service, "Coop Full G2").SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "coop_full_g2_deck",
		DeckSnapshot: validDeck("coop_full_g2_deck"),
	})
	if err == nil || ErrorCode(err) != codeRoomUnavailable {
		t.Fatalf("join into filled room must be room_unavailable, got %v", err)
	}
}

// 回归：非 coop 模式 RequiredPlayers = MinPlayers（mvp_boss_race = 2）。
func TestNonCoopModeRequiredPlayersUnchanged(t *testing.T) {
	service := NewService(Config{})
	created, err := service.CreateRoom(mustLogin(t, service, "Race Host").SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "race_host_deck",
		DeckSnapshot: validDeck("race_host_deck"),
	})
	if err != nil {
		t.Fatalf("create race room: %v", err)
	}
	if created.RequiredPlayers != 2 {
		t.Fatalf("race required = %d, want 2 (MinPlayers)", created.RequiredPlayers)
	}
}

// 队列路径：coop_players=4 的队首票决定成局人数。
func TestCoopQueueFillUsesSelectedCount(t *testing.T) {
	service := NewService(Config{})
	sessions := make([]*AuthSession, 4)
	for i := range sessions {
		sessions[i] = mustLogin(t, service, "Coop Q")
	}
	var last *QueueResponse
	for i, session := range sessions {
		resp, err := service.JoinQueue(session.SessionToken, JoinQueueRequest{
			ModeID:       "coop_boss",
			ActiveDeckID: "coop_q_deck",
			DeckSnapshot: validDeck("coop_q_deck"),
			ModeParams:   map[string]any{"coop_players": "4"},
		})
		if err != nil {
			t.Fatalf("join queue #%d: %v", i+1, err)
		}
		if i < 3 && resp.MatchID != "" {
			t.Fatalf("queue filled too early at #%d", i+1)
		}
		last = resp
	}
	if last == nil || last.MatchID == "" {
		t.Fatalf("4-player coop queue must fill at 4: %+v", last)
	}
	if last.RequiredPlayers != 4 {
		t.Fatalf("filled queue required = %d, want 4", last.RequiredPlayers)
	}
}
