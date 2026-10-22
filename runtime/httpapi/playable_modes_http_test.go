package httpapi

import (
	"net/http/httptest"
	"testing"

	"gensoulkyo/runtime/core"
)

// TestHTTPPlayableModesEndpoint: the room-building screen reads this so it can grey
// out modes that would fail when the match starts, rather than letting a player
// build the room and find out.
func TestHTTPPlayableModesEndpoint(t *testing.T) {
	service := core.NewService(core.Config{PlayableModes: core.PlayableModeIDs()})
	server := httptest.NewServer(New(service))
	defer server.Close()

	session := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{
		"device_id": "modes-http", "display_name": "Modes HTTP",
	})

	resp := getJSON[core.PlayableModesResponse](t, server.URL+"/v1/modes/playable", session.SessionToken)
	if !resp.OK {
		t.Fatalf("playable modes response not ok: %+v", resp)
	}
	if len(resp.ModeIDs) != 2 {
		t.Fatalf("mode ids = %v, want two", resp.ModeIDs)
	}
	// The whole point: the modes with no battle server simulation are absent.
	for _, id := range resp.ModeIDs {
		if !isPlayable(id) {
			t.Fatalf("server offered a mode with no simulation: %q", id)
		}
	}

	// And it is not open to anyone.
	raw := getRaw(t, server.URL+"/v1/modes/playable", "")
	if raw.OK {
		t.Fatalf("unauthenticated playable modes succeeded: %+v", raw)
	}
}

func isPlayable(id string) bool {
	return id == "coop_boss" || id == "mvp_boss_race"
}
