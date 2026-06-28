package security

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

const (
	BusinessEnvelopeVersion  = "business-v0-scaffold"
	BusinessEnvelopeMaxSkew  = 5 * time.Minute
	BusinessEnvelopeNonceCap = 128

	HeaderBusinessEnvelope    = "X-PhK-Business-Envelope"
	HeaderBusinessSeq         = "X-PhK-Business-Seq"
	HeaderBusinessTimestampMs = "X-PhK-Business-Timestamp-Ms"
	HeaderBusinessNonce       = "X-PhK-Business-Nonce"
	HeaderBusinessOp          = "X-PhK-Business-Op"
	HeaderBusinessKeyID       = "X-PhK-Business-Key-Id"
	HeaderBusinessTag         = "X-PhK-Business-Tag"
	HeaderBusinessMode        = "X-PhK-Business-Mode"
	HeaderBusinessBodyHash    = "X-PhK-Business-Body-Hash"

	CodeBusinessEnvelopeInvalid = "business_envelope_invalid"
	CodeBusinessEnvelopeReplay  = "business_envelope_replay"

	ReasonSessionMissing  = "session_missing"
	ReasonVersion         = "version"
	ReasonSeqMissing      = "seq_missing"
	ReasonTimestamp       = "timestamp"
	ReasonTimestampStale  = "timestamp_stale"
	ReasonTimestampFuture = "timestamp_future"
	ReasonNonceMissing    = "nonce_missing"
	ReasonOpMissing       = "op_missing"
	ReasonKeyIDMissing    = "key_id_missing"
	ReasonModeMissing     = "mode_missing"
	ReasonTag             = "tag"
	ReasonSeqReplay       = "seq_replay"
	ReasonNonceReplay     = "nonce_replay"
)

type BusinessEnvelope struct {
	SessionID   string
	Version     string
	Seq         int64
	TimestampMS int64
	Nonce       string
	Op          string
	KeyID       string
	Tag         string
	Mode        string
	Transport   string
	Endpoint    string
	UserID      string
	BodyHash    string
}

type BusinessEnvelopeValidation struct {
	OK      bool
	Code    string
	Reason  string
	Message string
	Replay  bool
	Seq     int64
	Nonce   string
}

type BusinessEnvelopeAudit struct {
	SessionIDHint string `json:"session_id_hint"`
	UserID        string `json:"user_id"`
	Transport     string `json:"transport"`
	Endpoint      string `json:"endpoint"`
	Op            string `json:"op"`
	KeyID         string `json:"key_id"`
	Version       string `json:"version"`
	Seq           int64  `json:"seq"`
	Nonce         string `json:"nonce"`
	TimestampMS   int64  `json:"timestamp_ms"`
	ServerTimeMS  int64  `json:"server_time_ms"`
	Accepted      bool   `json:"accepted"`
	Code          string `json:"code"`
	Reason        string `json:"reason"`
	Replay        bool   `json:"replay"`
	BodyHash      string `json:"body_hash"`
	AuthTagPrefix string `json:"auth_tag_prefix"`
}

type BusinessEnvelopeGuardSnapshot struct {
	Version      string                  `json:"version"`
	MaxSkewMS    int64                   `json:"max_skew_ms"`
	NonceCap     int                     `json:"nonce_cap"`
	Accepted     int64                   `json:"accepted"`
	Rejected     int64                   `json:"rejected"`
	AuditErrors  int64                   `json:"audit_errors"`
	SessionCount int                     `json:"session_count"`
	Audits       []BusinessEnvelopeAudit `json:"audits"`
}

type BusinessEnvelopeAuditSink interface {
	RecordBusinessEnvelopeAudit(audit BusinessEnvelopeAudit) error
}

type BusinessEnvelopeAuditSinkFunc func(BusinessEnvelopeAudit) error

func (fn BusinessEnvelopeAuditSinkFunc) RecordBusinessEnvelopeAudit(audit BusinessEnvelopeAudit) error {
	return fn(audit)
}

type BusinessEnvelopeGuard struct {
	mu          sync.Mutex
	version     string
	maxSkew     time.Duration
	nonceCap    int
	maxAudits   int
	now         func() time.Time
	auditSink   BusinessEnvelopeAuditSink
	sessions    map[string]*businessEnvelopeSessionGuard
	accepted    int64
	rejected    int64
	auditErrors int64
	auditTrail  []BusinessEnvelopeAudit
}

type businessEnvelopeSessionGuard struct {
	lastSeq    int64
	seenNonce  map[string]struct{}
	nonceOrder []string
}

type BusinessEnvelopeGuardOption func(*BusinessEnvelopeGuard)

