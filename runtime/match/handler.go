package match

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/Phantasm-Klash/Gensoulkyo/runtime/cards"
	"github.com/Phantasm-Klash/Gensoulkyo/runtime/config"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	MessageReady     = 1
	MessageInput     = 2
	MessageReconnect = 3
	OpMatchStart     = 101
	OpSnapshot       = 102
	OpMatchEnd       = 103
)

type Match struct{}

type RuntimeState struct {
	MatchID       string
	Authoritative State
	InputBuffers  map[string]map[int]InputPacket
	InputGuard    InputGuard
	Started       bool
	Persisted     bool
	Completed     bool
	EndTick       int
	SnapshotEvery int
	Mode          string
	DeckSnapshots map[string]cards.DeckSnapshot
}

func (m *Match) MatchInit(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, params map[string]interface{}) (interface{}, int, string) {
	matchID, _ := params["match_id"].(string)
	if matchID == "" {
		matchID = randomID()
	}
	seed, _ := params["server_seed"].(string)
	if seed == "" {
		seed = randomSeed()
	}
	endTick := intFromParam(params, "end_tick", config.DefaultTickRate*180)
	userIDs := userIDsFromParam(params)
	mode, _ := params["mode"].(string)
	if mode == "" {
		mode = "duel"
	}
	deckSnapshots := deckSnapshotsFromParam(params)
	state := NewState(matchID, config.CurrentRulesetVersion, seed, userIDs)
	return &RuntimeState{
		MatchID:       matchID,
		Authoritative: state,
		InputBuffers:  make(map[string]map[int]InputPacket),
		InputGuard:    NewInputGuard(),
		EndTick:       endTick,
		SnapshotEvery: config.DefaultSnapshotEvery,
		Mode:          mode,
		DeckSnapshots: deckSnapshots,
	}, config.DefaultTickRate, ""
}

func (m *Match) MatchJoinAttempt(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, presence runtime.Presence, metadata map[string]string) (interface{}, bool, string) {
	s := mustState(state)
	if !canJoin(s, presence.GetUserId()) {
		return s, false, "user is not a participant in this match"
	}
	return state, true, ""
}

func (m *Match) MatchJoin(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, presences []runtime.Presence) interface{} {
	s := mustState(state)
	for _, presence := range presences {
		userID := presence.GetUserId()
		if !canJoin(s, userID) {
			_ = dispatcher.MatchKick([]runtime.Presence{presence})
			continue
		}
		if _, ok := s.Authoritative.Players[userID]; !ok {
			player := PlayerState{
				UserID:    userID,
				Side:      len(s.Authoritative.Players),
				X:         startX(len(s.Authoritative.Players)),
				Bombs:     1,
				LastInput: NeutralInput(s.Authoritative.Tick),
				Connected: true,
			}
			s.Authoritative.Players[userID] = player
		}
		_ = dispatcher.BroadcastMessage(OpSnapshot, mustJSON(s.Authoritative.Snapshot(true)), []runtime.Presence{presence}, nil, true)
	}
	return s
}

func (m *Match) MatchLeave(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, presences []runtime.Presence) interface{} {
	s := mustState(state)
	for _, presence := range presences {
		userID := presence.GetUserId()
		player := s.Authoritative.Players[userID]
		player.Connected = false
		s.Authoritative.Players[userID] = player
		event := Event{Tick: s.Authoritative.Tick, Type: "disconnect", Payload: map[string]interface{}{"user_id": userID}}
		s.Authoritative.Events = append(s.Authoritative.Events, event)
		if s.Persisted {
			if err := persistEvents(ctx, db, s.Authoritative.MatchID, []Event{event}); err != nil {
				logger.Error("failed to persist disconnect event: %v", err)
			}
		}
	}
	return s
}

