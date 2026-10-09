package battlespawn

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
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
	case "echoargs":
		// Emits the exact argv it was started with so tests can assert on the
		// arguments the spawner builds (e.g. that `--max-ticks` is always set).
		if port <= 0 {
			port = 45679
		}
		fmt.Printf("READY port=%d match=%s\n", port, matchID)
		fmt.Printf("ARGS %s\n", strings.Join(args, " "))
		blockForever()
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

func TestConfigPortRangeFromEnv(t *testing.T) {
	t.Setenv(EnvPortMin, "46000")
	t.Setenv(EnvPortMax, "46099")
	spawner := NewSpawner(Config{})
	cfg := spawner.Config()
	if cfg.PortMin != 46000 || cfg.PortMax != 46099 {
		t.Fatalf("port range = [%d,%d], want [46000,46099]", cfg.PortMin, cfg.PortMax)
	}
	if !cfg.hasPortRange() {
		t.Fatalf("expected a valid port range")
	}
}

func TestConfigPortRangeInvalidFallsBackToEphemeral(t *testing.T) {
	cases := []struct {
		name string
		min  string
		max  string
	}{
		{"inverted", "47000", "46000"},
		{"out-of-range", "70000", "70001"},
		{"only-min", "46000", ""},
		{"only-max", "", "46000"},
		{"garbage", "abc", "def"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvPortMin, tc.min)
			t.Setenv(EnvPortMax, tc.max)
			cfg := NewSpawner(Config{}).Config()
			if cfg.hasPortRange() || cfg.PortMin != 0 || cfg.PortMax != 0 {
				t.Fatalf("invalid range must be disabled, got [%d,%d]", cfg.PortMin, cfg.PortMax)
			}
		})
	}
}

