package nakamaapi

import (
	"testing"

	"gensoulkyo/runtime/core"
)

// TestRecordsRPCRequiresTheBusinessEnvelope 证明 records.get 是登录后业务操作：
// 缺信封时被门禁拦截，带信封时返回服务端权威的 *core.RecordsView。
func TestRecordsRPCRequiresTheBusinessEnvelope(t *testing.T) {
	handler, mailer := newAccountRPCHandler(t)
	session := registerThroughRPC(t, handler, mailer, "nakama-rpc-records@example.com", "nakama_records")

	missing := handler.HandleRPC(RPCRequest{
		ID:        "records.get",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   map[string]any{},
	})
	if missing.OK || missing.ErrorCode != CodeBusinessEnvelopeRequired {
		t.Fatalf("records.get must require a business envelope, got %+v", missing)
	}

	view := handler.HandleRPC(RPCRequest{
		ID:        "records.get",
		SessionID: session.SessionToken,
		UserID:    session.UserID,
		Payload:   envelopePayload(1, "nonce-rpc-records-get", "records_get", map[string]any{}),
	})
	if !view.OK || view.Status != 200 {
		t.Fatalf("records.get through an envelope should succeed, got %+v", view)
	}
	records, ok := view.Payload.(*core.RecordsView)
	if !ok || records == nil || !records.OK || !records.ServerAuthoritative || records.ClientResultAuthoritative {
		t.Fatalf("records.get payload = %T (%+v), want an authoritative *core.RecordsView", view.Payload, view.Payload)
	}
	if records.UserID != session.UserID {
		t.Fatalf("records.get should project the caller: %+v", records)
	}
}
