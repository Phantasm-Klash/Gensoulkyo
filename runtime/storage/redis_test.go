package storage

import (
	"bufio"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- scripted fake Redis server ---------------------------------------------
//
// The client is a RESP implementation, so the tests assert both directions:
// what bytes the client puts on the wire, and how it parses canned replies.

type fakeRedis struct {
	t        *testing.T
	listener net.Listener

	mu       sync.Mutex
	commands [][]string
	replies  []string
	conns    int
	closed   bool
}

func newFakeRedis(t *testing.T) *fakeRedis {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &fakeRedis{t: t, listener: listener}
	t.Cleanup(func() { server.close() })
	go server.serve()
	return server
}

func (server *fakeRedis) addr() string { return server.listener.Addr().String() }

func (server *fakeRedis) close() {
	server.mu.Lock()
	if server.closed {
		server.mu.Unlock()
		return
	}
	server.closed = true
	server.mu.Unlock()
	server.listener.Close()
}

func (server *fakeRedis) serve() {
	for {
		conn, err := server.listener.Accept()
		if err != nil {
			return
		}
		server.mu.Lock()
		server.conns++
		server.mu.Unlock()
		go server.handle(conn)
	}
}

func (server *fakeRedis) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		args, err := readFakeCommand(reader)
		if err != nil {
			return
		}
		server.mu.Lock()
		server.commands = append(server.commands, args)
		reply := "+OK\r\n"
		if len(server.replies) > 0 {
			reply = server.replies[0]
			server.replies = server.replies[1:]
		}
		server.mu.Unlock()
		if _, err := conn.Write([]byte(reply)); err != nil {
			return
		}
	}
}

// reply queues raw RESP payloads answered in order, one per command.
func (server *fakeRedis) reply(payloads ...string) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.replies = append(server.replies, payloads...)
}

func (server *fakeRedis) recorded() [][]string {
	server.mu.Lock()
	defer server.mu.Unlock()
	out := make([][]string, len(server.commands))
	copy(out, server.commands)
	return out
}

func (server *fakeRedis) connectionCount() int {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.conns
}

func readFakeCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "*") {
		return nil, errors.New("expected an array header")
	}
	count, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		header = strings.TrimRight(header, "\r\n")
		if !strings.HasPrefix(header, "$") {
			return nil, errors.New("expected a bulk header")
		}
		length, err := strconv.Atoi(header[1:])
		if err != nil {
			return nil, err
		}
		buffer := make([]byte, length+2)
		total := 0
		for total < len(buffer) {
			read, err := reader.Read(buffer[total:])
			total += read
			if err != nil {
				return nil, err
			}
		}
		args = append(args, string(buffer[:length]))
	}
	return args, nil
}

