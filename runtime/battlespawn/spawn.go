// Package battlespawn manages the lifecycle of per-match C++ battle server
// processes (phk_battle_server).
//
// The lobby spawns one process per match, waits for the process to announce its
// UDP port on stdout (a line of the form "READY port=<P> match=<M>"), hands the
// endpoint to the clients through MatchStartMessage and kills the process once
// the match result callback arrives (or the process times out).
//
// The package never blocks indefinitely: waiting for READY is bounded by a
// timeout and every kill path is bounded by a grace period followed by a hard
// kill, so no zombie process is left behind.
package battlespawn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// EnvBinary overrides the battle server binary path.
	EnvBinary = "GENSOULKYO_BATTLE_SERVER_BIN"
	// EnvAdvertiseHost is the host advertised to clients for the spawned UDP endpoint.
	EnvAdvertiseHost = "GENSOULKYO_BATTLE_ADVERTISE_HOST"
	// EnvPortMin and EnvPortMax bound the UDP port range a spawned battle server
	// may bind. When either is unset the OS picks an ephemeral port (--port 0).
	EnvPortMin = "GENSOULKYO_BATTLE_PORT_MIN"
	EnvPortMax = "GENSOULKYO_BATTLE_PORT_MAX"
	// DefaultBinaryPath is used when EnvBinary is unset.
	DefaultBinaryPath = "../PhK-BattleServer/build-linux/phk_battle_server"
	// DefaultAdvertiseHost is used when EnvAdvertiseHost is unset.
	DefaultAdvertiseHost = "127.0.0.1"
	// DefaultRuleset matches the C++ battle server default.
	DefaultRuleset = "mvp-boss-race-s0"
	// DefaultReadyTimeout bounds how long we wait for the READY line.
	DefaultReadyTimeout = 10 * time.Second
	// DefaultKillGrace bounds how long we wait for a graceful exit before SIGKILL.
	DefaultKillGrace = 3 * time.Second
	// DefaultPortPickAttempts bounds how many candidate ports we probe before
	// falling back to an ephemeral port.
	DefaultPortPickAttempts = 12
)

// SpawnRequest describes a per-match battle server process.
type SpawnRequest struct {
	MatchID       string
	Seed          uint64
	PlayerIDs     []string
	LobbyEndpoint string // host:port of the lobby HTTP server; empty disables the result POST
	Ruleset       string
	BossMaxHP     uint64
	MaxTicks      uint64
	Port          int // 0 = ephemeral
	// TTL, when > 0, hard-kills the process after the duration as a safety net
	// against orphaned processes whose result callback never arrives.
	TTL time.Duration
}

// Config configures a Spawner.
type Config struct {
	BinaryPath    string
	AdvertiseHost string
	Ruleset       string
	LobbyEndpoint string
	ReadyTimeout  time.Duration
	KillGrace     time.Duration
	// PortMin and PortMax, when both are in 1..65535 and PortMin <= PortMax,
	// restrict the UDP port a spawned battle server binds. When unset (the
	// default) the OS assigns an ephemeral port via --port 0.
	PortMin int
	PortMax int
	// PortPickAttempts bounds how many candidate ports are probed before falling
	// back to an ephemeral port. Zero uses DefaultPortPickAttempts.
	PortPickAttempts int
	// ExtraEnv is appended to the inherited environment for spawned processes.
	ExtraEnv []string
	// OnLine, when set, receives every stdout line emitted by the process.
	OnLine func(matchID string, line string)
}

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.BinaryPath) == "" {
		if env := strings.TrimSpace(os.Getenv(EnvBinary)); env != "" {
			c.BinaryPath = env
		} else {
			c.BinaryPath = DefaultBinaryPath
		}
	}
	if strings.TrimSpace(c.AdvertiseHost) == "" {
		if env := strings.TrimSpace(os.Getenv(EnvAdvertiseHost)); env != "" {
			c.AdvertiseHost = env
		} else {
			c.AdvertiseHost = DefaultAdvertiseHost
		}
	}
	if strings.TrimSpace(c.Ruleset) == "" {
		c.Ruleset = DefaultRuleset
	}
	if c.ReadyTimeout <= 0 {
		c.ReadyTimeout = DefaultReadyTimeout
	}
	if c.KillGrace <= 0 {
		c.KillGrace = DefaultKillGrace
	}
	if c.PortPickAttempts <= 0 {
		c.PortPickAttempts = DefaultPortPickAttempts
	}
	if c.PortMin <= 0 && c.PortMax <= 0 {
		c.PortMin = envPort(EnvPortMin)
		c.PortMax = envPort(EnvPortMax)
	}
	if !validPortRange(c.PortMin, c.PortMax) {
		c.PortMin, c.PortMax = 0, 0
	}
	return c
}

