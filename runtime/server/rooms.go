package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Phantasm-Klash/Gensoulkyo/runtime/config"
	"github.com/Phantasm-Klash/Gensoulkyo/runtime/decks"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	defaultRoomTTL = "30 minutes"
	roomCodeLength = 6
)

type roomRecord struct {
	ID                string
	RoomCode          string
	Mode              string
	Status            string
	HostUserID        string
	GuestUserID       sql.NullString
	HostDeckSnapshot  []byte
	GuestDeckSnapshot []byte
	MatchID           sql.NullString
	NakamaMatchID     sql.NullString
	CreatedAt         string
	UpdatedAt         string
	ExpiresAt         string
}

func rpcRoomCreate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return "", err
	}
	var request struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil && strings.TrimSpace(payload) != "" {
		return "", err
	}
	mode := strings.TrimSpace(request.Mode)
	if mode == "" {
		mode = "duel"
	}
	if mode != "duel" {
		return "", fmt.Errorf("unsupported room mode: %s", mode)
	}
	snapshot, err := decks.ActiveSnapshot(ctx, db, userID)
	if err != nil {
		return "", fmt.Errorf("active deck validation failed: %w", err)
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}

	var room roomRecord
	for attempts := 0; attempts < 8; attempts++ {
		code := roomCode()
		err = db.QueryRowContext(ctx, `
			insert into match_rooms (room_code, mode, status, host_user_id, host_deck_snapshot_json, expires_at)
			values ($1, $2, 'open', $3, $4::jsonb, now() + $5::interval)
			returning id::text, room_code, mode, status, host_user_id::text, guest_user_id::text,
			          host_deck_snapshot_json, coalesce(guest_deck_snapshot_json, '{}'::jsonb),
			          match_id::text, nakama_match_id, created_at::text, updated_at::text, expires_at::text
		`, code, mode, userID, snapshotJSON, defaultRoomTTL).Scan(
			&room.ID, &room.RoomCode, &room.Mode, &room.Status, &room.HostUserID, &room.GuestUserID,
			&room.HostDeckSnapshot, &room.GuestDeckSnapshot, &room.MatchID, &room.NakamaMatchID,
			&room.CreatedAt, &room.UpdatedAt, &room.ExpiresAt,
		)
		if err == nil {
			return marshal(room.response())
		}
		if !isUniqueViolation(err) {
			return "", err
		}
	}
	return "", errors.New("failed to allocate unique room code")
}

func rpcRoomJoin(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return "", err
	}
	var request struct {
		RoomCode string `json:"room_code"`
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", err
	}
	code := normalizeRoomCode(request.RoomCode)
	if code == "" {
		return "", errors.New("room_code is required")
	}
	snapshot, err := decks.ActiveSnapshot(ctx, db, userID)
	if err != nil {
		return "", fmt.Errorf("active deck validation failed: %w", err)
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	room, err := lockRoomByCode(ctx, tx, code)
	if err != nil {
		return "", err
	}
	if room.Status != "open" && room.Status != "ready" {
		return "", errors.New("room is not joinable")
	}
	if room.HostUserID == userID {
		return "", errors.New("host is already in room")
	}
	if room.GuestUserID.Valid && room.GuestUserID.String != userID {
		return "", errors.New("room is full")
	}
	if _, err := tx.ExecContext(ctx, `
		update match_rooms
		set guest_user_id = $2,
		    guest_deck_snapshot_json = $3::jsonb,
		    status = 'ready',
		    updated_at = now()
		where id = $1
	`, room.ID, userID, snapshotJSON); err != nil {
		return "", err
	}
	room, err = lockRoomByCode(ctx, tx, code)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return marshal(room.response())
}

