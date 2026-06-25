package server

import (
	"regexp"
	"testing"
)

func TestRoomCodeShape(t *testing.T) {
	code := roomCode()
	if len(code) != roomCodeLength {
		t.Fatalf("expected %d character room code, got %q", roomCodeLength, code)
	}
	matched, err := regexp.MatchString(`^[A-HJ-NP-Z2-9]+$`, code)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatalf("room code contains ambiguous characters: %q", code)
	}
}

func TestNormalizeRoomCode(t *testing.T) {
	if got := normalizeRoomCode(" ab12cd "); got != "AB12CD" {
		t.Fatalf("unexpected normalized code: %q", got)
	}
}

func TestNewUUIDShape(t *testing.T) {
	id := newUUID()
	matched, err := regexp.MatchString(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, id)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatalf("expected UUIDv4, got %q", id)
	}
}