func envPort(key string) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 65535 {
		return 0
	}
	return value
}

func validPortRange(min int, max int) bool {
	return min >= 1 && max <= 65535 && min <= max
}

func (c Config) hasPortRange() bool {
	return validPortRange(c.PortMin, c.PortMax)
}

// Spawner tracks the battle server processes it started.
type Spawner struct {
	cfg Config

	mu    sync.Mutex
	procs map[string]*Process
}

// NewSpawner builds a Spawner, applying environment defaults.
func NewSpawner(cfg Config) *Spawner {
	return &Spawner{cfg: cfg.withDefaults(), procs: map[string]*Process{}}
}

// Config exposes the resolved configuration (useful for tests and logging).
func (s *Spawner) Config() Config { return s.cfg }

// choosePort returns a concrete UDP port from the configured range, or 0 to let
// the OS assign an ephemeral port (the default when no range is configured).
//
// A candidate is only accepted if it can be bound right now; this is a
// best-effort probe, so a small race window with the child process remains and
// callers must still tolerate a spawn failure.
func (s *Spawner) choosePort() int {
	cfg := s.cfg
	if !cfg.hasPortRange() {
		return 0
	}
	span := cfg.PortMax - cfg.PortMin + 1
	attempts := cfg.PortPickAttempts
	if attempts <= 0 {
		attempts = DefaultPortPickAttempts
	}
	if attempts > span {
		attempts = span
	}
	for i := 0; i < attempts; i++ {
		candidate := cfg.PortMin + rand.Intn(span)
		if udpPortAvailable(candidate) {
			return candidate
		}
	}
	return 0
}

// udpPortAvailable reports whether the given UDP port can currently be bound on
// all interfaces.
func udpPortAvailable(port int) bool {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Process is a running battle server process.
type Process struct {
	MatchID  string
	Port     int
	Endpoint string // advertised host:port clients should dial over UDP
	PID      int

	cmd      *exec.Cmd
	waitDone chan struct{}
	waitErr  error

	stopOnce  sync.Once
	stopped   chan struct{}
	killTTL   *time.Timer
	killGrace time.Duration
}

// Exited reports whether the process has been reaped.
func (p *Process) Exited() bool {
	select {
	case <-p.waitDone:
		return true
	default:
		return false
	}
}

// WaitErr returns the process exit error after it has been reaped.
func (p *Process) WaitErr() error {
	select {
	case <-p.waitDone:
		return p.waitErr
	default:
		return nil
	}
}

// Spawn starts a battle server process and waits until it announces READY.
//
// If the process does not print READY before ReadyTimeout (or ctx is cancelled)
// it is killed and an error is returned, so no process is leaked on failure.
func (s *Spawner) Spawn(ctx context.Context, req SpawnRequest) (*Process, error) {
	matchID := strings.TrimSpace(req.MatchID)
	if matchID == "" {
		return nil, errors.New("battlespawn: match_id is required")
	}
	cfg := s.cfg
	ruleset := strings.TrimSpace(req.Ruleset)
	if ruleset == "" {
		ruleset = cfg.Ruleset
	}
	port := req.Port
	if port == 0 {
		port = s.choosePort()
	}
	args := []string{
		"--port", strconv.Itoa(port),
		"--match-id", matchID,
		"--seed", strconv.FormatUint(req.Seed, 10),
		"--players", strings.Join(req.PlayerIDs, ","),
		"--ruleset", ruleset,
	}
	lobby := strings.TrimSpace(req.LobbyEndpoint)
	if lobby == "" {
		lobby = cfg.LobbyEndpoint
	}
	if lobby != "" {
		args = append(args, "--lobby", lobby)
	}
	if req.BossMaxHP > 0 {
		args = append(args, "--boss-hp", strconv.FormatUint(req.BossMaxHP, 10))
	}
	if req.MaxTicks > 0 {
		args = append(args, "--max-ticks", strconv.FormatUint(req.MaxTicks, 10))
	}

	cmd := exec.Command(cfg.BinaryPath, args...)
	if len(cfg.ExtraEnv) > 0 {
		cmd.Env = append(os.Environ(), cfg.ExtraEnv...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("battlespawn: stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("battlespawn: start %q: %w", cfg.BinaryPath, err)
	}

	p := &Process{
		MatchID:   matchID,
		PID:       cmd.Process.Pid,
		cmd:       cmd,
		waitDone:  make(chan struct{}),
		stopped:   make(chan struct{}),
		killGrace: cfg.KillGrace,
	}

	scanDone := make(chan struct{})
	readyCh := make(chan int, 1)
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if cfg.OnLine != nil {
				cfg.OnLine(matchID, line)
			}
			if port, _, ok := ParseReadyLine(line); ok {
				select {
				case readyCh <- port:
				default:
				}
			}
		}
	}()
	go func() {
		// Wait only after the reader observed EOF so we never race the pipe close.
		<-scanDone
		p.waitErr = cmd.Wait()
		close(p.waitDone)
	}()

	timer := time.NewTimer(cfg.ReadyTimeout)
	defer timer.Stop()
	select {
	case port := <-readyCh:
		p.Port = port
		p.Endpoint = net.JoinHostPort(cfg.AdvertiseHost, strconv.Itoa(port))
		if req.TTL > 0 {
			p.killTTL = time.AfterFunc(req.TTL, func() { _ = p.Kill() })
		}
		s.track(p)
		return p, nil
	case <-timer.C:
		_ = p.Kill()
		return nil, fmt.Errorf("battlespawn: timed out waiting for READY from match %s", matchID)
	case <-ctx.Done():
		_ = p.Kill()
		return nil, fmt.Errorf("battlespawn: %w", ctx.Err())
	}
}

func (s *Spawner) track(p *Process) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procs[p.MatchID] = p
}