func rpcRoomStart(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, err := authenticatedUserID(ctx)
	if err != nil {
		return "", err
	}
	var request struct {
		RoomCode string `json:"room_code"`
		EndTick  int    `json:"end_tick"`
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", err
	}
	code := normalizeRoomCode(request.RoomCode)
	if code == "" {
		return "", errors.New("room_code is required")
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	room, err := lockRoomByCode(ctx, tx, code)
	if err != nil {
		return "", err
	}
	if room.HostUserID != userID {
		return "", errors.New("only the room host can start the match")
	}
	if room.Status == "started" {
		return marshal(room.response())
	}
	if room.Status != "ready" || !room.GuestUserID.Valid {
		return "", errors.New("room is not ready")
	}

	hostSnapshot, err := decks.ActiveSnapshot(ctx, db, room.HostUserID)
	if err != nil {
		return "", fmt.Errorf("host active deck validation failed: %w", err)
	}
	guestSnapshot, err := decks.ActiveSnapshot(ctx, db, room.GuestUserID.String)
	if err != nil {
		return "", fmt.Errorf("guest active deck validation failed: %w", err)
	}
	dbMatchID := newUUID()
	params := map[string]interface{}{
		"mode":     room.Mode,
		"match_id": dbMatchID,
		"user_ids": stringsForParams([]string{room.HostUserID, room.GuestUserID.String}),
		"deck_snapshots": map[string]interface{}{
			room.HostUserID:         hostSnapshot,
			room.GuestUserID.String: guestSnapshot,
		},
	}
	if request.EndTick > 0 {
		params["end_tick"] = request.EndTick
	}
	nakamaMatchID, err := nk.MatchCreate(ctx, "gensoulkyo.duel", params)
	if err != nil {
		return "", err
	}
	hostSnapshotJSON, err := json.Marshal(hostSnapshot)
	if err != nil {
		return "", err
	}
	guestSnapshotJSON, err := json.Marshal(guestSnapshot)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		update match_rooms
		set status = 'started',
		    host_deck_snapshot_json = $2::jsonb,
		    guest_deck_snapshot_json = $3::jsonb,
		    match_id = $4,
		    nakama_match_id = $5,
		    updated_at = now()
		where id = $1
	`, room.ID, hostSnapshotJSON, guestSnapshotJSON, dbMatchID, nakamaMatchID); err != nil {
		return "", err
	}
	room, err = lockRoomByCode(ctx, tx, code)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	response := room.response()
	response["match_id"] = dbMatchID
	response["nakama_match_id"] = nakamaMatchID
	return marshal(response)
}

func lockRoomByCode(ctx context.Context, tx *sql.Tx, code string) (roomRecord, error) {
	var room roomRecord
	err := tx.QueryRowContext(ctx, `
		select id::text, room_code, mode, status, host_user_id::text, guest_user_id::text,
		       host_deck_snapshot_json, coalesce(guest_deck_snapshot_json, '{}'::jsonb),
		       match_id::text, nakama_match_id, created_at::text, updated_at::text, expires_at::text
		from match_rooms
		where room_code = $1 and expires_at > now()
		for update
	`, code).Scan(
		&room.ID, &room.RoomCode, &room.Mode, &room.Status, &room.HostUserID, &room.GuestUserID,
		&room.HostDeckSnapshot, &room.GuestDeckSnapshot, &room.MatchID, &room.NakamaMatchID,
		&room.CreatedAt, &room.UpdatedAt, &room.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return roomRecord{}, errors.New("room is unavailable")
	}
	return room, err
}

func (r roomRecord) response() map[string]interface{} {
	participants := []map[string]interface{}{
		{
			"user_id":       r.HostUserID,
			"role":          "host",
			"deck_snapshot": jsonObject(r.HostDeckSnapshot),
		},
	}
	if r.GuestUserID.Valid {
		participants = append(participants, map[string]interface{}{
			"user_id":       r.GuestUserID.String,
			"role":          "guest",
			"deck_snapshot": jsonObject(r.GuestDeckSnapshot),
		})
	}
	return map[string]interface{}{
		"id":              r.ID,
		"room_code":       r.RoomCode,
		"mode":            r.Mode,
		"status":          r.Status,
		"participants":    participants,
		"match_id":        nullableString(r.MatchID),
		"nakama_match_id": nullableString(r.NakamaMatchID),
		"ruleset_version": config.CurrentRulesetVersion,
		"created_at":      r.CreatedAt,
		"updated_at":      r.UpdatedAt,
		"expires_at":      r.ExpiresAt,
	}
}

func roomCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var bytes [roomCodeLength]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "AAAAAA"
	}
	code := make([]byte, roomCodeLength)
	for i, b := range bytes {
		code[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(code)
}

func normalizeRoomCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func newUUID() string {
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

func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "duplicate key value") || strings.Contains(err.Error(), "unique constraint")
}
