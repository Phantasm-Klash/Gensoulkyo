package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gensoulkyo/runtime/core"
)

// TestRevokedSessionTokenReturnsUnauthorized verifies the HTTP half of single
// sign-on: once a sign-in supersedes a token, that token can no longer reach an
// envelope-gated API route (it must 401), while the fresh token still works.
func TestRevokedSessionTokenReturnsUnauthorized(t *testing.T) {
	service := core.NewService(core.Config{})
	server := httptest.NewServer(New(service))
	defer server.Close()

	first, err := service.LoginExternal(core.ExternalSessionRequest{
		UserID:       "nakama-http-user",
		SessionToken: "nakama-http-a",
		DisplayName:  "First",
		Provider:     "nakama",
	})
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	if boot := getJSON[core.BootstrapSnapshot](t, server.URL+"/v1/bootstrap", first.SessionToken); boot.UserID != "nakama-http-user" {
		t.Fatalf("first token should bootstrap: %+v", boot)
	}

	second, err := service.LoginExternal(core.ExternalSessionRequest{
		UserID:       "nakama-http-user",
		SessionToken: "nakama-http-b",
		DisplayName:  "Second",
		Provider:     "nakama",
	})
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if second.SessionToken == first.SessionToken {
		t.Fatalf("second sign-in should mint a fresh token")
	}

	revoked := getRaw(t, server.URL+"/v1/inventory", first.SessionToken)
	if revoked.Code != http.StatusUnauthorized || revoked.ErrorCode != "unauthorized" {
		t.Fatalf("superseded token should be unauthorized, got %+v", revoked)
	}

	if inv := getJSON[core.InventorySnapshot](t, server.URL+"/v1/inventory", second.SessionToken); !inv.OK {
		t.Fatalf("fresh token should still work: %+v", inv)
	}
}
