package security

import (
	"fmt"
	"testing"
	"time"
)

func TestValidateBusinessEnvelopeRequestMapsTransportNeutralResult(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	guard := NewBusinessEnvelopeGuard(WithBusinessEnvelopeClock(func() time.Time { return now }))

	result := ValidateBusinessEnvelopeRequest(guard, validEnvelopeRequest(1, now, "nonce-a"))
	if !result.OK || result.Status != BusinessEnvelopeStatusOK || result.Seq != 1 {
		t.Fatalf("valid request result invalid: %+v", result)
	}

	replay := ValidateBusinessEnvelopeRequest(guard, validEnvelopeRequest(1, now, "nonce-b"))
	if replay.OK || replay.Status != BusinessEnvelopeStatusConflict || replay.Code != CodeBusinessEnvelopeReplay || replay.Reason != ReasonSeqReplay {
		t.Fatalf("replay request result invalid: %+v", replay)
	}
}

func TestValidateBusinessEnvelopeRequestMapsMissingSession(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	request := validEnvelopeRequest(1, now, "nonce-a")
	request.SessionID = ""
	result := ValidateBusinessEnvelopeRequest(NewBusinessEnvelopeGuard(WithBusinessEnvelopeClock(func() time.Time { return now })), request)
	if result.OK || result.Status != BusinessEnvelopeStatusUnauthorized || result.Code != CodeBusinessEnvelopeInvalid || result.Reason != ReasonSessionMissing {
		t.Fatalf("missing session result invalid: %+v", result)
	}
}

func TestValidateBusinessEnvelopeRequestAllowsNilGuard(t *testing.T) {
	now := time.Now()
	result := ValidateBusinessEnvelopeRequest(nil, validEnvelopeRequest(1, now, "nonce-a"))
	if !result.OK || result.Status != BusinessEnvelopeStatusOK {
		t.Fatalf("nil guard fallback invalid: %+v", result)
	}
}

func TestBusinessEnvelopeRequestAdaptersShareGuard(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	guard := NewBusinessEnvelopeGuard(WithBusinessEnvelopeClock(func() time.Time { return now }))

	httpRequest, ok := BusinessEnvelopeRequestFromHTTPHeaders(validEnvelopeHeaders(1, now, "nonce-http", "bootstrap"), BusinessEnvelopeRequestContext{
		SessionID: "shared-session",
		Endpoint:  "/v1/bootstrap",
	})
	if !ok || httpRequest.Transport != BusinessEnvelopeTransportHTTPFallback || httpRequest.Endpoint != "/v1/bootstrap" {
		t.Fatalf("HTTP request adapter invalid: ok=%v request=%+v", ok, httpRequest)
	}
	if result := ValidateBusinessEnvelopeRequest(guard, httpRequest); !result.OK {
		t.Fatalf("HTTP request should validate: %+v", result)
	}

	rpcRequest, ok := BusinessEnvelopeRequestFromNakamaRPCPayload("shared-session", "bootstrap", "user-a", validEnvelopePayload(2, now, "nonce-rpc", "bootstrap"))
	if !ok || rpcRequest.Transport != BusinessEnvelopeTransportNakamaRPC || rpcRequest.Endpoint != "rpc.bootstrap" || rpcRequest.UserID != "user-a" {
		t.Fatalf("Nakama RPC request adapter invalid: ok=%v request=%+v", ok, rpcRequest)
	}
	if result := ValidateBusinessEnvelopeRequest(guard, rpcRequest); !result.OK {
		t.Fatalf("Nakama RPC request should validate: %+v", result)
	}

	wssRequest, ok := BusinessEnvelopeRequestFromNakamaWSSPayload("shared-session", "presence.heartbeat", "user-a", validEnvelopePayload(2, now, "nonce-wss", "presence_heartbeat"))
	if !ok || wssRequest.Transport != BusinessEnvelopeTransportNakamaWSS || wssRequest.Endpoint != "wss.presence.heartbeat" {
		t.Fatalf("Nakama WSS request adapter invalid: ok=%v request=%+v", ok, wssRequest)
	}
	replay := ValidateBusinessEnvelopeRequest(guard, wssRequest)
	if replay.OK || replay.Status != BusinessEnvelopeStatusConflict || replay.Reason != ReasonSeqReplay {
		t.Fatalf("Nakama WSS should share seq replay guard with HTTP/RPC: %+v", replay)
	}

	snapshot := guard.Snapshot()
	if snapshot.Accepted != 2 || snapshot.Rejected != 1 || len(snapshot.Audits) != 3 {
		t.Fatalf("shared guard snapshot invalid: %+v", snapshot)
	}
	if snapshot.Audits[0].Transport != BusinessEnvelopeTransportHTTPFallback || snapshot.Audits[1].Transport != BusinessEnvelopeTransportNakamaRPC || snapshot.Audits[2].Transport != BusinessEnvelopeTransportNakamaWSS {
		t.Fatalf("adapter audit transport order invalid: %+v", snapshot.Audits)
	}
}

func TestBusinessEnvelopeRequestAdaptersDetectAbsentEnvelope(t *testing.T) {
	if _, ok := BusinessEnvelopeRequestFromHTTPHeaders(map[string][]string{"Content-Type": {"application/json"}}, BusinessEnvelopeRequestContext{}); ok {
		t.Fatalf("HTTP adapter should ignore requests without business headers")
	}
	if _, ok := BusinessEnvelopeRequestFromNakamaRPCPayload("session", "bootstrap", "user", map[string]any{"payload": "plain"}); ok {
		t.Fatalf("RPC adapter should ignore payloads without business envelope")
	}
}

func validEnvelopeRequest(seq int64, timestamp time.Time, nonce string) BusinessEnvelopeRequest {
	return BusinessEnvelopeRequest{
		SessionID:   "test-session",
		Version:     BusinessEnvelopeVersion,
		Seq:         seq,
		TimestampMS: timestamp.UnixMilli(),
		Nonce:       nonce,
		Op:          "bootstrap",
		KeyID:       "dev-business-envelope-v0",
		Tag:         "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Mode:        "not_encrypted_http_fallback",
		Transport:   "nakama_rpc",
		Endpoint:    "rpc.bootstrap",
		BodyHash:    "body-hash",
	}
}

func validEnvelopeHeaders(seq int64, timestamp time.Time, nonce string, op string) map[string][]string {
	return map[string][]string{
		HeaderBusinessEnvelope:    {BusinessEnvelopeVersion},
		HeaderBusinessSeq:         {fmt.Sprintf("%d", seq)},
		HeaderBusinessTimestampMs: {fmt.Sprintf("%d", timestamp.UnixMilli())},
		HeaderBusinessNonce:       {nonce},
		HeaderBusinessOp:          {op},
		HeaderBusinessKeyID:       {"dev-business-envelope-v0"},
		HeaderBusinessTag:         {"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		HeaderBusinessMode:        {"not_encrypted_http_fallback"},
		HeaderBusinessBodyHash:    {"body-hash"},
	}
}

func validEnvelopePayload(seq int64, timestamp time.Time, nonce string, op string) map[string]any {
	return map[string]any{
		BusinessEnvelopePayloadKey: map[string]any{
			"version":         BusinessEnvelopeVersion,
			"seq":             seq,
			"timestamp_ms":    timestamp.UnixMilli(),
			"nonce":           nonce,
			"op_code":         op,
			"key_id":          "dev-business-envelope-v0",
			"auth_tag":        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"ciphertext_mode": "not_encrypted_nakama_scaffold",
			"body_hash":       "body-hash",
		},
	}
}
