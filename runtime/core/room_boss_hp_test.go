package core

import (
	"testing"
)

// TestBossHPOptionReachesTheAllocation walks the option from the room to the
// allocation, the same way the match-length option is tested. It matters more
// here: boss health changes what the simulation computes, not just when the
// process stops, so a break in the chain produces a match that is silently a
// different fight from the one the room advertised.
func TestBossHPOptionReachesTheAllocation(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	alice := mustLogin(t, service, "HP Alice")
	bob := mustLogin(t, service, "HP Bob")

	created, err := service.CreateRoom(alice.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "hp_deck",
		DeckSnapshot: validDeck("hp_deck"),
		ModeParams:   map[string]any{"boss_id": "hui", "boss_health": 12000},
	})
	if err != nil {
		t.Fatalf("create room with boss_hp: %v", err)
	}
	joined, err := service.JoinRoom(bob.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "hp_guest_deck",
		DeckSnapshot: validDeck("hp_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	allocation, ok := service.BattleAllocationForMatch(joined.MatchID)
	if !ok {
		t.Fatalf("no allocation for match %s", joined.MatchID)
	}
	if allocation.BossHP != 12000 {
		t.Fatalf("allocation boss hp = %d, want 12000", allocation.BossHP)
	}
}

// TestBossHPOverridesTheCatalog: the catalog entry is a default for rooms that
// did not choose; a room that chose both a boss and a health gets the health it
// asked for.
func TestBossHPOverridesTheCatalog(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	alice := mustLogin(t, service, "Override Alice")
	bob := mustLogin(t, service, "Override Bob")

	created, err := service.CreateRoom(alice.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "override_deck",
		DeckSnapshot: validDeck("override_deck"),
		ModeParams:   map[string]any{"boss_id": "hui", "boss_health": 12000},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined, err := service.JoinRoom(bob.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "override_guest_deck",
		DeckSnapshot: validDeck("override_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}

	match := service.matches[joined.MatchID]
	if match == nil {
		t.Fatalf("match %s not found", joined.MatchID)
	}
	if match.BossMaxHP != 12000 {
		t.Fatalf("match boss max hp = %d, want 12000 (the room's choice, not hui's 24000)", match.BossMaxHP)
	}

	// Without the option the catalog value stands.
	service2 := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	carol := mustLogin(t, service2, "Catalog Carol")
	dave := mustLogin(t, service2, "Catalog Dave")
	room2, err := service2.CreateRoom(carol.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "catalog_deck",
		DeckSnapshot: validDeck("catalog_deck"),
		ModeParams:   map[string]any{"boss_id": "hui"},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined2, err := service2.JoinRoom(dave.SessionToken, room2.RoomCode, JoinRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "catalog_guest_deck",
		DeckSnapshot: validDeck("catalog_guest_deck"),
	})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	// What the default resolves to is the mode's business; what matters here is
	// that a room which set nothing does not get the other room's 12000.
	if got := service2.matches[joined2.MatchID].BossMaxHP; got == 12000 {
		t.Fatalf("boss health leaked between rooms: a room that set nothing got %d", got)
	}
}

// TestBossHPIsValidated covers the bounds.
func TestBossHPIsValidated(t *testing.T) {
	service := NewService(Config{Rooms: NewMemoryRoomStore(), QueueTickets: NewMemoryQueueTicketStore()})
	host := mustLogin(t, service, "BadHP Host")

	for _, hp := range []int{1, MinRoomBossHP - 1, MaxRoomBossHP + 1} {
		_, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
			ModeID:       "mvp_boss_race",
			ActiveDeckID: "badhp_deck",
			DeckSnapshot: validDeck("badhp_deck"),
			ModeParams:   map[string]any{"boss_health": hp},
		})
		if ErrorCode(err) != codeInvalidRequest {
			t.Fatalf("boss_hp=%d = %v, want %s", hp, err, codeInvalidRequest)
		}
	}
	host = mustLogin(t, service, "GoodHP Host")
	for _, hp := range []int{MinRoomBossHP, 12000, MaxRoomBossHP} {
		if _, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
			ModeID:       "mvp_boss_race",
			ActiveDeckID: "okhp_deck",
			DeckSnapshot: validDeck("okhp_deck"),
			ModeParams:   map[string]any{"boss_health": hp},
		}); err != nil {
			t.Fatalf("boss_hp=%d = %v, want accepted", hp, err)
		}
	}
}

// TestModeConfigHashCoversRoomOptions: two rooms in the same mode with different
// options must not share a hash. The hash is the allocation's statement of what
// the battle server was told to run; if it ignores the options, a replay and a
// result can both validate against a configuration that was never the real one.
func TestModeConfigHashCoversRoomOptions(t *testing.T) {
	base := modeConfigHash("mvp_boss_race", nil)
	withHP := modeConfigHash("mvp_boss_race", map[string]any{"boss_health": 12000})
	withTicks := modeConfigHash("mvp_boss_race", map[string]any{"max_ticks": 1800})
	sameHP := modeConfigHash("mvp_boss_race", map[string]any{"boss_health": 12000})

	if base == withHP {
		t.Fatal("boss_hp did not change the config hash")
	}
	if base == withTicks {
		t.Fatal("max_ticks did not change the config hash")
	}
	if withHP != sameHP {
		t.Fatal("the same options produced different hashes")
	}
	if withHP == withTicks {
		t.Fatal("different options produced the same hash")
	}
}

// TestRoomOptionDigestDropsUnknownKeys: a client adding a decorative key must not
// change the hash, or every client version would fork the allocation's identity.
func TestRoomOptionDigestDropsUnknownKeys(t *testing.T) {
	plain := modeConfigHash("mvp_boss_race", map[string]any{"boss_health": 12000})
	decorated := modeConfigHash("mvp_boss_race", map[string]any{
		"boss_health": 12000,
		"client_ui":   "new_panel",
		"decorative":  true,
	})
	if plain != decorated {
		t.Fatal("unknown keys leaked into the config hash")
	}
}
