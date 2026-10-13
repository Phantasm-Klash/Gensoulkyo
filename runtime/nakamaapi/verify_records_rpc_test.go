package nakamaapi

import (
	"testing"
)

// TestVerifyRecordsRPCAcceptsMixedCaseOperationID proves the switch really keys
// on the lower-cased id (the iron law about lower-case case labels): an
// upper-cased spelling must resolve to the same handler.
//
// NOTE: session-token validation is NOT this layer's job. The Nakama RPC bridge
// only runs after Nakama authenticated the caller, and loginExternal
// auto-provisions any non-empty (user_id, token) pair, so an unknown token is
// accepted here by design. Real rejection lives in core.userBySessionLocked,
// covered by TestVerifyRecordsRejectsInvalidSessionToken.
func TestVerifyRecordsRPCAcceptsMixedCaseOperationID(t *testing.T) {
	handler, mailer := newAccountRPCHandler(t)
	session := registerThroughRPC(t, handler, mailer, "nakama-rpc-records-case@example.com", "nakama_records_case")

	resp := handler.HandleRPC(RPCRequest{
		ID:        "Records.Get",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "nonce-records-case", "records_get", map[string]any{}),
	})
	if !resp.OK {
		t.Fatalf("Records.Get must normalize to records.get, got %+v", resp)
	}
}
