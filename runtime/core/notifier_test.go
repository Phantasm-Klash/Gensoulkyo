package core

import (
	"testing"
	"time"
)

type notifyCall struct {
	userIDs []string
	subject string
	payload map[string]any
}

// recordingNotifier captures pushes so a test can assert on them. The lobby
// dispatches from a goroutine, so the buffer has to hold more than one event and
// expect() skips subjects it is not waiting for.
type recordingNotifier struct {
	calls chan notifyCall
}

func newRecordingNotifier() *recordingNotifier {
	return &recordingNotifier{calls: make(chan notifyCall, 64)}
}

func (r *recordingNotifier) Notify(userIDs []string, subject string, payload map[string]any) error {
	r.calls <- notifyCall{userIDs: userIDs, subject: subject, payload: payload}
	return nil
}

func (r *recordingNotifier) expect(t *testing.T, subject string) notifyCall {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case call := <-r.calls:
			if call.subject == subject {
				return call
			}
		case <-deadline:
			t.Fatalf("no %q push arrived", subject)
			return notifyCall{}
		}
	}
}

func notifierService(notifier LobbyNotifier) *Service {
	return NewService(Config{
		Notifier:     notifier,
		Rooms:        NewMemoryRoomStore(),
		QueueTickets: NewMemoryQueueTicketStore(),
	})
}

// TestRoomStatePushCoversEverySeat: a seat change has to reach the seats that
// already existed, otherwise the host never learns a guest arrived.
func TestRoomStatePushCoversEverySeat(t *testing.T) {
	notifier := newRecordingNotifier()
	service := notifierService(notifier)
	host := mustLogin(t, service, "Push Host")

	created, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "push_host_deck",
		DeckSnapshot: validDeck("push_host_deck"),
		ModeParams:   map[string]any{"coop_players": 4},
	})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	first := notifier.expect(t, PushRoomState)
	if len(first.userIDs) != 1 || first.userIDs[0] != host.UserID {
		t.Fatalf("create push recipients = %v, want [%s]", first.userIDs, host.UserID)
	}
	if snapshot, ok := first.payload["room"].(RoomSnapshot); ok && snapshot.CurrentPlayers != 1 {
		t.Fatalf("create push players = %d, want 1", snapshot.CurrentPlayers)
	}

	guest := mustLogin(t, service, "Push Guest")
	if _, err := service.JoinRoom(guest.SessionToken, created.RoomCode, JoinRoomRequest{
		ModeID:       "coop_boss",
		ActiveDeckID: "push_guest_deck",
		DeckSnapshot: validDeck("push_guest_deck"),
		ModeParams:   map[string]any{"coop_players": 4},
	}); err != nil {
		t.Fatalf("join room: %v", err)
	}
	second := notifier.expect(t, PushRoomState)
	if len(second.userIDs) != 2 {
		t.Fatalf("join push recipients = %v, want both seats", second.userIDs)
	}
	seen := map[string]bool{}
	for _, userID := range second.userIDs {
		seen[userID] = true
	}
	if !seen[host.UserID] || !seen[guest.UserID] {
		t.Fatalf("join push recipients = %v, want host and guest", second.userIDs)
	}
	if snapshot, ok := second.payload["room"].(RoomSnapshot); ok && snapshot.CurrentPlayers != 2 {
		t.Fatalf("join push players = %d, want 2", snapshot.CurrentPlayers)
	}
}

// TestMatchStartPushCarriesTheSignedTicket: the push is what tells a client the
// match is live, so it has to carry everything needed to connect -- the start
// event and the ticket that authorises the battle server.
func TestMatchStartPushCarriesTheSignedTicket(t *testing.T) {
	notifier := newRecordingNotifier()
	service := notifierService(notifier)
	alice := mustLogin(t, service, "Start Alice")
	bob := mustLogin(t, service, "Start Bob")

	matchID := runningMatch(t, service, alice, bob)
	call := notifier.expect(t, PushMatchStart)
	if len(call.userIDs) != 2 {
		t.Fatalf("match_start recipients = %v, want both players", call.userIDs)
	}
	event, ok := call.payload["match_start"].(MatchStartEvent)
	if !ok {
		t.Fatalf("match_start payload missing the event: %+v", call.payload)
	}
	if event.MatchID != matchID {
		t.Fatalf("match_start event match = %q, want %q", event.MatchID, matchID)
	}
	if _, ok := call.payload["battle_ticket"].(*SignedBattleTicket); !ok {
		t.Fatalf("match_start payload missing the battle ticket: %+v", call.payload)
	}
}

// TestMatchResultPushFollowsSettlement: the result screen reads the settlement,
// so the push has to carry one per player.
func TestMatchResultPushFollowsSettlement(t *testing.T) {
	notifier := newRecordingNotifier()
	service := notifierService(notifier)
	alice := mustLogin(t, service, "Result Alice")
	bob := mustLogin(t, service, "Result Bob")

	matchID := runningMatch(t, service, alice, bob)
	allocation, ok := service.BattleAllocationForMatch(matchID)
	if !ok {
		t.Fatalf("no allocation for %s", matchID)
	}
	ids := playerIDsByUser(t, service, matchID, alice.UserID, bob.UserID)
	if _, err := service.ApplyBattleResultCallback(BattleResultCallback{
		MatchID:        matchID,
		ModeID:         allocation.ModeID,
		RulesetVersion: allocation.Version.RulesetVersion,
		MatchSeed:      uint64(allocation.ServerSeed),
		WinnerPlayerID: ids[alice.UserID],
		WinnerTick:     900,
		StateHash:      "sha256:push-state",
		Players: []BattleResultPlayer{
			{PlayerID: ids[alice.UserID], DamageDealt: 900, BossCurrentHP: 0},
			{PlayerID: ids[bob.UserID], DamageDealt: 400, BossCurrentHP: 300},
		},
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}

	call := notifier.expect(t, PushMatchResult)
	if len(call.userIDs) != 2 {
		t.Fatalf("match_result recipients = %v, want both players", call.userIDs)
	}
	if call.payload["match_id"] != matchID {
		t.Fatalf("match_result payload match = %v, want %q", call.payload["match_id"], matchID)
	}
	settlements, ok := call.payload["settlements"].(map[string]any)
	if !ok || len(settlements) != 2 {
		t.Fatalf("match_result settlements = %v, want one per player", call.payload["settlements"])
	}
}

// TestPushStatsCountWithoutANotifier keeps the health counters honest: with no
// notifier nothing is delivered, but asking must not panic.
func TestPushStatsCountWithoutANotifier(t *testing.T) {
	service := NewService(Config{})
	service.dispatchNotificationLocked([]string{"someone"}, PushRoomState, map[string]any{})
	stats := service.PushStats()
	if stats.Enabled || stats.Delivered != 0 || stats.Failed != 0 {
		t.Fatalf("stats without a notifier = %+v, want disabled and zeroed", stats)
	}
}