// Get returns the tracked process for a match, if any.
func (s *Spawner) Get(matchID string) (*Process, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.procs[matchID]
	return p, ok
}

// Kill stops and reaps the tracked process for a match.
func (s *Spawner) Kill(matchID string) error {
	s.mu.Lock()
	p := s.procs[matchID]
	delete(s.procs, matchID)
	s.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.Kill()
}

// KillAll stops every tracked process.
func (s *Spawner) KillAll() {
	s.mu.Lock()
	procs := make([]*Process, 0, len(s.procs))
	for _, p := range s.procs {
		procs = append(procs, p)
	}
	s.procs = map[string]*Process{}
	s.mu.Unlock()
	for _, p := range procs {
		_ = p.Kill()
	}
}

// Kill gracefully terminates the process (SIGTERM) and, if it does not exit
// within the grace period, kills it hard (SIGKILL). It is idempotent and always
// reaps the process.
func (p *Process) Kill() error {
	p.stopOnce.Do(func() {
		if p.killTTL != nil {
			p.killTTL.Stop()
		}
		if p.cmd != nil && p.cmd.Process != nil {
			// Best effort graceful shutdown; unsupported on some platforms.
			if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				_ = p.cmd.Process.Kill()
			}
		}
		select {
		case <-p.waitDone:
		case <-time.After(p.killGrace):
			if p.cmd != nil && p.cmd.Process != nil {
				_ = p.cmd.Process.Kill()
			}
			<-p.waitDone
		}
		close(p.stopped)
	})
	<-p.stopped
	return nil
}

// ParseReadyLine parses a battle server startup line of the form
// "READY port=<P> match=<M>". It returns ok=false for any other line.
func ParseReadyLine(line string) (port int, matchID string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 || fields[0] != "READY" {
		return 0, "", false
	}
	for _, field := range fields[1:] {
		switch {
		case strings.HasPrefix(field, "port="):
			value, err := strconv.Atoi(strings.TrimPrefix(field, "port="))
			if err != nil || value <= 0 || value > 65535 {
				return 0, "", false
			}
			port = value
		case strings.HasPrefix(field, "match="):
			matchID = strings.TrimPrefix(field, "match=")
		}
	}
	if port == 0 {
		return 0, "", false
	}
	return port, matchID, true
}
