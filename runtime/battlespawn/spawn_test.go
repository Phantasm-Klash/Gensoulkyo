package battlespawn

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// The tests re-execute the test binary itself as a fake battle server so the
// lifecycle can be exercised on any platform without a real C++ build.
func TestMain(m *testing.M) {
	if os.Getenv("PHK_FAKE_BATTLE_SERVER") == "1" {
		runFakeBattleServer(os.Args[1:])
		return
	}
	os.Exit(m.Run())
}

func runFakeBattleServer(args []string) {
	port := 0
	matchID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			if i+1 < len(args) {
				port, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--match-id":
			if i+1 < len(args) {
				matchID = args[i+1]
				i++
			}
		}
	}
	switch os.Getenv("PHK_FAKE_BATTLE_MODE") {
	case "noready":
		blockForever()
	case "exit":
		if port <= 0 {
			port = 45001
		}
		fmt.Printf("READY port=%d match=%s\n", port, matchID)
		os.Exit(0)
	default:
		if port <= 0 {
			port = 45678
		}
		fmt.Printf("READY port=%d match=%s\n", port, matchID)
		fmt.Println("RESULT {\"match_id\":\"" + matchID + "\"}")
		blockForever()
	}
}

func blockForever() {
	for {
		time.Sleep(time.Hour)
	}
}

func testBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

func TestParseReadyLine(t *testing.T) {
	cases := []struct {
		line    string
		port    int
		matchID string
		ok      bool
	}{
		{"READY port=7901 match=match_1", 7901, "match_1", true},
		{"READY port=65535 match=", 65535, "", true},
		{"READY match=match_2 port=1024", 1024, "match_2", true},
		{"ready port=7901", 0, "", false},
		{"READY port=0 match=m", 0, "", false},
		{"READY port=99999 match=m", 0, "", false},
		{"garbage", 0, "", false},
		{"", 0, "", false},
	}
	for _, tc := range cases {
		port, matchID, ok := ParseReadyLine(tc.line)
		if ok != tc.ok || port != tc.port || matchID != tc.matchID {
			t.Fatalf("ParseReadyLine(%q) = (%d,%q,%v), want (%d,%q,%v)", tc.line, port, matchID, ok, tc.port, tc.matchID, tc.ok)
		}
	}
}

func TestSpawnerReadyThenKill(t *testing.T) {
	spawner := NewSpawner(Config{
		BinaryPath:    testBinary(t),
		AdvertiseHost: "127.0.0.1",
		ReadyTimeout:  5 * time.Second,
		KillGrace:     2 * time.Second,
		ExtraEnv:      []string{"PHK_FAKE_BATTLE_SERVER=1", "PHK_FAKE_BATTLE_MODE=ready"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	proc, err := spawner.Spawn(ctx, SpawnRequest{
		MatchID:   "match_ready",
		Seed:      42,
		PlayerIDs: []string{"p1", "p2"},
		Port:      41234,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if proc.Port != 41234 {
		t.Fatalf("port = %d, want 41234", proc.Port)
	}
	if proc.Endpoint != "127.0.0.1:41234" {
		t.Fatalf("endpoint = %q, want 127.0.0.1:41234", proc.Endpoint)
	}
	if _, ok := spawner.Get("match_ready"); !ok {
		t.Fatalf("process not tracked after spawn")
	}
	if err := spawner.Kill("match_ready"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if !proc.Exited() {
		t.Fatalf("process was not reaped after Kill")
	}
	if _, ok := spawner.Get("match_ready"); ok {
		t.Fatalf("process still tracked after Kill")
	}
}

func TestSpawnerTimeoutWithoutReady(t *testing.T) {
	spawner := NewSpawner(Config{
		BinaryPath:   testBinary(t),
		ReadyTimeout: 1 * time.Second,
		KillGrace:    2 * time.Second,
		ExtraEnv:     []string{"PHK_FAKE_BATTLE_SERVER=1", "PHK_FAKE_BATTLE_MODE=noready"},
	})
	start := time.Now()
	_, err := spawner.Spawn(context.Background(), SpawnRequest{MatchID: "match_timeout"})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("timeout path took too long: %s", elapsed)
	}
	if _, ok := spawner.Get("match_timeout"); ok {
		t.Fatalf("failed spawn must not be tracked")
	}
}

func TestSpawnerProcessExitAfterReady(t *testing.T) {
	spawner := NewSpawner(Config{
		BinaryPath:   testBinary(t),
		ReadyTimeout: 5 * time.Second,
		ExtraEnv:     []string{"PHK_FAKE_BATTLE_SERVER=1", "PHK_FAKE_BATTLE_MODE=exit"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := spawner.Spawn(ctx, SpawnRequest{MatchID: "match_exit", Port: 45001})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !proc.Exited() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !proc.Exited() {
		t.Fatalf("process should have exited on its own")
	}
	spawner.KillAll()
}
