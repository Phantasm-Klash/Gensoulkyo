package httpapi

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"gensoulkyo/runtime/core"
)

// thread B: POST /v1/battle/replays/dem follows the signature-less
// service-origin guard sequence (registerBattleServer precedent), and
// GET /v1/replays/{id}/dem streams the container to the owning session.
func TestHTTPDemReplayUploadAndDownload(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	route := server.URL + "/v1/battle/replays/dem"
	callbackHeaders := map[string]string{
		headerServiceOrigin:  core.ServiceCallbackContext()[serviceOriginContextKey],
		headerBattleCallback: core.ServiceCallbackContext()[serviceCallbackContextKey],
	}

	// 无服务回调头 → 403。
	response := postRaw(t, route, "", map[string]any{})
	if response.Code != http.StatusForbidden || response.ErrorCode != "service_origin_required" {
		t.Fatalf("dem upload without callback headers must be rejected, got %+v", response)
	}

	player := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{"device_id": "dem-http-player", "display_name": "Dem HTTP Player"})

	// 带会话头 → 403（服务回调不得携带玩家上下文）。
	response = postRaw(t, route, player.SessionToken, map[string]any{})
	if response.Code != http.StatusForbidden || response.ErrorCode != "service_origin_required" {
		t.Fatalf("dem upload with player session must be rejected, got %+v", response)
	}

	dem := buildHTTPTestDem("match-http-dem", "boss", 7, strings.Repeat("z", 100))
	sha := sha256.Sum256(dem)
	demSHA := hex.EncodeToString(sha[:])
	total := (len(dem) + 31) / 32
	for index := 0; index < total; index++ {
		begin := index * 32
		end := begin + 32
		if end > len(dem) {
			end = len(dem)
		}
		chunk := dem[begin:end]
		md5sum := md5Hex(chunk)
		body := map[string]any{
			"replay_id":    "replay-http-dem",
			"match_id":     "match-http-dem",
			"dem_size":     len(dem),
			"dem_sha256":   demSHA,
			"chunk_index":  index,
			"chunk_total":  total,
			"chunk_md5":    md5sum,
			"chunk_base64": base64.StdEncoding.EncodeToString(chunk),
		}
		response = postRawWithHeaders(t, route, "", body, callbackHeaders)
		if response.Code != http.StatusOK {
			t.Fatalf("chunk %d upload must succeed, got %+v", index, response)
		}
	}

	// 属主下载（无 replay 记录时 user_id 为空，任何 session 可读；字节逐字节一致）。
	got := getRawBytes(t, server.URL+"/v1/replays/replay-http-dem/dem", player.SessionToken)
	if got.status != http.StatusOK {
		t.Fatalf("owner dem download must succeed, got %d", got.status)
	}
	if len(got.body) != len(dem) || hex.EncodeToString(got.body) != hex.EncodeToString(dem) {
		t.Fatalf("downloaded dem bytes differ from uploaded container")
	}

	// 缺 replay_id → 404。
	got = getRawBytes(t, server.URL+"/v1/replays/replay-missing/dem", player.SessionToken)
	if got.status != http.StatusNotFound {
		t.Fatalf("missing dem must 404, got %d", got.status)
	}

	// 未登录 → 非 200。
	got = getRawBytes(t, server.URL+"/v1/replays/replay-http-dem/dem", "")
	if got.status == http.StatusOK {
		t.Fatalf("unauthenticated dem download must not succeed")
	}
}

func md5Hex(chunk []byte) string {
	sum := md5.Sum(chunk)
	return hex.EncodeToString(sum[:])
}

type rawHTTPResult struct {
	status int
	body   []byte
}

func getRawBytes(t *testing.T, url string, token string) rawHTTPResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return rawHTTPResult{status: resp.StatusCode, body: raw}
}

func buildHTTPTestDem(matchID string, bossID string, entryCount int64, body string) []byte {
	header := `{"match_id":"` + matchID + `","mode_id":"pvp_duel","boss_id":"` + bossID +
		`","ruleset_version":"ruleset-v1","server_seed":"1","tick_rate_hz":60,"final_tick":10` +
		`,"final_state_hash":"h","players":[],"created_at":1,"entry_count":` +
		strconv.FormatInt(entryCount, 10) + `,"chain_head_digest":"c"}`
	out := []byte("PHDEM001")
	var u32 [4]byte
	binary.LittleEndian.PutUint32(u32[:], uint32(len(header)))
	out = append(out, u32[:]...)
	out = append(out, header...)
	var u64 [8]byte
	binary.LittleEndian.PutUint64(u64[:], uint64(len(body)))
	out = append(out, u64[:]...)
	out = append(out, body...)
	sum := sha256.Sum256(append([]byte(header), []byte(body)...))
	out = append(out, sum[:]...)
	return out
}
