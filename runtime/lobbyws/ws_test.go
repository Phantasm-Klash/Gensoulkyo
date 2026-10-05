package lobbyws

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// testWSClient is a minimal RFC6455 client used to exercise the server codec.
type testWSClient struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

func dialWS(t *testing.T, rawURL string) *testWSClient {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	keyBytes := make([]byte, 16)
	_, _ = rand.Read(keyBytes)
	key := base64.StdEncoding.EncodeToString(keyBytes)
	target := parsed.Path
	if target == "" {
		target = "/"
	}
	if parsed.RawQuery != "" {
		target += "?" + parsed.RawQuery
	}
	request := "GET " + target + " HTTP/1.1\r\n" +
		"Host: " + parsed.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(statusLine, "101") {
		rest, _ := io.ReadAll(br)
		t.Fatalf("expected 101, got %q body=%q", strings.TrimSpace(statusLine), string(rest))
	}
	headers := map[string]string{}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			headers[strings.ToLower(strings.TrimSpace(parts[0]))] = strings.TrimSpace(parts[1])
		}
	}
	expected := computeAccept(key)
	if headers["sec-websocket-accept"] != expected {
		t.Fatalf("accept mismatch: got %q want %q", headers["sec-websocket-accept"], expected)
	}
	return &testWSClient{t: t, conn: conn, br: br}
}

func (c *testWSClient) close() { _ = c.conn.Close() }

// sendLobby writes a JSON lobby envelope as a masked text frame.
func (c *testWSClient) sendLobby(msgType int, payload any) {
	c.t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		c.t.Fatalf("marshal payload: %v", err)
	}
	data, err := json.Marshal(envelope{Type: msgType, Payload: raw})
	if err != nil {
		c.t.Fatalf("marshal envelope: %v", err)
	}
	c.writeFrame(true, OpText, data)
}

// readEnvelope reads frames until one of the wanted type arrives or the timeout
// elapses, skipping unrelated messages.
func (c *testWSClient) readEnvelope(wantType int, timeout time.Duration) envelope {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.t.Fatalf("timed out waiting for lobby message type %d", wantType)
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(remaining))
		_, opcode, payload, err := c.tryReadFrame()
		if err != nil {
			c.t.Fatalf("read while waiting for type %d: %v", wantType, err)
		}
		if opcode != OpText {
			continue
		}
		var env envelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}
		if env.Type == wantType {
			return env
		}
	}
}

func (c *testWSClient) setDeadline(d time.Duration) {
	_ = c.conn.SetDeadline(time.Now().Add(d))
}

// writeFrame writes a client frame. Client frames must be masked.
func (c *testWSClient) writeFrame(fin bool, opcode int, payload []byte) {
	c.t.Helper()
	var header bytes.Buffer
	first := byte(opcode) & 0x0F
	if fin {
		first |= 0x80
	}
	header.WriteByte(first)
	length := len(payload)
	switch {
	case length < 126:
		header.WriteByte(0x80 | byte(length))
	case length <= 0xFFFF:
		header.WriteByte(0x80 | 126)
		var buf [2]byte
		binary.BigEndian.PutUint16(buf[:], uint16(length))
		header.Write(buf[:])
	default:
		header.WriteByte(0x80 | 127)
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(length))
		header.Write(buf[:])
	}
	maskKey := [4]byte{0x12, 0x34, 0x56, 0x78}
	header.Write(maskKey[:])
	masked := make([]byte, length)
	for i := 0; i < length; i++ {
		masked[i] = payload[i] ^ maskKey[i%4]
	}
	if _, err := c.conn.Write(header.Bytes()); err != nil {
		c.t.Fatalf("write header: %v", err)
	}
	if length > 0 {
		if _, err := c.conn.Write(masked); err != nil {
			c.t.Fatalf("write payload: %v", err)
		}
	}
}

