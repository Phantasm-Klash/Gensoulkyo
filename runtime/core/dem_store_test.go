package core

import (
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"
)

// buildTestDem 构造一个结构合法（magic + header + body_len + body + sha256）的
// .dem 容器。大厅只做整体 sha256/size + magic + header JSON 关键字段校验，
// 不校验 zlib/链，故 body 用任意字节即可。
func buildTestDem(matchID string, bossID string, entryCount int64, body string) []byte {
	header := `{"match_id":"` + matchID + `","mode_id":"pvp_duel","boss_id":"` + bossID +
		`","ruleset_version":"ruleset-v1","server_seed":"12345","tick_rate_hz":60,"final_tick":120` +
		`,"final_state_hash":"deadbeef","players":[],"created_at":1700000000,"entry_count":` +
		strconv.FormatInt(entryCount, 10) + `,"chain_head_digest":"cafe"}`
	compressed := []byte(body)
	out := []byte(demMagic)
	var u32 [4]byte
	binary.LittleEndian.PutUint32(u32[:], uint32(len(header)))
	out = append(out, u32[:]...)
	out = append(out, header...)
	var u64 [8]byte
	binary.LittleEndian.PutUint64(u64[:], uint64(len(compressed)))
	out = append(out, u64[:]...)
	out = append(out, compressed...)
	sum := sha256.Sum256(append([]byte(header), compressed...))
	out = append(out, sum[:]...)
	return out
}

func demSHA256Hex(dem []byte) string {
	sum := sha256.Sum256(dem)
	return hex.EncodeToString(sum[:])
}

func demMD5Hex(chunk []byte) string {
	sum := md5.Sum(chunk)
	return hex.EncodeToString(sum[:])
}

// submitDemChunks 按给定顺序把 dem 切成 chunkSize 大小的块上传。
// order 为 nil 时按 0..total-1 顺序上传。
func submitDemChunks(t *testing.T, service *Service, dem []byte, replayID string, matchID string, chunkSize int, order []int) []*DemReplayUploadResult {
	t.Helper()
	total := (len(dem) + chunkSize - 1) / chunkSize
	if total == 0 {
		total = 1
	}
	if order == nil {
		order = make([]int, 0, total)
		for index := 0; index < total; index++ {
			order = append(order, index)
		}
	}
	results := make([]*DemReplayUploadResult, 0, len(order))
	for _, index := range order {
		begin := index * chunkSize
		end := begin + chunkSize
		if end > len(dem) {
			end = len(dem)
		}
		if begin > len(dem) {
			begin = len(dem)
		}
		chunk := dem[begin:end]
		md5hex := demMD5Hex(chunk)
		result, err := service.SubmitDemReplayChunk(DemReplayUploadRequest{
			ReplayID:    replayID,
			MatchID:     matchID,
			DemSize:     int64(len(dem)),
			DemSHA256:   demSHA256Hex(dem),
			ChunkIndex:  index,
			ChunkTotal:  total,
			ChunkMD5:    md5hex,
			ChunkBase64: base64.StdEncoding.EncodeToString(chunk),
		})
		if err != nil {
			t.Fatalf("submit chunk %d: %v", index, err)
		}
		results = append(results, result)
	}
	return results
}

func TestDemStoreMigrationCoversRepositoryColumns(t *testing.T) {
	upSQL := readMigrationFile(t, "003_battle_dem_replays.up.sql")
	downSQL := readMigrationFile(t, "003_battle_dem_replays.down.sql")
	assertMigrationCoversInsertColumns(t, upSQL, insertDemSQL, "battle_dem_replays")
	assertMigrationHasColumns(t, upSQL, "battle_dem_replays", []string{
		"match_id",
		"replay_id",
		"user_id",
		"sha256",
		"size",
		"entry_count",
		"boss_id",
		"dem_bytes",
	})
	assertDownMigrationDropsTable(t, downSQL, "battle_dem_replays")
}

func TestMemoryDemStoreSaveGetIdempotent(t *testing.T) {
	store := NewMemoryDemStore()
	record := DemRecord{MatchID: "m1", ReplayID: "r1", UserID: "u1", SHA256: "aa", Size: 3, Bytes: []byte{1, 2, 3}}
	if err := store.SaveDem(record); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 幂等：同 match 重复 Save 不覆盖。
	if err := store.SaveDem(DemRecord{MatchID: "m1", ReplayID: "r2", Size: 99}); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	byMatch, ok := store.GetDemByMatch("m1")
	if !ok || byMatch.ReplayID != "r1" || byMatch.Size != 3 {
		t.Fatalf("by-match lookup invalid: %+v", byMatch)
	}
	byReplay, ok := store.GetDemByReplay("r1")
	if !ok || byReplay.MatchID != "m1" {
		t.Fatalf("by-replay lookup invalid: %+v", byReplay)
	}
	if _, ok := store.GetDemByReplay("r2"); ok {
		t.Fatalf("idempotent save must not register the second replay_id")
	}
	if has, size := store.HasDem("m1"); !has || size != 3 {
		t.Fatalf("has-dem invalid: %t %d", has, size)
	}
	if _, ok := store.GetDemByMatch("missing"); ok {
		t.Fatalf("missing match must not resolve")
	}
}

