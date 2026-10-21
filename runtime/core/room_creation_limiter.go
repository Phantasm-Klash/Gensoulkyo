package core

import (
	"strings"
	"sync"
	"time"
)

// DefaultRoomCreationLimit bounds how many rooms one player may create inside
// DefaultRoomCreationWindow.
//
// Room creation is cheap for the lobby but expensive for the battle host: every
// room that fills spawns a process, and a client that loops "create room, wait
// for a player, leave" would otherwise churn processes and allocations forever.
// The room TTL sweep reaps abandoned rooms, but it cannot outpace a client that
// creates them faster than the sweep runs.
const (
	DefaultRoomCreationLimit  = 5
	DefaultRoomCreationWindow = time.Minute
)

// RoomCreationLimiter answers "may this player create another room right now".
//
// Implementations must be safe for concurrent use. Returning an error means "I
// could not tell", which the lobby treats as allowed: a limiter outage must not
// stop people from playing, it only means the abuse window reopens.
type RoomCreationLimiter interface {
	AllowRoomCreation(userID string, now time.Time) (bool, error)
}

// memoryRoomCreationLimiter is the fallback when no Redis is configured.
//
// It is per-process, so it only holds back a single instance's worth of abuse;
// with several lobby replicas the real limiter is the Redis one. It still gives
// a single-node deployment the same protection and keeps tests free of a Redis
// dependency.
type memoryRoomCreationLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

// NewMemoryRoomCreationLimiter builds the in-process limiter. A non-positive
// limit falls back to the default, and a non-positive window disables the check
// (which is how a deployment opts out entirely).
func NewMemoryRoomCreationLimiter(limit int, window time.Duration) RoomCreationLimiter {
	if limit <= 0 {
		limit = DefaultRoomCreationLimit
	}
	return &memoryRoomCreationLimiter{
		limit:  limit,
		window: window,
		hits:   map[string][]time.Time{},
	}
}

func (limiter *memoryRoomCreationLimiter) AllowRoomCreation(userID string, now time.Time) (bool, error) {
	trimmed := strings.TrimSpace(userID)
	if limiter == nil || trimmed == "" || limiter.window <= 0 {
		return true, nil
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	// Drop hits that fell out of the window, then count what is left.
	recent := limiter.hits[trimmed][:0]
	for _, at := range limiter.hits[trimmed] {
		if now.Sub(at) < limiter.window {
			recent = append(recent, at)
		}
	}
	if len(recent) >= limiter.limit {
		limiter.hits[trimmed] = recent
		return false, nil
	}
	limiter.hits[trimmed] = append(recent, now)
	return true, nil
}

// roomCreationLimiterOrDefault supplies the in-process limiter when a deployment
// injects none.
//
// The limit is on by default because the cost it guards against -- a client
// looping create/abandon -- is a real failure mode, and the failure mode of the
// limiter itself is "allowed", so being wrong here degrades to the previous
// behaviour rather than to an outage. Deployments that run several lobby replicas
// inject the Redis-backed limiter from runtime/storage instead, because only a
// shared counter bounds the total.
func roomCreationLimiterOrDefault(limiter RoomCreationLimiter) RoomCreationLimiter {
	if limiter != nil {
		return limiter
	}
	return NewMemoryRoomCreationLimiter(DefaultRoomCreationLimit, DefaultRoomCreationWindow)
}

// checkRoomCreationLimitLocked applies the configured limiter. Callers must hold
// s.mu.
//
// The limiter is consulted, not trusted: an error is logged as a failed check and
// the request is allowed through, because refusing to create a room is worse
// than letting one extra abuse attempt through.
func (s *Service) checkRoomCreationLimitLocked(userID string) error {
	if s.roomCreationLimiter == nil {
		return nil
	}
	allowed, err := s.roomCreationLimiter.AllowRoomCreation(userID, s.clock())
	if err != nil {
		// The outcome counters exist for observability; a limiter that cannot
		// answer must not become the reason a player cannot play.
		s.recordStoreOutcomeLocked("room_creation_limit", false, err)
		return nil
	}
	if !allowed {
		return newError(codeRateLimited, "too many rooms created recently")
	}
	return nil
}
