package core

import (
	"strings"
	"time"
)

const (
	// Boss race settlement points: the first player to defeat their boss copy
	// wins; everyone who finishes still earns participation points.
	bossRaceWinPoints  = 3
	bossRaceLossPoints = 1
)

// BattleResultPlayer is one player's outcome reported by the C++ battle server.
type BattleResultPlayer struct {
	PlayerID      string `json:"player_id"`
	DamageDealt   int64  `json:"damage_dealt"`
	BossCurrentHP int64  `json:"boss_current_hp"`
}

// BattleResultCallback is the raw JSON payload POSTed by the C++ battle server
// to /internal/battle/result when a match finishes.
type BattleResultCallback struct {
	MatchID        string               `json:"match_id"`
	ModeID         string               `json:"mode_id"`
	RulesetVersion string               `json:"ruleset_version"`
	MatchSeed      uint64               `json:"match_seed"`
	WinnerPlayerID string               `json:"winner_player_id"`
	WinnerTick     uint64               `json:"winner_tick"`
	StateHash      string               `json:"state_hash"`
	Players        []BattleResultPlayer `json:"players"`
}

// BattleResultCallbackResponse summarizes the settlement produced by a battle
// result callback. It is the payload the lobby broadcasts as MatchResultMessage.
type BattleResultCallbackResponse struct {
	OK                  bool           `json:"ok"`
	MatchID             string         `json:"match_id"`
	ModeID              string         `json:"mode_id"`
	WinnerPlayerID      string         `json:"winner_player_id"`
	WinnerUserID        string         `json:"winner_user_id"`
	Points              map[string]int `json:"points"`
	ReplayID            string         `json:"replay_id"`
	StateHash           string         `json:"state_hash"`
	Duplicate           bool           `json:"duplicate"`
	ServerAuthoritative bool           `json:"server_authoritative"`
	ServerTime          time.Time      `json:"server_time"`
}

// ApplyBattleResultCallback settles a match from a trusted battle-server result
// callback. It reuses the standard core settlement path (settlement, rewards,
// progress, replay) but derives the winner from the authoritative battle server
// result instead of the lobby-side simulation score.
//
// The call is idempotent: replaying the same callback (same state hash) returns
// the existing settlement marked as a duplicate.
func (s *Service) ApplyBattleResultCallback(req BattleResultCallback) (*BattleResultCallbackResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	matchID := strings.TrimSpace(req.MatchID)
	if matchID == "" {
		return nil, newError(codeInvalidRequest, "match_id is required")
	}
	match := s.matches[matchID]
	if match == nil {
		return nil, newError(codeNotFound, "match not found")
	}
	if match.Status == "ended" {
		if req.StateHash != "" && match.BattleResultHash == req.StateHash {
			return s.battleResultCallbackResponseLocked(match, req, true, now), nil
		}
		return nil, newError(codeMatchState, "match is already ended")
	}

	playerToUser := s.matchPlayerIDMapLocked(match)
	winnerUserID := ""
	if pid := strings.TrimSpace(req.WinnerPlayerID); pid != "" {
		winnerUserID = playerToUser[pid]
	}

	bossRacePlayers := map[string]any{}
	for _, entry := range req.Players {
		userID, ok := playerToUser[entry.PlayerID]
		if !ok {
			continue
		}
		player := match.Players[userID]
		if player == nil {
			continue
		}
		if entry.DamageDealt > 0 {
			player.DamageDealt = int(entry.DamageDealt)
		}
		bossRacePlayers[entry.PlayerID] = map[string]any{
			"player_id":       entry.PlayerID,
			"damage_dealt":    entry.DamageDealt,
			"boss_current_hp": entry.BossCurrentHP,
		}
	}

	match.BattleResultHash = req.StateHash
	match.BattleResultReplay = "replay_" + match.MatchID
	match.BattleResultKeyID = s.allocationBattleServerIDLocked(match)
	match.BattleResultAt = now
	if match.ModeState == nil {
		match.ModeState = map[string]any{}
	}
	match.ModeState["battle_result_hash"] = req.StateHash
	match.ModeState["battle_result_replay_id"] = match.BattleResultReplay
	match.ModeState["battle_result_key_id"] = match.BattleResultKeyID
	match.ModeState["battle_result_verified"] = true
	match.ModeState["battle_result_owner"] = "battle_server"
	match.ModeState["boss_race"] = map[string]any{
		"winner_player_id": req.WinnerPlayerID,
		"winner_tick":      req.WinnerTick,
		"state_hash":       req.StateHash,
		"ruleset_version":  req.RulesetVersion,
		"players":          bossRacePlayers,
	}
	appendMatchEventLocked(match, MatchEvent{Type: "battle_result_verified", Tick: match.Tick, Status: "accepted"})

	s.settleMatchFromWinnerLocked(match, winnerUserID)
	return s.battleResultCallbackResponseLocked(match, req, false, now), nil
}

