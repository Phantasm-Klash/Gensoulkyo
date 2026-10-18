// Command battle-agent runs next to the C++ battle servers on a remote host and
// bridges them to the Gensoulkyo Nakama deployment.
//
// It is intentionally dependency-free (standard library only) so it can be
// cross-compiled on a CI box and copied to a small VPS without a Go toolchain.
//
// Responsibilities:
//
//  1. register this host as a battle server with Nakama (battle.servers.register)
//  2. heartbeat on a fixed interval (battle.servers.heartbeat)
//  3. poll the matches Nakama routed here (battle.agent.assignments)
//  4. spawn one phk_battle_server process per assignment, binding the allocated
//     endpoint port so clients can dial it directly over UDP
//  5. capture the process' RESULT line and submit it as a signed battle result
//     (battle.result.submit), then reap the process
//
// All Nakama calls are service-origin RPCs. The agent authenticates with the
// Nakama runtime HTTP key (HTTP basic auth) plus the shared service secret
// (X-Gensoulkyo-Service-Key header), matching the gate implemented in
// cmd/gensoulkyo_nakama.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	cfg := parseConfig()
	if err := cfg.validate(); err != nil {
		log.Fatalf("battle-agent: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	agent := newAgent(cfg)
	log.Printf("battle-agent starting: id=%s endpoint=%s nakama=%s binary=%s",
		cfg.BattleServerID, cfg.Endpoint(), cfg.NakamaBaseURL, cfg.BattleBinary)

	if err := agent.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("battle-agent: %v", err)
	}
	log.Printf("battle-agent stopped")
}

// ---------------------------------------------------------------------------
// configuration
// ---------------------------------------------------------------------------

// defaultMaxTicks bounds a single match so a playerless or stuck match
// self-terminates with TIMEOUT instead of running until the agent exits.
// 7200 ticks at 60 Hz is roughly two minutes.
const defaultMaxTicks = 7200

// maxSpawnAttempts caps how many times the agent will (re)spawn the same match.
// A match that never produces a RESULT (nobody connected, or the match died
// mid-flight) would otherwise be re-spawned on every poll until the core's
// allocation TTL abandons it. After this many attempts the agent stops trying
// and lets the TTL do the final cleanup.
const maxSpawnAttempts = 3

type config struct {
	NakamaBaseURL      string
	RuntimeHTTPKey     string
	ServiceCallbackKey string
	BattleServerID     string
	AdvertiseHost      string
	PortMin            int
	PortMax            int
	Region             string
	BuildID            string
	SupportedModes     string
	BattleBinary       string
	LobbyEndpoint      string
	RulesetOverride    string
	HeartbeatInterval  time.Duration
	PollInterval       time.Duration
	ReadyTimeout       time.Duration
	KillGrace          time.Duration
	DefaultBossHP      int
	DefaultMaxTicks    int
	TickRate           int
	TickScale          string
	HTTPTimeout        time.Duration
}

