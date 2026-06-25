package match

import (
	"regexp"
	"testing"

	"github.com/Phantasm-Klash/Gensoulkyo/runtime/cards"
)

func TestRandomIDIsUUID(t *testing.T) {
	id := randomID()
	matched, err := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, id)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatalf("expected UUIDv4 match id, got %q", id)
	}
}

func TestResultsForDuelWinLossAndDraw(t *testing.T) {
	results := resultsFor([]PlayerState{
		{UserID: "loser", Score: 10},
		{UserID: "winner", Score: 20},
	})
	if results["winner"] != "win" || results["loser"] != "loss" {
		t.Fatalf("unexpected results: %#v", results)
	}

	results = resultsFor([]PlayerState{
		{UserID: "a", Score: 10},
		{UserID: "b", Score: 10},
	})
	if results["a"] != "draw" || results["b"] != "draw" {
		t.Fatalf("expected draw results, got %#v", results)
	}
}

func TestRewardForCompletionAndWin(t *testing.T) {
	reward := rewardFor(PlayerState{UserID: "player", Score: 50, GrazeCount: 100}, "win")
	if reward.Points != 25 {
		t.Fatalf("expected 25 points, got %d", reward.Points)
	}
	if reward.CardDust != 3 {
		t.Fatalf("expected 3 card dust, got %d", reward.CardDust)
	}
	if reward.Reason != "match_completion" {
		t.Fatalf("unexpected reward reason: %s", reward.Reason)
	}
}

func TestDeckSnapshotsFromParam(t *testing.T) {
	snapshots := deckSnapshotsFromParam(map[string]interface{}{
		"deck_snapshots": map[string]interface{}{
			"player-a": map[string]interface{}{
				"deck_id":         "deck-a",
				"name":            "Main",
				"format":          "open-alpha-0",
				"ruleset_version": "open-alpha-0",
				"card_ids":        []interface{}{"pulse_shot", "focus_barrier"},
			},
			"player-b": map[string]interface{}{
				"deck_id":  "",
				"card_ids": []interface{}{},
			},
		},
	})
	if len(snapshots) != 1 {
		t.Fatalf("expected one valid snapshot, got %#v", snapshots)
	}
	if snapshots["player-a"].DeckID != "deck-a" {
		t.Fatalf("unexpected snapshot: %#v", snapshots["player-a"])
	}
	if snapshots["player-a"].CardIDs[1] != "focus_barrier" {
		t.Fatalf("unexpected card ids: %#v", snapshots["player-a"].CardIDs)
	}
}

func TestDeckSnapshotForUsesRuntimeSnapshot(t *testing.T) {
	state := &RuntimeState{
		Authoritative: NewState("match", "open-alpha-0", "seed", []string{"player"}),
		DeckSnapshots: map[string]cards.DeckSnapshot{
			"player": {
				DeckID:         "deck-1",
				Name:           "Active",
				Format:         "open-alpha-0",
				RulesetVersion: "open-alpha-0",
				CardIDs:        []string{"pulse_shot"},
			},
		},
	}
	snapshot := deckSnapshotFor(state, "player")
	if snapshot.DeckID != "deck-1" || len(snapshot.CardIDs) != 1 {
		t.Fatalf("expected runtime deck snapshot, got %#v", snapshot)
	}
}

func TestDeckSnapshotForFallback(t *testing.T) {
	state := &RuntimeState{
		Authoritative: NewState("match", "open-alpha-0", "seed", []string{"player"}),
	}
	snapshot := deckSnapshotFor(state, "player")
	if snapshot.RulesetVersion != "open-alpha-0" {
		t.Fatalf("expected ruleset fallback, got %#v", snapshot)
	}
	if len(snapshot.CardIDs) != 0 {
		t.Fatalf("expected empty card fallback, got %#v", snapshot.CardIDs)
	}
}

func TestApplySettlementSummary(t *testing.T) {
	snapshot := Snapshot{
		Players: []PlayerState{
			{UserID: "winner", Score: 20},
			{UserID: "loser", Score: 5},
		},
		ModeState: map[string]interface{}{},
	}
	applySettlementSummary(&snapshot)
	if snapshot.Settlement == nil {
		t.Fatal("expected settlement summary")
	}
	results, ok := snapshot.Settlement["results"].(map[string]string)
	if !ok {
		t.Fatalf("unexpected results payload: %#v", snapshot.Settlement["results"])
	}
	if results["winner"] != "win" || results["loser"] != "loss" {
		t.Fatalf("unexpected settlement results: %#v", results)
	}
	if snapshot.ModeState["settlement_final"] != true {
		t.Fatalf("expected settlement_final mode state, got %#v", snapshot.ModeState)
	}
}

func TestCanJoinRequiresLockedParticipantWhenPresent(t *testing.T) {
	state := &RuntimeState{
		Authoritative: NewState("match", "open-alpha-0", "seed", []string{"player-a", "player-b"}),
	}
	if !canJoin(state, "player-a") {
		t.Fatal("expected locked participant to join")
	}
	if canJoin(state, "intruder") {
		t.Fatal("expected non-participant join rejection")
	}
	diagnostic := &RuntimeState{Authoritative: NewState("match", "open-alpha-0", "seed", nil)}
	if !canJoin(diagnostic, "any-user") {
		t.Fatal("expected diagnostic empty-participant match to allow join")
	}
}