// settleMatchFromWinnerLocked mirrors settleMatchLocked but picks the winner
// from an explicit player instead of the lobby-side score, which is what a boss
// race requires (the first boss defeat wins).
func (s *Service) settleMatchFromWinnerLocked(match *matchState, winnerUserID string) {
	match.Status = "ended"
	match.EndedAt = s.clock()
	appendMatchEventLocked(match, MatchEvent{Type: "match_ended", Tick: match.Tick})
	draw := winnerUserID == ""
	for _, userID := range match.PlayerIDs {
		player := match.Players[userID]
		user := s.users[userID]
		if player == nil || user == nil {
			continue
		}
		result := "loss"
		if draw {
			result = "draw"
		} else if userID == winnerUserID {
			result = "win"
		}
		settlement := s.buildSettlementLocked(match, player, user, result)
		s.settlements[settlementKey(match.MatchID, userID)] = settlement
		replay := s.buildReplayRecordLocked(match, player, settlement)
		s.replays[settlement.ReplayID] = replay
		s.recordReplayAuditLocked(replay)
		s.applyRewardsLocked(user, match.MatchID, settlement.RewardJSON)
		s.applyProgressLocked(user, settlement)
	}
}

func (s *Service) matchPlayerIDMapLocked(match *matchState) map[string]string {
	playerToUser := map[string]string{}
	if allocation := s.battleAllocations[match.MatchID]; allocation != nil {
		for _, player := range allocation.Players {
			playerToUser[player.PlayerID] = player.UserID
		}
	}
	for _, userID := range match.PlayerIDs {
		playerToUser[playerIDForUser(match.MatchID, userID)] = userID
	}
	return playerToUser
}

func (s *Service) allocationBattleServerIDLocked(match *matchState) string {
	if allocation := s.battleAllocations[match.MatchID]; allocation != nil {
		return allocation.BattleServerID
	}
	return DefaultBattleServerID
}

func (s *Service) battleResultCallbackResponseLocked(match *matchState, req BattleResultCallback, duplicate bool, now time.Time) *BattleResultCallbackResponse {
	playerToUser := s.matchPlayerIDMapLocked(match)
	winnerPlayerID := strings.TrimSpace(req.WinnerPlayerID)
	winnerUserID := playerToUser[winnerPlayerID]

	points := map[string]int{}
	for playerID := range playerToUser {
		if winnerPlayerID != "" && playerID == winnerPlayerID {
			points[playerID] = bossRaceWinPoints
		} else {
			points[playerID] = bossRaceLossPoints
		}
	}

	replayID := match.BattleResultReplay
	if winnerUserID != "" {
		if settlement := s.settlements[settlementKey(match.MatchID, winnerUserID)]; settlement != nil && settlement.ReplayID != "" {
			replayID = settlement.ReplayID
		}
	}
	modeID := match.ModeID
	if strings.TrimSpace(req.ModeID) != "" {
		modeID = strings.TrimSpace(req.ModeID)
	}
	return &BattleResultCallbackResponse{
		OK:                  true,
		MatchID:             match.MatchID,
		ModeID:              modeID,
		WinnerPlayerID:      winnerPlayerID,
		WinnerUserID:        winnerUserID,
		Points:              points,
		ReplayID:            replayID,
		StateHash:           req.StateHash,
		Duplicate:           duplicate,
		ServerAuthoritative: true,
		ServerTime:          now,
	}
}
