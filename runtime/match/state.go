package match

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

const (
	ArenaMinX = -1000
	ArenaMaxX = 1000
	ArenaMinY = -1000
	ArenaMaxY = 1000
	FastSpeed = 6
	SlowSpeed = 3
)

type PlayerState struct {
	UserID     string      `json:"user_id"`
	Side       int         `json:"side"`
	X          int         `json:"x"`
	Y          int         `json:"y"`
	Score      int64       `json:"score"`
	GrazeCount int         `json:"graze_count"`
	HitCount   int         `json:"hit_count"`
	Bombs      int         `json:"bombs"`
	LastInput  InputPacket `json:"last_input"`
	Ready      bool        `json:"ready"`
	Connected  bool        `json:"connected"`
}

type State struct {
	MatchID        string                 `json:"match_id"`
	Tick           int                    `json:"tick"`
	RulesetVersion string                 `json:"ruleset_version"`
	ServerSeed     string                 `json:"server_seed"`
	Players        map[string]PlayerState `json:"players"`
	Events         []Event                `json:"events"`
}

type Event struct {
	Tick    int         `json:"tick"`
	Type    string      `json:"type"`
	Payload interface{} `json:"payload,omitempty"`
}

type Snapshot struct {
	MatchID      string                 `json:"match_id"`
	Tick         int                    `json:"tick"`
	StateHash    string                 `json:"state_hash"`
	Players      []PlayerState          `json:"players"`
	BulletsDelta []interface{}          `json:"bullets_delta"`
	Score        map[string]int64       `json:"score"`
	ActiveCards  []interface{}          `json:"active_cards"`
	ModeState    map[string]interface{} `json:"mode_state"`
	Settlement   map[string]interface{} `json:"settlement,omitempty"`
	Events       []Event                `json:"events"`
	Full         bool                   `json:"full"`
}

func NewState(matchID, rulesetVersion, seed string, userIDs []string) State {
	userIDs = append([]string(nil), userIDs...)
	sort.Strings(userIDs)
	players := make(map[string]PlayerState, len(userIDs))
	for i, userID := range userIDs {
		players[userID] = PlayerState{
			UserID:    userID,
			Side:      i,
			X:         startX(i),
			Y:         0,
			Bombs:     1,
			LastInput: NeutralInput(0),
			Connected: true,
		}
	}
	return State{
		MatchID:        matchID,
		RulesetVersion: rulesetVersion,
		ServerSeed:     seed,
		Players:        players,
	}
}

func (s *State) SetReady(userID string, ready bool) {
	player := s.Players[userID]
	player.Ready = ready
	player.Connected = true
	s.Players[userID] = player
	s.Events = append(s.Events, Event{Tick: s.Tick, Type: "ready", Payload: map[string]interface{}{"user_id": userID, "ready": ready}})
}

func (s *State) ApplyInput(userID string, input InputPacket) {
	player, ok := s.Players[userID]
	if !ok {
		return
	}
	dir := NormalizeDir(input.Dir)
	speed := FastSpeed
	if input.Slow {
		speed = SlowSpeed
	}
	player.X = clamp(player.X+dir.X*speed, ArenaMinX, ArenaMaxX)
	player.Y = clamp(player.Y+dir.Y*speed, ArenaMinY, ArenaMaxY)
	player.LastInput = input
	player.Connected = true
	if input.Shoot {
		player.Score += 1
	}
	if input.Bomb {
		if player.Bombs > 0 {
			player.Bombs--
			s.Events = append(s.Events, Event{Tick: s.Tick, Type: "bomb", Payload: map[string]interface{}{"user_id": userID}})
		} else {
			s.Events = append(s.Events, Event{Tick: s.Tick, Type: "invalid_input", Payload: map[string]interface{}{"user_id": userID, "reason": "bomb resource is unavailable"}})
		}
	}
	if input.CardSlot >= 0 {
		s.Events = append(s.Events, Event{Tick: s.Tick, Type: "card_request", Payload: map[string]interface{}{"user_id": userID, "card_slot": input.CardSlot}})
	}
	s.Players[userID] = player
}

func (s *State) Snapshot(full bool) Snapshot {
	players := make([]PlayerState, 0, len(s.Players))
	score := make(map[string]int64, len(s.Players))
	for _, player := range s.Players {
		players = append(players, player)
		score[player.UserID] = player.Score
	}
	sort.Slice(players, func(i, j int) bool { return players[i].UserID < players[j].UserID })
	return Snapshot{
		MatchID:      s.MatchID,
		Tick:         s.Tick,
		StateHash:    s.Hash(),
		Players:      players,
		BulletsDelta: []interface{}{},
		Score:        score,
		ActiveCards:  []interface{}{},
		ModeState: map[string]interface{}{
			"ruleset_version": s.RulesetVersion,
			"tick_rate":       30,
		},
		Events: append([]Event(nil), s.Events...),
		Full:   full,
	}
}

func (s State) Hash() string {
	players := make([]PlayerState, 0, len(s.Players))
	for _, player := range s.Players {
		players = append(players, player)
	}
	sort.Slice(players, func(i, j int) bool { return players[i].UserID < players[j].UserID })
	payload := struct {
		Tick    int           `json:"tick"`
		Players []PlayerState `json:"players"`
		Seed    string        `json:"seed"`
	}{
		Tick:    s.Tick,
		Players: players,
		Seed:    s.ServerSeed,
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func startX(index int) int {
	if index%2 == 0 {
		return -320
	}
	return 320
}