func TestSubmitDemReplayOutOfOrderAndIdempotent(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Dem Uploader")
	dem := buildTestDem("match-dem-1", "boss-sanae", 42, strings.Repeat("payload-", 20))
	service.mu.Lock()
	service.replays["replay-dem-1"] = &ReplayRecord{ReplayID: "replay-dem-1", MatchID: "match-dem-1", UserID: alice.UserID}
	service.mu.Unlock()

	// 乱序上传：先最后一块，再中间，再第一块。
	total := (len(dem) + 31) / 32
	order := []int{total - 1, 0}
	for index := 1; index < total-1; index++ {
		order = append(order, index)
	}
	results := submitDemChunks(t, service, dem, "replay-dem-1", "match-dem-1", 32, order)
	last := results[len(results)-1]
	if !last.OK || !last.Complete {
		t.Fatalf("final chunk must complete the upload: %+v", last)
	}
	for _, result := range results[:len(results)-1] {
		if result.Complete {
			t.Fatalf("non-final chunk must not report complete: %+v", result)
		}
	}

	// 完成后重复上传 → duplicate OK，不重复落库。
	dup, err := service.SubmitDemReplayChunk(DemReplayUploadRequest{
		ReplayID: "replay-dem-1", MatchID: "match-dem-1", DemSize: int64(len(dem)),
		DemSHA256: demSHA256Hex(dem), ChunkIndex: 0, ChunkTotal: total,
		ChunkBase64: base64.StdEncoding.EncodeToString(dem[:1]),
	})
	if err != nil || !dup.OK || !dup.Duplicate {
		t.Fatalf("duplicate upload must be idempotent OK: %+v err=%v", dup, err)
	}

	status := service.DemStoreStatus()
	if !status.OK || status.Records != 1 || status.Bytes != int64(len(dem)) || status.PendingUploads != 0 {
		t.Fatalf("dem store status invalid after upload: %+v", status)
	}
}

func TestSubmitDemReplayRejectsBadSHA256(t *testing.T) {
	service := NewService(Config{})
	dem := buildTestDem("match-dem-bad", "boss", 1, "body")
	total := (len(dem) + 15) / 16
	// 最后一块用错误的整体 sha256 → 组装后校验失败。
	for index := 0; index < total; index++ {
		begin := index * 16
		end := begin + 16
		if end > len(dem) {
			end = len(dem)
		}
		sha := demSHA256Hex(dem)
		if index == total-1 {
			sha = strings.Repeat("0", 64)
		}
		_, err := service.SubmitDemReplayChunk(DemReplayUploadRequest{
			ReplayID: "r-bad", MatchID: "match-dem-bad", DemSize: int64(len(dem)), DemSHA256: sha,
			ChunkIndex: index, ChunkTotal: total,
			ChunkMD5:    demMD5Hex(dem[begin:end]),
			ChunkBase64: base64.StdEncoding.EncodeToString(dem[begin:end]),
		})
		if index < total-1 && err != nil {
			t.Fatalf("chunk %d should be staged: %v", index, err)
		}
		if index == total-1 {
			if err == nil || ErrorCode(err) != codeInvalidRequest {
				t.Fatalf("bad sha256 must be rejected, got %v", err)
			}
		}
	}
	if _, ok := service.demRepo.GetDemByMatch("match-dem-bad"); ok {
		t.Fatalf("rejected upload must not be stored")
	}
}

func TestSubmitDemReplayRejectsChunkConflict(t *testing.T) {
	service := NewService(Config{})
	dem := buildTestDem("match-dem-conflict", "boss", 1, strings.Repeat("x", 64))
	_, err := service.SubmitDemReplayChunk(DemReplayUploadRequest{
		ReplayID: "r", MatchID: "match-dem-conflict", DemSize: int64(len(dem)), DemSHA256: demSHA256Hex(dem),
		ChunkIndex: 0, ChunkTotal: 2,
		ChunkMD5:    demMD5Hex(dem[:32]),
		ChunkBase64: base64.StdEncoding.EncodeToString(dem[:32]),
	})
	if err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	// 同一 index 不同字节 → 冲突。
	_, err = service.SubmitDemReplayChunk(DemReplayUploadRequest{
		ReplayID: "r", MatchID: "match-dem-conflict", DemSize: int64(len(dem)), DemSHA256: demSHA256Hex(dem),
		ChunkIndex: 0, ChunkTotal: 2,
		ChunkMD5:    demMD5Hex([]byte("different")),
		ChunkBase64: base64.StdEncoding.EncodeToString([]byte("different")),
	})
	if err == nil || ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("conflicting chunk must be rejected, got %v", err)
	}
}

