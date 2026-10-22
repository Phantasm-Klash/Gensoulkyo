package core

import (
	"strings"
)

// UpdateRoomOptionsRequest changes the room-level options a host picked when
// creating the room.
//
// Every field is a pointer so "not provided" and "explicitly set to the empty
// value" stay distinguishable -- clearing boss_id back to the default timeline is
// a real edit, not an omission.
type UpdateRoomOptionsRequest struct {
	BossID      *string        `json:"boss_id,omitempty"`
	CoopPlayers *int           `json:"coop_players,omitempty"`
	StageID     *string        `json:"stage_id,omitempty"`
	ModeParams  map[string]any `json:"mode_params,omitempty"`
}

// UpdateRoomOptions rewrites the room-level options while the room is still
// waiting for players.
//
// This replaces the "leave the room and create it again" workaround: the host
// keeps the room code, the guests that already joined keep their seats, and the
// audit trail records one edit instead of a churn of create/leave pairs.
//
// Three rules shape it:
//
//   - Only the host, and only on the server. The client sends its session; the
//     host check runs here. A guest that calls this is refused regardless of what
//     its UI enabled.
//   - Only while waiting. Once the match is found the options are already baked
//     into the tickets, the allocation and the spawned process; accepting an edit
//     then would leave those three disagreeing with the room record.
//   - The seats that are already taken must be written through to the tickets,
//     because the match snapshot is built from the tickets, not from the room.
//     Changing the boss or the stage on the room alone is the easiest way to make
//     a room that looks right and a match that is not.
func (s *Service) UpdateRoomOptions(sessionToken string, roomCode string, req UpdateRoomOptionsRequest) (*QueueResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return nil, err
	}
	code := strings.TrimSpace(roomCode)
	if code == "" {
		return nil, newError(codeInvalidRequest, "room_code is required")
	}
	room, ok, err := s.loadLiveRoomRecordLocked(code)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newError(codeNotFound, "room %q not found", code)
	}
	if room.HostUserID != user.UserID {
		return nil, newError(codeUnauthorized, "only the host can change room options")
	}
	if room.Status != RoomStatusWaiting {
		return nil, newError(codeRoomUnavailable, "room is %s; options are locked once a match is found", room.Status)
	}
	mode, ok := ModeConfigs[room.ModeID]
	if !ok {
		return nil, newError(codeInvalidMode, "unsupported mode %q", room.ModeID)
	}

	changed := false

	if req.BossID != nil {
		bossID, err := validateBossID(*req.BossID)
		if err != nil {
			return nil, err
		}
		if room.BossID != bossID {
			room.BossID = bossID
			changed = true
		}
	}

	if req.StageID != nil {
		stage := strings.TrimSpace(*req.StageID)
		if stage != "" {
			if _, ok := allowedStageIDs[stage]; !ok {
				return nil, newError(codeInvalidRequest, "unknown stage %q", stage)
			}
		}
		if room.StageID != stage {
			room.StageID = stage
			changed = true
		}
	}

	if req.CoopPlayers != nil {
		if room.ModeID != "coop_boss" {
			return nil, newError(codeInvalidRequest, "coop_players only applies to coop_boss rooms")
		}
		if err := validateCoopPlayers(room.ModeID, map[string]any{"coop_players": *req.CoopPlayers}); err != nil {
			return nil, err
		}
		// Shrinking below the seats already taken would leave a room that can
		// never fill; the host has to ask somebody to leave first.
		if *req.CoopPlayers > 0 && len(room.TicketIDs) > *req.CoopPlayers {
			return nil, newError(codeInvalidRequest,
				"%d players are already seated, cannot shrink to %d", len(room.TicketIDs), *req.CoopPlayers)
		}
		if room.CoopPlayers != *req.CoopPlayers {
			room.CoopPlayers = *req.CoopPlayers
			changed = true
		}
	}

	if len(req.ModeParams) > 0 {
		if room.ModeParams == nil {
			room.ModeParams = map[string]any{}
		}
		for key, value := range req.ModeParams {
			trimmed := strings.TrimSpace(key)
			if trimmed == "" {
				continue
			}
			room.ModeParams[trimmed] = value
			changed = true
		}
		if err := validateCoopPlayers(room.ModeID, room.ModeParams); err != nil {
			return nil, err
		}
		if _, err := maxTicksFromModeParams(room.ModeParams); err != nil {
			return nil, err
		}
		if _, err := bossHPFromModeParams(room.ModeParams); err != nil {
			return nil, err
		}
		// coop_players may have arrived through mode_params; mirror it onto the
		// dedicated column so requiredPlayersForRoomLocked sees it.
		if room.ModeID == "coop_boss" {
			if seats := coopPlayersFromModeParams(room.ModeParams); seats > 0 && seats != room.CoopPlayers {
				if len(room.TicketIDs) > seats {
					return nil, newError(codeInvalidRequest,
						"%d players are already seated, cannot shrink to %d", len(room.TicketIDs), seats)
				}
				room.CoopPlayers = seats
				changed = true
			}
		}
	}

	if !changed {
		// Nothing to write: answer with the room as it stands, so a client that
		// re-sends the same options is idempotent rather than an error.
		return s.hostQueueResponseLocked(room, mode, nil), nil
	}

	// Write the room's decision through to every ticket already in it. The match
	// snapshot is assembled from the tickets, so a room-only edit would produce a
	// match that disagrees with the room the players are looking at.
	for _, ticketID := range room.TicketIDs {
		ticket := s.queueTicketLocked(ticketID)
		if ticket == nil {
			continue
		}
		ticket.BossID = room.BossID
		ticket.CoopPlayers = room.CoopPlayers
		ticket.ModeParams = copyAnyMap(room.ModeParams)
		ticket.MaxTicks = intFromAnyValue(room.ModeParams["max_ticks"])
		if room.StageID != "" {
			ticket.Loadout.StageID = room.StageID
		}
		if err := s.saveQueueTicketLocked(ticket); err != nil {
			return nil, err
		}
	}

	room.UpdatedAt = s.clock()
	if err := s.saveRoomLocked(room); err != nil {
		return nil, err
	}
	// A shrink can fill the room. Form the match here rather than waiting for a
	// join that will never come.
	match, err := s.formMatchIfRoomFullLocked(&room, mode)
	if err != nil {
		return nil, err
	}
	s.notifyRoomStateLocked(&room)
	return s.hostQueueResponseLocked(room, mode, match), nil
}