func (m *Match) MatchLoop(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, messages []runtime.MatchData) interface{} {
	s := mustState(state)
	for _, message := range messages {
		userID := message.GetUserId()
		switch message.GetOpCode() {
		case MessageReady:
			var payload struct {
				Ready bool `json:"ready"`
			}
			_ = json.Unmarshal(message.GetData(), &payload)
			s.Authoritative.SetReady(userID, payload.Ready)
		case MessageInput:
			var input InputPacket
			if err := json.Unmarshal(message.GetData(), &input); err == nil {
				if err := s.InputGuard.Validate(userID, input, s.Authoritative.Tick); err != nil {
					s.Authoritative.Events = append(s.Authoritative.Events, Event{Tick: s.Authoritative.Tick, Type: "invalid_input", Payload: map[string]interface{}{"user_id": userID, "reason": err.Error()}})
					continue
				}
				if s.InputBuffers[userID] == nil {
					s.InputBuffers[userID] = make(map[int]InputPacket)
				}
				s.InputBuffers[userID][input.Tick] = input
				s.InputGuard.Accept(userID, input)
			}
		case MessageReconnect:
			s.Authoritative.Events = append(s.Authoritative.Events, Event{Tick: s.Authoritative.Tick, Type: "reconnect", Payload: map[string]interface{}{"user_id": userID}})
			_ = dispatcher.BroadcastMessage(OpSnapshot, mustJSON(s.Authoritative.Snapshot(true)), []runtime.Presence{messagePresence{message}}, nil, true)
		}
	}

	if !s.Started && allReady(s.Authoritative.Players) {
		s.Started = true
		if err := persistMatchStarted(ctx, db, s); err != nil {
			logger.Error("failed to persist match start: %v", err)
		} else {
			s.Persisted = true
		}
		_ = dispatcher.BroadcastMessage(OpMatchStart, mustJSON(map[string]interface{}{
			"match_id":        s.Authoritative.MatchID,
			"server_seed":     s.Authoritative.ServerSeed,
			"ruleset_version": s.Authoritative.RulesetVersion,
			"tick":            s.Authoritative.Tick,
		}), nil, nil, true)
	}
	if !s.Started {
		return s
	}

	s.Authoritative.Tick++
	for userID, player := range s.Authoritative.Players {
		input := takeInput(s.InputBuffers[userID], s.Authoritative.Tick)
		if IsEmptyInput(input) {
			input = RepeatableInput(player.LastInput, s.Authoritative.Tick)
		}
		s.Authoritative.ApplyInput(userID, input)
	}

	if s.Persisted && len(s.Authoritative.Events) > 0 {
		if err := persistEvents(ctx, db, s.Authoritative.MatchID, s.Authoritative.Events); err != nil {
			logger.Error("failed to persist match events: %v", err)
		}
	}
	if s.Authoritative.Tick%s.SnapshotEvery == 0 {
		if s.Persisted {
			if err := persistStateHashCheckpoint(ctx, db, s.Authoritative); err != nil {
				logger.Error("failed to persist match checkpoint: %v", err)
			}
		}
		_ = dispatcher.BroadcastMessage(OpSnapshot, mustJSON(s.Authoritative.Snapshot(false)), nil, nil, true)
	}
	if s.Authoritative.Tick >= s.EndTick {
		snapshot := s.Authoritative.Snapshot(true)
		applySettlementSummary(&snapshot)
		if err := completeMatch(ctx, db, s, snapshot, "completed"); err != nil {
			logger.Error("failed to complete match persistence: %v", err)
		}
		_ = dispatcher.BroadcastMessage(OpMatchEnd, mustJSON(snapshot), nil, nil, true)
		return nil
	}
	s.Authoritative.Events = nil
	return s
}

type messagePresence struct {
	runtime.MatchData
}

func (m *Match) MatchTerminate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, graceSeconds int) interface{} {
	s := mustState(state)
	snapshot := s.Authoritative.Snapshot(true)
	applySettlementSummary(&snapshot)
	if err := completeMatch(ctx, db, s, snapshot, "terminated"); err != nil {
		logger.Error("failed to terminate match persistence: %v", err)
	}
	_ = dispatcher.BroadcastMessage(OpMatchEnd, mustJSON(snapshot), nil, nil, true)
	return s
}

func (m *Match) MatchSignal(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, data string) (interface{}, string) {
	return state, ""
}

func allReady(players map[string]PlayerState) bool {
	if len(players) == 0 {
		return false
	}
	for _, player := range players {
		if !player.Ready {
			return false
		}
	}
	return true
}

func takeInput(buffer map[int]InputPacket, tick int) InputPacket {
	if buffer == nil {
		return InputPacket{}
	}
	input, ok := buffer[tick]
	if !ok {
		return InputPacket{}
	}
	delete(buffer, tick)
	return input
}

func mustState(state interface{}) *RuntimeState {
	if s, ok := state.(*RuntimeState); ok {
		return s
	}
	return &RuntimeState{
		Authoritative: NewState(randomID(), config.CurrentRulesetVersion, randomSeed(), nil),
		InputBuffers:  make(map[string]map[int]InputPacket),
		InputGuard:    NewInputGuard(),
		SnapshotEvery: config.DefaultSnapshotEvery,
		Mode:          "duel",
		DeckSnapshots: make(map[string]cards.DeckSnapshot),
	}
}

func canJoin(state *RuntimeState, userID string) bool {
	if state == nil || userID == "" {
		return false
	}
	if len(state.Authoritative.Players) == 0 {
		return true
	}
	_, ok := state.Authoritative.Players[userID]
	return ok
}

func mustJSON(value interface{}) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

func userIDsFromParam(params map[string]interface{}) []string {
	raw, ok := params["user_ids"].([]interface{})
	if !ok {
		return nil
	}
	userIDs := make([]string, 0, len(raw))
	for _, item := range raw {
		if userID, ok := item.(string); ok && userID != "" {
			userIDs = append(userIDs, userID)
		}
	}
	sort.Strings(userIDs)
	return userIDs
}

func deckSnapshotsFromParam(params map[string]interface{}) map[string]cards.DeckSnapshot {
	raw, ok := params["deck_snapshots"].(map[string]interface{})
	if !ok {
		return make(map[string]cards.DeckSnapshot)
	}
	snapshots := make(map[string]cards.DeckSnapshot, len(raw))
	for userID, value := range raw {
		encoded, err := json.Marshal(value)
		if err != nil {
			continue
		}
		var snapshot cards.DeckSnapshot
		if err := json.Unmarshal(encoded, &snapshot); err != nil {
			continue
		}
		if snapshot.DeckID == "" || len(snapshot.CardIDs) == 0 {
			continue
		}
		snapshots[userID] = snapshot
	}
	return snapshots
}

func intFromParam(params map[string]interface{}, key string, fallback int) int {
	value, ok := params[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return fallback
	}
}

func randomID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return hex.EncodeToString(bytes[0:4]) + "-" +
		hex.EncodeToString(bytes[4:6]) + "-" +
		hex.EncodeToString(bytes[6:8]) + "-" +
		hex.EncodeToString(bytes[8:10]) + "-" +
		hex.EncodeToString(bytes[10:16])
}

func randomSeed() string {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(bytes[:])
}
