package core

import (
	"testing"
)

// TestPlayableModesReportsTheInjectedSet: the client asks which modes will
// actually run, so the answer has to be the set this deployment was given -- not
// every declared mode, and not an empty list that reads as "nothing is playable".
func TestPlayableModesReportsTheInjectedSet(t *testing.T) {
	service := NewService(Config{PlayableModes: PlayableModeIDs()})
	got := service.PlayableModes()
	if len(got) != 2 || got[0] != "coop_boss" || got[1] != "mvp_boss_race" {
		t.Fatalf("playable modes = %v, want [coop_boss mvp_boss_race]", got)
	}

	resp, err := service.PlayableModesFor(mustLogin(t, service, "Modes Alice").SessionToken)
	if err != nil {
		t.Fatalf("playable modes for a session: %v", err)
	}
	if !resp.OK || len(resp.ModeIDs) != 2 || !resp.ServerAuthoritative {
		t.Fatalf("response = %+v, want the two modes and an authoritative flag", resp)
	}
}

// TestPlayableModesIsEmptyWithoutAnInjectedSet: no gate configured is not the same
// as nothing being playable. A caller that misreads this would hide every mode.
func TestPlayableModesIsEmptyWithoutAnInjectedSet(t *testing.T) {
	service := NewService(Config{})
	if got := service.PlayableModes(); len(got) != 0 {
		t.Fatalf("playable modes without an injected set = %v, want empty", got)
	}
}

// TestPlayableModesRequiresASession: it is not a public endpoint.
func TestPlayableModesRequiresASession(t *testing.T) {
	service := NewService(Config{PlayableModes: PlayableModeIDs()})
	if _, err := service.PlayableModesFor(""); ErrorCode(err) != codeUnauthorized {
		t.Fatalf("anonymous playable modes = %v, want %s", err, codeUnauthorized)
	}
}