func newTestClient(t *testing.T, server *fakeRedis, config RedisConfig) *RedisClient {
	t.Helper()
	config.Addr = server.addr()
	client, err := NewRedisClient(config)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

// --- handshake ---------------------------------------------------------------

func TestRedisClientAuthenticatesAndSelectsDatabaseOnConnect(t *testing.T) {
	server := newFakeRedis(t)
	server.reply("+OK\r\n", "+OK\r\n", "+PONG\r\n")
	client := newTestClient(t, server, RedisConfig{Password: "s3cret", DB: 1})

	if err := client.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	commands := server.recorded()
	if len(commands) != 3 {
		t.Fatalf("expected auth, select and ping, got %v", commands)
	}
	if strings.Join(commands[0], " ") != "AUTH s3cret" {
		t.Fatalf("auth command = %v", commands[0])
	}
	if strings.Join(commands[1], " ") != "SELECT 1" {
		t.Fatalf("select command = %v", commands[1])
	}
	if strings.Join(commands[2], " ") != "PING" {
		t.Fatalf("ping command = %v", commands[2])
	}
}

func TestRedisClientSkipsHandshakeWithoutPassword(t *testing.T) {
	server := newFakeRedis(t)
	server.reply("+PONG\r\n")
	client := newTestClient(t, server, RedisConfig{})
	if err := client.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	commands := server.recorded()
	if len(commands) != 1 || commands[0][0] != "PING" {
		t.Fatalf("expected a single ping, got %v", commands)
	}
}

// --- encoding ----------------------------------------------------------------

func TestRedisClientEncodesCommands(t *testing.T) {
	server := newFakeRedis(t)
	client := newTestClient(t, server, RedisConfig{})

	if err := client.SetWithTTL("room:ABCD", `{"status":"waiting"}`, 90*time.Second); err != nil {
		t.Fatalf("set: %v", err)
	}
	commands := server.recorded()
	want := []string{"SET", "room:ABCD", `{"status":"waiting"}`, "PX", "90000"}
	if len(commands) != 1 || len(commands[0]) != len(want) {
		t.Fatalf("command = %v", commands)
	}
	for i := range want {
		if commands[0][i] != want[i] {
			t.Fatalf("command[%d] = %q want %q (full %v)", i, commands[0][i], want[i], commands[0])
		}
	}
}

func TestRedisClientEncodesHashAndSortedSetCommands(t *testing.T) {
	server := newFakeRedis(t)
	client := newTestClient(t, server, RedisConfig{})
	if err := client.HSet("room:ABCD", "host", "u-1", "status", "waiting"); err != nil {
		t.Fatalf("hset: %v", err)
	}
	if err := client.ZAdd("queue:pvp", 12.5, "u-1"); err != nil {
		t.Fatalf("zadd: %v", err)
	}
	commands := server.recorded()
	if strings.Join(commands[0], " ") != "HSET room:ABCD host u-1 status waiting" {
		t.Fatalf("hset = %v", commands[0])
	}
	if strings.Join(commands[1], " ") != "ZADD queue:pvp 12.5 u-1" {
		t.Fatalf("zadd = %v", commands[1])
	}
}

// --- reply decoding ----------------------------------------------------------

func TestRedisClientGetReturnsValueAndNil(t *testing.T) {
	server := newFakeRedis(t)
	server.reply("$5\r\nhello\r\n", "$-1\r\n")
	client := newTestClient(t, server, RedisConfig{})

	value, err := client.Get("k")
	if err != nil || value != "hello" {
		t.Fatalf("get = %q, %v", value, err)
	}
	if _, err := client.Get("k"); !errors.Is(err, ErrNilReply) {
		t.Fatalf("expected ErrNilReply, got %v", err)
	}
}

func TestRedisClientDecodesIntegersArraysAndErrors(t *testing.T) {
	server := newFakeRedis(t)
	server.reply(
		":7\r\n",
		"*4\r\n$4\r\nhost\r\n$3\r\nu-1\r\n$6\r\nstatus\r\n$7\r\nwaiting\r\n",
		"-ERR unknown command\r\n",
	)
	client := newTestClient(t, server, RedisConfig{})

	count, err := client.Incr("rl:create_room:u-1")
	if err != nil || count != 7 {
		t.Fatalf("incr = %d, %v", count, err)
	}
	fields, err := client.HGetAll("room:ABCD")
	if err != nil {
		t.Fatalf("hgetall: %v", err)
	}
	if fields["host"] != "u-1" || fields["status"] != "waiting" || len(fields) != 2 {
		t.Fatalf("hgetall = %v", fields)
	}
	if _, err := client.Get("k"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("expected the redis error to surface, got %v", err)
	}
}

func TestRedisClientSetIfAbsentReportsOwnership(t *testing.T) {
	server := newFakeRedis(t)
	server.reply("+OK\r\n", "$-1\r\n")
	client := newTestClient(t, server, RedisConfig{})

	created, err := client.SetIfAbsent("idem:chest:abc", "locked", time.Minute)
	if err != nil || !created {
		t.Fatalf("first SetIfAbsent = %v, %v", created, err)
	}
	created, err = client.SetIfAbsent("idem:chest:abc", "locked", time.Minute)
	if err != nil || created {
		t.Fatalf("second SetIfAbsent = %v, %v", created, err)
	}
	commands := server.recorded()
	if len(commands[0]) != 6 || commands[0][3] != "NX" || commands[0][4] != "PX" || commands[0][5] != "60000" {
		t.Fatalf("set command = %v", commands[0])
	}
}

func TestRedisClientTTLPassesThroughRedisConventions(t *testing.T) {
	server := newFakeRedis(t)
	server.reply(":-1\r\n", ":-2\r\n")
	client := newTestClient(t, server, RedisConfig{})
	noExpiry, err := client.TTL("room:ABCD")
	if err != nil || noExpiry != -1 {
		t.Fatalf("ttl = %d, %v", noExpiry, err)
	}
	missing, err := client.TTL("room:ZZZZ")
	if err != nil || missing != -2 {
		t.Fatalf("ttl = %d, %v", missing, err)
	}
}

// --- rate limiter ------------------------------------------------------------

func TestRedisClientAllowRunsTheLuaLimiterAtomically(t *testing.T) {
	server := newFakeRedis(t)
	server.reply(":1\r\n", ":0\r\n")
	client := newTestClient(t, server, RedisConfig{})

	allowed, err := client.Allow("rl:create_room:u-1", 3, time.Minute)
	if err != nil || !allowed {
		t.Fatalf("first Allow = %v, %v", allowed, err)
	}
	allowed, err = client.Allow("rl:create_room:u-1", 3, time.Minute)
	if err != nil || allowed {
		t.Fatalf("second Allow = %v, %v", allowed, err)
	}

	commands := server.recorded()
	if len(commands) != 2 || commands[0][0] != "EVAL" {
		t.Fatalf("expected two EVAL commands, got %v", commands)
	}
	// EVAL <script> <numkeys> <key> <window ms> <limit>
	if commands[0][1] != redisRateLimitScript || commands[0][2] != "1" {
		t.Fatalf("eval script/numkeys mismatch: %q %q", commands[0][1], commands[0][2])
	}
	if commands[0][3] != "rl:create_room:u-1" || commands[0][4] != "60000" || commands[0][5] != "3" {
		t.Fatalf("eval args = %v", commands[0][3:])
	}
}

// --- pool / lifecycle --------------------------------------------------------

func TestRedisClientReusesPooledConnections(t *testing.T) {
	server := newFakeRedis(t)
	client := newTestClient(t, server, RedisConfig{PoolSize: 2})
	for i := 0; i < 5; i++ {
		if err := client.Set("k", "v"); err != nil {
			t.Fatalf("set %d: %v", i, err)
		}
	}
	if got := server.connectionCount(); got != 1 {
		t.Fatalf("expected one pooled connection, dialled %d", got)
	}
}

func TestRedisClientBoundsConcurrentConnections(t *testing.T) {
	server := newFakeRedis(t)
	client := newTestClient(t, server, RedisConfig{PoolSize: 2})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client.Set("k", "v")
		}()
	}
	wg.Wait()
	if got := server.connectionCount(); got < 1 || got > 2 {
		t.Fatalf("expected at most 2 connections, dialled %d", got)
	}
}

