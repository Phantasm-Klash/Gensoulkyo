package core

import (
	"errors"
	"testing"
	"time"
)

// failingLimiter stands in for a Redis that is down: it cannot answer.
type failingLimiter struct{}

func (failingLimiter) AllowRoomCreation(userID string, now time.Time) (bool, error) {
	return false, errors.New("redis unreachable")
}

func TestMemoryRoomCreationLimiterAllowsUpToTheLimitThenRefuses(t *testing.T) {
	now := time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)
	limiter := NewMemoryRoomCreationLimiter(3, time.Minute)

	for attempt := 1; attempt <= 3; attempt++ {
		allowed, err := limiter.AllowRoomCreation("user-a", now)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if !allowed {
			t.Fatalf("attempt %d was refused, want the first three allowed", attempt)
		}
	}
	allowed, err := limiter.AllowRoomCreation("user-a", now)
	if err != nil {
		t.Fatalf("fourth attempt: %v", err)
	}
	if allowed {
		t.Fatalf("fourth attempt was allowed, want a refusal at the limit")
	}

	// Another player is unaffected: the budget is per user.
	if allowed, err := limiter.AllowRoomCreation("user-b", now); err != nil || !allowed {
		t.Fatalf("other user = (%v, %v), want allowed", allowed, err)
	}

	// Once the oldest hit leaves the window there is room again.
	later := now.Add(time.Minute + time.Second)
	if allowed, err := limiter.AllowRoomCreation("user-a", later); err != nil || !allowed {
		t.Fatalf("after the window = (%v, %v), want allowed", allowed, err)
	}
}

func TestCreateRoomIsRefusedPastTheLimit(t *testing.T) {
	service := NewService(Config{
		RoomCreationLimiter: NewMemoryRoomCreationLimiter(2, time.Minute),
		Rooms:               NewMemoryRoomStore(),
		QueueTickets:        NewMemoryQueueTicketStore(),
	})
	host := mustLogin(t, service, "Limited Host")

	deck := CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "limited_deck",
		DeckSnapshot: validDeck("limited_deck"),
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := service.CreateRoom(host.SessionToken, deck); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	_, err := service.CreateRoom(host.SessionToken, deck)
	if ErrorCode(err) != codeRateLimited {
		t.Fatalf("third create = %v, want %s", err, codeRateLimited)
	}
}

// TestLimiterFailureDoesNotBlockRoomCreation: a limiter that cannot answer must
// not become the reason a player cannot play.
func TestLimiterFailureDoesNotBlockRoomCreation(t *testing.T) {
	service := NewService(Config{
		RoomCreationLimiter: failingLimiter{},
		Rooms:               NewMemoryRoomStore(),
		QueueTickets:        NewMemoryQueueTicketStore(),
	})
	host := mustLogin(t, service, "Failover Host")

	if _, err := service.CreateRoom(host.SessionToken, CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "failover_deck",
		DeckSnapshot: validDeck("failover_deck"),
	}); err != nil {
		t.Fatalf("create room with a failing limiter: %v", err)
	}
}

// TestTheDefaultLimiterIsOn: a deployment that injects nothing still gets the
// in-process limiter, because the cost it guards against is real and the
// limiter's own failure mode is "allowed".
func TestTheDefaultLimiterIsOn(t *testing.T) {
	service := NewService(Config{})
	host := mustLogin(t, service, "Default Host")
	request := CreateRoomRequest{
		ModeID:       "mvp_boss_race",
		ActiveDeckID: "default_deck",
		DeckSnapshot: validDeck("default_deck"),
	}
	for attempt := 1; attempt <= DefaultRoomCreationLimit; attempt++ {
		if _, err := service.CreateRoom(host.SessionToken, request); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if _, err := service.CreateRoom(host.SessionToken, request); ErrorCode(err) != codeRateLimited {
		t.Fatalf("one past the default limit = %v, want %s", err, codeRateLimited)
	}
}
