package lobbyws

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gensoulkyo/runtime/core"
)

// TestLobbyReapsSilentClient pins the WebSocket keepalive. A client that
// vanishes without a TCP FIN/RST (process killed, network drop, NAT timeout)
// used to block ReadMessage forever, leaking the client, its goroutine and its
// room membership. The server now pings every PingPeriod and drops a peer that
// has been silent for PongWait.
func TestLobbyReapsSilentClient(t *testing.T) {
	service := core.NewService(core.Config{})
	server := New(Options{
		Service: service,
		Logger:  log.New(io.Discard, "", 0),
		// Shrink the window so the test does not wait a real minute.
		PongWait:   250 * time.Millisecond,
		PingPeriod: 80 * time.Millisecond,
		WriteWait:  time.Second,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/lobby/ws", server.HandleLobby)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer server.Close()

	// A raw WebSocket client that never answers pings.
	client := dialWS(t, wsURL(ts, "/v1/lobby/ws"))
	defer client.close()

	// It must first show up as registered...
	waitForClientCount(t, server, 1, 2*time.Second)
	// ...and then be reaped once the read deadline fires.
	waitForClientCount(t, server, 0, 5*time.Second)
}

// TestLobbyKeepsRespondingClient checks the other half of the contract: a
// client that answers pings (or simply keeps sending frames) is not reaped.
func TestLobbyKeepsRespondingClient(t *testing.T) {
	service := core.NewService(core.Config{})
	server := New(Options{
		Service:    service,
		Logger:     log.New(io.Discard, "", 0),
		PongWait:   300 * time.Millisecond,
		PingPeriod: 60 * time.Millisecond,
		WriteWait:  time.Second,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/lobby/ws", server.HandleLobby)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	defer server.Close()

	session := mustLogin(t, service, "keepalive")
	client := dialWS(t, wsURL(ts, "/v1/lobby/ws"))
	defer client.close()

	// Keep the read deadline refreshed well past the reaper window.
	deadline := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(deadline) {
		client.sendLobby(TypeAuthRequest, LobbyAuthRequest{SessionToken: session.SessionToken})
		time.Sleep(50 * time.Millisecond)
	}

	server.mu.Lock()
	got := len(server.clients)
	server.mu.Unlock()
	if got != 1 {
		t.Fatalf("an active client must not be reaped; registered=%d want 1", got)
	}
}

func waitForClientCount(t *testing.T, server *Server, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		server.mu.Lock()
		got := len(server.clients)
		server.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d registered client(s); have %d", want, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