func TestRedisClientRejectsCommandsAfterClose(t *testing.T) {
	server := newFakeRedis(t)
	config := RedisConfig{Addr: server.addr()}
	client, err := NewRedisClient(config)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := client.Get("k"); !errors.Is(err, ErrRedisClosed) {
		t.Fatalf("expected ErrRedisClosed, got %v", err)
	}
}

func TestNewRedisClientRequiresAddress(t *testing.T) {
	if _, err := NewRedisClient(RedisConfig{}); err == nil {
		t.Fatalf("expected an address requirement")
	}
}

// --- configuration -----------------------------------------------------------

func TestParseEnvironmentDefaultsToProduction(t *testing.T) {
	cases := map[string]Environment{
		"":          EnvironmentProd,
		"prod":      EnvironmentProd,
		"PROD ":     EnvironmentProd,
		"production": EnvironmentProd,
		"typo":      EnvironmentProd,
		"test":      EnvironmentTest,
		" TESTING ": EnvironmentTest,
	}
	for value, want := range cases {
		if got := ParseEnvironment(value); got != want {
			t.Fatalf("ParseEnvironment(%q) = %q want %q", value, got, want)
		}
	}
	if DatabaseName(EnvironmentProd) != DatabaseNameProd || DatabaseName(EnvironmentTest) != DatabaseNameTest {
		t.Fatalf("database names mismatch")
	}
}

