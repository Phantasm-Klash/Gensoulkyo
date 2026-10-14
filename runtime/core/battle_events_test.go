package core

import (
	"strings"
	"testing"
)

// H-1: posted battle card events land in the match event log and are visible
// through MatchEvents for a match player.
func TestSubmitBattleEventsAppendsCardEventsToMatchLog(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Events Alice")
	bob := mustLogin(t, service, "Events Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")

	allocation, err := service.BattleAllocation(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("allocation: %v", err)
	}
	before, err := service.MatchEvents(alice.SessionToken, matchID, 0, 64)
	if err != nil {
		t.Fatalf("match events before: %v", err)
	}
	eventsBefore := len(before.Events)

	resp, err := service.SubmitBattleEvents(BattleEventsSubmitRequest{
		BattleServerID: allocation.BattleServerID,
		MatchID:        matchID,
		Events: []BattleEventRow{
			{Type: "card_accepted", Tick: 5, UserID: alice.UserID, CardID: "focus_lens", Slot: 0},
			{Type: "card_effect_applied", Tick: 5, UserID: alice.UserID, CardID: "focus_lens", Value: 7, TargetUserID: bob.UserID},
			{Type: "card_rejected", Tick: 6, UserID: bob.UserID, CardID: "purge_bell", Slot: 3, Reason: "cast_card_insufficient_energy"},
		},
	})
	if err != nil {
		t.Fatalf("submit battle events: %v", err)
	}
	if !resp.OK || resp.Accepted != 3 || resp.MatchID != matchID {
		t.Fatalf("submit response invalid: %+v", resp)
	}

	after, err := service.MatchEvents(alice.SessionToken, matchID, 0, 64)
	if err != nil {
		t.Fatalf("match events after: %v", err)
	}
	if len(after.Events) != eventsBefore+3 {
		t.Fatalf("event log grew by %d, want 3 (before=%d after=%d)", len(after.Events)-eventsBefore, eventsBefore, len(after.Events))
	}
	appended := after.Events[eventsBefore:]
	want := []MatchEvent{
		{Type: "card_accepted", Tick: 5, UserID: alice.UserID, CardID: "focus_lens", Slot: 0},
		{Type: "card_effect_applied", Tick: 5, UserID: alice.UserID, CardID: "focus_lens", Value: 7, TargetUserID: bob.UserID},
		{Type: "card_rejected", Tick: 6, UserID: bob.UserID, CardID: "purge_bell", Slot: 3, Reason: "cast_card_insufficient_energy"},
	}
	for index, expected := range want {
		got := appended[index]
		got.Seq = 0 // seq is assigned by appendMatchEventLocked
		if got != expected {
			t.Fatalf("appended event %d = %+v, want %+v", index, got, expected)
		}
	}
	if appended[0].Seq == 0 || appended[1].Seq != appended[0].Seq+1 || appended[2].Seq != appended[1].Seq+1 {
		t.Fatalf("appended events must carry consecutive seqs: %+v", appended)
	}
}

