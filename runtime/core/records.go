package core

import "time"

// recordsHistoryLimit 是个人战绩里保留的最近对局条数上限。
const recordsHistoryLimit = 20

// MatchHistoryEntry 描述单局对局在个人记录里的快照。字段名与 JSON tag 是
// 服务端与客户端共同遵守的契约，不得擅自改动。
type MatchHistoryEntry struct {
	MatchID       string    `json:"match_id"`
	ModeID        string    `json:"mode_id"`
	StageID       string    `json:"stage_id"`
	Result        string    `json:"result"` // win | loss | draw
	Score         int       `json:"score"`
	RankDelta     int       `json:"rank_delta"`
	OpponentID    string    `json:"opponent_id,omitempty"`
	GrazeCount    int       `json:"graze_count"`
	HitCount      int       `json:"hit_count"`
	DamageDealt   int       `json:"damage_dealt"`
	CardPlays     int       `json:"card_plays"`
	DurationTicks int       `json:"duration_ticks"`
	EndedAt       time.Time `json:"ended_at"`
	// HasDem / DemSize 是 thread B 追加字段：该对局是否已有可下载的 .dem
	// 录制，以及容器字节数。仅追加，不改动上面既有字段的语义。
	HasDem  bool  `json:"has_dem"`
	DemSize int64 `json:"dem_size,omitempty"`
}

// PlayerRecords 是每名玩家的个人记录数据仓库，聚合对局统计并维护有界的最近对局列表。
// 它是服务端权威状态，客户端只能通过 records.get 读取投影。
type PlayerRecords struct {
	MatchesPlayed    int
	Wins             int
	Losses           int
	Draws            int
	BestScore        int
	TotalScore       int
	TotalGraze       int
	TotalHits        int
	TotalDamageDealt int
	TotalCardPlays   int
	Recent           []MatchHistoryEntry // 上限 recordsHistoryLimit，最新在前
}

// RecordsView 是 records.get / GET /v1/records 的响应体。
type RecordsView struct {
	OK                        bool                `json:"ok"`
	UserID                    string              `json:"user_id"`
	DisplayName               string              `json:"display_name"`
	Gold                      int                 `json:"gold"`
	Tickets                   int                 `json:"tickets"`
	Points                    int                 `json:"points"` // = Certification.RankScore
	RatingCode                string              `json:"rating_code"`
	SeasonID                  string              `json:"season_id"`
	MatchesPlayed             int                 `json:"matches_played"`
	Wins                      int                 `json:"wins"`
	Losses                    int                 `json:"losses"`
	Draws                     int                 `json:"draws"`
	WinRate                   float64             `json:"win_rate"`
	BestScore                 int                 `json:"best_score"`
	AverageScore              float64             `json:"average_score"`
	TotalGraze                int                 `json:"total_graze"`
	TotalHits                 int                 `json:"total_hits"`
	TotalDamageDealt          int                 `json:"total_damage_dealt"`
	TotalCardPlays            int                 `json:"total_card_plays"`
	DeckCount                 int                 `json:"deck_count"`
	CardCount                 int                 `json:"card_count"`
	Recent                    []MatchHistoryEntry `json:"recent"`
	ServerTime                time.Time           `json:"server_time"`
	ServerAuthoritative       bool                `json:"server_authoritative"`
	ClientResultAuthoritative bool                `json:"client_result_authoritative"`
}

// newPlayerRecords 返回一个已初始化 Recent 切片的空记录仓库。
func newPlayerRecords() *PlayerRecords {
	return &PlayerRecords{Recent: make([]MatchHistoryEntry, 0, recordsHistoryLimit)}
}

// ensure 在 nil 接收者上懒初始化，方便 userState.Records 为 nil 的老用户按空记录处理。
func (r *PlayerRecords) ensure() *PlayerRecords {
	if r == nil {
		return newPlayerRecords()
	}
	return r
}

// record 把一局结果并入聚合统计，并把该局插入有界 Recent（最新在前，超出裁掉最旧）。
func (r *PlayerRecords) record(entry MatchHistoryEntry) {
	if r == nil {
		return
	}
	r.MatchesPlayed++
	switch entry.Result {
	case "win":
		r.Wins++
	case "loss":
		r.Losses++
	case "draw":
		r.Draws++
	}
	if entry.Score > r.BestScore {
		r.BestScore = entry.Score
	}
	r.TotalScore += entry.Score
	r.TotalGraze += entry.GrazeCount
	r.TotalHits += entry.HitCount
	r.TotalDamageDealt += entry.DamageDealt
	r.TotalCardPlays += entry.CardPlays
	r.Recent = append([]MatchHistoryEntry{entry}, r.Recent...)
	if len(r.Recent) > recordsHistoryLimit {
		r.Recent = r.Recent[:recordsHistoryLimit]
	}
}