// hostQueueResponseLocked builds the room response from the host's ticket, which
// is the one the caller asked with.
func (s *Service) hostQueueResponseLocked(room RoomRecord, mode ModeConfig, match *matchState) *QueueResponse {
	var hostTicket *queueTicket
	for _, ticketID := range room.TicketIDs {
		ticket := s.queueTicketLocked(ticketID)
		if ticket == nil {
			continue
		}
		if ticket.UserID == room.HostUserID {
			hostTicket = ticket
			break
		}
	}
	if hostTicket == nil {
		// The host's ticket is missing (it was reaped, or the room record is
		// stale). Report the room without inventing a ticket response.
		return &QueueResponse{
			OK:              true,
			QueueStatus:     room.Status,
			ModeID:          room.ModeID,
			RoomCode:        room.RoomCode,
			RoomStatus:      room.Status,
			RequiredPlayers: s.requiredPlayersForRoomLocked(mode, &room),
			CurrentPlayers:  len(room.TicketIDs),
		}
	}
	return s.queueResponseLocked(hostTicket, mode, len(room.TicketIDs), match)
}

// formMatchIfRoomFullLocked creates the match once the room has enough seats.
//
// It exists because "the room is full" is no longer only a consequence of
// somebody joining: dropping a four-seat room to two seats fills it too, and at
// that moment nobody is going to join and trigger the check. Without this the
// room would sit at waiting forever with every seat taken.
//
// It does only the shared part -- create the match, flip the room to found, audit
// the matched seats. Callers still do their own member write and room-state push,
// because JoinRoom records the arriving seat and UpdateRoomOptions does not have
// one to record.
func (s *Service) formMatchIfRoomFullLocked(room *RoomRecord, mode ModeConfig) (*matchState, error) {
	if room == nil || room.Status != RoomStatusWaiting {
		return nil, nil
	}
	required := s.requiredPlayersForRoomLocked(mode, room)
	if required <= 0 || len(room.TicketIDs) < required {
		return nil, nil
	}
	groupIDs := append([]string{}, room.TicketIDs[:required]...)
	match, err := s.createMatchLocked(mode, groupIDs)
	if err != nil {
		return nil, err
	}
	room.MatchID = match.MatchID
	room.Status = RoomStatusFound
	if err := s.saveRoomLocked(*room); err != nil {
		return nil, err
	}
	now := s.clock()
	for _, matchedTicketID := range groupIDs {
		matchedTicket := s.queueTicketLocked(matchedTicketID)
		if matchedTicket == nil {
			continue
		}
		s.recordLobbyRoomAuditLocked(room, matchedTicket, matchedTicket.UserID, "matched", now)
	}
	return match, nil
}

// MinRoomMaxTicks / MaxRoomMaxTicks bound the match-length option.
//
// 600 ticks is ten seconds at 60Hz -- short enough to be a real choice for a
// quick duel, long enough that a match is not over before it starts. 7200 is the
// battle host's own process backstop, so a room cannot ask for more than the
// deployment would allow anyway. Steps are one second (60 ticks), which keeps the
// value readable as "how long is this match" instead of an arbitrary tick count.
const (
	MinRoomMaxTicks  = 600
	MaxRoomMaxTicks  = 7200
	RoomMaxTicksStep = 60
)