// H-1: every rejection path leaves the event log untouched.
func TestSubmitBattleEventsRejections(t *testing.T) {
	newMatch := func(t *testing.T) (*Service, string, string, *AuthSession, *AuthSession) {
		t.Helper()
		service := NewService(Config{})
		alice := mustLogin(t, service, "Reject Alice")
		bob := mustLogin(t, service, "Reject Bob")
		matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")
		allocation, err := service.BattleAllocation(alice.SessionToken, matchID)
		if err != nil {
			t.Fatalf("allocation: %v", err)
		}
		return service, matchID, allocation.BattleServerID, alice, bob
	}
	eventCount := func(service *Service, matchID string) int {
		service.mu.Lock()
		defer service.mu.Unlock()
		match := service.matches[matchID]
		if match == nil {
			return -1
		}
		return len(match.EventLog)
	}
	validRow := func(user *AuthSession) BattleEventRow {
		return BattleEventRow{Type: "card_accepted", Tick: 5, UserID: user.UserID, CardID: "focus_lens", Slot: 0}
	}

	cases := []struct {
		name    string
		mutate  func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession)
		prep    func(t *testing.T, service *Service, matchID string)
		wantErr string
	}{
		{
			name:    "unknown match",
			mutate:  func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) { req.MatchID = "match-nope" },
			wantErr: codeNotFound,
		},
		{
			name: "wrong battle server",
			mutate: func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) {
				req.BattleServerID = "battle-impostor"
			},
			wantErr: codeBattleServer,
		},
		{
			name:    "empty events",
			mutate:  func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) { req.Events = nil },
			wantErr: codeInvalidRequest,
		},
		{
			name: "too many events",
			mutate: func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) {
				req.Events = make([]BattleEventRow, maxBattleEventsPerSubmit+1)
				for index := range req.Events {
					req.Events[index] = validRow(alice)
				}
			},
			wantErr: codeInvalidRequest,
		},
		{
			name: "unknown event type",
			mutate: func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) {
				req.Events[0].Type = "card_forged"
			},
			wantErr: codeInvalidRequest,
		},
		{
			name:    "negative tick",
			mutate:  func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) { req.Events[0].Tick = -1 },
			wantErr: codeInvalidRequest,
		},
		{
			name: "tick far ahead of match",
			mutate: func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) {
				req.Events[0].Tick = 1 << 20
			},
			wantErr: codeInvalidRequest,
		},
		{
			name: "user not in match",
			mutate: func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) {
				req.Events[0].UserID = "user-stranger"
			},
			wantErr: codeInvalidRequest,
		},
		{
			name: "empty card id",
			mutate: func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) {
				req.Events[0].CardID = "  "
			},
			wantErr: codeInvalidRequest,
		},
		{
			name: "unknown card id",
			mutate: func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) {
				req.Events[0].CardID = "no_such_card"
			},
			wantErr: codeInvalidRequest,
		},
		{
			name:    "negative value",
			mutate:  func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) { req.Events[0].Value = -3 },
			wantErr: codeInvalidRequest,
		},
		{
			name: "oversized reason",
			mutate: func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) {
				req.Events[0].Reason = strings.Repeat("x", maxBattleEventReasonLen+1)
			},
			wantErr: codeInvalidRequest,
		},
		{
			name: "one bad row rejects the whole batch",
			mutate: func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) {
				req.Events = append(req.Events, BattleEventRow{Type: "card_rejected", Tick: 5, UserID: alice.UserID, CardID: "no_such_card"})
			},
			wantErr: codeInvalidRequest,
		},
		{
			name: "match not running",
			mutate: func(req *BattleEventsSubmitRequest, alice *AuthSession, bob *AuthSession) {
			},
			prep: func(t *testing.T, service *Service, matchID string) {
				service.mu.Lock()
				service.matches[matchID].Status = "ended"
				service.mu.Unlock()
			},
			wantErr: codeMatchState,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			service, matchID, battleServerID, alice, bob := newMatch(t)
			req := BattleEventsSubmitRequest{
				BattleServerID: battleServerID,
				MatchID:        matchID,
				Events:         []BattleEventRow{validRow(alice)},
			}
			if testCase.prep != nil {
				testCase.prep(t, service, matchID)
			}
			testCase.mutate(&req, alice, bob)
			before := eventCount(service, matchID)
			_, err := service.SubmitBattleEvents(req)
			if err == nil {
				t.Fatalf("expected rejection %s, got success", testCase.wantErr)
			}
			if ErrorCode(err) != testCase.wantErr {
				t.Fatalf("error code = %q, want %q (%v)", ErrorCode(err), testCase.wantErr, err)
			}
			if after := eventCount(service, matchID); after != before {
				t.Fatalf("event log must stay untouched on rejection: before=%d after=%d", before, after)
			}
		})
	}
}
