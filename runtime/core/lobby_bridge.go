package core

import (
	"strings"
)

// BindBattleServerAllocation points an existing match allocation at a freshly
// spawned per-match battle server and invalidates any cached battle tickets so
// that subsequent ticket issuance carries the new endpoint.
//
// The lobby calls this right after it has started a phk_battle_server process
// for the match, so clients receive the real UDP endpoint through the
// MatchStartMessage instead of the static default allocation.
func (s *Service) BindBattleServerAllocation(matchID string, battleServerID string, endpoint string) (*BattleServerAllocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	matchID = strings.TrimSpace(matchID)
	endpoint = strings.TrimSpace(endpoint)
	battleServerID = strings.TrimSpace(battleServerID)
	if matchID == "" {
		return nil, newError(codeInvalidRequest, "match_id is required")
	}
	if endpoint == "" {
		return nil, newError(codeInvalidRequest, "endpoint is required")
	}
	match := s.matches[matchID]
	if match == nil {
		return nil, newError(codeNotFound, "match not found")
	}
	if battleServerID == "" {
		battleServerID = "battle-" + matchID
	}
	// Register the spawned server so it shows up in the server list and is never
	// picked for other matches (capacity 1 with one active match).
	if _, err := s.upsertBattleServerLocked(BattleServerHeartbeatRequest{
		BattleServerID: battleServerID,
		Endpoint:       endpoint,
		Region:         "local",
		Capacity:       1,
		ActiveMatches:  1,
		Load:           1,
		Status:         "online",
		SupportedModes: []string{match.ModeID, "*"},
	}); err != nil {
		return nil, err
	}
	allocation := s.battleAllocations[matchID]
	if allocation == nil {
		allocation = s.ensureBattleAllocationLocked(match)
	}
	if allocation == nil {
		return nil, newError(codeBattleServer, "battle allocation unavailable")
	}
	allocation.BattleServerID = battleServerID
	allocation.Endpoint = endpoint
	allocation.AllocatedAt = s.clock()
	s.battleAllocations[matchID] = allocation
	match.BattleAllocation = allocation
	s.invalidateBattleTicketsLocked(matchID)

	copy := copyBattleAllocation(allocation)
	return &copy, nil
}

func (s *Service) invalidateBattleTicketsLocked(matchID string) {
	revokedAt := s.clock()
	for key, signed := range s.battleTickets {
		if signed == nil || signed.Ticket.MatchID != matchID {
			continue
		}
		if _, consumed := s.consumedBattleTickets[signed.Ticket.TicketID]; !consumed {
			s.recordBattleTicketRevokedAuditLocked(signed, revokedAt)
		}
		delete(s.battleTickets, key)
		delete(s.battleTicketsByID, signed.Ticket.TicketID)
	}
}

// MatchStart returns the match start payload and a fresh signed battle ticket
// for a player who is already a member of a running match. It is read-only and
// safe to call repeatedly (for example when the lobby broadcasts the start to
// every player in a room).
func (s *Service) MatchStart(sessionToken string, matchID string) (*MatchStartEvent, *SignedBattleTicket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, nil, err
	}
	match, _, err := s.matchPlayerLocked(user.UserID, matchID)
	if err != nil {
		return nil, nil, err
	}
	if match.Status != "running" {
		return nil, nil, newError(codeMatchState, "match is %s", match.Status)
	}
	event := s.matchStartLocked(match)
	signed, err := s.signedBattleTicketLocked(match, user)
	if err != nil {
		return nil, nil, err
	}
	return &event, signed, nil
}

// BattleAllocationForMatch returns the current allocation for a match without a
// player session. It is used by the lobby when it needs the allocation outside
// of a client request (for example during result fan-out).
func (s *Service) BattleAllocationForMatch(matchID string) (*BattleServerAllocation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	allocation := s.battleAllocations[strings.TrimSpace(matchID)]
	if allocation == nil {
		return nil, false
	}
	copy := copyBattleAllocation(allocation)
	return &copy, true
}