// maxTicksFromModeParams reads and validates the match-length option, returning 0
// when the room does not set one (meaning "use the battle host default").
func maxTicksFromModeParams(params map[string]any) (int, error) {
	raw, ok := params["max_ticks"]
	if !ok || raw == nil {
		return 0, nil
	}
	ticks := intFromAnyValue(raw)
	if ticks <= 0 {
		return 0, nil
	}
	if ticks < MinRoomMaxTicks || ticks > MaxRoomMaxTicks {
		return 0, newError(codeInvalidRequest,
			"max_ticks must be between %d and %d, got %d", MinRoomMaxTicks, MaxRoomMaxTicks, ticks)
	}
	if ticks%RoomMaxTicksStep != 0 {
		return 0, newError(codeInvalidRequest,
			"max_ticks must be a multiple of %d, got %d", RoomMaxTicksStep, ticks)
	}
	return ticks, nil
}

// allocationMaxTicks reads the budget the match was created with.
func allocationMaxTicks(match *matchState) int {
	if match == nil {
		return 0
	}
	if ticks := intFromAnyValue(match.ModeState["max_ticks"]); ticks > 0 {
		return ticks
	}
	return 0
}

// userInMatchByTicketsLocked answers "is this player in this match" from the
// queue tickets.
//
// It exists because MatchRecord only stores a player count, not a roster: after
// a restart there is no durable list of who was in a match except the tickets
// themselves, which carry user_id and match_id. Anything that serves match data
// from a persisted match record has to check membership this way, or it would
// hand one match's allocation to anybody who asks for it.
func (s *Service) userInMatchByTicketsLocked(matchID string, userID string) (bool, error) {
	if matchID == "" || userID == "" {
		return false, nil
	}
	for _, ticket := range s.tickets {
		if ticket != nil && ticket.MatchID == matchID && ticket.UserID == userID {
			return true, nil
		}
	}
	records, err := s.queueTicketStore.ListByMatch(matchID)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if record.UserID == userID {
			return true, nil
		}
	}
	return false, nil
}

// MinRoomBossHP / MaxRoomBossHP bound the boss-health option.
//
// The low end keeps a boss from being a one-hit prop; the high end is above the
// biggest entry in the boss catalog, so a room can make a fight longer but not
// invent a boss that takes longer than the match length allows (see
// MaxRoomMaxTicks -- the two options bound the same fight from opposite sides).
const (
	MinRoomBossHP = 1000
	MaxRoomBossHP = 100000
)

// bossHPFromModeParams reads and validates the boss-health option, returning 0
// when the room does not set one, which means "whatever the boss catalog says".
//
// The key is boss_health, not boss_hp, and that is not cosmetic: boss_hp is one
// of the client-forbidden fields (see forbiddenClientFields), because a client
// that can submit it can lie about how much of a boss is left. Room options
// arrive as client-submitted mode_params, so an option named boss_hp would be
// rejected outright by the forbidden-field sweep. Naming it boss_health keeps
// the option available without opening that door.
func bossHPFromModeParams(params map[string]any) (int, error) {
	raw, ok := params["boss_health"]
	if !ok || raw == nil {
		return 0, nil
	}
	hp := intFromAnyValue(raw)
	if hp <= 0 {
		return 0, nil
	}
	if hp < MinRoomBossHP || hp > MaxRoomBossHP {
		return 0, newError(codeInvalidRequest,
			"boss_hp must be between %d and %d, got %d", MinRoomBossHP, MaxRoomBossHP, hp)
	}
	return hp, nil
}

// roomOptionDigest keeps only the options that change how the match runs, so the
// mode config hash covers them.
//
// This is the fix for a hash that used to cover the ModeConfig constants alone:
// two rooms in the same mode with different boss health or match length produced
// the same hash, so the allocation's "expected configuration" did not actually
// describe what the battle server was told to run. Keys are fixed and sorted so
// the digest is stable; unknown keys are dropped, because a client adding a
// decorative key must not change the hash.
func roomOptionDigest(params map[string]any) map[string]any {
	digest := map[string]any{}
	if params == nil {
		return digest
	}
	for _, key := range []string{"boss_id", "boss_health", "max_ticks", "stage_id", "coop_players"} {
		raw, ok := params[key]
		if !ok || raw == nil {
			continue
		}
		digest[key] = raw
	}
	return digest
}

// matchOptionDigest rebuilds the room's option snapshot from a live match. The
// match keeps these in ModeState because that is what survives alongside the
// match row, but only the fixed option keys are used -- ModeState also carries
// runtime bookkeeping that must never reach a hash.
func matchOptionDigest(match *matchState) map[string]any {
	if match == nil {
		return map[string]any{}
	}
	params := map[string]any{
		"boss_id":  match.BossID,
		"stage_id": match.StageID,
	}
	if hp := intFromAnyValue(match.ModeState["boss_hp"]); hp > 0 {
		params["boss_health"] = hp
	}
	if ticks := intFromAnyValue(match.ModeState["max_ticks"]); ticks > 0 {
		params["max_ticks"] = ticks
	}
	if seats := intFromAnyValue(match.ModeState["coop_players"]); seats > 0 {
		params["coop_players"] = seats
	}
	return roomOptionDigest(params)
}

// allocationBossHP reads the boss health the room chose, for the agent to pass on.
func allocationBossHP(match *matchState) int {
	if match == nil {
		return 0
	}
	return intFromAnyValue(match.ModeState["boss_hp"])
}
