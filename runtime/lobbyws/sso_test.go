package lobbyws

import (
	"testing"
	"time"

	"gensoulkyo/runtime/core"
)

// TestSessionReplacedClosesSupersededConnection pins the single sign-on
// behaviour end to end over the lobby WebSocket: a second sign-in for the same
// identity must push an explicit session_replaced error to the first connection
// and then disconnect it, while the new connection stays fully usable.
func TestSessionReplacedClosesSupersededConnection(t *testing.T) {
	ts, _, server := newLobbyTestServer(t)

	first := dialWS(t, wsURL(ts, "/v1/lobby/ws"))
	t.Cleanup(first.close)
	first.setDeadline(10 * time.Second)
	first.sendLobby(TypeAuthRequest, LobbyAuthRequest{UserID: "sso-user", Platform: "test"})
	firstAuth := decodePayload[LobbyAuthResponse](t, first.readEnvelope(TypeAuthResponse, 5*time.Second))
	if firstAuth.Error != nil || firstAuth.SessionToken == "" {
		t.Fatalf("first auth failed: %+v", firstAuth)
	}

	second := dialWS(t, wsURL(ts, "/v1/lobby/ws"))
	t.Cleanup(second.close)
	second.setDeadline(10 * time.Second)
	second.sendLobby(TypeAuthRequest, LobbyAuthRequest{UserID: "sso-user", Platform: "test"})
	secondAuth := decodePayload[LobbyAuthResponse](t, second.readEnvelope(TypeAuthResponse, 5*time.Second))
	if secondAuth.Error != nil || secondAuth.SessionToken == "" {
		t.Fatalf("second auth failed: %+v", secondAuth)
	}
	if secondAuth.SessionToken == firstAuth.SessionToken {
		t.Fatalf("second sign-in should mint a fresh token, both got %q", firstAuth.SessionToken)
	}

	// The superseded connection is told why it is being dropped...
	replaced := decodePayload[ErrorStatus](t, first.readEnvelope(TypeError, 5*time.Second))
	if replaced.Code != core.CodeSessionReplaced {
		t.Fatalf("superseded error code = %q, want %q", replaced.Code, core.CodeSessionReplaced)
	}
	// ...and is then disconnected by the server (only the new client remains).
	waitForClientCount(t, server, 1, 5*time.Second)

	// The surviving connection keeps working: bootstrap then create a room.
	second.sendLobby(TypeBootstrapRequest, LobbyBootstrapRequest{})
	boot := decodePayload[LobbyBootstrapResponse](t, second.readEnvelope(TypeBootstrapResponse, 5*time.Second))
	if boot.Error != nil || boot.Profile.UserID != secondAuth.UserID {
		t.Fatalf("second bootstrap failed: %+v", boot)
	}
	second.sendLobby(TypeRoomCreateRequest, RoomCreateRequest{ModeID: "certification"})
	created := decodePayload[RoomCreateResponse](t, second.readEnvelope(TypeRoomCreateResponse, 5*time.Second))
	if created.Error != nil || created.RoomCode == "" {
		t.Fatalf("second room create failed: %+v", created)
	}
}

// TestSessionReplacedRemovesSupersededClientFromRooms checks that a superseded
// connection leaves no zombie behind: it must disappear from both the client set
// and its room membership.
func TestSessionReplacedRemovesSupersededClientFromRooms(t *testing.T) {
	ts, _, server := newLobbyTestServer(t)

	first := dialWS(t, wsURL(ts, "/v1/lobby/ws"))
	t.Cleanup(first.close)
	first.setDeadline(10 * time.Second)
	first.sendLobby(TypeAuthRequest, LobbyAuthRequest{UserID: "sso-roomer", Platform: "test"})
	firstAuth := decodePayload[LobbyAuthResponse](t, first.readEnvelope(TypeAuthResponse, 5*time.Second))
	if firstAuth.Error != nil || firstAuth.SessionToken == "" {
		t.Fatalf("first auth failed: %+v", firstAuth)
	}
	first.sendLobby(TypeRoomCreateRequest, RoomCreateRequest{ModeID: "certification"})
	created := decodePayload[RoomCreateResponse](t, first.readEnvelope(TypeRoomCreateResponse, 5*time.Second))
	if created.Error != nil || created.RoomCode == "" {
		t.Fatalf("first room create failed: %+v", created)
	}
	roomCode := created.RoomCode
	waitForRoomClients(t, server, roomCode, 1, 5*time.Second)

	second := dialWS(t, wsURL(ts, "/v1/lobby/ws"))
	t.Cleanup(second.close)
	second.setDeadline(10 * time.Second)
	second.sendLobby(TypeAuthRequest, LobbyAuthRequest{UserID: "sso-roomer", Platform: "test"})
	secondAuth := decodePayload[LobbyAuthResponse](t, second.readEnvelope(TypeAuthResponse, 5*time.Second))
	if secondAuth.Error != nil || secondAuth.SessionToken == "" {
		t.Fatalf("second auth failed: %+v", secondAuth)
	}
	_ = decodePayload[ErrorStatus](t, first.readEnvelope(TypeError, 5*time.Second))

	waitForClientCount(t, server, 1, 5*time.Second)
	// The room held only the superseded client, so it must now be empty/reaped.
	waitForRoomClients(t, server, roomCode, 0, 5*time.Second)
}

func waitForRoomClients(t *testing.T, server *Server, roomCode string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		server.mu.Lock()
		got := len(server.rooms[roomCode])
		server.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d client(s) in room %s; have %d", want, roomCode, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
