package lobbyws

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gensoulkyo/runtime/core"
)

func newLobbyTestServer(t *testing.T) (*httptest.Server, *core.Service, *Server) {
	t.Helper()
	service := core.NewService(core.Config{})
	server := New(Options{Service: service, Logger: log.New(io.Discard, "", 0)})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/lobby/ws", server.HandleLobby)
	mux.HandleFunc("/v1/battle/relay", server.HandleRelay)
	mux.HandleFunc("/internal/battle/result", server.HandleBattleResult)
	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		server.Close()
		ts.Close()
	})
	return ts, service, server
}

func wsURL(ts *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + path
}

func mustLogin(t *testing.T, service *core.Service, name string) *core.AuthSession {
	t.Helper()
	session, err := service.LoginAnonymous(core.AnonymousLoginRequest{DeviceID: "dev-" + name, DisplayName: name})
	if err != nil {
		t.Fatalf("login %s: %v", name, err)
	}
	return session
}

func authClient(t *testing.T, ts *httptest.Server, session *core.AuthSession) *testWSClient {
	t.Helper()
	client := dialWS(t, wsURL(ts, "/v1/lobby/ws"))
	t.Cleanup(client.close)
	client.setDeadline(10 * time.Second)
	client.sendLobby(TypeAuthRequest, LobbyAuthRequest{SessionToken: session.SessionToken})
	env := client.readEnvelope(TypeAuthResponse, 5*time.Second)
	var resp LobbyAuthResponse
	if err := json.Unmarshal(env.Payload, &resp); err != nil {
		t.Fatalf("unmarshal auth response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("auth error: %+v", resp.Error)
	}
	if resp.UserID != session.UserID {
		t.Fatalf("auth user mismatch: got %q want %q", resp.UserID, session.UserID)
	}
	return client
}

func decodePayload[T any](t *testing.T, env envelope) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(env.Payload, &out); err != nil {
		t.Fatalf("unmarshal payload (type %d): %v", env.Type, err)
	}
	return out
}

func TestLobbyRoomFlowMatchStartAndResult(t *testing.T) {
	ts, service, _ := newLobbyTestServer(t)
	aliceSession := mustLogin(t, service, "Alice")
	bobSession := mustLogin(t, service, "Bob")

	alice := authClient(t, ts, aliceSession)
	bob := authClient(t, ts, bobSession)

	// Bootstrap after auth.
	alice.sendLobby(TypeBootstrapRequest, LobbyBootstrapRequest{})
	bootstrap := decodePayload[LobbyBootstrapResponse](t, alice.readEnvelope(TypeBootstrapResponse, 5*time.Second))
	if bootstrap.Error != nil || bootstrap.Profile.UserID != aliceSession.UserID {
		t.Fatalf("unexpected bootstrap: %+v", bootstrap)
	}

	// Create a room.
	alice.sendLobby(TypeRoomCreateRequest, RoomCreateRequest{ModeID: "certification"})
	created := decodePayload[RoomCreateResponse](t, alice.readEnvelope(TypeRoomCreateResponse, 5*time.Second))
	if created.Error != nil || created.RoomCode == "" {
		t.Fatalf("unexpected create room response: %+v", created)
	}
	aliceRoom := decodePayload[RoomStateMessage](t, alice.readEnvelope(TypeRoomState, 5*time.Second))
	if aliceRoom.RoomCode != created.RoomCode || len(aliceRoom.Players) != 1 || !aliceRoom.Players[0].Host {
		t.Fatalf("unexpected room state after create: %+v", aliceRoom)
	}

	// Bob joins; the room fills and the match starts automatically.
	bob.sendLobby(TypeRoomJoinRequest, RoomJoinRequest{RoomCode: created.RoomCode})
	joined := decodePayload[RoomJoinResponse](t, bob.readEnvelope(TypeRoomJoinResponse, 5*time.Second))
	if joined.Error != nil || joined.RoomCode != created.RoomCode {
		t.Fatalf("unexpected join room response: %+v", joined)
	}

	bobStart := decodePayload[MatchStartMessage](t, bob.readEnvelope(TypeMatchStart, 10*time.Second))
	if bobStart.MatchID == "" || len(bobStart.PlayerIDs) != 2 {
		t.Fatalf("unexpected match start for bob: %+v", bobStart)
	}
	if bobStart.Endpoint == "" {
		t.Fatalf("match start must carry an endpoint: %+v", bobStart)
	}
	if bobStart.SignedBattleTicket == nil {
		t.Fatalf("match start must carry a signed battle ticket: %+v", bobStart)
	}

	aliceStart := decodePayload[MatchStartMessage](t, alice.readEnvelope(TypeMatchStart, 10*time.Second))
	if aliceStart.MatchID != bobStart.MatchID {
		t.Fatalf("both players must see the same match: %q vs %q", aliceStart.MatchID, bobStart.MatchID)
	}

	// Result callback from the battle server.
	winner := bobStart.PlayerIDs[0]
	loser := bobStart.PlayerIDs[1]
	callback := core.BattleResultCallback{
		MatchID:        bobStart.MatchID,
		ModeID:         bobStart.ModeID,
		RulesetVersion: bobStart.RulesetVersion,
		MatchSeed:      uint64(bobStart.ServerSeed),
		WinnerPlayerID: winner,
		WinnerTick:     1234,
		StateHash:      "sha256:lobby-test",
		Players: []core.BattleResultPlayer{
			{PlayerID: winner, DamageDealt: 900, BossCurrentHP: 0},
			{PlayerID: loser, DamageDealt: 500, BossCurrentHP: 300},
		},
	}
	status, resultResp := postBattleResult(t, ts, callback)
	if status != http.StatusOK {
		t.Fatalf("result callback status = %d", status)
	}
	if !resultResp.OK || resultResp.Duplicate {
		t.Fatalf("unexpected result response: %+v", resultResp)
	}

	aliceResult := decodePayload[MatchResultMessage](t, alice.readEnvelope(TypeMatchResult, 5*time.Second))
	if aliceResult.MatchID != bobStart.MatchID || aliceResult.WinnerPlayerID != winner {
		t.Fatalf("unexpected match result: %+v", aliceResult)
	}
	if aliceResult.Points[winner] != 3 || aliceResult.Points[loser] != 1 {
		t.Fatalf("unexpected match result points: %+v", aliceResult.Points)
	}
	if !aliceResult.ServerAuthoritative {
		t.Fatalf("match result must be server authoritative")
	}

	// Replaying the same callback is idempotent and must not re-broadcast.
	status, duplicate := postBattleResult(t, ts, callback)
	if status != http.StatusOK || !duplicate.Duplicate {
		t.Fatalf("expected idempotent duplicate, got status=%d resp=%+v", status, duplicate)
	}
}

