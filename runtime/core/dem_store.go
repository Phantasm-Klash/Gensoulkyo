package core

// 对局录制（.dem v1 容器）在大厅的存储与上传/下载。
//
// 选型与理由（COORDINATION.md §0.1 thread B）：
//   - 存储层沿用 battle_lifecycle_sql.go / lobby_lifecycle_sql.go 的
//     「接口 + 可选注入 PG repo + 内存兜底」三件套。这样无 DB 的开发/测试
//     路径与生产路径共用同一套 Service 逻辑，和现有 audit repo 完全同形。
//   - 生产库表见 migrations/003_battle_dem_replays.{up,down}.sql；.dem 容器
//     字节以 BYTEA 整段落库（容器本身已 zlib 压缩，二次压缩无收益）。
//   - 分块上传的暂存区（demUploadStaging）在 Service 内存里，和 users /
//     matches / replays 一样是单进程状态：大厅当前就是内存权威，这里保持一致。
//     PG repo 只负责「完成态」记录的持久化，不参与分块暂存。
//   - 幂等键：match_id（C++ 侧 dem.cpp 注释明确「存储以 match_id 为键」，
//     replay_id 作为元信息/审计用）。同一 match 重复上传直接返回 duplicate。
//
// .dem v1 容器布局（逐字节冻结，双端共识 COORDINATION §0.2）：
//   [8B magic "PHDEM001"][u32 header_len LE][header JSON]
//   [u64 body_len LE][zlib(body)][32B sha256(header_json ++ compressed)]
// 大厅只做「整体 sha256 + size + magic + header JSON 关键字段」校验；zlib
// 解压与链验证属于对局服/客户端（C++ WriteDemDocument / TS replayer）职责。

