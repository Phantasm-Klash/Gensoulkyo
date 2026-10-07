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
	req.MatchID = matchID
	req.ModeID = strings.TrimSpace(req.ModeID)
	req.RulesetVersion = strings.TrimSpace(req.RulesetVersion)
	req.WinnerPlayerID = strings.TrimSpace(req.WinnerPlayerID)
	req.StateHash = strings.TrimSpace(req.StateHash)
	for index := range req.Players {
		req.Players[index].PlayerID = strings.TrimSpace(req.Players[index].PlayerID)
	}
	match := s.matches[matchID]
	if match == nil {
		return nil, newError(codeNotFound, "match not found")
	}

	allocation := s.battleAllocations[matchID]
	if allocation == nil {
		allocation = match.BattleAllocation
	}
	if err := validateBattleResultCallback(req, match, allocation); err != nil {
		s.recordBattleResultCallbackAuditLocked(match, allocation, req, "rejected", ErrorCode(err), now)
		return nil, err
	}
	if match.Status == "ended" {
		if match.BattleResultHash == req.StateHash {
			s.recordBattleResultCallbackAuditLocked(match, allocation, req, "duplicate", "", now)
			return s.battleResultCallbackResponseLocked(match, req, true, now), nil
		}
		err := newError(codeMatchState, "match is already ended")
		s.recordBattleResultCallbackAuditLocked(match, allocation, req, "rejected", ErrorCode(err), now)
		return nil, err
	}
	if match.Status != "running" {
		err := newError(codeMatchState, "battle result requires a running match, got %s", match.Status)
		s.recordBattleResultCallbackAuditLocked(match, allocation, req, "rejected", ErrorCode(err), now)
		return nil, err
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
	s.recordBattleResultCallbackAuditLocked(match, allocation, req, "accepted", "", now)
	return s.battleResultCallbackResponseLocked(match, req, false, now), nil
}

func validateBattleResultCallback(req BattleResultCallback, match *matchState, allocation *BattleServerAllocation) error {
	if match == nil {
		return newError(codeNotFound, "match not found")
	}
	if allocation == nil {
		return newError(codeBattleServer, "battle allocation unavailable")
	}
	if allocation.MatchID != match.MatchID {
		return newError(codeBattleServer, "battle allocation match mismatch")
	}
	if req.ModeID == "" {
		return newError(codeInvalidRequest, "mode_id is required")
	}
	if req.ModeID != match.ModeID || req.ModeID != allocation.ModeID {
		return newError(codeInvalidMode, "battle result mode is %q, match mode is %q", req.ModeID, match.ModeID)
	}
	expectedRuleset := strings.TrimSpace(match.RulesetVersion)
	allocationRuleset := strings.TrimSpace(allocation.Version.RulesetVersion)
	if expectedRuleset != "" && allocationRuleset != "" && expectedRuleset != allocationRuleset {
		return newError(codeBattleServer, "battle allocation ruleset version mismatch")
	}
	if expectedRuleset == "" {
		expectedRuleset = allocationRuleset
	}
	if expectedRuleset == "" {
		expectedRuleset = RulesetVersion
	}
	if req.RulesetVersion == "" {
		return newError(codeInvalidRequest, "ruleset_version is required")
	}
	if req.RulesetVersion != expectedRuleset {
		return newError(codeInvalidRequest, "battle result ruleset version mismatch")
	}
	if strings.TrimSpace(req.StateHash) == "" {
		return newError(codeInvalidRequest, "state_hash is required")
	}
	if match.ServerSeed < 0 || allocation.ServerSeed < 0 || match.ServerSeed != allocation.ServerSeed {
		return newError(codeBattleServer, "battle allocation seed mismatch")
	}
	if req.MatchSeed != uint64(allocation.ServerSeed) {
		return newError(codeInvalidRequest, "match_seed does not match allocation")
	}
	if !sameStringSet(battleResultCallbackPlayerIDs(req), allocationPlayerIDs(allocation)) {
		return newError(codeInvalidRequest, "battle result players do not match allocation")
	}
	if req.WinnerPlayerID == "" || !stringSliceContains(allocationPlayerIDs(allocation), req.WinnerPlayerID) {
		return newError(codeInvalidRequest, "winner_player_id must belong to allocation")
	}
	return nil
}

func battleResultCallbackPlayerIDs(req BattleResultCallback) []string {
	playerIDs := make([]string, 0, len(req.Players))
	for _, player := range req.Players {
		playerIDs = append(playerIDs, strings.TrimSpace(player.PlayerID))
	}
	return playerIDs
}

func (s *Service) recordBattleResultCallbackAuditLocked(match *matchState, allocation *BattleServerAllocation, req BattleResultCallback, status string, reason string, verifiedAt time.Time) {
	if s.battleAuditRepo == nil || match == nil {
		return
	}
	battleServerID := s.allocationBattleServerIDLocked(match)
	if allocation != nil && strings.TrimSpace(allocation.BattleServerID) != "" {
		battleServerID = strings.TrimSpace(allocation.BattleServerID)
	}
	replayID := match.BattleResultReplay
	if replayID == "" {
		replayID = "replay_" + match.MatchID
	}
	settledAt := match.BattleResultAt
	if settledAt.IsZero() {
		settledAt = verifiedAt
	}
	err := s.battleAuditRepo.RecordBattleResultAudit(BattleResultAuditRecord{
		MatchID:             match.MatchID,
		ModeID:              match.ModeID,
		BattleServerID:      battleServerID,
		ResultHash:          strings.TrimSpace(req.StateHash),
		ReplayID:            replayID,
		KeyID:               battleServerID,
		PlayerIDs:           battleResultCallbackPlayerIDs(req),
		SettlementKey:       battleResultSettlementKey(match.MatchID),
		Status:              status,
		RejectReason:        reason,
		VerifiedAt:          verifiedAt,
		SettledAt:           settledAt,
		ServerAuthoritative: true,
	})
	operation := "battle_result"
	if status == "duplicate" {
		operation = "battle_result_duplicate"
	} else if status == "rejected" {
		operation = "battle_result_rejected"
	}
	fingerprint := lifecycleFingerprint("battle:result:"+status, match.MatchID, match.ModeID, battleServerID, req.StateHash, replayID, req.WinnerPlayerID)
	s.recordBattleAuditOutcomeLocked(operation, fingerprint, verifiedAt, err)
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
