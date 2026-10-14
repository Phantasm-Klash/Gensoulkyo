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
)

// allowedBattleEventTypes are the only event types the battle server may post.
var allowedBattleEventTypes = map[string]bool{
	"card_accepted":       true,
	"card_effect_applied": true,
	"card_rejected":       true,
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
	return &BattleEventsSubmitResponse{
		OK:                  true,
		Version:             currentVersionStamp(),
		MatchID:             match.MatchID,
		Accepted:            len(req.Events),
		ServerAuthoritative: true,
		ServerTime:          now,
	}, nil
}

func validateBattleEventRowLocked(match *matchState, row BattleEventRow) error {
	if !allowedBattleEventTypes[row.Type] {
		return newError(codeInvalidRequest, "unknown battle event type %q", row.Type)
	}
	if row.Tick < 0 {
		return newError(codeInvalidRequest, "battle event tick must not be negative")
	}
	if row.Tick > match.Tick+maxBattleEventTickSlack {
		return newError(codeInvalidRequest, "battle event tick %d is ahead of match tick %d", row.Tick, match.Tick)
	}
	if _, ok := match.Players[row.UserID]; !ok {
		return newError(codeInvalidRequest, "battle event user %q is not a player of match %q", row.UserID, match.MatchID)
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
	if len(row.Reason) > maxBattleEventReasonLen {
		return newError(codeInvalidRequest, "battle event reason exceeds %d bytes", maxBattleEventReasonLen)
	}
	return nil
}
