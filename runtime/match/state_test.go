package match

import (
	"errors"
	"testing"
)

func TestStateHashIsDeterministic(t *testing.T) {
	a := NewState("match-1", "rules", "seed", []string{"b", "a"})
	b := NewState("match-1", "rules", "seed", []string{"a", "b"})

	input := InputPacket{Tick: 1, Seq: 1, Dir: 8, Slow: true, Shoot: true, CardSlot: -1}
	a.Tick = 1
	b.Tick = 1
	a.ApplyInput("a", input)
	b.ApplyInput("a", input)

	if a.Hash() != b.Hash() {
		t.Fatalf("expected deterministic hash, got %s and %s", a.Hash(), b.Hash())
	}
}

func TestApplyInputKeepsPlayerInsideArena(t *testing.T) {
	state := NewState("match-1", "rules", "seed", []string{"player"})
	for i := 0; i < 1000; i++ {
		state.Tick = i + 1
		state.ApplyInput("player", InputPacket{Tick: state.Tick, Dir: 8, CardSlot: -1})
	}
	player := state.Players["player"]
	if player.X > ArenaMaxX {
		t.Fatalf("player exceeded arena max x: %d", player.X)
	}
}

func TestSnapshotIncludesScoreAndHash(t *testing.T) {
	state := NewState("match-1", "rules", "seed", []string{"player"})
	state.Tick = 1
	state.ApplyInput("player", InputPacket{Tick: 1, Dir: 0, Shoot: true, CardSlot: -1})
	snapshot := state.Snapshot(true)
	if snapshot.StateHash == "" {
		t.Fatal("expected state hash")
	}
	if snapshot.Score["player"] != 1 {
		t.Fatalf("expected score 1, got %d", snapshot.Score["player"])
	}
}

func TestInputGuardRejectsInvalidPackets(t *testing.T) {
	guard := NewInputGuard()
	valid := InputPacket{Tick: 3, Seq: 1, Dir: 8, CardSlot: -1}
	if err := guard.Validate("player", valid, 2); err != nil {
		t.Fatalf("expected valid input, got %v", err)
	}
	guard.Accept("player", valid)

	tests := []struct {
		name  string
		input InputPacket
		want  error
	}{
		{name: "past tick", input: InputPacket{Tick: 1, Seq: 2, Dir: 0, CardSlot: -1}, want: ErrInputPastTick},
		{name: "duplicate tick", input: InputPacket{Tick: 3, Seq: 2, Dir: 0, CardSlot: -1}, want: ErrInputDuplicateTick},
		{name: "duplicate seq", input: InputPacket{Tick: 4, Seq: 1, Dir: 0, CardSlot: -1}, want: ErrInputDuplicateSeq},
		{name: "bad dir", input: InputPacket{Tick: 4, Seq: 2, Dir: 16, CardSlot: -1}, want: ErrInputInvalidDir},
		{name: "bad card slot", input: InputPacket{Tick: 4, Seq: 2, Dir: 0, CardSlot: 6}, want: ErrInputInvalidCard},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := guard.Validate("player", tt.input, 2); !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}

func TestApplyInputConsumesBombOnce(t *testing.T) {
	state := NewState("match-1", "rules", "seed", []string{"player"})
	state.Tick = 1
	state.ApplyInput("player", InputPacket{Tick: 1, Seq: 1, Bomb: true, CardSlot: -1})
	player := state.Players["player"]
	if player.Bombs != 0 {
		t.Fatalf("expected bomb resource to be consumed, got %d", player.Bombs)
	}
	if len(state.Events) != 1 || state.Events[0].Type != "bomb" {
		t.Fatalf("expected bomb event, got %#v", state.Events)
	}

	state.Tick = 2
	state.ApplyInput("player", InputPacket{Tick: 2, Seq: 2, Bomb: true, CardSlot: -1})
	if len(state.Events) != 2 || state.Events[1].Type != "invalid_input" {
		t.Fatalf("expected invalid input event for second bomb, got %#v", state.Events)
	}
}

func TestRepeatableInputDropsOneShotActions(t *testing.T) {
	input := RepeatableInput(InputPacket{Tick: 1, Seq: 3, Dir: 8, Shoot: true, Bomb: true, CardSlot: 2}, 9)
	if input.Tick != 9 {
		t.Fatalf("expected tick rewrite, got %d", input.Tick)
	}
	if input.Bomb || input.CardSlot != -1 {
		t.Fatalf("expected one-shot actions to be dropped, got %#v", input)
	}
	if !input.Shoot || input.Dir != 8 {
		t.Fatalf("expected repeatable movement/shoot state to remain, got %#v", input)
	}
}
