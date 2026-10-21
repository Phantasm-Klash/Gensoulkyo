package storage

import (
	"errors"
	"strings"
	"time"

	"gensoulkyo/runtime/core"
)

// RedisRoomCreationLimiter is the cross-instance counterpart of the in-process
// limiter: with more than one lobby replica only a shared counter bounds the
// total.
//
// The window is fixed, not sliding. RedisClient.Allow increments a counter and
// sets a TTL on the first hit, so the budget resets at window boundaries rather
// than continuously. That is the cheaper shape (one key per user, no history) and
// it is enough to stop a loop: the worst case is a burst at a boundary, bounded
// by the limit, not an unbounded stream. The in-process limiter is the sliding
// one, because it already keeps the recent hits in memory.
type RedisRoomCreationLimiter struct {
	client *RedisClient
	limit  int64
	window time.Duration
}

// NewRedisRoomCreationLimiter wraps a client. A nil client is refused rather
// than silently becoming a no-op limiter, so a deployment cannot end up thinking
// it is rate limited when it is not.
func NewRedisRoomCreationLimiter(client *RedisClient, limit int, window time.Duration) (*RedisRoomCreationLimiter, error) {
	if client == nil {
		return nil, errors.New("redis room creation limiter requires a client")
	}
	if limit <= 0 {
		limit = core.DefaultRoomCreationLimit
	}
	if window <= 0 {
		window = core.DefaultRoomCreationWindow
	}
	return &RedisRoomCreationLimiter{
		client: client,
		limit:  int64(limit),
		window: window,
	}, nil
}

// AllowRoomCreation implements core.RoomCreationLimiter.
//
// The now argument is part of the interface for the in-process limiter, which
// needs a clock to expire its window. Redis keeps its own time via the key TTL,
// so it is unused here.
func (limiter *RedisRoomCreationLimiter) AllowRoomCreation(userID string, now time.Time) (bool, error) {
	_ = now
	if limiter == nil || limiter.client == nil {
		return true, nil
	}
	trimmed := strings.TrimSpace(userID)
	if trimmed == "" {
		return true, nil
	}
	return limiter.client.Allow(roomCreationKey(trimmed), limiter.limit, limiter.window)
}

// roomCreationKey namespaces the counter. The prefix is part of the operational
// contract: ops scripts that inspect or clear these keys grep for it.
func roomCreationKey(userID string) string {
	return "phk:roomcreate:" + userID
}
