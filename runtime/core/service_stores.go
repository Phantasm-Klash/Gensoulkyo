package core

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// This file is the wiring layer between Service and the persistence seams in
// room_store.go / match_store.go / ticket_store.go. It answers three questions
// every caller needs answered the same way: which store owns which state, how a
// room/match/ticket round-trips between the live representation the request
// handlers use and the durable record, and what happens when the durable write
// fails.
//
// Ownership:
//
//	rooms   -> RoomStore         (lobby_rooms + lobby_room_members)
//	matches -> MatchStore        (lobby_matches + lobby_match_players), for the
//	                             lifecycle only: the per-tick battle simulation
//	                             cannot be and must not be written every frame
//	tickets -> BattleTicketStore (battle_tickets)
//
// Concurrency: every helper here is *Locked and must be called with Service.mu
// held. The mutex is not replaced by the stores — it serialises the compound
// read-modify-write sequences inside one lobby process exactly as it did when
// those sequences walked the maps, and the SQL layer adds the cross-process
// guarantees a map could never give (Consume is guarded on status = 'issued',
// so two lobbies cannot both win the same ticket). The lock protects the
// sequence; the store protects the row.
//
// Failure policy: a durable write that fails is propagated. Silently answering
// "ok" for a room that never reached disk would promise a wait the next
// request cannot find. StoreStatus additionally records the failure so it is
// visible to operators rather than only to the unlucky caller.