import (
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

const (
	// demMagic 是 .dem v1 容器开头的 8 字节 magic（不含结尾 NUL）。
	demMagic = "PHDEM001"
	// demHeaderOffset 是 header_len（u32 LE）在容器里的偏移。
	demHeaderOffset = len(demMagic)
	// maxDemUploadBytes 是单场 .dem 容器允许的最大明文字节数。录制条目
	// （每 tick 的输入）远小于该值；上限只用于拒绝恶意/异常体，防止内存放大。
	maxDemUploadBytes = 256 << 20
	// demChunkCountLimit 是分块数上限（防御性；512KB/块时对应 128GB，远超上限）。
	demChunkCountLimit = 1 << 20
)

// DemRecord 是一段已完成上传、通过整体 sha256 校验的 .dem 容器及其元信息。
// 它是服务端权威记录，客户端通过 GET /v1/replays/{replay_id}/dem 取字节。
type DemRecord struct {
	MatchID             string    `json:"match_id"`
	ReplayID            string    `json:"replay_id"`
	UserID              string    `json:"user_id,omitempty"`
	ModeID              string    `json:"mode_id,omitempty"`
	BossID              string    `json:"boss_id,omitempty"`
	SHA256              string    `json:"sha256"`
	Size                int64     `json:"size"`
	EntryCount          int64     `json:"entry_count"`
	FinalTick           int64     `json:"final_tick"`
	FinalStateHash      string    `json:"final_state_hash,omitempty"`
	ServerSeed          string    `json:"server_seed,omitempty"`
	TickRateHz          int       `json:"tick_rate_hz"`
	CreatedAt           time.Time `json:"created_at"`
	ServerAuthoritative bool      `json:"server_authoritative"`
	Bytes               []byte    `json:"-"`
}

// DemRepository 是 .dem 完成态记录的持久化契约。注入 PG 实现即持久化，
// 未注入时 NewService 回落到内存实现（与 audit repo 的可选注入风格一致）。
type DemRepository interface {
	// SaveDem 幂等落库：同 match_id 重复调用不覆盖既有记录。
	SaveDem(record DemRecord) error
	// GetDemByMatch 按 match_id 取记录（.dem 的主键）。
	GetDemByMatch(matchID string) (DemRecord, bool)
	// GetDemByReplay 按 replay_id 取记录（下载路由的入参）。
	GetDemByReplay(replayID string) (DemRecord, bool)
	// HasDem 报告某 match 是否已有 .dem，以及字节数（records/replay 视图用）。
	HasDem(matchID string) (bool, int64)
}

// ---------------------------------------------------------------------------
// 内存实现（默认；无 DB 的开发/测试路径）
// ---------------------------------------------------------------------------

// MemoryDemStore 是 DemRepository 的进程内实现。
type MemoryDemStore struct {
	mu       sync.RWMutex
	byMatch  map[string]DemRecord
	byReplay map[string]string // replay_id -> match_id
}

func NewMemoryDemStore() *MemoryDemStore {
	return &MemoryDemStore{
		byMatch:  map[string]DemRecord{},
		byReplay: map[string]string{},
	}
}

// demRepositoryOrDefault 在未注入 PG repo 时回落到进程内实现，保证
// Service.demRepo 永不为 nil（上传/下载路径无需到处判空）。
func demRepositoryOrDefault(repo DemRepository) DemRepository {
	if repo == nil {
		return NewMemoryDemStore()
	}
	return repo
}

func (store *MemoryDemStore) SaveDem(record DemRecord) error {
	if store == nil {
		return errors.New("dem store is not configured")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.byMatch[record.MatchID]; ok {
		return nil
	}
	stored := record
	stored.Bytes = append([]byte(nil), record.Bytes...)
	store.byMatch[record.MatchID] = stored
	if record.ReplayID != "" {
		store.byReplay[record.ReplayID] = record.MatchID
	}
	return nil
}

func (store *MemoryDemStore) GetDemByMatch(matchID string) (DemRecord, bool) {
	if store == nil {
		return DemRecord{}, false
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, ok := store.byMatch[matchID]
	return record, ok
}

func (store *MemoryDemStore) GetDemByReplay(replayID string) (DemRecord, bool) {
	if store == nil {
		return DemRecord{}, false
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	matchID, ok := store.byReplay[replayID]
	if !ok {
		return DemRecord{}, false
	}
	record, ok := store.byMatch[matchID]
	return record, ok
}

func (store *MemoryDemStore) HasDem(matchID string) (bool, int64) {
	record, ok := store.GetDemByMatch(matchID)
	if !ok {
		return false, 0
	}
	return true, record.Size
}

// DemStoreStatus 是 /v1/security/... 风格的存储诊断快照（供 demo/运维查看）。
type DemStoreStatus struct {
	OK                  bool      `json:"ok"`
	Configured          bool      `json:"configured"`
	Persistent          bool      `json:"persistent"`
	Records             int       `json:"records"`
	Bytes               int64     `json:"bytes"`
	PendingUploads      int       `json:"pending_uploads"`
	LastSuccessMatchID  string    `json:"last_success_match_id,omitempty"`
	LastSuccessAt       time.Time `json:"last_success_at,omitempty"`
	LastErrorMatchID    string    `json:"last_error_match_id,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	LastErrorAt         time.Time `json:"last_error_at,omitempty"`
	ServerAuthoritative bool      `json:"server_authoritative"`
}

// ---------------------------------------------------------------------------
// PG 实现（生产路径）
// ---------------------------------------------------------------------------

const selectDemByMatchSQL = `
SELECT match_id, replay_id, user_id, mode_id, boss_id, sha256, size, entry_count,
       final_tick, final_state_hash, server_seed, tick_rate_hz, created_at,
       server_authoritative, dem_bytes
FROM battle_dem_replays
WHERE match_id = $1`

const selectDemByReplaySQL = `
SELECT match_id, replay_id, user_id, mode_id, boss_id, sha256, size, entry_count,
       final_tick, final_state_hash, server_seed, tick_rate_hz, created_at,
       server_authoritative, dem_bytes
FROM battle_dem_replays
WHERE replay_id = $1
ORDER BY created_at DESC
LIMIT 1`

const insertDemSQL = `
INSERT INTO battle_dem_replays (
    match_id,
    replay_id,
    user_id,
    mode_id,
    boss_id,
    sha256,
    size,
    entry_count,
    final_tick,
    final_state_hash,
    server_seed,
    tick_rate_hz,
    dem_bytes,
    created_at,
    server_authoritative
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
) ON CONFLICT (match_id) DO NOTHING`

// SQLDemStore 是 DemRepository 的 PostgreSQL 实现。字节以 BYTEA 整段落库。
type SQLDemStore struct {
	db        *sql.DB
	insertSQL string
	byMatch   string
	byReplay  string
}

type SQLDemStoreOption func(*SQLDemStore)

func NewSQLDemStore(db *sql.DB, options ...SQLDemStoreOption) (*SQLDemStore, error) {
	if db == nil {
		return nil, errors.New("dem store requires db")
	}
	store := &SQLDemStore{
		db:        db,
		insertSQL: insertDemSQL,
		byMatch:   selectDemByMatchSQL,
		byReplay:  selectDemByReplaySQL,
	}
	for _, option := range options {
		option(store)
	}
	return store, nil
}

// withSQLDemStatements 供测试注入自定义语句（与 audit repo 的 with... 同形）。
func withSQLDemStatements(insertSQL string, byMatchSQL string, byReplaySQL string) SQLDemStoreOption {
	return func(store *SQLDemStore) {
		if insertSQL != "" {
			store.insertSQL = insertSQL
		}
		if byMatchSQL != "" {
			store.byMatch = byMatchSQL
		}
		if byReplaySQL != "" {
			store.byReplay = byReplaySQL
		}
	}
}

func (store *SQLDemStore) SaveDem(record DemRecord) error {
	if store == nil || store.db == nil {
		return errors.New("dem store is not configured")
	}
	_, err := store.db.Exec(
		store.insertSQL,
		record.MatchID,
		record.ReplayID,
		record.UserID,
		record.ModeID,
		record.BossID,
		record.SHA256,
		record.Size,
		record.EntryCount,
		record.FinalTick,
		record.FinalStateHash,
		record.ServerSeed,
		record.TickRateHz,
		record.Bytes,
		record.CreatedAt,
		record.ServerAuthoritative,
	)
	return err
}

func (store *SQLDemStore) GetDemByMatch(matchID string) (DemRecord, bool) {
	if store == nil || store.db == nil || strings.TrimSpace(matchID) == "" {
		return DemRecord{}, false
	}
	return scanDemRecord(store.db.QueryRow(store.byMatch, matchID))
}

func (store *SQLDemStore) GetDemByReplay(replayID string) (DemRecord, bool) {
	if store == nil || store.db == nil || strings.TrimSpace(replayID) == "" {
		return DemRecord{}, false
	}
	return scanDemRecord(store.db.QueryRow(store.byReplay, replayID))
}

func (store *SQLDemStore) HasDem(matchID string) (bool, int64) {
	record, ok := store.GetDemByMatch(matchID)
	if !ok {
		return false, 0
	}
	return true, record.Size
}

func scanDemRecord(row *sql.Row) (DemRecord, bool) {
	if row == nil {
		return DemRecord{}, false
	}
	var record DemRecord
	var bytes []byte
	if err := row.Scan(
		&record.MatchID,
		&record.ReplayID,
		&record.UserID,
		&record.ModeID,
		&record.BossID,
		&record.SHA256,
		&record.Size,
		&record.EntryCount,
		&record.FinalTick,
		&record.FinalStateHash,
		&record.ServerSeed,
		&record.TickRateHz,
		&record.CreatedAt,
		&record.ServerAuthoritative,
		&bytes,
	); err != nil {
		return DemRecord{}, false
	}
	record.Bytes = bytes
	return record, true
}

// ---------------------------------------------------------------------------
// 容器 header 解析（只取大厅视图需要的字段）
// ---------------------------------------------------------------------------

// demHeaderSummary 是 .dem header JSON 里大厅关心的字段子集。
type demHeaderSummary struct {
	MatchID        string `json:"match_id"`
	ModeID         string `json:"mode_id"`
	BossID         string `json:"boss_id"`
	ServerSeed     string `json:"server_seed"`
	TickRateHz     int    `json:"tick_rate_hz"`
	FinalTick      int64  `json:"final_tick"`
	FinalStateHash string `json:"final_state_hash"`
	EntryCount     int64  `json:"entry_count"`
}

// parseDemHeaderSummary 校验 magic + u32 header_len，并解析 header JSON 的关键字段。
// 只做结构校验，不做 zlib/链验证（那是 C++/TS 侧职责）。
func parseDemHeaderSummary(bytes []byte) (demHeaderSummary, error) {
	if len(bytes) < demHeaderOffset+4 {
		return demHeaderSummary{}, newError(codeInvalidRequest, "dem container is truncated")
	}
	if string(bytes[:len(demMagic)]) != demMagic {
		return demHeaderSummary{}, newError(codeInvalidRequest, "dem container magic mismatch")
	}
	headerLen := int(binary.LittleEndian.Uint32(bytes[demHeaderOffset : demHeaderOffset+4]))
	if headerLen <= 0 || demHeaderOffset+4+headerLen > len(bytes) {
		return demHeaderSummary{}, newError(codeInvalidRequest, "dem container header length out of range")
	}
	headerJSON := bytes[demHeaderOffset+4 : demHeaderOffset+4+headerLen]
	var summary demHeaderSummary
	if err := json.Unmarshal(headerJSON, &summary); err != nil {
		return demHeaderSummary{}, newError(codeInvalidRequest, "dem container header is not valid JSON: %s", err.Error())
	}
	return summary, nil
}

// ---------------------------------------------------------------------------
// 分块上传暂存（Service 内存态）
// ---------------------------------------------------------------------------

// demUploadStaging 累积一次分块上传。所有字段在 Service.mu 下读写。
type demUploadStaging struct {
	replayID   string
	matchID    string
	demSize    int64
	demSHA256  string
	chunkTotal int
	chunks     map[int][]byte
	bytes      int64
	createdAt  time.Time
}

// DemReplayUploadRequest 是 POST /v1/battle/replays/dem 的单块请求体。
// 字段名与 C++ dem.cpp BuildDemUploadChunkJson 逐字段对齐（双端共识）。
type DemReplayUploadRequest struct {
	ReplayID    string `json:"replay_id"`
	MatchID     string `json:"match_id"`
	DemSize     int64  `json:"dem_size"`
	DemSHA256   string `json:"dem_sha256"`
	ChunkIndex  int    `json:"chunk_index"`
	ChunkTotal  int    `json:"chunk_total"`
	ChunkMD5    string `json:"chunk_md5"`
	ChunkBase64 string `json:"chunk_base64"`
}

// DemReplayUploadResult 是分块上传的响应体。OK 字段必须渲染为 "ok":true
// （C++ 侧用 body.find("\"ok\":true") 判定成功）。
type DemReplayUploadResult struct {
	OK                  bool   `json:"ok"`
	Complete            bool   `json:"complete"`
	Duplicate           bool   `json:"duplicate,omitempty"`
	MatchID             string `json:"match_id"`
	ReplayID            string `json:"replay_id"`
	ReceivedChunks      int    `json:"received_chunks"`
	ChunkTotal          int    `json:"chunk_total"`
	ServerAuthoritative bool   `json:"server_authoritative"`
}

// SubmitDemReplayChunk 接收一块 .dem 上传。幂等（match_id 为键）、支持乱序/续传：
//   - 同 match_id 已完成 → duplicate OK，不再接收；
//   - 同一 chunk_index 重复且字节一致 → no-op；字节不同 → 冲突错误；
//   - 全部到齐 → 组装 + 校验整体 sha256/size + 解析 header → 落库。
func (s *Service) SubmitDemReplayChunk(req DemReplayUploadRequest) (*DemReplayUploadResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	matchID := strings.TrimSpace(req.MatchID)
	replayID := strings.TrimSpace(req.ReplayID)
	if matchID == "" {
		return nil, newError(codeInvalidRequest, "match_id is required")
	}
	if req.ChunkTotal <= 0 || req.ChunkTotal > demChunkCountLimit {
		return nil, newError(codeInvalidRequest, "chunk_total is out of range")
	}
	if req.ChunkIndex < 0 || req.ChunkIndex >= req.ChunkTotal {
		return nil, newError(codeInvalidRequest, "chunk_index is out of range")
	}
	if req.DemSize < 0 || req.DemSize > maxDemUploadBytes {
		return nil, newError(codeInvalidRequest, "dem_size is out of range")
	}

	if s.demRepo != nil {
		if existing, ok := s.demRepo.GetDemByMatch(matchID); ok {
			return &DemReplayUploadResult{
				OK:                  true,
				Complete:            true,
				Duplicate:           true,
				MatchID:             matchID,
				ReplayID:            firstNonEmptyCore(replayID, existing.ReplayID),
				ReceivedChunks:      req.ChunkTotal,
				ChunkTotal:          req.ChunkTotal,
				ServerAuthoritative: true,
			}, nil
		}
	}

	chunk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.ChunkBase64))
	if err != nil {
		return s.recordDemUploadErrorLocked(matchID, "invalid_chunk_base64",
			newError(codeInvalidRequest, "chunk_base64 is not valid base64: %s", err.Error()))
	}
	if len(chunk) == 0 && req.DemSize != 0 {
		return s.recordDemUploadErrorLocked(matchID, "empty_chunk",
			newError(codeInvalidRequest, "chunk_base64 decoded to zero bytes"))
	}
	expectedMD5 := strings.ToLower(strings.TrimSpace(req.ChunkMD5))
	if expectedMD5 != "" {
		sum := md5.Sum(chunk)
		if hex.EncodeToString(sum[:]) != expectedMD5 {
			return s.recordDemUploadErrorLocked(matchID, "chunk_md5_mismatch",
				newError(codeInvalidRequest, "chunk_md5 does not match chunk payload"))
		}
	}

	staging := s.demUploads[matchID]
	if staging == nil {
		staging = &demUploadStaging{
			replayID:   replayID,
			matchID:    matchID,
			demSize:    req.DemSize,
			demSHA256:  strings.ToLower(strings.TrimSpace(req.DemSHA256)),
			chunkTotal: req.ChunkTotal,
			chunks:     map[int][]byte{},
			createdAt:  s.clock(),
		}
		s.demUploads[matchID] = staging
	} else if staging.chunkTotal != req.ChunkTotal || staging.demSize != req.DemSize ||
		(staging.demSHA256 != "" && strings.ToLower(strings.TrimSpace(req.DemSHA256)) != "" &&
			staging.demSHA256 != strings.ToLower(strings.TrimSpace(req.DemSHA256))) {
		return s.recordDemUploadErrorLocked(matchID, "chunk_metadata_conflict",
			newError(codeInvalidRequest, "chunk metadata conflicts with earlier chunks for match %q", matchID))
	}
	if staging.replayID == "" {
		staging.replayID = replayID
	}

	if prev, ok := staging.chunks[req.ChunkIndex]; ok {
		if !bytesEqual(prev, chunk) {
			return s.recordDemUploadErrorLocked(matchID, "chunk_conflict",
				newError(codeInvalidRequest, "chunk_index %d was already uploaded with different bytes", req.ChunkIndex))
		}
		return &DemReplayUploadResult{
			OK:                  true,
			Complete:            false,
			MatchID:             matchID,
			ReplayID:            staging.replayID,
			ReceivedChunks:      len(staging.chunks),
			ChunkTotal:          staging.chunkTotal,
			ServerAuthoritative: true,
		}, nil
	}

	staging.chunks[req.ChunkIndex] = chunk
	staging.bytes += int64(len(chunk))
	if staging.bytes > maxDemUploadBytes {
		delete(s.demUploads, matchID)
		return s.recordDemUploadErrorLocked(matchID, "upload_too_large",
			newError(codeInvalidRequest, "dem upload exceeds %d bytes", maxDemUploadBytes))
	}

	if len(staging.chunks) < staging.chunkTotal {
		return &DemReplayUploadResult{
			OK:                  true,
			Complete:            false,
			MatchID:             matchID,
			ReplayID:            staging.replayID,
			ReceivedChunks:      len(staging.chunks),
			ChunkTotal:          staging.chunkTotal,
			ServerAuthoritative: true,
		}, nil
	}

	// 全部到齐：按 chunk_index 升序组装（支持乱序上传）。
	assembled := make([]byte, 0, staging.bytes)
	for index := 0; index < staging.chunkTotal; index++ {
		assembled = append(assembled, staging.chunks[index]...)
	}
	delete(s.demUploads, matchID)

	if staging.demSize != 0 && int64(len(assembled)) != staging.demSize {
		return s.recordDemUploadErrorLocked(matchID, "size_mismatch",
			newError(codeInvalidRequest, "assembled dem size %d does not match declared %d", len(assembled), staging.demSize))
	}
	sum := sha256.Sum256(assembled)
	actualSHA := hex.EncodeToString(sum[:])
	if staging.demSHA256 != "" && actualSHA != staging.demSHA256 {
		return s.recordDemUploadErrorLocked(matchID, "sha256_mismatch",
			newError(codeInvalidRequest, "assembled dem sha256 does not match declared digest"))
	}
	summary, err := parseDemHeaderSummary(assembled)
	if err != nil {
		return s.recordDemUploadErrorLocked(matchID, "header_invalid", err)
	}

	record := DemRecord{
		MatchID:             matchID,
		ReplayID:            firstNonEmptyCore(staging.replayID, summary.MatchID),
		UserID:              s.demRecordUserIDLocked(matchID, staging.replayID),
		ModeID:              summary.ModeID,
		BossID:              summary.BossID,
		SHA256:              actualSHA,
		Size:                int64(len(assembled)),
		EntryCount:          summary.EntryCount,
		FinalTick:           summary.FinalTick,
		FinalStateHash:      summary.FinalStateHash,
		ServerSeed:          summary.ServerSeed,
		TickRateHz:          summary.TickRateHz,
		CreatedAt:           s.clock(),
		ServerAuthoritative: true,
		Bytes:               assembled,
	}
	if s.demRepo != nil {
		if err := s.demRepo.SaveDem(record); err != nil {
			return s.recordDemUploadErrorLocked(matchID, "store_failed",
				newError(codeInvalidRequest, "failed to persist dem record: %s", err.Error()))
		}
	}
	s.demStatus.Records++
	s.demStatus.Bytes += record.Size
	s.demStatus.LastSuccessMatchID = matchID
	s.demStatus.LastSuccessAt = s.clock()

	return &DemReplayUploadResult{
		OK:                  true,
		Complete:            true,
		MatchID:             matchID,
		ReplayID:            record.ReplayID,
		ReceivedChunks:      staging.chunkTotal,
		ChunkTotal:          staging.chunkTotal,
		ServerAuthoritative: true,
	}, nil
}

func (s *Service) recordDemUploadErrorLocked(matchID string, reason string, err error) (*DemReplayUploadResult, error) {
	s.demStatus.LastErrorMatchID = matchID
	s.demStatus.LastError = reason
	s.demStatus.LastErrorAt = s.clock()
	return nil, err
}

// demRecordUserIDLocked 用 replay_id 反查属主（.dem 上传体不含 user_id）。
func (s *Service) demRecordUserIDLocked(matchID string, replayID string) string {
	if replayID != "" {
		if replay, ok := s.replays[replayID]; ok && replay != nil {
			return replay.UserID
		}
	}
	for _, replay := range s.replays {
		if replay != nil && replay.MatchID == matchID {
			return replay.UserID
		}
	}
	return ""
}

func (s *Service) demHasForMatchLocked(matchID string) (bool, int64) {
	if s.demRepo == nil || strings.TrimSpace(matchID) == "" {
		return false, 0
	}
	return s.demRepo.HasDem(matchID)
}

// DemDownload 返回某 replay 的 .dem 容器字节（session 鉴权 + 属主校验，
// 与 Service.Replay 同口径）。先按 replay_id 直查（上传时记录），再回落到
// replay 记录里的 match_id（存在 replay 但 .dem 的 replay_id 为空时）。
func (s *Service) DemDownload(sessionToken string, replayID string) (DemRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	user, err := s.userBySessionLocked(sessionToken)
	if err != nil {
		return DemRecord{}, err
	}
	replayID = strings.TrimSpace(replayID)
	if replayID == "" {
		return DemRecord{}, newError(codeInvalidRequest, "replay_id is required")
	}
	if s.demRepo == nil {
		return DemRecord{}, newError(codeNotFound, "dem not found")
	}
	record, ok := s.demRepo.GetDemByReplay(replayID)
	if !ok {
		if replay, found := s.replays[replayID]; found && replay != nil {
			record, ok = s.demRepo.GetDemByMatch(replay.MatchID)
		}
	}
	if !ok {
		return DemRecord{}, newError(codeNotFound, "dem not found")
	}
	// 属主校验：以「请求的 replay_id 的属主」为准（同 Service.Replay 口径），
	// 这样同一 match 的其他参与者用各自的 replay_id 也能下载同一份 .dem；
	// replay 记录缺失时回落到 .dem 记录自带的 user_id。
	ownerID := ""
	if replay, found := s.replays[replayID]; found && replay != nil {
		ownerID = replay.UserID
	}
	if ownerID == "" {
		ownerID = record.UserID
	}
	if ownerID != "" && ownerID != user.UserID {
		return DemRecord{}, newError(codeUnauthorized, "user cannot read dem")
	}
	return record, nil
}

// DemStoreStatus 返回存储诊断快照。
func (s *Service) DemStoreStatus() DemStoreStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.demStatus
	status.OK = true
	status.Configured = s.demRepo != nil
	status.PendingUploads = len(s.demUploads)
	status.ServerAuthoritative = true
	return status
}

func bytesEqual(a []byte, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
