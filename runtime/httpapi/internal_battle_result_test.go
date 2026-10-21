package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gensoulkyo/runtime/core"
)

// TestInternalBattleResultRouteIsAnswered pins the route the remote battle host
// posts its settled result to.
//
// The path is baked into the battle server binary (result_path in
// match_lifecycle.hpp), so the lobby cannot move it: if this route is missing,
// every settled match is silently lost and the only symptom is a 404 in a log on
// a different machine. The assertions deliberately look at the business error
// code rather than the status: a missing route answers with a bare 404 and no
// error_code at all, which is how the breakage used to hide.
func TestInternalBattleResultRouteIsAnswered(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	// A callback for a match the lobby does not know must reach the business
	// layer and keep its not_found, so a stale battle server sees the same
	// answer it always did instead of a routing error.
	unknown := postRaw(t, server.URL+"/internal/battle/result", "", map[string]any{
		"match_id": "match_missing",
	})
	if unknown.ErrorCode != "not_found" {
		t.Fatalf("unknown match = %+v (code %d), want not_found from the handler", unknown, unknown.Code)
	}

	// An unparseable body is rejected by the handler, which is only reachable
	// when the route is mounted; a bare 404 would carry no error code.
	garbage := postRaw(t, server.URL+"/internal/battle/result", "", map[string]any{
		"match_id": 12345,
	})
	if garbage.ErrorCode == "" {
		t.Fatalf("malformed callback = %+v (code %d), want a business error code", garbage, garbage.Code)
	}
	if garbage.Code == http.StatusNotFound {
		t.Fatalf("malformed callback answered 404, which is the missing-route signature: %+v", garbage)
	}
}