func TestBattleResultUnknownMatchReturns404(t *testing.T) {
	ts, _, _ := newLobbyTestServer(t)
	status, _ := postBattleResult(t, ts, core.BattleResultCallback{MatchID: "match_missing", StateHash: "x"})
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown match, got %d", status)
	}
}

func TestRelayForwardsDatagrams(t *testing.T) {
	ts, service, _ := newLobbyTestServer(t)

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { _ = udpConn.Close() })
	go func() {
		buffer := make([]byte, 65536)
		for {
			n, addr, err := udpConn.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = udpConn.WriteToUDP(buffer[:n], addr)
		}
	}()

	host := mustLogin(t, service, "Host")
	guest := mustLogin(t, service, "Guest")
	created, err := service.CreateRoom(host.SessionToken, core.CreateRoomRequest{ModeID: "certification", DeckSnapshot: core.DeckSnapshot{DeckID: "local_default"}})
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	joined, err := service.JoinRoom(guest.SessionToken, created.RoomCode, core.JoinRoomRequest{DeckSnapshot: core.DeckSnapshot{DeckID: "local_default"}})
	if err != nil {
		t.Fatalf("join room: %v", err)
	}
	matchID := joined.MatchID
	if matchID == "" {
		t.Fatalf("expected a match")
	}
	if _, err := service.ReadyMatch(host.SessionToken, matchID); err != nil {
		t.Fatalf("ready host: %v", err)
	}
	if _, err := service.ReadyMatch(guest.SessionToken, matchID); err != nil {
		t.Fatalf("ready guest: %v", err)
	}
	if _, err := service.BindBattleServerAllocation(matchID, "battle-relay", udpConn.LocalAddr().String()); err != nil {
		t.Fatalf("bind allocation: %v", err)
	}

	relay := dialWS(t, wsURL(ts, "/v1/battle/relay?match_id="+matchID))
	t.Cleanup(relay.close)
	relay.setDeadline(5 * time.Second)

	payload := []byte("raw-kcp-datagram")
	relay.writeFrame(true, OpBinary, payload)
	_, opcode, got := relay.readFrame()
	if opcode != OpBinary || !bytes.Equal(got, payload) {
		t.Fatalf("relay roundtrip mismatch: opcode=%d payload=%q", opcode, got)
	}
}

func TestRelayWithoutMatchReturnsError(t *testing.T) {
	ts, _, _ := newLobbyTestServer(t)
	response, err := http.Get(ts.URL + "/v1/battle/relay")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing match_id, got %d", response.StatusCode)
	}
}

func postBattleResult(t *testing.T, ts *httptest.Server, callback core.BattleResultCallback) (int, core.BattleResultCallbackResponse) {
	t.Helper()
	body, err := json.Marshal(callback)
	if err != nil {
		t.Fatalf("marshal callback: %v", err)
	}
	response, err := http.Post(ts.URL+"/internal/battle/result", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post result: %v", err)
	}
	defer response.Body.Close()
	var decoded core.BattleResultCallbackResponse
	raw, _ := io.ReadAll(response.Body)
	_ = json.Unmarshal(raw, &decoded)
	return response.StatusCode, decoded
}