func parseConfig() config {
	cfg := config{}
	flag.StringVar(&cfg.NakamaBaseURL, "nakama", envOr("GENSOULKYO_NAKAMA_URL", "http://127.0.0.1:7350"), "Nakama HTTP base URL")
	flag.StringVar(&cfg.RuntimeHTTPKey, "http-key", os.Getenv("GENSOULKYO_NAKAMA_HTTP_KEY"), "Nakama runtime HTTP key (basic auth)")
	flag.StringVar(&cfg.ServiceCallbackKey, "service-key", os.Getenv("GENSOULKYO_SERVICE_CALLBACK_KEY"), "shared service callback secret")
	flag.StringVar(&cfg.BattleServerID, "id", envOr("GENSOULKYO_BATTLE_SERVER_ID", "battle-agent-1"), "battle server id registered with Nakama")
	flag.StringVar(&cfg.AdvertiseHost, "advertise-host", os.Getenv("GENSOULKYO_BATTLE_ADVERTISE_HOST"), "host clients use to reach this agent's battle servers")
	flag.IntVar(&cfg.PortMin, "port-min", envIntOr("GENSOULKYO_BATTLE_PORT_MIN", 7400), "lowest UDP port to bind")
	flag.IntVar(&cfg.PortMax, "port-max", envIntOr("GENSOULKYO_BATTLE_PORT_MAX", 7419), "highest UDP port to bind")
	flag.StringVar(&cfg.Region, "region", envOr("GENSOULKYO_BATTLE_REGION", "default"), "battle server region label")
	flag.StringVar(&cfg.BuildID, "build-id", envOr("GENSOULKYO_BATTLE_BUILD_ID", "dev"), "battle server build id")
	flag.StringVar(&cfg.SupportedModes, "modes", envOr("GENSOULKYO_BATTLE_MODES", "mvp_boss_race"), "comma separated supported mode ids")
	flag.StringVar(&cfg.BattleBinary, "binary", envOr("PHK_BATTLE_SERVER_BIN", "./phk_battle_server"), "path to the phk_battle_server binary")
	// `GENSOULKYO_BATTLE_LOBBY` is the agent's own name; `GENSOULKYO_LOBBY_ENDPOINT`
	// is the lobby service's name for the same value and is accepted too so a
	// deployment does not silently end up without a result/dem upload target.
	flag.StringVar(&cfg.LobbyEndpoint, "lobby", envOr("GENSOULKYO_BATTLE_LOBBY", os.Getenv("GENSOULKYO_LOBBY_ENDPOINT")), "host:port the battle process posts its result and .dem to")
	flag.StringVar(&cfg.RulesetOverride, "ruleset", os.Getenv("GENSOULKYO_BATTLE_RULESET"), "ruleset override (defaults to the assignment ruleset)")
	flag.DurationVar(&cfg.HeartbeatInterval, "heartbeat", 5*time.Second, "heartbeat interval")
	flag.DurationVar(&cfg.PollInterval, "poll", 3*time.Second, "assignment poll interval")
	flag.DurationVar(&cfg.ReadyTimeout, "ready-timeout", 10*time.Second, "time to wait for a spawned process READY line")
	flag.DurationVar(&cfg.KillGrace, "kill-grace", 3*time.Second, "grace period between SIGTERM and SIGKILL")
	flag.IntVar(&cfg.DefaultBossHP, "boss-hp", envIntOr("GENSOULKYO_BATTLE_BOSS_HP", 0), "boss max hp override (0 = binary default)")
	flag.IntVar(&cfg.DefaultMaxTicks, "max-ticks", envIntOr("GENSOULKYO_BATTLE_MAX_TICKS", defaultMaxTicks), "tick cap (0 = unlimited)")
	flag.IntVar(&cfg.TickRate, "tick-rate", envIntOr("GENSOULKYO_BATTLE_TICK_RATE", 0), "server tick rate in Hz (60, 64, 128; 0 = binary default)")
	flag.StringVar(&cfg.TickScale, "tick-scale", os.Getenv("GENSOULKYO_BATTLE_TICK_SCALE"), "test pacing: scales the wall-clock tick interval (1 = real time, 0 = unpaced)")
	flag.DurationVar(&cfg.HTTPTimeout, "http-timeout", 10*time.Second, "per-request HTTP timeout")
	flag.Parse()
	return cfg
}

func (c config) validate() error {
	if strings.TrimSpace(c.NakamaBaseURL) == "" {
		return errors.New("nakama base url is required")
	}
	if strings.TrimSpace(c.RuntimeHTTPKey) == "" {
		return errors.New("nakama runtime http key is required (-http-key or GENSOULKYO_NAKAMA_HTTP_KEY)")
	}
	if strings.TrimSpace(c.ServiceCallbackKey) == "" {
		return errors.New("service callback key is required (-service-key or GENSOULKYO_SERVICE_CALLBACK_KEY)")
	}
	if strings.TrimSpace(c.AdvertiseHost) == "" {
		return errors.New("advertise host is required (-advertise-host or GENSOULKYO_BATTLE_ADVERTISE_HOST)")
	}
	if c.PortMax < c.PortMin {
		return fmt.Errorf("port range invalid: min=%d max=%d", c.PortMin, c.PortMax)
	}
	if _, err := os.Stat(c.BattleBinary); err != nil {
		return fmt.Errorf("battle binary %q not accessible: %w", c.BattleBinary, err)
	}
	return nil
}

// Endpoint returns the advertised host:port for a given port.
func (c config) endpoint(port int) string {
	return net.JoinHostPort(c.AdvertiseHost, strconv.Itoa(port))
}

// BaseEndpoint is used for registration/heartbeat. The concrete per-match port
// is chosen at spawn time; registration advertises the low bound so Nakama has
// a stable handle while allocations carry the exact port.
func (c config) Endpoint() string {
	return c.endpoint(c.PortMin)
}