func TestRedisConfigForSeparatesProductionAndTestDatabases(t *testing.T) {
	t.Setenv("GENSOULKYO_REDIS_ADDR", "192.168.32.10:6379")
	t.Setenv("GENSOULKYO_REDIS_PASSWORD", "pw")

	prod := RedisConfigFor(EnvironmentProd)
	if prod.DB != RedisDatabaseProd || prod.Addr != "192.168.32.10:6379" || prod.Password != "pw" {
		t.Fatalf("prod redis config = %+v", prod)
	}
	test := RedisConfigFor(EnvironmentTest)
	if test.DB != RedisDatabaseTest {
		t.Fatalf("test redis config = %+v", test)
	}
	if prod.DB == test.DB {
		t.Fatalf("prod and test must not share a redis database")
	}
	if prod.PoolSize != defaultRedisPoolSize || prod.ReadTimeout <= 0 {
		t.Fatalf("defaults not applied: %+v", prod)
	}
}

func TestRedisConfigFromEnvUsesEnvironmentVariable(t *testing.T) {
	t.Setenv("GENSOULKYO_ENV", "test")
	if got := RedisConfigFromEnv().DB; got != RedisDatabaseTest {
		t.Fatalf("db = %d want %d", got, RedisDatabaseTest)
	}
	t.Setenv("GENSOULKYO_ENV", "prod")
	if got := RedisConfigFromEnv().DB; got != RedisDatabaseProd {
		t.Fatalf("db = %d want %d", got, RedisDatabaseProd)
	}
}

func TestPostgresConfigTargetsTheEnvironmentDatabase(t *testing.T) {
	t.Setenv("GENSOULKYO_DATABASE_HOST", "192.168.32.2")
	t.Setenv("GENSOULKYO_DATABASE_PORT", "5432")
	t.Setenv("GENSOULKYO_DATABASE_USER_PROD", "gensoulkyo_prod")
	t.Setenv("GENSOULKYO_DATABASE_PASSWORD_PROD", "prod-pw")
	t.Setenv("GENSOULKYO_DATABASE_USER_TEST", "gensoulkyo_test")
	t.Setenv("GENSOULKYO_DATABASE_PASSWORD_TEST", "test-pw")

	prod := PostgresConfig(EnvironmentProd)
	if !strings.Contains(prod.URL, "/"+DatabaseNameProd) || !strings.Contains(prod.URL, "gensoulkyo_prod:prod-pw") {
		t.Fatalf("prod url = %q", prod.URL)
	}
	test := PostgresConfig(EnvironmentTest)
	if !strings.Contains(test.URL, "/"+DatabaseNameTest) || !strings.Contains(test.URL, "gensoulkyo_test:test-pw") {
		t.Fatalf("test url = %q", test.URL)
	}
	if prod.URL == test.URL {
		t.Fatalf("prod and test DSNs must differ")
	}
	if prod.Driver != DefaultPostgresDriver {
		t.Fatalf("driver = %q", prod.Driver)
	}
}

func TestPostgresConfigHonoursExplicitURLOverride(t *testing.T) {
	t.Setenv("GENSOULKYO_DATABASE_URL_PROD", "postgres://someone:pw@10.0.0.5:5432/nakama_prod?sslmode=require")
	if got := PostgresConfig(EnvironmentProd).URL; got != "postgres://someone:pw@10.0.0.5:5432/nakama_prod?sslmode=require" {
		t.Fatalf("url = %q", got)
	}
}

func TestPostgresConfigFromEnvUsesEnvironmentVariable(t *testing.T) {
	t.Setenv("GENSOULKYO_ENV", "test")
	t.Setenv("GENSOULKYO_DATABASE_HOST", "127.0.0.1")
	if got := PostgresConfigFromEnv().URL; !strings.Contains(got, "/"+DatabaseNameTest) {
		t.Fatalf("url = %q", got)
	}
}
