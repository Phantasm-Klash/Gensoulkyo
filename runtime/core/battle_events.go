package core

import (
	"strings"
	"time"
)

// H-1: battle-server -> lobby card event callback.
//
// The battle server posts the card events it simulated (card_accepted /
// card_effect_applied / card_rejected) to POST /v1/battle/events, authenticated
// with the same service-callback headers as battle results. The rows are
// validated against the match and then appended to the match event log with
// appendMatchEventLocked, so the existing player poll
// (GET /v1/matches/{id}/events -> MatchEvents) renders them without any client
// change.

const (
	maxBattleEventsPerSubmit = 64
	// The lobby match tick only advances through submitted inputs, so a battle
	// server event may sit a few ticks ahead of match.Tick; anything beyond this
	// slack is rejected as not belonging to this match.
	maxBattleEventTickSlack = 64
	maxBattleEventReasonLen = 128
	// B4: the callback face is HMAC-authenticated but was unbounded in
	// frequency, so a misbehaving (or compromised) battle server could drive
	// unbounded work through the match event log. 30 batches/second leaves a
	// wide margin over a real match's bursts.
	battleEventSubmitWindow        = time.Second
	maxBattleEventSubmitsPerWindow = 30
)

// allowedBattleEventTypes are the only event types the battle server may post.
var allowedBattleEventTypes = map[string]bool{
	"card_accepted":       true,
	"card_effect_applied": true,
	"card_rejected":       true,
	// T9: the mixed-pool draft trigger. The lobby accepts a boss-defeat row
	// and routes it to the draft engine; the battle server does not emit this
	// row yet (see task-queue/out/T9_impl_notes.md for the wiring gap).
	"boss_defeated": true,
	// T10 rule 2: every boss process finished while a player was still
	// passively dropped. The battle server supplies only the SIGNAL — the
	// lobby decides the outcome (a mutual flee, no contest, no score).
	"all_players_disconnected": true,
	// B3: bullet-player contact rows. They name the player that was touched
	// and carry no card, so the validator exempts them from the card checks
	// (see validateBattleEventRowLocked). The battle server starts emitting
	// them once this whitelist lands.
	"hit":   true,
	"graze": true,
}

