package storage

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// This package deliberately implements the Redis client on the standard library
// (RESP over TCP) instead of pulling in github.com/redis/go-redis: the plugin
// builder image is offline-friendly and the module already pins a small
// dependency set. Only the commands the lobby actually needs are implemented.

var (
	// ErrNilReply is returned when Redis answers with a nil bulk/array reply,
	// i.e. "the key is not there". It is not a transport failure.
	ErrNilReply = errors.New("storage: redis nil reply")
	// ErrRedisPoolExhausted is returned when every pooled connection is busy.
	ErrRedisPoolExhausted = errors.New("storage: redis pool exhausted")
	// ErrRedisClosed is returned after Close.
	ErrRedisClosed = errors.New("storage: redis client closed")
)

const (
	// RedisDatabaseProd is db0: session/在线状态, room live state, matchmaking
	// queue, rate limits, idempotency keys for production traffic.
	RedisDatabaseProd = 0
	// RedisDatabaseTest is db1: the same key layout for the test environment.
	RedisDatabaseTest = 1
)

const (
	defaultRedisAddr         = "127.0.0.1:6379"
	defaultRedisPoolSize     = 8
	defaultRedisDialTimeout  = 3 * time.Second
	defaultRedisReadTimeout  = 3 * time.Second
	defaultRedisWriteTimeout = 3 * time.Second
	defaultRedisWaitTimeout  = 3 * time.Second
)

// redisRateLimitScript is the fixed-window limiter used by Allow. It is atomic
// on the Redis side, so concurrent lobby goroutines cannot slip past the limit
// by racing an INCR/EXPIRE pair from the client.
const redisRateLimitScript = `
local current = redis.call('INCR', KEYS[1])
if current == 1 then
    redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
if current > tonumber(ARGV[2]) then
    return 0
end
return 1`

// RedisConfig describes how to reach one Redis instance. DB is the logical
// database index (0 = prod, 1 = test); it is a namespace, not a security
// boundary — the password is what actually guards the instance.
type RedisConfig struct {
	Addr         string
	Password     string
	DB           int
	PoolSize     int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// WaitTimeout bounds how long Do waits for a free pooled connection.
	WaitTimeout time.Duration
}

// WithDefaults fills in the zero-valued fields.
func (config RedisConfig) WithDefaults() RedisConfig {
	if strings.TrimSpace(config.Addr) == "" {
		config.Addr = defaultRedisAddr
	}
	if config.PoolSize <= 0 {
		config.PoolSize = defaultRedisPoolSize
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = defaultRedisDialTimeout
	}
	if config.ReadTimeout <= 0 {
		config.ReadTimeout = defaultRedisReadTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = defaultRedisWriteTimeout
	}
	if config.WaitTimeout <= 0 {
		config.WaitTimeout = defaultRedisWaitTimeout
	}
	return config
}

// Enabled reports whether an address is configured.
func (config RedisConfig) Enabled() bool {
	return strings.TrimSpace(config.Addr) != ""
}

// RedisConfigFor builds the config for an environment:
// GENSOULKYO_REDIS_ADDR, GENSOULKYO_REDIS_PASSWORD and
// GENSOULKYO_REDIS_DB_PROD / GENSOULKYO_REDIS_DB_TEST.
func RedisConfigFor(env Environment) RedisConfig {
	env = ParseEnvironment(string(env))
	db := RedisDatabaseProd
	if env == EnvironmentTest {
		db = RedisDatabaseTest
	}
	if raw := strings.TrimSpace(os.Getenv("GENSOULKYO_REDIS_DB_" + strings.ToUpper(string(env)))); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			db = parsed
		}
	}
	return RedisConfig{
		Addr:     envOr("GENSOULKYO_REDIS_ADDR", defaultRedisAddr),
		Password: os.Getenv("GENSOULKYO_REDIS_PASSWORD"),
		DB:       db,
	}.WithDefaults()
}

// RedisConfigFromEnv is RedisConfigFor(EnvironmentFromEnv()).
func RedisConfigFromEnv() RedisConfig {
	return RedisConfigFor(EnvironmentFromEnv())
}

// RedisClient is a small pooled RESP client. It is safe for concurrent use.
type RedisClient struct {
	config RedisConfig
	// slots bounds how many connections may exist; free holds idle ones.
	slots chan struct{}
	free  chan *redisConn

	mu     sync.Mutex
	closed bool
}

// NewRedisClient validates the config and prepares the pool. Connections are
// dialled lazily, so constructing a client never blocks on the network.
func NewRedisClient(config RedisConfig) (*RedisClient, error) {
	if !config.Enabled() {
		return nil, errors.New("redis address is required")
	}
	config = config.WithDefaults()
	return &RedisClient{
		config: config,
		slots:  make(chan struct{}, config.PoolSize),
		free:   make(chan *redisConn, config.PoolSize),
	}, nil
}

