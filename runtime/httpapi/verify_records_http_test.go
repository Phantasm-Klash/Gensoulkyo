package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gensoulkyo/runtime/core"
)

// TestVerifyHTTPRecordsEndpoint exercises GET /v1/records end-to-end and proves
// an unauthenticated call is rejected.
func TestVerifyHTTPRecordsEndpoint(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	player := postJSON[core.AuthSession](t, server.URL+"/v1/auth/anonymous", "", map[string]any{
		"device_id":    "vfy-records-http",
		"display_name": "Vfy Records HTTP",
	})

	view := getJSON[core.RecordsView](t, server.URL+"/v1/records", player.SessionToken)
	if !view.OK || !view.ServerAuthoritative || view.ClientResultAuthoritative {
		t.Fatalf("GET /v1/records authority invalid: %+v", view)
	}
	if view.UserID != player.UserID || view.DisplayName != "Vfy Records HTTP" {
		t.Fatalf("GET /v1/records identity invalid: %+v", view)
	}
	if view.MatchesPlayed != 0 || len(view.Recent) != 0 {
		t.Fatalf("fresh player records must be empty: %+v", view)
	}

	bad := getRaw(t, server.URL+"/v1/records", "bogus-session-token")
	if bad.Code == http.StatusOK {
		t.Fatalf("GET /v1/records with a bad token must not return 200: %+v", bad)
	}
}