// BattleEventRow is one battle-simulated card event row.
type BattleEventRow struct {
	Type         string `json:"type"`
	Tick         int    `json:"tick"`
	UserID       string `json:"user_id"`
	CardID       string `json:"card_id"`
	Value        int    `json:"value"`
	TargetUserID string `json:"target_user_id,omitempty"`
	Slot         int    `json:"slot,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// BattleEventsSubmitRequest is the H-1 callback payload.
type BattleEventsSubmitRequest struct {
	BattleServerID string           `json:"battle_server_id"`
	MatchID        string           `json:"match_id"`
	Events         []BattleEventRow `json:"events"`
}

// BattleEventsSubmitResponse reports how many rows were appended.
type BattleEventsSubmitResponse struct {
	OK                  bool         `json:"ok"`
	Version             VersionStamp `json:"version"`
	MatchID             string       `json:"match_id"`
	Accepted            int          `json:"accepted"`
	ServerAuthoritative bool         `json:"server_authoritative"`
	ServerTime          time.Time    `json:"server_time"`
}

// SubmitBattleEvents appends battle-simulated card events to the match event
// log. It follows the lock and allocation-ownership rules of
// SubmitBattleResult: the caller must be the battle server registered for the
// match's allocation, the match must be running, and every row is validated
// before anything is appended (all-or-nothing).
func (s *Service) SubmitBattleEvents(req BattleEventsSubmitRequest) (*BattleEventsSubmitResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	match, ok := s.matches[req.MatchID]
	if !ok {
		return nil, newError(codeNotFound, "match not found")
	}
	// A callback is for an allocation that already exists; never allocate one
	// from the callback path (same rule as SubmitBattleResult).
	allocation := s.battleAllocations[req.MatchID]
	if allocation == nil {
		allocation = match.BattleAllocation
	}
	if allocation == nil {
		return nil, newError(codeBattleServer, "battle allocation unavailable")
	}
	if req.BattleServerID != allocation.BattleServerID {
		return nil, newError(codeBattleServer, "battle events server id %q does not match battle server %q", req.BattleServerID, allocation.BattleServerID)
	}
	if match.Status != "running" {
		return nil, newError(codeMatchState, "battle events require a running match, got %s", match.Status)
	}
	// B4: bound the submission rate. The identity and ownership checks above
	// already ran, so only an authenticated server consumes this budget.
	if !s.allowBattleEventSubmitLocked(req.BattleServerID, now) {
		return nil, newError(codeRateLimited, "battle event submissions exceeded %d per %s", maxBattleEventSubmitsPerWindow, battleEventSubmitWindow)
	}
	if len(req.Events) == 0 {
		return nil, newError(codeInvalidRequest, "events are required")
	}
	if len(req.Events) > maxBattleEventsPerSubmit {
		return nil, newError(codeInvalidRequest, "too many events: %d > %d", len(req.Events), maxBattleEventsPerSubmit)
	}
	for _, row := range req.Events {
		if err := validateBattleEventRowLocked(match, row); err != nil {
			return nil, err
		}
	}
	for _, row := range req.Events {
		appendMatchEventLocked(match, MatchEvent{
			Type:         row.Type,
			Tick:         row.Tick,
			UserID:       row.UserID,
			CardID:       row.CardID,
			Value:        row.Value,
			TargetUserID: row.TargetUserID,
			Slot:         row.Slot,
			Reason:       row.Reason,
		})
	}
	// T9: route boss-defeat rows to the mixed-pool draft engine. The row's
	// Reason carries the boss id. This is the lobby half of the wiring; the
	// battle server does not emit boss_defeated rows yet.
	for _, row := range req.Events {
		if row.Type == "boss_defeated" {
			_, _ = s.reportBossDefeatLocked(match, row.UserID, strings.TrimSpace(row.Reason), row.Tick)
		}
	}
	// T10 rule 2: the boss processes all finished while a player was still
	// passively dropped — nobody reconnected and nobody claimed the win, so the
	// match is a mutual flee: no contest and no score for either side. Only the
	// lobby settles; the battle server never invents an outcome.
	for _, row := range req.Events {
		if row.Type == "all_players_disconnected" {
			if hasUnresolvedDropLocked(match) {
				if err := s.settleFleeLocked(match, "", true); err != nil {
					return nil, err
				}
			}
			break
		}
	}
	return &BattleEventsSubmitResponse{
		OK:                  true,
		Version:             currentVersionStamp(),
		MatchID:             match.MatchID,
		Accepted:            len(req.Events),
		ServerAuthoritative: true,
		ServerTime:          now,
	}, nil
}

// hasUnresolvedDropLocked reports whether some player is passively dropped and
// may still reconnect (T10 rule 4). An explicit abandon already settled the
// match, so such a player never shows up here as "unresolved".
func hasUnresolvedDropLocked(match *matchState) bool {
	for _, userID := range match.PlayerIDs {
		player := match.Players[userID]
		if player != nil && !player.Connected && !player.Abandoned {
			return true
		}
	}
	return false
}

// isMatchLevelBattleEvent marks rows that describe the match as a whole rather
// than one player's action. Such a row carries no user id, so the per-player
// membership check must not apply to it.
func isMatchLevelBattleEvent(rowType string) bool {
	return rowType == "all_players_disconnected"
}

// allowBattleEventSubmitLocked reports whether this battle server may submit a
// batch now, recording the attempt when it may. The caller holds s.mu. The map
// is initialised lazily so NewService needs no change.
func (s *Service) allowBattleEventSubmitLocked(battleServerID string, now time.Time) bool {
	if s.battleEventSubmits == nil {
		s.battleEventSubmits = map[string][]time.Time{}
	}
	cutoff := now.Add(-battleEventSubmitWindow)
	kept := s.battleEventSubmits[battleServerID][:0]
	for _, at := range s.battleEventSubmits[battleServerID] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= maxBattleEventSubmitsPerWindow {
		s.battleEventSubmits[battleServerID] = kept
		return false
	}
	s.battleEventSubmits[battleServerID] = append(kept, now)
	return true
}

func validateBattleEventRowLocked(match *matchState, row BattleEventRow) error {
	if !allowedBattleEventTypes[row.Type] {
		return newError(codeInvalidRequest, "unknown battle event type %q", row.Type)
	}
	if row.Tick < 0 {
		return newError(codeInvalidRequest, "battle event tick must not be negative")
	}
	// A match-level row is not tied to a moment of play, and the lobby's
	// match.Tick only advances through submitted inputs: once every player goes
	// silent it freezes while the battle server keeps ticking, so such a row
	// legitimately arrives ahead of the slack (measured: the last player drops
	// at tick_rate_hz*3 = 180, i.e. ~116 ticks past a slack of 64). Only
	// per-player rows are held to the window.
	if !isMatchLevelBattleEvent(row.Type) && row.Tick > match.Tick+maxBattleEventTickSlack {
		return newError(codeInvalidRequest, "battle event tick %d is ahead of match tick %d", row.Tick, match.Tick)
	}
	if !isMatchLevelBattleEvent(row.Type) {
		if _, ok := match.Players[row.UserID]; !ok {
			return newError(codeInvalidRequest, "battle event user %q is not a player of match %q", row.UserID, match.MatchID)
		}
	}
	if len(row.Reason) > maxBattleEventReasonLen {
		return newError(codeInvalidRequest, "battle event reason exceeds %d bytes", maxBattleEventReasonLen)
	}
	if row.Type == "boss_defeated" {
		// T9: a boss-defeat row carries the boss id in reason and has no card.
		if strings.TrimSpace(row.Reason) == "" {
			return newError(codeInvalidRequest, "boss_defeated reason must carry the boss id")
		}
		return nil
	}
	if row.Type == "all_players_disconnected" {
		// T10 rule 2: a match-level completion signal — it names neither a card
		// nor a player, so the card checks below do not apply (same shape as the
		// boss_defeated early return above).
		return nil
	}
	if row.Type == "hit" || row.Type == "graze" {
		// B3: a contact row names the player it touched but carries no card.
		// The player-membership check above already ran; keep the same value
		// sanity check the card rows get.
		if row.Value < 0 {
			return newError(codeInvalidRequest, "battle event value must not be negative")
		}
		return nil
	}
	cardID := strings.TrimSpace(row.CardID)
	if cardID == "" {
		return newError(codeInvalidRequest, "battle event card id is empty")
	}
	if _, ok := serverCardCatalog[cardID]; !ok {
		return newError(codeInvalidRequest, "unknown card %q", cardID)
	}
	if row.Value < 0 {
		return newError(codeInvalidRequest, "battle event value must not be negative")
	}
	return nil
}