func TestSubmitDemReplayRejectsBadChunkMD5(t *testing.T) {
	service := NewService(Config{})
	dem := buildTestDem("match-dem-md5", "boss", 1, "abc")
	_, err := service.SubmitDemReplayChunk(DemReplayUploadRequest{
		ReplayID: "r", MatchID: "match-dem-md5", DemSize: int64(len(dem)), DemSHA256: demSHA256Hex(dem),
		ChunkIndex: 0, ChunkTotal: 1,
		ChunkMD5:    strings.Repeat("0", 32),
		ChunkBase64: base64.StdEncoding.EncodeToString(dem),
	})
	if err == nil || ErrorCode(err) != codeInvalidRequest {
		t.Fatalf("bad chunk md5 must be rejected, got %v", err)
	}
}

func TestDemDownloadOwnerOnly(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Dem Owner")
	bob := mustLogin(t, service, "Dem Other")
	dem := buildTestDem("match-dem-owner", "boss", 3, "body")
	service.mu.Lock()
	service.replays["replay-owner"] = &ReplayRecord{ReplayID: "replay-owner", MatchID: "match-dem-owner", UserID: alice.UserID}
	service.mu.Unlock()
	submitDemChunks(t, service, dem, "replay-owner", "match-dem-owner", 24, nil)

	record, err := service.DemDownload(alice.SessionToken, "replay-owner")
	if err != nil {
		t.Fatalf("owner download: %v", err)
	}
	if !bytesEqual(record.Bytes, dem) {
		t.Fatalf("downloaded bytes differ from uploaded container")
	}
	if _, err := service.DemDownload(bob.SessionToken, "replay-owner"); err == nil || ErrorCode(err) != codeUnauthorized {
		t.Fatalf("non-owner download must be unauthorized, got %v", err)
	}
	if _, err := service.DemDownload(alice.SessionToken, "replay-missing"); err == nil || ErrorCode(err) != codeNotFound {
		t.Fatalf("missing replay must be not_found, got %v", err)
	}
}

func TestRecordsViewAnnotatesHasDem(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Dem Records")
	dem := buildTestDem("match-dem-records", "boss", 5, "body")
	service.mu.Lock()
	state := service.users[alice.UserID]
	state.Records = state.Records.ensure()
	state.Records.record(MatchHistoryEntry{MatchID: "match-dem-records", Result: "win", Score: 10})
	service.mu.Unlock()
	submitDemChunks(t, service, dem, "replay-records", "match-dem-records", 16, nil)

	view, err := service.Records(alice.SessionToken)
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if len(view.Recent) != 1 || !view.Recent[0].HasDem || view.Recent[0].DemSize != int64(len(dem)) {
		t.Fatalf("records recent must annotate has_dem/dem_size: %+v", view.Recent)
	}
}

func TestSQLDemStoreRejectsNilDB(t *testing.T) {
	if _, err := NewSQLDemStore(nil); err == nil {
		t.Fatalf("nil db must be rejected")
	}
	var store *SQLDemStore
	if err := store.SaveDem(DemRecord{MatchID: "m"}); err == nil {
		t.Fatalf("nil store must reject SaveDem")
	}
	if _, ok := store.GetDemByMatch("m"); ok {
		t.Fatalf("nil store must return not-found")
	}
}

func TestSQLDemStorePersistsInsert(t *testing.T) {
	driverName := registerBattleAuditCaptureDriver(t)
	db, err := sql.Open(driverName, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSQLDemStore(db)
	if err != nil {
		t.Fatal(err)
	}
	dem := buildTestDem("match-sql-dem", "boss", 2, "body")
	if err := store.SaveDem(DemRecord{
		MatchID: "match-sql-dem", ReplayID: "r", UserID: "u", SHA256: demSHA256Hex(dem),
		Size: int64(len(dem)), EntryCount: 2, BossID: "boss", Bytes: dem, ServerAuthoritative: true,
	}); err != nil {
		t.Fatalf("sql save: %v", err)
	}
	calls := battleAuditCaptureCalls()
	found := false
	for _, call := range calls {
		if strings.Contains(call.query, "battle_dem_replays") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an insert into battle_dem_replays, got %+v", calls)
	}
}