// Config returns the effective configuration.
func (client *RedisClient) Config() RedisConfig {
	if client == nil {
		return RedisConfig{}
	}
	return client.config
}

// Close closes every idle pooled connection and rejects further commands.
func (client *RedisClient) Close() error {
	if client == nil {
		return nil
	}
	client.mu.Lock()
	if client.closed {
		client.mu.Unlock()
		return nil
	}
	client.closed = true
	client.mu.Unlock()
	for {
		select {
		case conn := <-client.free:
			conn.close()
			continue
		default:
		}
		break
	}
	return nil
}

// Do sends one command and returns the raw reply.
func (client *RedisClient) Do(args ...string) (RedisReply, error) {
	if client == nil {
		return RedisReply{}, errors.New("storage: redis client is not configured")
	}
	conn, err := client.acquire()
	if err != nil {
		return RedisReply{}, err
	}
	defer client.release(conn)
	return conn.do(args...)
}

func (client *RedisClient) acquire() (*redisConn, error) {
	client.mu.Lock()
	closed := client.closed
	client.mu.Unlock()
	if closed {
		return nil, ErrRedisClosed
	}
	timer := time.NewTimer(client.config.WaitTimeout)
	defer timer.Stop()
	select {
	case client.slots <- struct{}{}:
	case <-timer.C:
		return nil, ErrRedisPoolExhausted
	}
	select {
	case conn := <-client.free:
		return conn, nil
	default:
	}
	conn, err := dialRedisConn(client.config)
	if err != nil {
		<-client.slots
		return nil, err
	}
	return conn, nil
}

func (client *RedisClient) release(conn *redisConn) {
	if conn == nil {
		return
	}
	if conn.err != nil {
		conn.close()
		<-client.slots
		return
	}
	select {
	case client.free <- conn:
	default:
		conn.close()
	}
	<-client.slots
}

type redisConn struct {
	netConn      net.Conn
	reader       *bufio.Reader
	readTimeout  time.Duration
	writeTimeout time.Duration
	err          error
}

func dialRedisConn(config RedisConfig) (*redisConn, error) {
	netConn, err := net.DialTimeout("tcp", config.Addr, config.DialTimeout)
	if err != nil {
		return nil, err
	}
	conn := &redisConn{
		netConn:      netConn,
		reader:       bufio.NewReader(netConn),
		readTimeout:  config.ReadTimeout,
		writeTimeout: config.WriteTimeout,
	}
	if config.Password != "" {
		if _, err := conn.do("AUTH", config.Password); err != nil {
			conn.close()
			return nil, err
		}
	}
	if config.DB != 0 {
		if _, err := conn.do("SELECT", strconv.Itoa(config.DB)); err != nil {
			conn.close()
			return nil, err
		}
	}
	return conn, nil
}

func (conn *redisConn) close() {
	if conn.netConn != nil {
		conn.netConn.Close()
		conn.netConn = nil
	}
}

func (conn *redisConn) do(args ...string) (RedisReply, error) {
	if conn.err != nil {
		return RedisReply{}, conn.err
	}
	if err := conn.netConn.SetWriteDeadline(time.Now().Add(conn.writeTimeout)); err != nil {
		return RedisReply{}, err
	}
	if _, err := conn.netConn.Write(encodeRedisCommand(args)); err != nil {
		conn.err = err
		return RedisReply{}, err
	}
	if err := conn.netConn.SetReadDeadline(time.Now().Add(conn.readTimeout)); err != nil {
		return RedisReply{}, err
	}
	reply, err := readRedisReply(conn.reader)
	if err != nil {
		conn.err = err
		return RedisReply{}, err
	}
	if reply.kind == '-' {
		return reply, errors.New("storage: redis error: " + reply.text)
	}
	return reply, nil
}

func encodeRedisCommand(args []string) []byte {
	var builder strings.Builder
	builder.WriteByte('*')
	builder.WriteString(strconv.Itoa(len(args)))
	builder.WriteString("\r\n")
	for _, arg := range args {
		builder.WriteByte('$')
		builder.WriteString(strconv.Itoa(len(arg)))
		builder.WriteString("\r\n")
		builder.WriteString(arg)
		builder.WriteString("\r\n")
	}
	return []byte(builder.String())
}

// RedisReply is one RESP reply.
type RedisReply struct {
	kind  byte
	text  string
	num   int64
	items []RedisReply
	isNil bool
}

// IsNil reports whether the reply is a nil bulk string or nil array.
func (reply RedisReply) IsNil() bool { return reply.isNil }

