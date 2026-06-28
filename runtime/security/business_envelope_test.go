package security

import (
	"errors"
	"testing"
	"time"
)

func TestBusinessEnvelopeGuardValidatesReplayAndAudit(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	guard := NewBusinessEnvelopeGuard(WithBusinessEnvelopeClock(func() time.Time { return now }))

	first := validEnvelope(1, now, "nonce-a")
	result := guard.Validate(first)
	if !result.OK || result.Seq != 1 || result.Nonce != "nonce-a" {
		t.Fatalf("valid envelope rejected: %+v", result)
	}

	seqReplay := validEnvelope(1, now, "nonce-b")
	result = guard.Validate(seqReplay)
	if result.OK || result.Code != CodeBusinessEnvelopeReplay || result.Reason != ReasonSeqReplay || !result.Replay {
		t.Fatalf("seq replay should be rejected: %+v", result)
	}

	nonceReplay := validEnvelope(2, now, "nonce-a")
	result = guard.Validate(nonceReplay)
	if result.OK || result.Code != CodeBusinessEnvelopeReplay || result.Reason != ReasonNonceReplay || !result.Replay {
		t.Fatalf("nonce replay should be rejected: %+v", result)
	}

	stale := validEnvelope(3, now.Add(-10*time.Minute), "nonce-c")
	result = guard.Validate(stale)
	if result.OK || result.Code != CodeBusinessEnvelopeReplay || result.Reason != ReasonTimestampStale || !result.Replay {
		t.Fatalf("stale timestamp should be rejected: %+v", result)
	}

	snapshot := guard.Snapshot()
	if snapshot.Version != BusinessEnvelopeVersion || snapshot.Accepted != 1 || snapshot.Rejected != 3 || snapshot.SessionCount != 1 || len(snapshot.Audits) != 4 {
		t.Fatalf("guard snapshot invalid: %+v", snapshot)
	}
	if snapshot.Audits[0].SessionIDHint == "test-session" || snapshot.Audits[0].SessionIDHint == "" || !snapshot.Audits[0].Accepted {
		t.Fatalf("audit should use a hashed session hint: %+v", snapshot.Audits[0])
	}
}

func TestBusinessEnvelopeGuardRejectsMalformedFields(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	guard := NewBusinessEnvelopeGuard(WithBusinessEnvelopeClock(func() time.Time { return now }))

	cases := []struct {
		name   string
		mutate func(*BusinessEnvelope)
		reason string
	}{
		{name: "session", mutate: func(envelope *BusinessEnvelope) { envelope.SessionID = "" }, reason: ReasonSessionMissing},
		{name: "version", mutate: func(envelope *BusinessEnvelope) { envelope.Version = "bad" }, reason: ReasonVersion},
		{name: "seq", mutate: func(envelope *BusinessEnvelope) { envelope.Seq = 0 }, reason: ReasonSeqMissing},
		{name: "timestamp", mutate: func(envelope *BusinessEnvelope) { envelope.TimestampMS = 0 }, reason: ReasonTimestamp},
		{name: "future", mutate: func(envelope *BusinessEnvelope) { envelope.TimestampMS = now.Add(10 * time.Minute).UnixMilli() }, reason: ReasonTimestampFuture},
		{name: "nonce", mutate: func(envelope *BusinessEnvelope) { envelope.Nonce = "" }, reason: ReasonNonceMissing},
		{name: "op", mutate: func(envelope *BusinessEnvelope) { envelope.Op = "" }, reason: ReasonOpMissing},
		{name: "key", mutate: func(envelope *BusinessEnvelope) { envelope.KeyID = "" }, reason: ReasonKeyIDMissing},
		{name: "mode", mutate: func(envelope *BusinessEnvelope) { envelope.Mode = "" }, reason: ReasonModeMissing},
		{name: "tag", mutate: func(envelope *BusinessEnvelope) { envelope.Tag = "not-hex" }, reason: ReasonTag},
	}

	for index, tc := range cases {
		envelope := validEnvelope(int64(index+1), now, tc.name+"-nonce")
		tc.mutate(&envelope)
		result := guard.Validate(envelope)
		if result.OK || result.Reason != tc.reason {
			t.Fatalf("%s malformed envelope mismatch: %+v", tc.name, result)
		}
	}
}

func TestBusinessEnvelopeGuardWritesAuditSink(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	audits := []BusinessEnvelopeAudit{}
	guard := NewBusinessEnvelopeGuard(
		WithBusinessEnvelopeClock(func() time.Time { return now }),
		WithBusinessEnvelopeAuditSink(BusinessEnvelopeAuditSinkFunc(func(audit BusinessEnvelopeAudit) error {
			audits = append(audits, audit)
			return nil
		})),
	)

	if result := guard.Validate(validEnvelope(1, now, "nonce-a")); !result.OK {
		t.Fatalf("valid envelope rejected: %+v", result)
	}
	if result := guard.Validate(validEnvelope(1, now, "nonce-b")); result.OK || result.Reason != ReasonSeqReplay {
		t.Fatalf("replayed envelope should be rejected: %+v", result)
	}

	if len(audits) != 2 || !audits[0].Accepted || audits[1].Accepted || audits[1].Reason != ReasonSeqReplay {
		t.Fatalf("audit sink mismatch: %+v", audits)
	}
	if audits[0].SessionIDHint == "test-session" || audits[0].SessionIDHint == "" {
		t.Fatalf("audit sink should receive a sanitized session hint: %+v", audits[0])
	}
	if snapshot := guard.Snapshot(); snapshot.AuditErrors != 0 {
		t.Fatalf("audit sink should not report errors: %+v", snapshot)
	}
}

func TestBusinessEnvelopeGuardCountsAuditSinkErrors(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	guard := NewBusinessEnvelopeGuard(
		WithBusinessEnvelopeClock(func() time.Time { return now }),
		WithBusinessEnvelopeAuditSink(BusinessEnvelopeAuditSinkFunc(func(audit BusinessEnvelopeAudit) error {
			return errors.New("audit store offline")
		})),
	)

	result := guard.Validate(validEnvelope(1, now, "nonce-a"))
	if !result.OK {
		t.Fatalf("audit sink failure should not reject a valid envelope: %+v", result)
	}
	snapshot := guard.Snapshot()
	if snapshot.Accepted != 1 || snapshot.AuditErrors != 1 || len(snapshot.Audits) != 1 {
		t.Fatalf("audit sink error accounting invalid: %+v", snapshot)
	}
}

func validEnvelope(seq int64, timestamp time.Time, nonce string) BusinessEnvelope {
	return BusinessEnvelope{
		SessionID:   "test-session",
		Version:     BusinessEnvelopeVersion,
		Seq:         seq,
		TimestampMS: timestamp.UnixMilli(),
		Nonce:       nonce,
		Op:          "bootstrap",
		KeyID:       "dev-business-envelope-v0",
		Tag:         "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Mode:        "not_encrypted_http_fallback",
	}
}
