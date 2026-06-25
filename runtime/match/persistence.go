package match

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"

	"github.com/Phantasm-Klash/Gensoulkyo/runtime/cards"
)

type settlementReward struct {
	Points    int64  `json:"points"`
	CardDust  int64  `json:"card_dust"`
	ChestKeys int64  `json:"chest_keys"`
	Reason    string `json:"reason"`
}

func persistMatchStarted(ctx context.Context, db *sql.DB, state *RuntimeState) error {
	if db == nil {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		insert into matches (id, mode, mode_ruleset_version, ruleset_version, server_seed, status, started_at)
		values ($1, $2, $3, $4, $5, 'running', now())
		on conflict (id) do update
		set status = 'running',
		    started_at = coalesce(matches.started_at, excluded.started_at)
	`, state.Authoritative.MatchID, state.Mode, state.Mode, state.Authoritative.RulesetVersion, state.Authoritative.ServerSeed); err != nil {
		return err
	}

	players := sortedPlayers(state.Authoritative.Players)
	for _, player := range players {
		deckSnapshot, err := json.Marshal(deckSnapshotFor(state, player.UserID))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			insert into match_players (match_id, user_id, side, deck_snapshot_json, score, graze_count, hit_count)
			values ($1, $2, $3, $4::jsonb, $5, $6, $7)
			on conflict (match_id, user_id) do update
			set side = excluded.side,
			    deck_snapshot_json = excluded.deck_snapshot_json
		`, state.Authoritative.MatchID, player.UserID, player.Side, deckSnapshot, player.Score, player.GrazeCount, player.HitCount); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func persistEvents(ctx context.Context, db *sql.DB, matchID string, events []Event) error {
	if db == nil || len(events) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := persistEventsTx(ctx, tx, matchID, events); err != nil {
		return err
	}
	return tx.Commit()
}

func persistEventsTx(ctx context.Context, tx *sql.Tx, matchID string, events []Event) error {
	for _, event := range events {
		if event.Type == "" {
			continue
		}
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			insert into match_events (match_id, tick, event_type, payload_json)
			values ($1, $2, $3, $4::jsonb)
		`, matchID, event.Tick, event.Type, payload); err != nil {
			return err
		}
	}
	return nil
}

func persistStateHashCheckpoint(ctx context.Context, db *sql.DB, state State) error {
	if db == nil {
		return nil
	}
	payload, err := json.Marshal(map[string]interface{}{
		"state_hash": state.Hash(),
	})
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		insert into match_events (match_id, tick, event_type, payload_json)
		values ($1, $2, 'state_hash_checkpoint', $3::jsonb)
	`, state.MatchID, state.Tick, payload)
	return err
}

func completeMatch(ctx context.Context, db *sql.DB, state *RuntimeState, snapshot Snapshot, status string) error {
	applySettlementSummary(&snapshot)
	if db == nil || state.Completed {
		state.Completed = true
		return nil
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if !state.Persisted {
		if err := persistMatchStartedTx(ctx, tx, state); err != nil {
			return err
		}
		state.Persisted = true
	}

	resultByUser := resultsFor(snapshot.Players)
	for _, player := range snapshot.Players {
		reward := rewardFor(player, resultByUser[player.UserID])
		rewardJSON, err := json.Marshal(reward)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			update match_players
			set score = $3,
			    graze_count = $4,
			    hit_count = $5,
			    result = $6,
			    reward_json = $7::jsonb
			where match_id = $1 and user_id = $2
		`, state.Authoritative.MatchID, player.UserID, player.Score, player.GrazeCount, player.HitCount, resultByUser[player.UserID], rewardJSON); err != nil {
			return err
		}
		if err := settleReward(ctx, tx, state.Authoritative.MatchID, player.UserID, reward, rewardJSON); err != nil {
			return err
		}
	}

	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into match_events (match_id, tick, event_type, payload_json)
		values ($1, $2, 'match_end', $3::jsonb)
	`, state.Authoritative.MatchID, snapshot.Tick, snapshotJSON); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		update matches
		set status = $2,
		    ended_at = now()
		where id = $1
	`, state.Authoritative.MatchID, status); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	state.Completed = true
	return nil
}

func persistMatchStartedTx(ctx context.Context, tx *sql.Tx, state *RuntimeState) error {
	if _, err := tx.ExecContext(ctx, `
		insert into matches (id, mode, mode_ruleset_version, ruleset_version, server_seed, status, started_at)
		values ($1, $2, $3, $4, $5, 'running', now())
		on conflict (id) do update
		set status = 'running',
		    started_at = coalesce(matches.started_at, excluded.started_at)
	`, state.Authoritative.MatchID, state.Mode, state.Mode, state.Authoritative.RulesetVersion, state.Authoritative.ServerSeed); err != nil {
		return err
	}
	for _, player := range sortedPlayers(state.Authoritative.Players) {
		deckSnapshot, err := json.Marshal(deckSnapshotFor(state, player.UserID))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			insert into match_players (match_id, user_id, side, deck_snapshot_json, score, graze_count, hit_count)
			values ($1, $2, $3, $4::jsonb, $5, $6, $7)
			on conflict (match_id, user_id) do update
			set side = excluded.side,
			    deck_snapshot_json = excluded.deck_snapshot_json
		`, state.Authoritative.MatchID, player.UserID, player.Side, deckSnapshot, player.Score, player.GrazeCount, player.HitCount); err != nil {
			return err
		}
	}
	return nil
}

