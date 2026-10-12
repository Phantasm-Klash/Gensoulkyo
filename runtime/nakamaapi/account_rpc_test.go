package nakamaapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"gensoulkyo/runtime/account"
	"gensoulkyo/runtime/core"
)

// captureMailer records the last verification code delivered to each address so
// the account RPC tests can finish a registration without an SMTP relay.
type captureMailer struct {
	mu    sync.Mutex
	codes map[string]string
}

func newCaptureMailer() *captureMailer {
	return &captureMailer{codes: map[string]string{}}
}

func (m *captureMailer) Enabled() bool { return true }

func (m *captureMailer) SendVerificationCode(_ context.Context, to string, code string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[to] = code
	return nil
}

func (m *captureMailer) last(to string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.codes[to]
}

// newAccountRPCHandler wires the lobby with a working account subsystem so the
// account and check-in operations can be exercised end to end through the Nakama
// transport, not only through the HTTP router.
func newAccountRPCHandler(t *testing.T) (*Handler, *captureMailer) {
	t.Helper()
	mailer := newCaptureMailer()
	accounts := account.NewService(account.Config{
		Store:           account.NewMemoryStore(),
		Mailer:          mailer,
		VerificationTTL: 10 * time.Minute,
		ResendCooldown:  time.Minute,
		HourlySendLimit: 5,
		MaxAttempts:     3,
		CodeSecret:      "nakama-account-rpc-test-secret",
	})
	return New(core.NewService(core.Config{Accounts: accounts})), mailer
}

// registerThroughRPC signs an account up the way the client does: request a
// verification code, read it from the mailer, then register. It returns the
// issued session.
func registerThroughRPC(t *testing.T, handler *Handler, mailer *captureMailer, email string, username string) *core.AuthSession {
	t.Helper()

	code := handler.HandleRPC(RPCRequest{ID: "auth.emailCode", Payload: map[string]any{"email": email}})
	if !code.OK || code.Status != 200 {
		t.Fatalf("auth.emailCode without an envelope should be accepted, got %+v", code)
	}
	if _, ok := code.Payload.(*core.AccountEmailCodeResponse); !ok {
		t.Fatalf("auth.emailCode payload = %T, want *core.AccountEmailCodeResponse", code.Payload)
	}
	verification := mailer.last(email)
	if len(verification) != account.VerificationCodeLength {
		t.Fatalf("delivered verification code = %q, want %d digits", verification, account.VerificationCodeLength)
	}

	register := handler.HandleRPC(RPCRequest{
		ID: "auth.register",
		Payload: map[string]any{
			"username":     username,
			"email":        email,
			"password":     "nakama-rpc-password-1",
			"display_name": "Nakama RPC",
			"code":         verification,
		},
	})
	if !register.OK || register.Status != 200 {
		t.Fatalf("auth.register without an envelope should be accepted, got %+v", register)
	}
	session, ok := register.Payload.(*core.AuthSession)
	if !ok || session == nil || session.SessionToken == "" || session.UserID == "" {
		t.Fatalf("auth.register payload = %T, want a signed-in session", register.Payload)
	}
	return session
}

// TestAccountRPCsAreReachableWithoutTheBusinessEnvelope proves the Nakama
// transport can drive the account surface. These operations run before the
// caller owns a session, so they cannot present a signed business envelope and
// must be exempt from the envelope gate — the same exemption auth.anonymous has.
func TestAccountRPCsAreReachableWithoutTheBusinessEnvelope(t *testing.T) {
	handler, mailer := newAccountRPCHandler(t)
	session := registerThroughRPC(t, handler, mailer, "nakama-rpc@example.com", "nakama_rpc")

	login := handler.HandleRPC(RPCRequest{
		ID:      "auth.login",
		Payload: map[string]any{"email": "nakama-rpc@example.com", "password": "nakama-rpc-password-1"},
	})
	if !login.OK || login.Status != 200 {
		t.Fatalf("auth.login without an envelope should be accepted, got %+v", login)
	}
	relogin, ok := login.Payload.(*core.AuthSession)
	if !ok || relogin.UserID != session.UserID {
		t.Fatalf("auth.login payload = %T (%+v), want the same account %q", login.Payload, login.Payload, session.UserID)
	}
}

// TestAccountLogoutRequiresTheBusinessEnvelope covers the post-authentication
// half of the account surface: auth.logout is not pre-auth, so it stays gated.
func TestAccountLogoutRequiresTheBusinessEnvelope(t *testing.T) {
	handler, mailer := newAccountRPCHandler(t)
	session := registerThroughRPC(t, handler, mailer, "nakama-rpc-logout@example.com", "nakama_logout")

	missing := handler.HandleRPC(RPCRequest{
		ID:        "auth.logout",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   map[string]any{},
	})
	if missing.OK || missing.ErrorCode != CodeBusinessEnvelopeRequired {
		t.Fatalf("auth.logout must require a business envelope, got %+v", missing)
	}

	logout := handler.HandleRPC(RPCRequest{
		ID:        "auth.logout",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "nonce-rpc-logout", "auth_logout", map[string]any{}),
	})
	if !logout.OK || logout.Status != 200 {
		t.Fatalf("auth.logout through an envelope should succeed, got %+v", logout)
	}
	body, ok := logout.Payload.(map[string]any)
	if !ok || body["ok"] != true {
		t.Fatalf("auth.logout payload = %T (%+v), want {ok:true}", logout.Payload, logout.Payload)
	}
}

