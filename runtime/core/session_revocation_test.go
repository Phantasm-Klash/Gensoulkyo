package core

import (
	"testing"
	"time"
)

// TestLoginExternalRevokesSupersededSessionOutsideServiceLock pins the single
// sign-on contract for the external (Nakama) login path:
//
//  1. a repeat sign-in for the same user id invalidates the previous token;
//  2. OnSessionRevoked fires exactly once with the *superseded* token; and
//  3. the hook runs with Service.mu released, so it can call back into the
//     Service (here via Bootstrap) without deadlocking.
//
// If (3) were violated the callback's Bootstrap call would block on the
// non-reentrant Service.mu and the second LoginExternal would never return,
// which the timeout below turns into a failure instead of a hung test binary.
func TestLoginExternalRevokesSupersededSessionOutsideServiceLock(t *testing.T) {
	service := NewService(Config{})

	hookDone := make(chan struct{})
	var (
		hookCalls int
		hookUser  string
		hookToken string
		hookErr   error
	)
	service.SetSessionRevokedHook(func(userID string, sessionToken string) {
		// Takes Service.mu. If the hook ran while the lock was held this
		// deadlocks and the outer timeout fires.
		_, err := service.Bootstrap(sessionToken)
		hookCalls++
		hookUser = userID
		hookToken = sessionToken
		hookErr = err
		close(hookDone)
	})

	first, err := service.LoginExternal(ExternalSessionRequest{
		UserID:       "nakama-user-1",
		SessionToken: "nakama-session-a",
		DisplayName:  "First",
		Provider:     "nakama",
	})
	if err != nil {
		t.Fatalf("first login: %v", err)
	}

	resultCh := make(chan error, 1)
	var second *AuthSession
	go func() {
		session, err := service.LoginExternal(ExternalSessionRequest{
			UserID:       "nakama-user-1",
			SessionToken: "nakama-session-b",
			DisplayName:  "Second",
			Provider:     "nakama",
		})
		second = session
		resultCh <- err
	}()

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("second login: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second LoginExternal deadlocked: OnSessionRevoked appears to run while Service.mu is held")
	}

	select {
	case <-hookDone:
	case <-time.After(3 * time.Second):
		t.Fatal("OnSessionRevoked was not invoked for the superseded session")
	}

	if hookCalls != 1 {
		t.Fatalf("OnSessionRevoked calls = %d, want 1", hookCalls)
	}
	if hookUser != "nakama-user-1" || hookToken != "nakama-session-a" {
		t.Fatalf("OnSessionRevoked args = (%q, %q), want (nakama-user-1, nakama-session-a)", hookUser, hookToken)
	}
	// The old token must already be gone when the hook observes it, proving the
	// state change and the notification are not racing.
	if ErrorCode(hookErr) != codeUnauthorized {
		t.Fatalf("superseded token must be invalid inside the hook, got %v", hookErr)
	}

	if second == nil || second.SessionToken != "nakama-session-b" || second.UserID != "nakama-user-1" {
		t.Fatalf("second session invalid: %+v", second)
	}
	if _, err := service.Bootstrap(first.SessionToken); ErrorCode(err) != codeUnauthorized {
		t.Fatalf("superseded token should no longer bootstrap, got %v", err)
	}
	if boot, err := service.Bootstrap(second.SessionToken); err != nil || boot.UserID != "nakama-user-1" {
		t.Fatalf("new token should bootstrap: boot=%+v err=%v", boot, err)
	}
}

// TestLoginExternalSameTokenDoesNotRevoke guards against spurious disconnects: a
// reconnect that reuses the current token is the same logical session and must
// not fire OnSessionRevoked (which would tear down the live socket).
func TestLoginExternalSameTokenDoesNotRevoke(t *testing.T) {
	service := NewService(Config{})

	hookCalls := 0
	service.SetSessionRevokedHook(func(userID string, sessionToken string) {
		hookCalls++
	})

	if _, err := service.LoginExternal(ExternalSessionRequest{
		UserID:       "nakama-user-2",
		SessionToken: "nakama-session-x",
		DisplayName:  "Reconnect",
		Provider:     "nakama",
	}); err != nil {
		t.Fatalf("first login: %v", err)
	}
	if _, err := service.LoginExternal(ExternalSessionRequest{
		UserID:       "nakama-user-2",
		SessionToken: "nakama-session-x",
		DisplayName:  "Reconnect",
		Provider:     "nakama",
	}); err != nil {
		t.Fatalf("reconnect login: %v", err)
	}

	if hookCalls != 0 {
		t.Fatalf("reusing the same token must not revoke, got %d hook call(s)", hookCalls)
	}
	if boot, err := service.Bootstrap("nakama-session-x"); err != nil || boot.UserID != "nakama-user-2" {
		t.Fatalf("token should remain valid: boot=%+v err=%v", boot, err)
	}
}