func NewBusinessEnvelopeGuard(options ...BusinessEnvelopeGuardOption) *BusinessEnvelopeGuard {
	guard := &BusinessEnvelopeGuard{
		version:   BusinessEnvelopeVersion,
		maxSkew:   BusinessEnvelopeMaxSkew,
		nonceCap:  BusinessEnvelopeNonceCap,
		maxAudits: 64,
		now:       time.Now,
		sessions:  map[string]*businessEnvelopeSessionGuard{},
	}
	for _, option := range options {
		option(guard)
	}
	if guard.now == nil {
		guard.now = time.Now
	}
	if guard.nonceCap <= 0 {
		guard.nonceCap = BusinessEnvelopeNonceCap
	}
	if guard.maxSkew <= 0 {
		guard.maxSkew = BusinessEnvelopeMaxSkew
	}
	if guard.maxAudits <= 0 {
		guard.maxAudits = 64
	}
	return guard
}

func WithBusinessEnvelopeClock(clock func() time.Time) BusinessEnvelopeGuardOption {
	return func(guard *BusinessEnvelopeGuard) {
		if clock != nil {
			guard.now = clock
		}
	}
}

func WithBusinessEnvelopeMaxSkew(maxSkew time.Duration) BusinessEnvelopeGuardOption {
	return func(guard *BusinessEnvelopeGuard) {
		if maxSkew > 0 {
			guard.maxSkew = maxSkew
		}
	}
}

func WithBusinessEnvelopeNonceCap(nonceCap int) BusinessEnvelopeGuardOption {
	return func(guard *BusinessEnvelopeGuard) {
		if nonceCap > 0 {
			guard.nonceCap = nonceCap
		}
	}
}

func WithBusinessEnvelopeAuditCap(maxAudits int) BusinessEnvelopeGuardOption {
	return func(guard *BusinessEnvelopeGuard) {
		if maxAudits > 0 {
			guard.maxAudits = maxAudits
		}
	}
}

func WithBusinessEnvelopeAuditSink(sink BusinessEnvelopeAuditSink) BusinessEnvelopeGuardOption {
	return func(guard *BusinessEnvelopeGuard) {
		guard.auditSink = sink
	}
}

func (guard *BusinessEnvelopeGuard) Validate(envelope BusinessEnvelope) BusinessEnvelopeValidation {
	envelope = normalizeBusinessEnvelope(envelope)
	nowMS := guard.now().UnixMilli()
	if envelope.SessionID == "" {
		return guard.reject(envelope, nowMS, CodeBusinessEnvelopeInvalid, ReasonSessionMissing, "business envelope requires an authenticated session", false)
	}
	if envelope.Version != guard.version {
		return guard.reject(envelope, nowMS, CodeBusinessEnvelopeInvalid, ReasonVersion, "business envelope version is not supported", false)
	}
	if envelope.Seq <= 0 {
		return guard.reject(envelope, nowMS, CodeBusinessEnvelopeInvalid, ReasonSeqMissing, "business envelope seq is invalid", false)
	}
	if envelope.TimestampMS <= 0 {
		return guard.reject(envelope, nowMS, CodeBusinessEnvelopeInvalid, ReasonTimestamp, "business envelope timestamp is invalid", false)
	}
	maxSkewMS := guard.maxSkew.Milliseconds()
	if envelope.TimestampMS < nowMS-maxSkewMS {
		return guard.reject(envelope, nowMS, CodeBusinessEnvelopeReplay, ReasonTimestampStale, "business envelope timestamp is stale", true)
	}
	if envelope.TimestampMS > nowMS+maxSkewMS {
		return guard.reject(envelope, nowMS, CodeBusinessEnvelopeReplay, ReasonTimestampFuture, "business envelope timestamp is from the future", true)
	}
	if envelope.Nonce == "" {
		return guard.reject(envelope, nowMS, CodeBusinessEnvelopeInvalid, ReasonNonceMissing, "business envelope nonce is missing", false)
	}
	if envelope.Op == "" {
		return guard.reject(envelope, nowMS, CodeBusinessEnvelopeInvalid, ReasonOpMissing, "business envelope op is missing", false)
	}
	if envelope.KeyID == "" {
		return guard.reject(envelope, nowMS, CodeBusinessEnvelopeInvalid, ReasonKeyIDMissing, "business envelope key id is missing", false)
	}
	if envelope.Mode == "" {
		return guard.reject(envelope, nowMS, CodeBusinessEnvelopeInvalid, ReasonModeMissing, "business envelope mode is missing", false)
	}
	if len(envelope.Tag) != 64 || !isHex(envelope.Tag) {
		return guard.reject(envelope, nowMS, CodeBusinessEnvelopeInvalid, ReasonTag, "business envelope tag is invalid", false)
	}

	guard.mu.Lock()
	defer guard.mu.Unlock()
	session := guard.sessions[envelope.SessionID]
	if session == nil {
		session = &businessEnvelopeSessionGuard{seenNonce: map[string]struct{}{}}
		guard.sessions[envelope.SessionID] = session
	}
	if envelope.Seq <= session.lastSeq {
		return guard.rejectLocked(envelope, nowMS, CodeBusinessEnvelopeReplay, ReasonSeqReplay, "business envelope seq was already used", true)
	}
	if _, ok := session.seenNonce[envelope.Nonce]; ok {
		return guard.rejectLocked(envelope, nowMS, CodeBusinessEnvelopeReplay, ReasonNonceReplay, "business envelope nonce was already used", true)
	}
	session.lastSeq = envelope.Seq
	session.seenNonce[envelope.Nonce] = struct{}{}
	session.nonceOrder = append(session.nonceOrder, envelope.Nonce)
	for len(session.nonceOrder) > guard.nonceCap {
		oldest := session.nonceOrder[0]
		session.nonceOrder = session.nonceOrder[1:]
		delete(session.seenNonce, oldest)
	}
	guard.accepted++
	guard.recordAuditLocked(envelope, nowMS, true, "", "")
	return BusinessEnvelopeValidation{OK: true, Seq: envelope.Seq, Nonce: envelope.Nonce}
}