// TestCheckinRPCsRequireTheBusinessEnvelope covers the check-in surface, which
// previously existed only as HTTP routes.
func TestCheckinRPCsRequireTheBusinessEnvelope(t *testing.T) {
	handler, mailer := newAccountRPCHandler(t)
	session := registerThroughRPC(t, handler, mailer, "nakama-rpc-checkin@example.com", "nakama_checkin")

	for _, id := range []string{"checkin.get", "checkin.claim"} {
		missing := handler.HandleRPC(RPCRequest{
			ID:        id,
			SessionID: session.SessionToken,
			UserID:    session.UserID,
			Payload:   map[string]any{},
		})
		if missing.OK || missing.ErrorCode != CodeBusinessEnvelopeRequired {
			t.Fatalf("%s must require a business envelope, got %+v", id, missing)
		}
	}

	view := handler.HandleRPC(RPCRequest{
		ID:        "checkin.get",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "nonce-rpc-checkin-get", "checkin_get", map[string]any{}),
	})
	if !view.OK || view.Status != 200 {
		t.Fatalf("checkin.get through an envelope should succeed, got %+v", view)
	}
	snapshot, ok := view.Payload.(*core.CheckinView)
	if !ok || !snapshot.OK || len(snapshot.Days) == 0 {
		t.Fatalf("checkin.get payload = %T (%+v), want a populated check-in view", view.Payload, view.Payload)
	}

	claim := handler.HandleRPC(RPCRequest{
		ID:        "checkin.claim",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(2, "nonce-rpc-checkin-claim", "checkin_claim", map[string]any{}),
	})
	if !claim.OK || claim.Status != 200 {
		t.Fatalf("checkin.claim through an envelope should succeed, got %+v", claim)
	}
	if _, ok := claim.Payload.(*core.CheckinClaimView); !ok {
		t.Fatalf("checkin.claim payload = %T, want *core.CheckinClaimView", claim.Payload)
	}
}

// TestNakamaRPCRoutesClientContractAliases pins the alias spellings the client's
// REST route table also publishes. They must resolve on the RPC transport too,
// otherwise the two transports are not interchangeable.
func TestNakamaRPCRoutesClientContractAliases(t *testing.T) {
	handler, mailer := newAccountRPCHandler(t)
	session := registerThroughRPC(t, handler, mailer, "nakama-rpc-alias@example.com", "nakama_alias")

	// An unregistered rpc id answers not_found. The aliases are envelope-gated
	// operations, so reaching the envelope check proves the alias was routed.
	for _, id := range []string{"chests.get", "rooms.messages"} {
		missing := handler.HandleRPC(RPCRequest{
			ID:        id,
			SessionID: session.SessionToken,
			UserID:    session.UserID,
			Payload:   map[string]any{},
		})
		if missing.ErrorCode != CodeBusinessEnvelopeRequired {
			t.Fatalf("alias %s should be routed and envelope-gated, got %+v", id, missing)
		}
	}

	unknown := handler.HandleRPC(RPCRequest{
		ID:        "chests.nonexistent",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "nonce-rpc-unknown", "chests_nonexistent", map[string]any{}),
	})
	if unknown.ErrorCode != "not_found" {
		t.Fatalf("an unregistered rpc id must answer not_found, got %+v", unknown)
	}

	chests := handler.HandleRPC(RPCRequest{
		ID:        "chests.get",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(2, "nonce-rpc-chests-get", "chests_get", map[string]any{}),
	})
	if !chests.OK || chests.Status != 200 {
		t.Fatalf("chests.get through an envelope should succeed, got %+v", chests)
	}
	if _, ok := chests.Payload.(*core.ChestSnapshot); !ok {
		t.Fatalf("chests.get payload = %T, want *core.ChestSnapshot", chests.Payload)
	}
}

// TestClientOperationContractsSeparatePreAuthFromBusinessOperations keeps the
// published contract honest. The contract is the *business* surface, and every
// entry it lists must be envelope-gated. The pre-authentication account
// operations cannot be, so — exactly like the long-standing auth.anonymous —
// they must stay out of it and be registered separately.
func TestClientOperationContractsSeparatePreAuthFromBusinessOperations(t *testing.T) {
	for _, operation := range []string{"auth.anonymous", "auth.emailCode", "auth.register", "auth.login"} {
		if rpcTestHasOperation(core.ContractClientOperations(), operation) {
			t.Fatalf("pre-auth operation %q must stay out of the business contract: %+v", operation, core.ContractClientOperations())
		}
	}

	// The post-authentication half of the same surfaces is business-gated, so it
	// belongs in the contract.
	postAuth := []string{"auth.logout", "checkin.get", "checkin.claim"}
	for _, operation := range postAuth {
		if !rpcTestHasOperation(core.ContractClientOperations(), operation) {
			t.Fatalf("client operation contract is missing %q: %+v", operation, core.ContractClientOperations())
		}
	}

	seen := map[string]bool{}
	for _, contract := range core.ContractClientOperationContracts() {
		seen[contract.Operation] = true
		if !contract.BusinessEnvelopeRequired {
			t.Fatalf("every published client operation must be envelope-gated, %q is not: %+v", contract.Operation, contract)
		}
	}
	if len(seen) == 0 {
		t.Fatalf("client operation contracts must not be empty")
	}
	for _, operation := range postAuth {
		if !seen[operation] {
			t.Fatalf("published client contract for %q is missing", operation)
		}
	}
}

func rpcTestHasOperation(operations []string, want string) bool {
	for _, operation := range operations {
		if operation == want {
			return true
		}
	}
	return false
}