// StoreStatus mirrors the lifecycle-audit status blocks: it tells an operator
// whether this process is actually talking to durable storage or quietly
// running on the process-local fallback.
type StoreStatus struct {
	OK                  bool      `json:"ok"`
	Configured          bool      `json:"configured"`
	RoomsConfigured     bool      `json:"rooms_configured"`
	MatchesConfigured   bool      `json:"matches_configured"`
	TicketsConfigured   bool      `json:"tickets_configured"`
	Writes              int       `json:"writes"`
	Reads               int       `json:"reads"`
	ReapedRooms         int       `json:"reaped_rooms"`
	ReapedMatches       int       `json:"reaped_matches"`
	ReapedTickets       int       `json:"reaped_tickets"`
	LastOperation       string    `json:"last_operation,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	LastErrorAt         time.Time `json:"last_error_at,omitempty"`
	ServerAuthoritative bool      `json:"server_authoritative"`
}

// Default TTLs. A room that never fills and a match stuck in "loading" are the
// two shapes that used to leak forever: the lobby had neither a sweep nor a
// place to write one to. Both records now carry expires_at and the Sweep*
// methods close whatever is past due.
const (
	// DefaultRoomTTL bounds how long a room may sit waiting for players.
	DefaultRoomTTL = 30 * time.Minute
	// DefaultMatchTTL bounds how long a match may stay unfinished — the shape
	// where a player never sends match.ready, or the battle server never
	// reports back.
	DefaultMatchTTL = 30 * time.Minute
	// DefaultSweepLimit caps one pass so a pathological backlog cannot turn a
	// tick into a full-table rewrite.
	DefaultSweepLimit = 200
)

func roomStoreOrDefault(store RoomStore) RoomStore {
	if store != nil {
		return store
	}
	return NewMemoryRoomStore()
}

func matchStoreOrDefault(store MatchStore) MatchStore {
	if store != nil {
		return store
	}
	return NewMemoryMatchStore()
}

func battleTicketStoreOrDefault(store BattleTicketStore) BattleTicketStore {
	if store != nil {
		return store
	}
	return NewMemoryBattleTicketStore()
}

func queueTicketStoreOrDefault(store QueueTicketStore) QueueTicketStore {
	if store != nil {
		return store
	}
	return NewMemoryQueueTicketStore()
}

// RuntimeStoreStatus reports which seams are wired to durable storage.
//
// Configured is true when any seam was injected; OK additionally requires that
// no durable write has failed. The per-seam flags let a deployment verify it
// did not end up half sql / half memory by accident.
func (s *Service) RuntimeStoreStatus() StoreStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.storeStatus
	status.Configured = status.RoomsConfigured || status.MatchesConfigured || status.TicketsConfigured
	status.OK = status.Configured && status.LastError == ""
	status.ServerAuthoritative = true
	return status
}

// recordStoreOutcomeLocked updates the shared counter after a durable call. It
// does not swallow anything: the caller has already decided to propagate an
// error, this only makes success and failure observable.
func (s *Service) recordStoreOutcomeLocked(operation string, readOnly bool, err error) {
	if readOnly {
		s.storeStatus.Reads++
	} else {
		s.storeStatus.Writes++
	}
	s.storeStatus.LastOperation = operation
	if err != nil {
		s.storeStatus.OK = false
		s.storeStatus.LastError = err.Error()
		s.storeStatus.LastErrorAt = s.clock()
		return
	}
	if s.storeStatus.LastError != "" {
		s.storeStatus.LastError = ""
		s.storeStatus.OK = true
	}
}

// --- rooms -------------------------------------------------------------------

func roomIsTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case RoomStatusClosed, RoomStatusCancelled, RoomStatusExpired:
		return true
	}
	return false
}

// loadRoomRecordLocked reads one room straight from the store. A missing row
// reports ok=false with no error.
func (s *Service) loadRoomRecordLocked(roomCode string) (RoomRecord, bool, error) {
	code := normalizeRoomCode(roomCode)
	if code == "" {
		return RoomRecord{}, false, nil
	}
	record, ok, err := s.roomStore.LoadRoom(code)
	s.recordStoreOutcomeLocked("room_load", true, err)
	if err != nil || !ok {
		return record, ok, err
	}
	if err := s.hydrateRoomMembersLocked(&record); err != nil {
		return RoomRecord{}, false, err
	}
	return record, true, nil
}

// loadLiveRoomRecordLocked is loadRoomRecordLocked with terminal rooms folded
// into "not found". This preserves the pre-persistence contract: when the last
// member left, the room used to vanish from the map and every reader saw
// `not_found`; it is now closed in place and must still read as absent.
func (s *Service) loadLiveRoomRecordLocked(roomCode string) (RoomRecord, bool, error) {
	record, ok, err := s.loadRoomRecordLocked(roomCode)
	if err != nil || !ok || roomIsTerminal(record.Status) {
		return RoomRecord{}, false, err
	}
	return record, true, nil
}

// saveRoomLocked writes the room back to the store. Callers must hold s.mu.
func (s *Service) saveRoomLocked(record RoomRecord) error {
	record.RoomCode = normalizeRoomCode(record.RoomCode)
	if record.Status == "" {
		record.Status = RoomStatusWaiting
	}
	err := s.roomStore.SaveRoom(record)
	s.recordStoreOutcomeLocked("room_save", false, err)
	return err
}

// queueTicketLocked resolves a ticket by id, falling back to the store once.
//
// The in-process map stays the hot path: a lookup that hits it costs nothing.
// A miss consults the store, because after a restart the map is empty while the
// tickets are still durable -- that is the case this fallback exists for. A miss
// on an id that truly does not exist is remembered, so a caller polling a stale
// ticket does not open a database round trip on every tick. Callers must hold
// s.mu.
func (s *Service) queueTicketLocked(ticketID string) *queueTicket {
	if ticketID == "" {
		return nil
	}
	if ticket := s.tickets[ticketID]; ticket != nil {
		return ticket
	}
	if s.missingQueueTickets[ticketID] {
		return nil
	}
	record, ok, err := s.queueTicketStore.Load(ticketID)
	s.recordStoreOutcomeLocked("queue_ticket_load", true, err)
	if err != nil || !ok {
		// A read failure is treated like an absent ticket here: every caller
		// already has to handle "no such ticket", and a transient database
		// error must not take a whole request path down. The outcome recorder
		// above is what surfaces the failure.
		s.missingQueueTickets[ticketID] = true
		return nil
	}
	ticket := record.toQueueTicket()
	s.tickets[ticketID] = &ticket
	delete(s.missingQueueTickets, ticketID)
	return &ticket
}

// queueTicketByIDLocked is queueTicketLocked with the map-lookup signature the
// call sites already use. Callers must hold s.mu.
func (s *Service) queueTicketByIDLocked(ticketID string) (*queueTicket, bool) {
	ticket := s.queueTicketLocked(ticketID)
	return ticket, ticket != nil
}

// saveQueueTicketLocked writes a ticket through to the store. Callers must hold
// s.mu.
//
// The write is not best-effort: a ticket the lobby handed out but did not
// persist would vanish on the next restart while the client still holds it, so
// the failure is returned to the operation that caused it.
func (s *Service) saveQueueTicketLocked(ticket *queueTicket) error {
	if ticket == nil {
		return nil
	}
	err := s.queueTicketStore.Save(queueTicketRecordFromTicket(ticket))
	s.recordStoreOutcomeLocked("queue_ticket_save", false, err)
	if err == nil {
		delete(s.missingQueueTickets, ticket.TicketID)
	}
	return err
}

// deleteQueueTicketLocked drops a ticket from memory and from the store.
// Callers must hold s.mu.
func (s *Service) deleteQueueTicketLocked(ticketID string) error {
	delete(s.tickets, ticketID)
	delete(s.missingQueueTickets, ticketID)
	err := s.queueTicketStore.Delete(ticketID)
	s.recordStoreOutcomeLocked("queue_ticket_delete", false, err)
	return err
}

// roomMemberFromTicket projects a queue ticket into its durable seat record.
//
// Everything a room snapshot needs to name a participant again after a restart
// travels here: the seat index, whether it belongs to the host, and the deck and
// loadout the player queued with.
func roomMemberFromTicket(room RoomRecord, ticket queueTicket, seatIndex int, isHost bool) RoomMemberRecord {
	return RoomMemberRecord{
		RoomCode:     room.RoomCode,
		UserID:       ticket.UserID,
		TicketID:     ticket.TicketID,
		SeatIndex:    seatIndex,
		IsHost:       isHost,
		DeckSnapshot: ticket.DeckSnapshot,
		Loadout:      ticket.Loadout,
		BossID:       ticket.BossID,
		JoinedAt:     ticket.CreatedAt,
	}
}

// saveMemberLocked persists one seat. Callers must hold s.mu.
//
// Seats are written next to the room, in the same critical section: a room row
// that lists ticket ids nobody can resolve is worse than no room row at all,
// because it reads as an empty but healthy room.
func (s *Service) saveMemberLocked(member RoomMemberRecord) error {
	member.RoomCode = normalizeRoomCode(member.RoomCode)
	err := s.roomStore.SaveMember(member)
	s.recordStoreOutcomeLocked("room_member_save", false, err)
	return err
}

// removeMemberLocked stamps left_at for a departing seat instead of deleting
// the row, so a departure stays auditable. Callers must hold s.mu.
func (s *Service) removeMemberLocked(roomCode string, userID string, at time.Time) error {
	err := s.roomStore.RemoveMember(normalizeRoomCode(roomCode), userID, at)
	s.recordStoreOutcomeLocked("room_member_remove", false, err)
	return err
}

// hydrateRoomMembersLocked fills record.Members from the store.
//
// A read failure propagates rather than being swallowed: a room whose seats
// cannot be read would render as empty, and an empty room is indistinguishable
// from a room that lost every player. Callers must hold s.mu.
func (s *Service) hydrateRoomMembersLocked(record *RoomRecord) error {
	if record == nil || record.RoomCode == "" {
		return nil
	}
	members, err := s.roomStore.ListMembers(normalizeRoomCode(record.RoomCode))
	s.recordStoreOutcomeLocked("room_member_list", true, err)
	if err != nil {
		return err
	}
	record.Members = members
	return nil
}

// closeRoomRecordLocked moves the room to a terminal status. Callers must hold
// s.mu.
func (s *Service) closeRoomRecordLocked(roomCode string, status string, at time.Time) error {
	err := s.roomStore.CloseRoom(normalizeRoomCode(roomCode), status, at)
	s.recordStoreOutcomeLocked("room_close", false, err)
	return err
}

// markRoomFoundLocked records that the room produced a match.
func (s *Service) markRoomFoundLocked(roomCode string, matchID string, at time.Time) error {
	err := s.roomStore.MarkRoomFound(normalizeRoomCode(roomCode), matchID, at)
	s.recordStoreOutcomeLocked("room_found", false, err)
	return err
}

// listRoomsByStatusLocked reads rooms straight from the store, terminal ones
// included. A limit <= 0 falls back to the store default.
func (s *Service) listRoomsByStatusLocked(statuses []string, limit int) ([]RoomRecord, error) {
	records, err := s.roomStore.ListRoomsByStatus(statuses, limit)
	s.recordStoreOutcomeLocked("room_list", true, err)
	if err != nil {
		return nil, err
	}
	for index := range records {
		if err := s.hydrateRoomMembersLocked(&records[index]); err != nil {
			return nil, err
		}
	}
	return records, nil
}

// SweepExpiredRooms closes rooms whose expires_at is past and returns how many
// it reaped. It is caller-driven on purpose: the lobby has no background
// ticker, so whoever owns the process decides when a pass happens (the same way
// the battle agent poll works).
func (s *Service) SweepExpiredRooms(now time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweepExpiredRoomsLocked(now, limit)
}

func (s *Service) sweepExpiredRoomsLocked(now time.Time, limit int) (int, error) {
	if now.IsZero() {
		now = s.clock()
	}
	if limit <= 0 {
		limit = DefaultSweepLimit
	}
	rooms, err := s.roomStore.ListExpiringRooms(now, limit)
	s.recordStoreOutcomeLocked("room_list_expiring", true, err)
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, room := range rooms {
		if err := s.roomStore.CloseRoom(room.RoomCode, RoomStatusExpired, now); err != nil {
			s.recordStoreOutcomeLocked("room_close", false, err)
			return reaped, err
		}
		s.recordStoreOutcomeLocked("room_close", false, nil)
		reaped++
	}
	s.storeStatus.ReapedRooms += reaped
	return reaped, nil
}

// --- matches -----------------------------------------------------------------

// matchRecordFromStateLocked projects the durable subset of a live match. Only
// settlement-relevant fields leave the process: writing the per-tick simulation
// to Postgres every frame would overstate what survives, because the
// authoritative record of a finished match is the signed callback result.
func (s *Service) matchRecordFromStateLocked(match *matchState) MatchRecord {
	record := MatchRecord{
		MatchID:            match.MatchID,
		ModeID:             match.ModeID,
		RoomCode:           matchRoomCodeForRecordLocked(s, match.MatchID),
		Status:             firstNonEmptyCore(match.Status, MatchStatusLoading),
		BossID:             match.BossID,
		StageID:            match.StageID,
		RulesetVersion:     match.RulesetVersion,
		ModeRulesetVersion: match.ModeRulesetVersion,
		RewardTableID:      match.RewardTableID,
		ServerSeed:         match.ServerSeed,
		Tick:               match.Tick,
		BossHP:             match.BossHP,
		BossMaxHP:          match.BossMaxHP,
		PlayerCount:        len(match.PlayerIDs),
		BattleResultHash:   match.BattleResultHash,
		BattleResultReplay: match.BattleResultReplay,
		BattleResultKeyID:  match.BattleResultKeyID,
		BattleResultAt:     match.BattleResultAt,
		CreatedAt:          match.CreatedAt,
		StartedAt:          match.StartedAt,
		EndedAt:            match.EndedAt,
		UpdatedAt:          s.clock(),
	}
	if allocation := copyBattleAllocation(match.BattleAllocation); allocation.MatchID != "" {
		record.BattleAllocation = &allocation
	}
	if record.ExpiresAt.IsZero() {
		record.ExpiresAt = record.CreatedAt.Add(DefaultMatchTTL)
	}
	return record
}

// matchRoomCodeForRecordLocked recovers the room a match came from. Every
// ticket that produced the match carries its room code, so a restarted lobby
// can still answer "which room did this match come from" without the room.
func matchRoomCodeForRecordLocked(s *Service, matchID string) string {
	for _, ticket := range s.tickets {
		if ticket != nil && ticket.MatchID == matchID && ticket.RoomCode != "" {
			return ticket.RoomCode
		}
	}
	return ""
}

// persistMatchLocked writes the match row and one row per player. It runs at
// every lifecycle transition and never during a tick.
func (s *Service) persistMatchLocked(match *matchState) error {
	if match == nil {
		return nil
	}
	if err := s.matchStore.SaveMatch(s.matchRecordFromStateLocked(match)); err != nil {
		s.recordStoreOutcomeLocked("match_save", false, err)
		return err
	}
	s.recordStoreOutcomeLocked("match_save", false, nil)
	for index, userID := range match.PlayerIDs {
		player := match.Players[userID]
		if player == nil {
			continue
		}
		playerRecord := MatchPlayerRecord{
			MatchID:         match.MatchID,
			UserID:          player.UserID,
			SeatIndex:       index,
			DisplayName:     player.DisplayName,
			BossID:          match.BossID,
			DeckSnapshot:    copyDeckSnapshot(player.DeckSnapshot),
			Loadout:         player.Loadout,
			Ready:           player.Ready,
			Score:           player.Score,
			WorldBossDamage: match.WorldBossDamage,
			JoinedAt:        match.CreatedAt,
		}
		if err := s.matchStore.SavePlayer(playerRecord); err != nil {
			s.recordStoreOutcomeLocked("match_player_save", false, err)
			return err
		}
		s.recordStoreOutcomeLocked("match_player_save", false, nil)
	}
	return nil
}

// updateMatchStatusLocked persists a pure status change (loading -> running,
// running -> abandoned, ...).
func (s *Service) updateMatchStatusLocked(match *matchState, status string, at time.Time) error {
	err := s.matchStore.UpdateMatchStatus(match.MatchID, status, at)
	s.recordStoreOutcomeLocked("match_status", false, err)
	return err
}

// settleMatchRecordLocked stamps the verified battle result onto the durable
// row. That is what lets a restarted lobby reject a replayed callback: the row
// already says "ended, with this hash".
func (s *Service) settleMatchRecordLocked(match *matchState, at time.Time) error {
	err := s.matchStore.SettleMatch(match.MatchID, match.BattleResultHash, match.BattleResultReplay, match.BattleResultKeyID, at)
	s.recordStoreOutcomeLocked("match_settle", false, err)
	return err
}

// setMatchAllocationRecordLocked stamps an allocation onto the durable row of a
// match that has no live simulation in this process (a match whose row survived
// a restart). Without it an allocation update would silently evaporate and the
// next reader would hand clients the pre-restart endpoint.
func (s *Service) setMatchAllocationRecordLocked(matchID string, allocation *BattleServerAllocation) error {
	record, ok, err := s.matchStore.LoadMatch(matchID)
	s.recordStoreOutcomeLocked("match_load", true, err)
	if err != nil || !ok {
		return err
	}
	if allocation != nil {
		allocationCopy := copyBattleAllocation(allocation)
		record.BattleAllocation = &allocationCopy
	}
	record.UpdatedAt = s.clock()
	if err := s.matchStore.SaveMatch(record); err != nil {
		s.recordStoreOutcomeLocked("match_save", false, err)
		return err
	}
	s.recordStoreOutcomeLocked("match_save", false, nil)
	return nil
}

// loadMatchRecordLocked reads the durable row for a match that may no longer
// have live simulation state in this process.
func (s *Service) loadMatchRecordLocked(matchID string) (MatchRecord, bool, error) {
	record, ok, err := s.matchStore.LoadMatch(matchID)
	s.recordStoreOutcomeLocked("match_load", true, err)
	return record, ok, err
}

// SweepExpiredMatches marks matches that are still unfinished past their TTL as
// expired, so the battle agent stops polling them and operators can count them.
func (s *Service) SweepExpiredMatches(now time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweepExpiredMatchesLocked(now, limit)
}

func (s *Service) sweepExpiredMatchesLocked(now time.Time, limit int) (int, error) {
	if now.IsZero() {
		now = s.clock()
	}
	if limit <= 0 {
		limit = DefaultSweepLimit
	}
	matches, err := s.matchStore.ListExpiringMatches(now, limit)
	s.recordStoreOutcomeLocked("match_list_expiring", true, err)
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, record := range matches {
		if err := s.matchStore.UpdateMatchStatus(record.MatchID, MatchStatusExpired, now); err != nil {
			s.recordStoreOutcomeLocked("match_status", false, err)
			return reaped, err
		}
		s.recordStoreOutcomeLocked("match_status", false, nil)
		reaped++
	}
	s.storeStatus.ReapedMatches += reaped
	return reaped, nil
}

// --- battle tickets ----------------------------------------------------------

// issueBattleTicketRecordLocked writes a freshly signed ticket through the
// store, including the exact envelope handed to the battle server so a
// restarted lobby can re-verify a late callback.
func (s *Service) issueBattleTicketRecordLocked(signed *SignedBattleTicket) error {
	if signed == nil {
		return nil
	}
	envelope, err := json.Marshal(signed)
	if err != nil {
		s.recordStoreOutcomeLocked("battle_ticket_issue", false, err)
		return err
	}
	record := BattleTicketRecord{
		TicketID:            signed.Ticket.TicketID,
		MatchID:             signed.Ticket.MatchID,
		UserID:              signed.Ticket.UserID,
		PlayerID:            signed.Ticket.PlayerID,
		ModeID:              signed.Ticket.ModeID,
		BattleServerID:      signed.Ticket.BattleServerID,
		Endpoint:            signed.Ticket.Endpoint,
		KeyID:               signed.KeyID,
		SignatureHex:        signed.SignatureHex,
		NonceHex:            signed.Ticket.TicketNonceHex,
		RulesetVersion:      signed.Ticket.RulesetVersion,
		ProtocolVersion:     fmt.Sprintf("%d", signed.Ticket.Version.ProtocolVersion),
		BusinessAPIVersion:  signed.Ticket.Version.BusinessAPIVersion,
		BattleAPIVersion:    signed.Ticket.Version.BattleAPIVersion,
		DeckSnapshotHash:    signed.Ticket.DeckSnapshotHash,
		TicketJSON:          string(envelope),
		Status:              TicketStatusIssued,
		IssuedAt:            signed.Ticket.IssuedAt,
		ExpiresAt:           signed.Ticket.ExpiresAt,
		ServerAuthoritative: signed.ServerAuthoritative,
	}
	storeErr := s.battleTicketStore.Issue(record)
	s.recordStoreOutcomeLocked("battle_ticket_issue", false, storeErr)
	return storeErr
}

// loadBattleTicketRecordLocked reads one durable ticket row.
func (s *Service) loadBattleTicketRecordLocked(ticketID string) (BattleTicketRecord, bool, error) {
	record, ok, err := s.battleTicketStore.Load(ticketID)
	s.recordStoreOutcomeLocked("battle_ticket_load", true, err)
	return record, ok, err
}

// listBattleTicketsForMatchLocked reads every ticket this lobby ever issued for
// a match, whatever its current state.
func (s *Service) listBattleTicketsForMatchLocked(matchID string) ([]BattleTicketRecord, error) {
	records, err := s.battleTicketStore.ListByMatch(matchID)
	s.recordStoreOutcomeLocked("battle_ticket_list", true, err)
	return records, err
}

// signedBattleTicketFromRecord rebuilds the exact envelope a client was handed
// from its durable copy. The signature was produced by this process' key, so
// the reconstruction is faithful.
func (s *Service) signedBattleTicketFromRecord(record BattleTicketRecord) (*SignedBattleTicket, error) {
	if strings.TrimSpace(record.TicketJSON) == "" {
		return nil, ErrTicketNotFound
	}
	var signed SignedBattleTicket
	if err := json.Unmarshal([]byte(record.TicketJSON), &signed); err != nil {
		return nil, err
	}
	if signed.KeyID == "" {
		signed.KeyID = record.KeyID
	}
	if signed.SignatureHex == "" {
		signed.SignatureHex = record.SignatureHex
	}
	if signed.PublicKeyHex == "" {
		signed.PublicKeyHex = hex.EncodeToString(s.signingPublicKey)
	}
	signed.OK = true
	signed.SignatureAlg = "ED25519"
	signed.ServerAuthoritative = true
	return &signed, nil
}

// SweepExpiredBattleTickets moves issued tickets past their TTL to 'expired'.
// This is the durable counterpart of the old in-process expiry check, which
// also only ran while a request happened to walk the map.
func (s *Service) SweepExpiredBattleTickets(now time.Time, limit int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sweepExpiredBattleTicketsLocked(now, limit)
}

func (s *Service) sweepExpiredBattleTicketsLocked(now time.Time, limit int) (int, error) {
	if now.IsZero() {
		now = s.clock()
	}
	if limit <= 0 {
		limit = DefaultSweepLimit
	}
	expired, err := s.battleTicketStore.Expire(now, limit)
	s.recordStoreOutcomeLocked("battle_ticket_expire", false, err)
	if err != nil {
		return 0, err
	}
	s.storeStatus.ReapedTickets += expired
	return expired, nil
}

// SweepAll runs one pass of every TTL sweep and returns rooms, matches and
// tickets reaped, so the caller does not have to remember the order or take the
// lock three times.
func (s *Service) SweepAll(now time.Time, limit int) (int, int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rooms, err := s.sweepExpiredRoomsLocked(now, limit)
	if err != nil {
		return rooms, 0, 0, err
	}
	matches, err := s.sweepExpiredMatchesLocked(now, limit)
	if err != nil {
		return rooms, matches, 0, err
	}
	tickets, err := s.sweepExpiredBattleTicketsLocked(now, limit)
	return rooms, matches, tickets, err
}