// Text returns the reply as a string: a status/bulk string verbatim, an integer
// rendered as digits. A nil reply yields ErrNilReply.
func (reply RedisReply) Text() (string, error) {
	if reply.isNil {
		return "", ErrNilReply
	}
	switch reply.kind {
	case '+', '$':
		return reply.text, nil
	case ':':
		return strconv.FormatInt(reply.num, 10), nil
	default:
		return "", fmt.Errorf("storage: redis reply of kind %q is not a string", string(rune(reply.kind)))
	}
}

// Int returns an integer reply, or ErrNilReply for a nil reply.
func (reply RedisReply) Int() (int64, error) {
	if reply.isNil {
		return 0, ErrNilReply
	}
	if reply.kind != ':' {
		return 0, fmt.Errorf("storage: redis reply of kind %q is not an integer", string(rune(reply.kind)))
	}
	return reply.num, nil
}

// Bool treats an integer reply as a Redis boolean (1 = true).
func (reply RedisReply) Bool() (bool, error) {
	value, err := reply.Int()
	if err != nil {
		return false, err
	}
	return value == 1, nil
}

// Strings flattens an array reply into strings.
func (reply RedisReply) Strings() ([]string, error) {
	if reply.isNil {
		return nil, ErrNilReply
	}
	if reply.kind != '*' {
		return nil, fmt.Errorf("storage: redis reply of kind %q is not an array", string(rune(reply.kind)))
	}
	out := make([]string, 0, len(reply.items))
	for _, item := range reply.items {
		text, err := item.Text()
		if err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, nil
}

// StringMap turns a flat array reply into pairs (HGETALL).
func (reply RedisReply) StringMap() (map[string]string, error) {
	items, err := reply.Strings()
	if err != nil {
		return nil, err
	}
	if len(items)%2 != 0 {
		return nil, fmt.Errorf("storage: redis map reply has %d elements", len(items))
	}
	out := make(map[string]string, len(items)/2)
	for i := 0; i+1 < len(items); i += 2 {
		out[items[i]] = items[i+1]
	}
	return out, nil
}

func readRedisReply(reader *bufio.Reader) (RedisReply, error) {
	line, err := readRedisLine(reader)
	if err != nil {
		return RedisReply{}, err
	}
	if line == "" {
		return RedisReply{}, errors.New("storage: redis sent an empty reply")
	}
	kind := line[0]
	body := line[1:]
	switch kind {
	case '+', '-':
		return RedisReply{kind: kind, text: body}, nil
	case ':':
		num, err := strconv.ParseInt(strings.TrimSpace(body), 10, 64)
		if err != nil {
			return RedisReply{}, err
		}
		return RedisReply{kind: kind, num: num}, nil
	case '$':
		length, err := strconv.Atoi(strings.TrimSpace(body))
		if err != nil {
			return RedisReply{}, err
		}
		if length < 0 {
			return RedisReply{kind: kind, isNil: true}, nil
		}
		payload := make([]byte, length+2)
		if _, err := readFullRedis(reader, payload); err != nil {
			return RedisReply{}, err
		}
		return RedisReply{kind: kind, text: string(payload[:length])}, nil
	case '*':
		count, err := strconv.Atoi(strings.TrimSpace(body))
		if err != nil {
			return RedisReply{}, err
		}
		if count < 0 {
			return RedisReply{kind: kind, isNil: true}, nil
		}
		items := make([]RedisReply, 0, count)
		for i := 0; i < count; i++ {
			item, err := readRedisReply(reader)
			if err != nil {
				return RedisReply{}, err
			}
			items = append(items, item)
		}
		return RedisReply{kind: kind, items: items}, nil
	default:
		return RedisReply{}, fmt.Errorf("storage: unsupported redis reply kind %q", string(rune(kind)))
	}
}

func readRedisLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func readFullRedis(reader *bufio.Reader, buffer []byte) (int, error) {
	total := 0
	for total < len(buffer) {
		read, err := reader.Read(buffer[total:])
		total += read
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// --- command helpers ---------------------------------------------------------

// Ping checks the connection.
func (client *RedisClient) Ping() error {
	reply, err := client.Do("PING")
	if err != nil {
		return err
	}
	text, err := reply.Text()
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(text), "PONG") {
		return fmt.Errorf("storage: unexpected redis ping reply %q", text)
	}
	return nil
}

// Get returns the value at key, or ErrNilReply when it is absent.
func (client *RedisClient) Get(key string) (string, error) {
	reply, err := client.Do("GET", key)
	if err != nil {
		return "", err
	}
	return reply.Text()
}

// Set stores value at key.
func (client *RedisClient) Set(key string, value string) error {
	_, err := client.Do("SET", key, value)
	return err
}

// SetWithTTL stores value at key and expires it after ttl.
func (client *RedisClient) SetWithTTL(key string, value string, ttl time.Duration) error {
	_, err := client.Do("SET", key, value, "PX", strconv.FormatInt(ttl.Milliseconds(), 10))
	return err
}

// SetIfAbsent stores value at key only when the key does not exist, with an
// optional ttl. It returns true when this call created the key, which is what
// makes it usable as an idempotency or leader-election guard.
func (client *RedisClient) SetIfAbsent(key string, value string, ttl time.Duration) (bool, error) {
	args := []string{"SET", key, value, "NX"}
	if ttl > 0 {
		args = append(args, "PX", strconv.FormatInt(ttl.Milliseconds(), 10))
	}
	reply, err := client.Do(args...)
	if err != nil {
		return false, err
	}
	if reply.IsNil() {
		return false, nil
	}
	text, err := reply.Text()
	if err != nil {
		return false, err
	}
	return strings.EqualFold(text, "OK"), nil
}

// Del removes keys and reports how many existed.
func (client *RedisClient) Del(keys ...string) (int64, error) {
	reply, err := client.Do(append([]string{"DEL"}, keys...)...)
	if err != nil {
		return 0, err
	}
	return reply.Int()
}

// Expire sets a ttl on key and reports whether the key existed.
func (client *RedisClient) Expire(key string, ttl time.Duration) (bool, error) {
	reply, err := client.Do("PEXPIRE", key, strconv.FormatInt(ttl.Milliseconds(), 10))
	if err != nil {
		return false, err
	}
	return reply.Bool()
}

// TTL returns the remaining ttl in seconds, or -1 when the key has no expiry
// and -2 when the key does not exist (Redis' own convention).
func (client *RedisClient) TTL(key string) (int64, error) {
	reply, err := client.Do("TTL", key)
	if err != nil {
		return 0, err
	}
	return reply.Int()
}

// Incr increments the integer at key.
func (client *RedisClient) Incr(key string) (int64, error) {
	reply, err := client.Do("INCR", key)
	if err != nil {
		return 0, err
	}
	return reply.Int()
}

// HSet sets field/value pairs on a hash. Values are alternating.
func (client *RedisClient) HSet(key string, fieldValues ...string) error {
	if len(fieldValues)%2 != 0 {
		return errors.New("storage: redis HSet needs an even number of field/value arguments")
	}
	_, err := client.Do(append([]string{"HSET", key}, fieldValues...)...)
	return err
}

// HGet returns one hash field, or ErrNilReply when it is absent.
func (client *RedisClient) HGet(key string, field string) (string, error) {
	reply, err := client.Do("HGET", key, field)
	if err != nil {
		return "", err
	}
	return reply.Text()
}

// HGetAll returns the whole hash.
func (client *RedisClient) HGetAll(key string) (map[string]string, error) {
	reply, err := client.Do("HGETALL", key)
	if err != nil {
		return nil, err
	}
	return reply.StringMap()
}

// HDel removes hash fields.
func (client *RedisClient) HDel(key string, fields ...string) (int64, error) {
	reply, err := client.Do(append([]string{"HDEL", key}, fields...)...)
	if err != nil {
		return 0, err
	}
	return reply.Int()
}

// ZAdd adds a member with a score to a sorted set.
func (client *RedisClient) ZAdd(key string, score float64, member string) error {
	_, err := client.Do("ZADD", key, strconv.FormatFloat(score, 'f', -1, 64), member)
	return err
}

// ZRangeWithScores returns a sorted-set slice by rank as alternating
// member/score strings.
func (client *RedisClient) ZRangeWithScores(key string, start int, stop int) ([]string, error) {
	reply, err := client.Do("ZRANGE", key, strconv.Itoa(start), strconv.Itoa(stop), "WITHSCORES")
	if err != nil {
		return nil, err
	}
	return reply.Strings()
}

// Eval runs a Lua script atomically.
func (client *RedisClient) Eval(script string, keys []string, args ...string) (RedisReply, error) {
	command := []string{"EVAL", script, strconv.Itoa(len(keys))}
	command = append(command, keys...)
	command = append(command, args...)
	return client.Do(command...)
}

// Allow is the fixed-window rate limiter: it returns true while fewer than
// limit operations have been seen for key inside window. The counter window is
// created on the first call and expires on its own, so no sweeper is needed —
// which is the point, the lobby has no background ticker.
func (client *RedisClient) Allow(key string, limit int64, window time.Duration) (bool, error) {
	reply, err := client.Eval(
		redisRateLimitScript,
		[]string{key},
		strconv.FormatInt(window.Milliseconds(), 10),
		strconv.FormatInt(limit, 10),
	)
	if err != nil {
		return false, err
	}
	return reply.Bool()
}
