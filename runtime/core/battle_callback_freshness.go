package core

import (
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// B4-7.1: freshness + replay protection for the battle-server callback face.
//
// T6 (out/T6_callback_auth.md) authenticated every callback with
// hex(HMAC-SHA256(perServerSecret, rawBody)), but the signed material covered
// only the body, so a captured callback could be replayed verbatim. This file
// adds two optional request headers and, when BOTH are present, folds them into
// the signed material and enforces a freshness window plus nonce uniqueness:
//
//	signed_material = timestamp + "\n" + nonce + "\n" + rawBody
//
// The rollout is staged: the C++ signer that will emit these headers lives in
// another repo and cannot be changed here, so the legacy form (no headers, HMAC
// over the raw body alone) is still accepted AND counted (see
// LegacyBattleCallbackCount). Only once that counter reaches zero can the
// legacy form be rejected — a one-line follow-up. See
// task-queue/out/B4-7.1_replay_go.md for the exact contract.
const (
	// battleCallbackTimestampWindow is the tolerated clock skew on the
	// X-PhK-Battle-Timestamp header (unix seconds): ±300s.
	battleCallbackTimestampWindow = 300 * time.Second
	// battleCallbackNonceRetention is how long a seen nonce is remembered. It
	// is twice the timestamp window so a nonce survives until any callback
	// carrying it would already be rejected as stale: a timestamp at now+300s
	// stays fresh until now+600s, so the nonce must be held that long.
	battleCallbackNonceRetention = 2 * battleCallbackTimestampWindow
	// maxBattleCallbackNonceLen caps the accepted X-PhK-Battle-Nonce length.
	maxBattleCallbackNonceLen = 64
	// maxBattleCallbackNoncesPerServer bounds the per-server seen-nonce store.
	// The hard cap is a memory backstop; the submission rate limit (B4-7.2)
	// is the primary flood control.
	maxBattleCallbackNoncesPerServer = 4096
)

// BattleCallbackSignedMaterial returns the exact byte string the callback HMAC
// must cover. When BOTH the timestamp and the nonce are non-empty the material
// is timestamp + "\n" + nonce + "\n" + rawBody; otherwise it is the raw body
// alone (the legacy, pre-signer form). The C++ signer must produce the same
// bytes: LF separators, raw header text (no JSON quoting/escaping).
func BattleCallbackSignedMaterial(timestamp string, nonce string, rawBody []byte) []byte {
	ts := strings.TrimSpace(timestamp)
	n := strings.TrimSpace(nonce)
	if ts == "" || n == "" {
		return rawBody
	}
	material := make([]byte, 0, len(ts)+1+len(n)+1+len(rawBody))
	material = append(material, ts...)
	material = append(material, '\n')
	material = append(material, n...)
	material = append(material, '\n')
	material = append(material, rawBody...)
	return material
}

// battleCallbackNonceWindow is the bounded, insertion-ordered seen-nonce set
// for one battle server. seen gives O(1) replay detection; order is the FIFO
// eviction queue (oldest first).
type battleCallbackNonceWindow struct {
	seen  map[string]time.Time
	order []string
}

// VerifyBattleCallback authenticates a service callback for battleServerID and,
// when the freshness headers are present, enforces the timestamp window and
// nonce uniqueness. It returns ("", true) when accepted; otherwise a stable
// error_code and false. The caller maps the code to a 403 response.
//
// Order matters: the HMAC is verified FIRST, so an attacker without the secret
// cannot consume nonce budget or evict legitimate nonces. A partial header set
// (only one of timestamp/nonce) is rejected rather than downgraded to the
// replayable legacy material.
func (s *Service) VerifyBattleCallback(battleServerID string, rawBody []byte, timestamp string, nonce string, presentedSignature string) (string, bool) {
	serverID := strings.TrimSpace(battleServerID)
	if serverID == "" {
		return "battle_callback_signature_invalid", false
	}
	ts := strings.TrimSpace(timestamp)
	n := strings.TrimSpace(nonce)
	if (ts == "") != (n == "") {
		return "battle_callback_freshness_incomplete", false
	}
	if ts == "" {
		// Legacy form (pre-C++-signer): HMAC over the raw body only. Accepted
		// during the staged rollout and counted so the migration is observable.
		if !s.BattleCallbackSignatureValid(serverID, rawBody, presentedSignature) {
			return "battle_callback_signature_invalid", false
		}
		atomic.AddInt64(&s.legacyBattleCallbacks, 1)
		return "", true
	}
	if len(n) > maxBattleCallbackNonceLen {
		return "battle_callback_nonce_invalid", false
	}
	material := BattleCallbackSignedMaterial(ts, n, rawBody)
	if !s.BattleCallbackSignatureValid(serverID, material, presentedSignature) {
		return "battle_callback_signature_invalid", false
	}
	now := s.clock()
	if !battleCallbackTimestampFresh(ts, now) {
		return "battle_callback_timestamp_stale", false
	}
	s.mu.Lock()
	fresh := s.allowBattleCallbackNonceLocked(serverID, n, now)
	s.mu.Unlock()
	if !fresh {
		return "battle_callback_replay", false
	}
	return "", true
}

// LegacyBattleCallbackCount reports how many callbacks authenticated through the
// pre-freshness form (HMAC over the raw body only, no timestamp/nonce headers).
// It is the rollout signal: once it is zero, the C++ signer ships the new
// headers and the legacy form can be rejected (see the B4-7.1 doc).
func (s *Service) LegacyBattleCallbackCount() int64 {
	return atomic.LoadInt64(&s.legacyBattleCallbacks)
}

// allowBattleCallbackNonceLocked records nonce as seen for battleServerID and
// reports whether it was new (false = replay). The caller holds s.mu and must
// have already verified the HMAC over the freshness-bearing material.
func (s *Service) allowBattleCallbackNonceLocked(battleServerID string, nonce string, now time.Time) bool {
	if s.battleCallbackNonces == nil {
		s.battleCallbackNonces = map[string]*battleCallbackNonceWindow{}
	}
	window := s.battleCallbackNonces[battleServerID]
	if window == nil {
		window = &battleCallbackNonceWindow{seen: map[string]time.Time{}}
		s.battleCallbackNonces[battleServerID] = window
	}
	// Drop nonces past retention. order is insertion order, so expired entries
	// are a prefix.
	cutoff := now.Add(-battleCallbackNonceRetention)
	drop := 0
	for drop < len(window.order) {
		if seenAt := window.seen[window.order[drop]]; seenAt.After(cutoff) {
			break
		}
		delete(window.seen, window.order[drop])
		drop++
	}
	if drop > 0 {
		window.order = window.order[drop:]
	}
	if _, replayed := window.seen[nonce]; replayed {
		return false
	}
	// Enforce the hard size bound, evicting the oldest first. Under the
	// configured submission rate limit this only triggers on a sustained flood.
	for len(window.order) >= maxBattleCallbackNoncesPerServer {
		evicted := window.order[0]
		window.order = window.order[1:]
		delete(window.seen, evicted)
	}
	window.seen[nonce] = now
	window.order = append(window.order, nonce)
	return true
}

// battleCallbackTimestampFresh reports whether timestamp is a unix-seconds value
// within battleCallbackTimestampWindow of now.
func battleCallbackTimestampFresh(timestamp string, now time.Time) bool {
	secs, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	delta := now.Unix() - secs
	if delta < 0 {
		delta = -delta
	}
	return delta <= int64(battleCallbackTimestampWindow/time.Second)
}