func settleReward(ctx context.Context, tx *sql.Tx, matchID, userID string, reward settlementReward, rewardJSON []byte) error {
	result, err := tx.ExecContext(ctx, `
		insert into match_reward_settlements (match_id, user_id, reward_json, created_at)
		values ($1, $2, $3::jsonb, now())
		on conflict (match_id, user_id) do nothing
	`, matchID, userID, rewardJSON)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return nil
	}
	if reward.Points == 0 && reward.CardDust == 0 && reward.ChestKeys == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
		insert into player_wallets (user_id, points, card_dust, chest_keys, updated_at)
		values ($1, $2, $3, $4, now())
		on conflict (user_id) do update
		set points = player_wallets.points + excluded.points,
		    card_dust = player_wallets.card_dust + excluded.card_dust,
		    chest_keys = player_wallets.chest_keys + excluded.chest_keys,
		    updated_at = now()
	`, userID, reward.Points, reward.CardDust, reward.ChestKeys)
	return err
}

func resultsFor(players []PlayerState) map[string]string {
	results := make(map[string]string, len(players))
	if len(players) == 0 {
		return results
	}
	ordered := append([]PlayerState(nil), players...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Score == ordered[j].Score {
			return ordered[i].UserID < ordered[j].UserID
		}
		return ordered[i].Score > ordered[j].Score
	})
	if len(ordered) == 1 {
		results[ordered[0].UserID] = "completed"
		return results
	}
	if ordered[0].Score == ordered[1].Score {
		for _, player := range ordered {
			results[player.UserID] = "draw"
		}
		return results
	}
	results[ordered[0].UserID] = "win"
	for _, player := range ordered[1:] {
		results[player.UserID] = "loss"
	}
	return results
}

func rewardFor(player PlayerState, result string) settlementReward {
	reward := settlementReward{
		Points:    10,
		CardDust:  player.Score / 25,
		ChestKeys: 0,
		Reason:    "match_completion",
	}
	if result == "win" || result == "completed" {
		reward.Points += 15
	}
	if player.GrazeCount >= 100 {
		reward.CardDust += 1
	}
	return reward
}

func applySettlementSummary(snapshot *Snapshot) {
	if snapshot == nil {
		return
	}
	resultByUser := resultsFor(snapshot.Players)
	rewards := make(map[string]settlementReward, len(snapshot.Players))
	for _, player := range snapshot.Players {
		rewards[player.UserID] = rewardFor(player, resultByUser[player.UserID])
	}
	snapshot.Settlement = map[string]interface{}{
		"results": resultByUser,
		"rewards": rewards,
	}
	if snapshot.ModeState == nil {
		snapshot.ModeState = make(map[string]interface{})
	}
	snapshot.ModeState["settlement_final"] = true
}

func sortedPlayers(players map[string]PlayerState) []PlayerState {
	result := make([]PlayerState, 0, len(players))
	for _, player := range players {
		result = append(result, player)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UserID < result[j].UserID })
	return result
}

func deckSnapshotFor(state *RuntimeState, userID string) cards.DeckSnapshot {
	if state != nil && state.DeckSnapshots != nil {
		if snapshot, ok := state.DeckSnapshots[userID]; ok {
			return snapshot
		}
	}
	rulesetVersion := ""
	if state != nil {
		rulesetVersion = state.Authoritative.RulesetVersion
	}
	return cards.DeckSnapshot{
		RulesetVersion: rulesetVersion,
		CardIDs:        []string{},
	}
}
