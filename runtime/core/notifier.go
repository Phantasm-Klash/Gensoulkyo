package core

import (
	"strings"
	"sync/atomic"
)

// Push subjects. These are the names a client switches on; they are part of the
// lobby's external contract, so changing one is a protocol change.
const (
	// PushRoomState fires whenever a room's seat list changes: somebody joined,
	// left, or the room moved status.
	PushRoomState = "phk.room_state"
	// PushMatchStart fires once a match has every player ready and the lobby has
	// an allocation for it.
	PushMatchStart = "phk.match_start"
	// PushMatchResult fires once a match has settled.
	PushMatchResult = "phk.match_result"
)

// LobbyNotifier delivers a server-initiated lobby event to players.
//
// The lobby has no transport of its own: under Nakama realtime is Nakama's
// socket and its protocol is a closed enum, so a push travels as a Nakama
// notification and the client dispatches on the subject. Keeping the interface
// here, with no runtime types in it, is what stops core from importing the
// runtime module it is hosted in.
//
// Notify is called with Service.mu released, from a goroutine, and may block for
// as long as the transport takes. Implementations must be safe for concurrent
// use and must not call back into the Service synchronously.
type LobbyNotifier interface {
	Notify(userIDs []string, subject string, payload map[string]any) error
}

// PushStats reports how the notifier has been doing. It exists for the health
// surface: a silent push outage looks exactly like a quiet lobby otherwise.
type PushStats struct {
	Delivered int64 `json:"delivered"`
	Failed    int64 `json:"failed"`
	Enabled   bool  `json:"enabled"`
}

// PushStats returns the notifier counters.
func (s *Service) PushStats() PushStats {
	return PushStats{
		Delivered: atomic.LoadInt64(&s.pushDelivered),
		Failed:    atomic.LoadInt64(&s.pushFailed),
		Enabled:   s.notifier != nil,
	}
}

// dispatchNotificationLocked fans a payload out to the given players.
//
// Callers hold Service.mu, so delivery is moved to a goroutine: the notifier
// talks to the runtime's socket layer and holding the lobby lock across that
// call would let one slow push stall every lobby request behind it. Duplicate
// and blank ids are dropped here rather than in every caller, because a room's
// player list and its participant list are not always the same shape.
func (s *Service) dispatchNotificationLocked(userIDs []string, subject string, payload map[string]any) {
	if s.notifier == nil || subject == "" {
		return
	}
	seen := make(map[string]struct{}, len(userIDs))
	recipients := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		trimmed := strings.TrimSpace(userID)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		recipients = append(recipients, trimmed)
	}
	if len(recipients) == 0 {
		return
	}
	notifier := s.notifier
	go func() {
		if err := notifier.Notify(recipients, subject, payload); err != nil {
			atomic.AddInt64(&s.pushFailed, 1)
			return
		}
		atomic.AddInt64(&s.pushDelivered, 1)
	}()
}

// notifyRoomStateLocked pushes the room's current snapshot to everyone seated in
// it. Callers must hold s.mu.
func (s *Service) notifyRoomStateLocked(room *RoomRecord) {
	if s.notifier == nil || room == nil {
		return
	}
	snapshot := s.roomSnapshotLocked(room, s.clock())
	userIDs := make([]string, 0, len(snapshot.Participants))
	for _, participant := range snapshot.Participants {
		userIDs = append(userIDs, participant.UserID)
	}
	// A room whose last player just left has nobody to tell, but the host id is
	// still worth a delivery attempt in that case: the client that left is the
	// one that needs to learn the room is gone.
	if len(userIDs) == 0 && strings.TrimSpace(room.HostUserID) != "" {
		userIDs = append(userIDs, room.HostUserID)
	}
	s.dispatchNotificationLocked(userIDs, PushRoomState, map[string]any{"room": snapshot})
}

// notifyMatchStartLocked tells every player the match is live.
//
// Callers must hold s.mu. The signed ticket travels with the event so the client
// does not have to ask for it in a second round trip, and a client that misses
// the push can still fetch the same data from GET /v1/matches/{id}/snapshot.
func (s *Service) notifyMatchStartLocked(match *matchState, event MatchStartEvent, ticket *SignedBattleTicket) {
	if s.notifier == nil || match == nil {
		return
	}
	payload := map[string]any{"match_start": event}
	if ticket != nil {
		payload["battle_ticket"] = ticket
	}
	s.dispatchNotificationLocked(match.PlayerIDs, PushMatchStart, payload)
}

// notifyMatchResultLocked tells every player how the match settled.
//
// Callers must hold s.mu. The payload carries the same settlement records the
// result screen reads from the snapshot, so a client that receives the push can
// render the result immediately.
func (s *Service) notifyMatchResultLocked(match *matchState) {
	if s.notifier == nil || match == nil {
		return
	}
	settlements := make(map[string]any, len(match.PlayerIDs))
	for _, userID := range match.PlayerIDs {
		if settlement, ok := s.settlements[settlementKey(match.MatchID, userID)]; ok {
			settlements[userID] = settlement
		}
	}
	s.dispatchNotificationLocked(match.PlayerIDs, PushMatchResult, map[string]any{
		"match_id":    match.MatchID,
		"settlements": settlements,
	})
}