func (c config) modes() []string {
	parts := strings.Split(c.SupportedModes, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// agent
// ---------------------------------------------------------------------------

type agent struct {
	cfg    config
	client *http.Client

	// callbackSecret is the per-battle-server callback secret returned by
	// battle.servers.register (hex(HMAC-SHA256(master, battle_server_id))). It
	// is handed to each spawned battle server so the process can sign its
	// service callbacks (result-submit, card events, dem upload).
	callbackSecret string

	mu        sync.Mutex
	processes map[string]*battleProcess // match_id -> running process
	usedPorts map[int]string            // port -> match_id
	attempts  map[string]int            // match_id -> spawn attempt count
	exhausted map[string]struct{}       // match_id -> already logged the cap warning
	// pendingReports holds the per-match endpoint reports that have not been
	// acknowledged by the lobby yet. A report is retried on every poll until it
	// lands, because a match that keeps the registry port gets relayed into the
	// wrong battle process (concurrent matches collide on one UDP port).
	pendingReports map[string]string // match_id -> endpoint
}

type battleProcess struct {
	matchID  string
	port     int
	endpoint string
	cmd      *exec.Cmd
	done     chan struct{}
	result   *battleResultLine
	err      error
}

func newAgent(cfg config) *agent {
	return &agent{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.HTTPTimeout,
		},
		processes: map[string]*battleProcess{},
		usedPorts: map[int]string{},
		attempts:  map[string]int{},
		exhausted: map[string]struct{}{},

		pendingReports: map[string]string{},
	}
}

func (a *agent) run(ctx context.Context) error {
	a.reapOrphanProcesses()

	if err := a.register(ctx); err != nil {
		return fmt.Errorf("register battle server: %w", err)
	}
	defer a.offline(context.WithoutCancel(ctx))

	heartbeat := time.NewTicker(a.cfg.HeartbeatInterval)
	defer heartbeat.Stop()
	poll := time.NewTicker(a.cfg.PollInterval)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			a.reapAll()
			return ctx.Err()
		case <-heartbeat.C:
			if err := a.heartbeat(ctx); err != nil {
				log.Printf("heartbeat failed: %v", err)
			}
		case <-poll.C:
			if err := a.reconcile(ctx); err != nil {
				log.Printf("reconcile failed: %v", err)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Nakama RPC
// ---------------------------------------------------------------------------

// callRPC performs a service-origin Nakama RPC. It authenticates with the
// runtime HTTP key and marks the request with the shared service secret.
func (a *agent) callRPC(ctx context.Context, rpcID string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	// Nakama HTTP RPC bodies must be JSON values; a JSON object is accepted and
	// decoded by the runtime binding.
	url := strings.TrimRight(a.cfg.NakamaBaseURL, "/") + "/v2/rpc/" + rpcID + "?unwrap=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gensoulkyo-Service-Key", a.cfg.ServiceCallbackKey)
	req.SetBasicAuth(a.cfg.RuntimeHTTPKey, "")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// rpcEnvelope mirrors the runtime/nakamaapi.Response shape so the agent can
// detect business-level failures that arrive with HTTP 200.
type rpcEnvelope struct {
	OK        bool            `json:"ok"`
	Status    int             `json:"status"`
	ErrorCode string          `json:"error_code,omitempty"`
	Message   string          `json:"message,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func (a *agent) callRPCDecoded(ctx context.Context, rpcID string, body any, out any) error {
	data, err := a.callRPC(ctx, rpcID, body)
	if err != nil {
		return err
	}
	var env rpcEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("decode %s envelope: %w (raw=%s)", rpcID, err, truncate(string(data), 256))
	}
	if !env.OK {
		return fmt.Errorf("%s rejected: status=%d code=%s message=%s", rpcID, env.Status, env.ErrorCode, env.Message)
	}
	if out != nil && len(env.Payload) > 0 {
		if err := json.Unmarshal(env.Payload, out); err != nil {
			return fmt.Errorf("decode %s payload: %w", rpcID, err)
		}
	}
	return nil
}

func (a *agent) register(ctx context.Context) error {
	body := map[string]any{
		"battle_server_id": a.cfg.BattleServerID,
		"endpoint":         a.cfg.Endpoint(),
		"region":           a.cfg.Region,
		"build_id":         a.cfg.BuildID,
		"capacity":         a.cfg.PortMax - a.cfg.PortMin + 1,
		"active_matches":   0,
		"load":             0.0,
		"status":           "ready",
		"supported_modes":  a.cfg.modes(),
	}
	var out struct {
		CallbackSecret string `json:"callback_secret"`
	}
	if err := a.callRPCDecoded(ctx, "battle.servers.register", body, &out); err != nil {
		return err
	}
	if secret := strings.TrimSpace(out.CallbackSecret); secret != "" {
		a.mu.Lock()
		a.callbackSecret = secret
		a.mu.Unlock()
	}
	log.Printf("registered with Nakama as %s (%s)", a.cfg.BattleServerID, a.cfg.Endpoint())
	return nil
}

func (a *agent) heartbeat(ctx context.Context) error {
	active, load := a.load()
	body := map[string]any{
		"battle_server_id": a.cfg.BattleServerID,
		"endpoint":         a.cfg.Endpoint(),
		"region":           a.cfg.Region,
		"build_id":         a.cfg.BuildID,
		"capacity":         a.cfg.PortMax - a.cfg.PortMin + 1,
		"active_matches":   active,
		"load":             load,
		"status":           "online",
		"supported_modes":  a.cfg.modes(),
	}
	return a.callRPCDecoded(ctx, "battle.servers.heartbeat", body, nil)
}

func (a *agent) offline(ctx context.Context) {
	body := map[string]any{"battle_server_id": a.cfg.BattleServerID, "reason": "agent_shutdown"}
	if err := a.callRPCDecoded(ctx, "battle.servers.offline", body, nil); err != nil {
		log.Printf("offline notice failed: %v", err)
	}
}

type assignment struct {
	MatchID        string   `json:"match_id"`
	ModeID         string   `json:"mode_id"`
	Ruleset        string   `json:"ruleset"`
	Endpoint       string   `json:"endpoint"`
	ServerSeedHex  string   `json:"server_seed_hex"`
	ModeConfigHash string   `json:"mode_config_hash"`
	PlayerIDs      []string `json:"player_ids"`
	// BossID / Loadouts 是线程A「游戏内容」新增字段：boss 房间级选择 + 每玩家
	// 自机 loadout，逐条透传给 phk_battle_server 的 --boss-id / --loadout。
	BossID   string              `json:"boss_id,omitempty"`
	Loadouts []assignmentLoadout `json:"loadouts"`
}

// assignmentLoadout 与 core.BattleAgentLoadout 的 JSON 同名（player_id /
// character_id / shot_type / bomb_type），仅在本包内保留一份以免耦合 core 包。
type assignmentLoadout struct {
	PlayerID    string `json:"player_id"`
	CharacterID string `json:"character_id"`
	ShotType    string `json:"shot_type"`
	BombType    string `json:"bomb_type"`
}

type assignmentsResponse struct {
	OK          bool         `json:"ok"`
	Assignments []assignment `json:"assignments"`
}

// reconcile polls Nakama for the matches routed to this agent and makes the
// local process set converge on it: spawn missing, reap finished.
func (a *agent) reconcile(ctx context.Context) error {
	var resp assignmentsResponse
	body := map[string]any{"battle_server_id": a.cfg.BattleServerID}
	if err := a.callRPCDecoded(ctx, "battle.agent.assignments", body, &resp); err != nil {
		return err
	}

	want := make(map[string]assignment, len(resp.Assignments))
	for _, item := range resp.Assignments {
		want[item.MatchID] = item
	}

	// Drop spawn bookkeeping for matches the lobby no longer routes here
	// before looking at new work, so a recycled match id starts with a clean
	// attempt count instead of inheriting one from a dead match.
	a.forgetStaleAttempts(want)

	a.reapFinished()

	// Retry reports that a previous tick could not deliver before looking at new
	// work, so a match spawned while the lobby was unreachable is corrected as
	// early as possible.
	a.flushPendingReports(ctx)

	for matchID, item := range want {
		if a.isRunning(matchID) {
			continue
		}
		if !a.reserveAttempt(matchID) {
			continue
		}
		if err := a.spawn(ctx, item); err != nil {
			log.Printf("spawn match=%s failed: %v", matchID, err)
		}
	}
	return nil
}

// reserveAttempt records a spawn attempt for matchID. It returns false once the
// match has hit maxSpawnAttempts, logging the cap exactly once.
func (a *agent) reserveAttempt(matchID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.attempts[matchID] >= maxSpawnAttempts {
		if _, logged := a.exhausted[matchID]; !logged {
			a.exhausted[matchID] = struct{}{}
			log.Printf("match=%s exceeded %d spawn attempts; leaving it to the allocation TTL",
				matchID, maxSpawnAttempts)
		}
		return false
	}
	a.attempts[matchID]++
	return true
}

// forgetStaleAttempts drops the spawn-attempt bookkeeping (and the one-shot
// "exceeded" log marker) for every match id absent from want, i.e. no longer
// routed to this agent by the lobby. Those matches are dead as far as the
// lobby is concerned, so their counts have no value; without this cleanup the
// map only ever grew and a recycled match id — e.g. after a lobby restart that
// re-issues small counters — would inherit a stale count and be refused spawn
// forever. Matches still present in want keep their counts so the
// maxSpawnAttempts cap keeps protecting the allocation TTL.
func (a *agent) forgetStaleAttempts(want map[string]assignment) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for matchID := range a.attempts {
		if _, live := want[matchID]; live {
			continue
		}
		delete(a.attempts, matchID)
		delete(a.exhausted, matchID)
	}
}

// reportEndpoint tells the lobby which UDP endpoint this agent actually bound
// for a match.
//
// The lobby seeds an allocation's endpoint from the battle server registry
// (advertise-host:PortMin), but the concrete port is only known here: it is the
// first free port at or above PortMin, so a leftover process from an earlier
// match pushes the new one up. Without this report the lobby hands clients (and
// its WS relay) the registry port and concurrent matches all get relayed into
// one battle process.
//
// The call is idempotent — re-reporting the same endpoint is a no-op server
// side — so it is safe to retry until it is acknowledged.
func (a *agent) reportEndpoint(ctx context.Context, matchID string, endpoint string) error {
	body := map[string]any{
		"battle_server_id": a.cfg.BattleServerID,
		"match_id":         matchID,
		"endpoint":         endpoint,
	}
	return a.callRPCDecoded(ctx, "battle.agent.endpoint", body, nil)
}

// publishEndpoint reports the endpoint right away and queues it for retry if
// the lobby could not be reached. A failed report never fails the spawn: the
// match is already running, and the retry loop will correct the allocation.
func (a *agent) publishEndpoint(ctx context.Context, matchID string, endpoint string) {
	reportCtx, cancel := context.WithTimeout(ctx, a.cfg.HTTPTimeout)
	defer cancel()
	if err := a.reportEndpoint(reportCtx, matchID, endpoint); err != nil {
		log.Printf("match=%s endpoint report failed (will retry): %v", matchID, err)
		a.setPendingReport(matchID, endpoint)
		return
	}
	a.clearPendingReport(matchID)
	log.Printf("reported match=%s endpoint=%s", matchID, endpoint)
}

func (a *agent) setPendingReport(matchID string, endpoint string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pendingReports[matchID] = endpoint
}

func (a *agent) clearPendingReport(matchID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pendingReports, matchID)
}

// flushPendingReports retries every report the lobby has not acknowledged yet.
func (a *agent) flushPendingReports(ctx context.Context) {
	a.mu.Lock()
	pending := make(map[string]string, len(a.pendingReports))
	for matchID, endpoint := range a.pendingReports {
		pending[matchID] = endpoint
	}
	a.mu.Unlock()

	for matchID, endpoint := range pending {
		reportCtx, cancel := context.WithTimeout(ctx, a.cfg.HTTPTimeout)
		err := a.reportEndpoint(reportCtx, matchID, endpoint)
		cancel()
		if err != nil {
			log.Printf("match=%s endpoint report retry failed: %v", matchID, err)
			continue
		}
		a.clearPendingReport(matchID)
		log.Printf("reported match=%s endpoint=%s", matchID, endpoint)
	}
}

func (a *agent) load() (int, float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	active := len(a.processes)
	capacity := a.cfg.PortMax - a.cfg.PortMin + 1
	if capacity <= 0 {
		capacity = 1
	}
	return active, float64(active) / float64(capacity)
}

func (a *agent) isRunning(matchID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.processes[matchID]
	return ok
}

// ---------------------------------------------------------------------------
// process management
// ---------------------------------------------------------------------------

// spawnArgs is the battle binary's command line. Kept separate from spawn so a
// test can assert the settlement contract without launching a process.
func (a *agent) spawnArgs(item assignment, port int) []string {
	args := []string{
		"--port", strconv.Itoa(port),
		"--match-id", item.MatchID,
		"--seed", seedFromHex(item.ServerSeedHex),
		"--players", strings.Join(item.PlayerIDs, ","),
		// 结算权只留一条路：agent 的签名 RPC battle.result.submit。对局服
		// 不再 POST /internal/battle/result，否则两条路径竞争（先到者写
		// replay_ 前缀 hash，后到者 409），结算结果取决于时序。
		"--no-result-submit",
	}
	ruleset := a.cfg.RulesetOverride
	if ruleset == "" {
		ruleset = item.Ruleset
	}
	if ruleset != "" {
		args = append(args, "--ruleset", ruleset)
	}
	if a.cfg.LobbyEndpoint != "" {
		args = append(args, "--lobby", a.cfg.LobbyEndpoint)
	}
	if a.cfg.DefaultBossHP > 0 {
		args = append(args, "--boss-hp", strconv.Itoa(a.cfg.DefaultBossHP))
	}
	if a.cfg.DefaultMaxTicks > 0 {
		args = append(args, "--max-ticks", strconv.Itoa(a.cfg.DefaultMaxTicks))
	}
	if a.cfg.TickRate > 0 {
		args = append(args, "--tick-rate", strconv.Itoa(a.cfg.TickRate))
	}
	if scale := strings.TrimSpace(a.cfg.TickScale); scale != "" {
		args = append(args, "--tick-scale", scale)
	}
	// 房间 boss（仅房主可选）+ 每玩家自机 loadout。
	if item.BossID != "" {
		args = append(args, "--boss-id", item.BossID)
	}
	if encoded := formatLoadoutArgs(item.Loadouts); encoded != "" {
		args = append(args, "--loadout", encoded)
	}
	// 服务回调签名：对局服用自己的 battle_server_id + 每服密钥签
	// X-PhK-Battle-Signature（结果回传 / 卡牌事件 / .dem 上传）。密钥来自
	// 注册响应，只在这一条路径上流转。
	if a.cfg.BattleServerID != "" {
		args = append(args, "--battle-server-id", a.cfg.BattleServerID)
	}
	a.mu.Lock()
	callbackSecret := a.callbackSecret
	a.mu.Unlock()
	if callbackSecret != "" {
		args = append(args, "--callback-secret", callbackSecret)
	}
	return args
}

func (a *agent) spawn(ctx context.Context, item assignment) error {
	port, err := a.allocatePort(item)
	if err != nil {
		return err
	}
	endpoint := a.cfg.endpoint(port)

	args := a.spawnArgs(item, port)

	cmd := exec.Command(a.cfg.BattleBinary, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		a.releasePort(port)
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		a.releasePort(port)
		return err
	}
	if err := cmd.Start(); err != nil {
		a.releasePort(port)
		return fmt.Errorf("start battle binary: %w", err)
	}

	proc := &battleProcess{
		matchID:  item.MatchID,
		port:     port,
		endpoint: endpoint,
		cmd:      cmd,
		done:     make(chan struct{}),
	}

	a.mu.Lock()
	a.processes[item.MatchID] = proc
	a.usedPorts[port] = item.MatchID
	a.mu.Unlock()

	log.Printf("spawned match=%s pid=%d port=%d endpoint=%s", item.MatchID, cmd.Process.Pid, port, endpoint)

	// The lobby still believes this match is on the registry port. Tell it the
	// real one before the relay or the client can dial it.
	a.publishEndpoint(ctx, item.MatchID, endpoint)

	go a.consumeOutput(proc, stdout, stderr)
	go a.waitProcess(proc, item)
	return nil
}

// consumeOutput parses the battle binary's stdout contract:
//
//	READY port=<p> match=<m>
//	RESULT <json>
//	SUBMIT ok=<0|1> status=<code>
//	TIMEOUT ticks=<n>
//	SHUTDOWN ticks=<n>
func (a *agent) consumeOutput(proc *battleProcess, stdout io.Reader, stderr io.Reader) {
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			log.Printf("match=%s stderr: %s", proc.matchID, scanner.Text())
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	ready := false
	readyDeadline := time.Now().Add(a.cfg.ReadyTimeout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "READY "):
			ready = true
			log.Printf("match=%s ready: %s", proc.matchID, line)
		case strings.HasPrefix(line, "RESULT "):
			payload := strings.TrimSpace(strings.TrimPrefix(line, "RESULT "))
			proc.result = &battleResultLine{json: payload}
			log.Printf("match=%s result: %s", proc.matchID, truncate(payload, 512))
		case strings.HasPrefix(line, "SUBMIT "), strings.HasPrefix(line, "TIMEOUT "), strings.HasPrefix(line, "SHUTDOWN "):
			log.Printf("match=%s %s", proc.matchID, line)
		default:
			log.Printf("match=%s out: %s", proc.matchID, truncate(line, 256))
		}
		if !ready && time.Now().After(readyDeadline) {
			log.Printf("match=%s did not report READY within %s", proc.matchID, a.cfg.ReadyTimeout)
		}
	}
}

func (a *agent) waitProcess(proc *battleProcess, item assignment) {
	err := proc.cmd.Wait()
	proc.err = err
	close(proc.done)

	if proc.result != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if submitErr := a.submitResult(ctx, item, *proc.result); submitErr != nil {
			log.Printf("match=%s result submit failed: %v", proc.matchID, submitErr)
		} else {
			log.Printf("match=%s result submitted", proc.matchID)
		}
		cancel()
	} else {
		log.Printf("match=%s exited without a result line (err=%v)", proc.matchID, err)
	}

	a.mu.Lock()
	delete(a.processes, proc.matchID)
	delete(a.usedPorts, proc.port)
	delete(a.pendingReports, proc.matchID)
	a.mu.Unlock()
}

// reapOrphanProcesses kills battle server processes that were spawned by a
// previous agent run and are still alive. Without this, restarting the agent
// leaves matches running on ports it no longer tracks: they never receive a
// heartbeat, never submit a result, and never release their UDP port.
//
// It is scoped to the configured binary path so it cannot touch unrelated
// processes on the host.
func (a *agent) reapOrphanProcesses() {
	matches, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return
	}
	self := os.Getpid()
	killed := 0
	for _, dir := range matches {
		pidStr := filepath.Base(dir)
		pid, convErr := strconv.Atoi(pidStr)
		if convErr != nil || pid == self {
			continue
		}
		exe, linkErr := os.Readlink(filepath.Join(dir, "exe"))
		if linkErr != nil || exe != a.cfg.BattleBinary {
			continue
		}
		if killErr := syscall.Kill(pid, syscall.SIGKILL); killErr == nil {
			killed++
			log.Printf("reaped orphan battle server pid=%d", pid)
		}
	}
	if killed > 0 {
		log.Printf("reaped %d orphan battle server process(es) from a previous run", killed)
	}
}

func (a *agent) reapFinished() {
	a.mu.Lock()
	finished := make([]*battleProcess, 0)
	for _, proc := range a.processes {
		select {
		case <-proc.done:
			finished = append(finished, proc)
		default:
		}
	}
	a.mu.Unlock()
	for _, proc := range finished {
		a.mu.Lock()
		delete(a.processes, proc.matchID)
		delete(a.usedPorts, proc.port)
		a.mu.Unlock()
	}
}

func (a *agent) reapAll() {
	a.mu.Lock()
	procs := make([]*battleProcess, 0, len(a.processes))
	for _, proc := range a.processes {
		procs = append(procs, proc)
	}
	a.mu.Unlock()
	for _, proc := range procs {
		a.terminate(proc)
	}
	// give processes a moment to flush their RESULT line
	for _, proc := range procs {
		select {
		case <-proc.done:
		case <-time.After(a.cfg.KillGrace + time.Second):
		}
	}
}

func (a *agent) terminate(proc *battleProcess) {
	if proc.cmd.Process == nil {
		return
	}
	_ = proc.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-proc.done:
		return
	case <-time.After(a.cfg.KillGrace):
	}
	_ = proc.cmd.Process.Kill()
}

func (a *agent) allocatePort(item assignment) (int, error) {
	// If the assignment endpoint already pins a port, honour it.
	if _, portStr, err := net.SplitHostPort(item.Endpoint); err == nil {
		if port, convErr := strconv.Atoi(portStr); convErr == nil && port > 0 {
			if a.portFree(port) {
				return port, nil
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for port := a.cfg.PortMin; port <= a.cfg.PortMax; port++ {
		if _, used := a.usedPorts[port]; used {
			continue
		}
		if a.portFreeLocked(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port in range %d-%d", a.cfg.PortMin, a.cfg.PortMax)
}

func (a *agent) portFree(port int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.portFreeLocked(port)
}

func (a *agent) portFreeLocked(port int) bool {
	if _, used := a.usedPorts[port]; used {
		return false
	}
	conn, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (a *agent) releasePort(port int) {
	a.mu.Lock()
	delete(a.usedPorts, port)
	a.mu.Unlock()
}

// ---------------------------------------------------------------------------
// result submission
// ---------------------------------------------------------------------------

type battleResultLine struct {
	json string
}

// rawResult mirrors the battle binary's RESULT payload.
type rawResult struct {
	MatchID        string `json:"match_id"`
	ModeID         string `json:"mode_id"`
	RulesetVersion string `json:"ruleset_version"`
	MatchSeed      int64  `json:"match_seed"`
	WinnerPlayerID string `json:"winner_player_id"`
	WinnerTick     int64  `json:"winner_tick"`
	StateHash      string `json:"state_hash"`
	Players        []struct {
		PlayerID      string `json:"player_id"`
		DamageDealt   int64  `json:"damage_dealt"`
		BossCurrentHP int64  `json:"boss_current_hp"`
	} `json:"players"`
}

// submitResult converts the binary's unsigned RESULT payload into the signed
// battle.result.submit contract and reports it to Nakama.
func (a *agent) submitResult(ctx context.Context, item assignment, line battleResultLine) error {
	var raw rawResult
	if err := json.Unmarshal([]byte(line.json), &raw); err != nil {
		return fmt.Errorf("decode RESULT json: %w", err)
	}
	if raw.MatchID == "" {
		raw.MatchID = item.MatchID
	}

	body := signedResultPayload(item, raw, a.cfg.BattleServerID, time.Now().UTC().UnixMilli())
	return a.callRPCDecoded(ctx, "battle.result.submit", body, nil)
}

// signedResultPayload is the exact JSON envelope the lobby decodes into
// core.BattleResultSubmitRequest. Kept separate so a test can feed the real
// payload through the lobby's validation without an HTTP round trip.
func signedResultPayload(item assignment, raw rawResult, keyID string, settledAtMS int64) map[string]any {
	playerIDs := make([]string, 0, len(raw.Players))
	for _, p := range raw.Players {
		playerIDs = append(playerIDs, p.PlayerID)
	}
	if len(playerIDs) == 0 {
		playerIDs = append(playerIDs, item.PlayerIDs...)
	}

	modalJSON, _ := json.Marshal(map[string]any{
		"winner_player_id": raw.WinnerPlayerID,
		"winner_tick":      raw.WinnerTick,
		"match_seed":       raw.MatchSeed,
		"state_hash":       raw.StateHash,
		"players":          raw.Players,
	})

	result := map[string]any{
		"version":          versionStamp(),
		"match_id":         raw.MatchID,
		"mode_id":          firstNonEmpty(raw.ModeID, item.ModeID),
		"result_hash":      resultHashOf(raw),
		"replay_id":        "replay-" + raw.MatchID,
		"player_ids":       playerIDs,
		"mode_result_json": string(modalJSON),
		"settled_at_ms":    settledAtMS,
	}

	return signedResultBody(result, keyID, devSignature(result, keyID))
}

// signedResultBody is the envelope; signature is computed by the caller so the
// signed bytes stay exactly the result map that is sent on the wire.
func signedResultBody(result map[string]any, keyID string, signature string) map[string]any {
	return map[string]any{
		"signed_result": map[string]any{
			"ok":                   true,
			"result":               result,
			"signature_alg":        "ED25519",
			"key_id":               keyID,
			"signature_hex":        signature,
			"server_authoritative": true,
		},
	}
}

// resultHashOf mirrors the lobby's `looksLikeSha256Ref` contract: the digest
// has to carry the `sha256:` prefix, otherwise `battle.result.submit` rejects
// the whole result with "battle result hash is invalid".
func resultHashOf(raw rawResult) string {
	canonical := fmt.Sprintf("%s|%s|%s|%d|%s", raw.MatchID, raw.ModeID, raw.WinnerPlayerID, raw.WinnerTick, raw.StateHash)
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// devSignature mirrors the scaffold signature shape required by core
// validation: exactly 128 hex characters (64 bytes), derived deterministically
// from the result and the battle server key id. `len(signature_hex) != 128` is
// rejected with "battle result signature shape is invalid" before settlement,
// so the digest must be a 64-byte one. It is intentionally not a real MAC;
// production builds must replace it with a hardware-backed ED25519 signature.
func devSignature(result map[string]any, keyID string) string {
	canonical, _ := json.Marshal(result)
	sum := sha512.Sum512([]byte(string(canonical) + ":" + keyID + ":result-signature"))
	return hex.EncodeToString(sum[:])
}

func versionStamp() map[string]any {
	return map[string]any{
		"protocol_version":     1,
		"business_api_version": "0.1.0-draft",
		"battle_api_version":   "0.1.0-draft",
		"ruleset_version":      "ruleset-local-s0",
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func seedFromHex(hexSeed string) string {
	hexSeed = strings.TrimSpace(hexSeed)
	if hexSeed == "" {
		return "0"
	}
	value, err := strconv.ParseInt(strings.TrimLeft(hexSeed, "0"), 16, 64)
	if err != nil || strings.Trim(hexSeed, "0") == "" {
		return "0"
	}
	return strconv.FormatInt(value, 10)
}

// formatLoadoutArgs 把每玩家 loadout 编成
// `<player_id>:<character_id>:<shot_type>:<bomb_type>` 并以逗号连接。
// 空字段保留（例如未选自机时 shot/bomb 为空串），让 C++ 侧按「缺省复现现状」
// 处理；玩家 id 为 `p-<hash>`，不含冒号/逗号，转义无歧义。空列表返回空串，
// 调用方据此决定是否追加 --loadout。
func formatLoadoutArgs(loadouts []assignmentLoadout) string {
	encoded := make([]string, 0, len(loadouts))
	for _, loadout := range loadouts {
		if strings.TrimSpace(loadout.PlayerID) == "" {
			continue
		}
		encoded = append(encoded, strings.Join([]string{
			loadout.PlayerID,
			loadout.CharacterID,
			loadout.ShotType,
			loadout.BombType,
		}, ":"))
	}
	return strings.Join(encoded, ",")
}

func envOr(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}