func (guard *BusinessEnvelopeGuard) Snapshot() BusinessEnvelopeGuardSnapshot {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	audits := make([]BusinessEnvelopeAudit, len(guard.auditTrail))
	copy(audits, guard.auditTrail)
	return BusinessEnvelopeGuardSnapshot{
		Version:      guard.version,
		MaxSkewMS:    guard.maxSkew.Milliseconds(),
		NonceCap:     guard.nonceCap,
		Accepted:     guard.accepted,
		Rejected:     guard.rejected,
		AuditErrors:  guard.auditErrors,
		SessionCount: len(guard.sessions),
		Audits:       audits,
	}
}

func (guard *BusinessEnvelopeGuard) reject(envelope BusinessEnvelope, nowMS int64, code string, reason string, message string, replay bool) BusinessEnvelopeValidation {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	return guard.rejectLocked(envelope, nowMS, code, reason, message, replay)
}

func (guard *BusinessEnvelopeGuard) rejectLocked(envelope BusinessEnvelope, nowMS int64, code string, reason string, message string, replay bool) BusinessEnvelopeValidation {
	guard.rejected++
	guard.recordAuditLocked(envelope, nowMS, false, code, reason)
	return BusinessEnvelopeValidation{
		OK:      false,
		Code:    code,
		Reason:  reason,
		Message: message,
		Replay:  replay,
		Seq:     envelope.Seq,
		Nonce:   envelope.Nonce,
	}
}

func (guard *BusinessEnvelopeGuard) recordAuditLocked(envelope BusinessEnvelope, nowMS int64, accepted bool, code string, reason string) {
	replay := code == CodeBusinessEnvelopeReplay
	audit := BusinessEnvelopeAudit{
		SessionIDHint: sessionIDHint(envelope.SessionID),
		UserID:        envelope.UserID,
		Transport:     envelope.Transport,
		Endpoint:      envelope.Endpoint,
		Op:            envelope.Op,
		KeyID:         envelope.KeyID,
		Version:       envelope.Version,
		Seq:           envelope.Seq,
		Nonce:         envelope.Nonce,
		TimestampMS:   envelope.TimestampMS,
		ServerTimeMS:  nowMS,
		Accepted:      accepted,
		Code:          code,
		Reason:        reason,
		Replay:        replay,
		BodyHash:      envelope.BodyHash,
		AuthTagPrefix: authTagPrefix(envelope.Tag),
	}
	guard.auditTrail = append(guard.auditTrail, audit)
	for len(guard.auditTrail) > guard.maxAudits {
		guard.auditTrail = guard.auditTrail[1:]
	}
	if guard.auditSink != nil {
		if err := guard.auditSink.RecordBusinessEnvelopeAudit(audit); err != nil {
			guard.auditErrors++
		}
	}
}

func normalizeBusinessEnvelope(envelope BusinessEnvelope) BusinessEnvelope {
	if envelope.Transport == "" {
		envelope.Transport = "unknown"
	}
	if envelope.Endpoint == "" {
		envelope.Endpoint = envelope.Op
	}
	return envelope
}

func authTagPrefix(tag string) string {
	if len(tag) <= 16 {
		return tag
	}
	return tag[:16]
}

func sessionIDHint(sessionID string) string {
	if sessionID == "" {
		return "anonymous"
	}
	digest := sha256.Sum256([]byte(sessionID))
	return fmt.Sprintf("session:%s", hex.EncodeToString(digest[:])[:16])
}

func isHex(value string) bool {
	for _, ch := range value {
		if ch >= '0' && ch <= '9' {
			continue
		}
		if ch >= 'a' && ch <= 'f' {
			continue
		}
		if ch >= 'A' && ch <= 'F' {
			continue
		}
		return false
	}
	return true
}