func TestSpawnerUsesConfiguredPortRange(t *testing.T) {
	spawner := NewSpawner(Config{
		BinaryPath:    testBinary(t),
		AdvertiseHost: "10.0.0.9",
		ReadyTimeout:  5 * time.Second,
		KillGrace:     2 * time.Second,
		PortMin:       46000,
		PortMax:       46099,
		ExtraEnv:      []string{"PHK_FAKE_BATTLE_SERVER=1", "PHK_FAKE_BATTLE_MODE=ready"},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	proc, err := spawner.Spawn(ctx, SpawnRequest{MatchID: "match_range", Port: 0})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer spawner.KillAll()
	if proc.Port < 46000 || proc.Port > 46099 {
		t.Fatalf("spawned port %d outside configured range [46000,46099]", proc.Port)
	}
	wantEndpoint := "10.0.0.9:" + strconv.Itoa(proc.Port)
	if proc.Endpoint != wantEndpoint {
		t.Fatalf("endpoint = %q, want %q", proc.Endpoint, wantEndpoint)
	}
}

func TestSpawnerEphemeralPortWhenNoRange(t *testing.T) {
	t.Setenv(EnvPortMin, "")
	t.Setenv(EnvPortMax, "")
	spawner := NewSpawner(Config{
		BinaryPath:   testBinary(t),
		ReadyTimeout: 5 * time.Second,
		KillGrace:    2 * time.Second,
		ExtraEnv:     []string{"PHK_FAKE_BATTLE_SERVER=1", "PHK_FAKE_BATTLE_MODE=ready"},
	})
	if spawner.choosePort() != 0 {
		t.Fatalf("choosePort must return 0 when no range is configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	proc, err := spawner.Spawn(ctx, SpawnRequest{MatchID: "match_ephemeral", Port: 0})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer spawner.KillAll()
	if proc.Port != 45678 {
		t.Fatalf("fake server should fall back to its default port, got %d", proc.Port)
	}
}

func TestUDPPortAvailable(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer conn.Close()
	port := conn.LocalAddr().(*net.UDPAddr).Port
	if udpPortAvailable(port) {
		t.Fatalf("port %d is bound but reported available", port)
	}
}

// --- leak guards -----------------------------------------------------------
//
// A battle server started without `--max-ticks` only exits when the match
// settles, so an abandoned match used to pin a process forever; the same held
// for a process whose map entry was overwritten by a re-spawn. Together those
// two leaks exhausted the host's memory. These tests pin both down.

// TestConfigDefaultsMaxTicksAndMatchTTL asserts the spawner can never be built
// without both guards, even from a zero-value Config.
func TestConfigDefaultsMaxTicksAndMatchTTL(t *testing.T) {
	cfg := NewSpawner(Config{}).Config()
	if cfg.MaxTicks != DefaultMaxTicks {
		t.Fatalf("MaxTicks default = %d, want %d", cfg.MaxTicks, DefaultMaxTicks)
	}
	if cfg.MatchTTL != DefaultMatchTTL {
		t.Fatalf("MatchTTL default = %s, want %s", cfg.MatchTTL, DefaultMatchTTL)
	}
}

// TestConfigMaxTicksFromEnv checks the operator override.
func TestConfigMaxTicksFromEnv(t *testing.T) {
	t.Setenv(EnvMaxTicks, "1234")
	if got := NewSpawner(Config{}).Config().MaxTicks; got != 1234 {
		t.Fatalf("MaxTicks from env = %d, want 1234", got)
	}
}

// TestSpawnAlwaysPassesMaxTicks is the core regression: with no explicit budget
// on the request, the spawner must still hand the battle server `--max-ticks`.
func TestSpawnAlwaysPassesMaxTicks(t *testing.T) {
	spawner, lines := newArgCapturingSpawner(t)
	defer spawner.KillAll()

	if _, err := spawner.Spawn(context.Background(), SpawnRequest{MatchID: "M-ticks"}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	argv := waitForArgv(t, lines)
	if !strings.Contains(argv, "--max-ticks") {
		t.Fatalf("spawned argv is missing --max-ticks: %q", argv)
	}
	if want := strconv.FormatUint(DefaultMaxTicks, 10); !strings.Contains(argv, "--max-ticks "+want) {
		t.Fatalf("spawned argv should carry --max-ticks %s: %q", want, argv)
	}
}

// TestSpawnHonoursExplicitMaxTicks checks a caller-supplied budget wins over the
// configured default.
func TestSpawnHonoursExplicitMaxTicks(t *testing.T) {
	spawner, lines := newArgCapturingSpawner(t)
	defer spawner.KillAll()

	if _, err := spawner.Spawn(context.Background(), SpawnRequest{MatchID: "M-ticks2", MaxTicks: 99}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	argv := waitForArgv(t, lines)
	if !strings.Contains(argv, "--max-ticks 99") {
		t.Fatalf("spawned argv should carry the explicit --max-ticks 99: %q", argv)
	}
}

// TestTrackKillsReplacedProcessForSameMatch pins the orphan-on-respawn fix: a
// second spawn for the same match must kill the first rather than silently
// dropping it from the map.
func TestTrackKillsReplacedProcessForSameMatch(t *testing.T) {
	spawner := NewSpawner(Config{
		BinaryPath: testBinary(t),
		ExtraEnv:   []string{"PHK_FAKE_BATTLE_SERVER=1", "PHK_FAKE_BATTLE_MODE=ready"},
	})
	defer spawner.KillAll()

	first, err := spawner.Spawn(context.Background(), SpawnRequest{MatchID: "M-dup"})
	if err != nil {
		t.Fatalf("first Spawn: %v", err)
	}
	second, err := spawner.Spawn(context.Background(), SpawnRequest{MatchID: "M-dup"})
	if err != nil {
		t.Fatalf("second Spawn: %v", err)
	}
	if tracked, ok := spawner.Get("M-dup"); !ok || tracked != second {
		t.Fatalf("Get should return the latest process for the match")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !first.Exited() {
		if time.Now().After(deadline) {
			t.Fatalf("replaced process for %s was never reaped", first.MatchID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newArgCapturingSpawner(t *testing.T) (*Spawner, *argLines) {
	t.Helper()
	lines := &argLines{}
	spawner := NewSpawner(Config{
		BinaryPath: testBinary(t),
		ExtraEnv:   []string{"PHK_FAKE_BATTLE_SERVER=1", "PHK_FAKE_BATTLE_MODE=echoargs"},
		OnLine:     func(_ string, line string) { lines.add(line) },
	})
	return spawner, lines
}

type argLines struct {
	mu    sync.Mutex
	lines []string
}

func (a *argLines) add(line string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lines = append(a.lines, line)
}

func (a *argLines) find(prefix string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, line := range a.lines {
		if strings.HasPrefix(line, prefix) {
			return line, true
		}
	}
	return "", false
}

func waitForArgv(t *testing.T, lines *argLines) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if line, ok := lines.find("ARGS "); ok {
			return strings.TrimPrefix(line, "ARGS ")
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the fake server to echo its argv")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