// tryReadFrame reads one server frame, returning an error instead of failing
// the test (used by the polling envelope reader).
func (c *testWSClient) tryReadFrame() (bool, int, []byte, error) {
	var first [2]byte
	if _, err := io.ReadFull(c.br, first[:]); err != nil {
		return false, 0, nil, err
	}
	fin := first[0]&0x80 != 0
	opcode := int(first[0] & 0x0F)
	if first[1]&0x80 != 0 {
		return false, 0, nil, fmt.Errorf("server frame must not be masked")
	}
	length := int64(first[1] & 0x7F)
	switch length {
	case 126:
		var buf [2]byte
		if _, err := io.ReadFull(c.br, buf[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(buf[:]))
	case 127:
		var buf [8]byte
		if _, err := io.ReadFull(c.br, buf[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(buf[:]))
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return false, 0, nil, err
		}
	}
	return fin, opcode, payload, nil
}

// readFrame reads one server frame (server frames are never masked).
func (c *testWSClient) readFrame() (bool, int, []byte) {
	c.t.Helper()
	fin, opcode, payload, err := c.tryReadFrame()
	if err != nil {
		c.t.Fatalf("read frame: %v", err)
	}
	return fin, opcode, payload
}

func echoServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := Upgrade(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for {
			opcode, payload, err := conn.ReadMessage()
			if err != nil {
				_ = conn.Close()
				return
			}
			if opcode == OpClose {
				_ = conn.WriteClose(1000, "")
				return
			}
			if err := conn.WriteMessage(opcode, payload); err != nil {
				_ = conn.Close()
				return
			}
		}
	}))
}

func TestWebSocketHandshakeRejectsPlainHTTP(t *testing.T) {
	server := echoServer()
	defer server.Close()
	response, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain GET should be rejected with 400, got %d", response.StatusCode)
	}
}

func TestWebSocketTextAndBinaryRoundTrip(t *testing.T) {
	server := echoServer()
	defer server.Close()
	client := dialWS(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.setDeadline(5 * time.Second)

	client.writeFrame(true, OpText, []byte("hello lobby"))
	fin, opcode, payload := client.readFrame()
	if !fin || opcode != OpText || string(payload) != "hello lobby" {
		t.Fatalf("text roundtrip mismatch: fin=%v opcode=%d payload=%q", fin, opcode, payload)
	}

	binaryPayload := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}
	client.writeFrame(true, OpBinary, binaryPayload)
	_, opcode, payload = client.readFrame()
	if opcode != OpBinary || !bytes.Equal(payload, binaryPayload) {
		t.Fatalf("binary roundtrip mismatch: opcode=%d payload=%v", opcode, payload)
	}
}

func TestWebSocketExtendedLengths(t *testing.T) {
	server := echoServer()
	defer server.Close()
	client := dialWS(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.setDeadline(5 * time.Second)

	for _, size := range []int{125, 126, 65535, 70000} {
		payload := bytes.Repeat([]byte{'x'}, size)
		client.writeFrame(true, OpBinary, payload)
		_, opcode, got := client.readFrame()
		if opcode != OpBinary || !bytes.Equal(got, payload) {
			t.Fatalf("payload size %d roundtrip mismatch (opcode=%d len=%d)", size, opcode, len(got))
		}
	}
}

func TestWebSocketPingPongAndClose(t *testing.T) {
	server := echoServer()
	defer server.Close()
	client := dialWS(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.setDeadline(5 * time.Second)

	client.writeFrame(true, OpPing, []byte("ping"))
	_, opcode, payload := client.readFrame()
	if opcode != OpPong || string(payload) != "ping" {
		t.Fatalf("expected pong echo, got opcode=%d payload=%q", opcode, payload)
	}

	client.writeFrame(true, OpClose, []byte{0x03, 0xE8})
	_, opcode, _ = client.readFrame()
	if opcode != OpClose {
		t.Fatalf("expected close frame, got opcode=%d", opcode)
	}
}

func TestWebSocketFragmentedMessage(t *testing.T) {
	server := echoServer()
	defer server.Close()
	client := dialWS(t, "ws"+strings.TrimPrefix(server.URL, "http"))
	defer client.close()
	client.setDeadline(5 * time.Second)

	client.writeFrame(false, OpText, []byte("frag"))
	client.writeFrame(false, OpContinuation, []byte("men"))
	client.writeFrame(true, OpContinuation, []byte("ted"))
	fin, opcode, payload := client.readFrame()
	if !fin || opcode != OpText || string(payload) != "fragmented" {
		t.Fatalf("fragmented roundtrip mismatch: fin=%v opcode=%d payload=%q", fin, opcode, payload)
	}
}

func TestComputeAcceptMatchesRFCExample(t *testing.T) {
	// RFC6455 section 1.3 example key.
	got := computeAccept("dGhlIHNhbXBsZSBub25jZQ==")
	if got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("computeAccept mismatch: %s", got)
	}
}

func TestParseEnvelopeRoundTrip(t *testing.T) {
	raw := []byte(fmt.Sprintf(`{"type":%d,"seq":7,"payload":{"room_code":"ABCD"}}`, TypeRoomCreateRequest))
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != TypeRoomCreateRequest || env.Seq != 7 {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	var payload struct {
		RoomCode string `json:"room_code"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.RoomCode != "ABCD" {
		t.Fatalf("payload mismatch: %+v", payload)
	}
}
