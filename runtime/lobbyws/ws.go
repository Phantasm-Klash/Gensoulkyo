// Package lobbyws implements the Gensoulkyo WebSocket lobby and the web battle
// relay on top of the Go standard library only.
//
// The WebSocket layer is a minimal RFC6455 server implementation written by
// hand (no third-party dependency): the HTTP upgrade is completed through
// http.Hijacker, the Sec-WebSocket-Accept value is derived with SHA-1, and
// frames are parsed and serialized directly. Client frames must be masked and
// are unmasked on read; server frames are never masked.
package lobbyws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// WebSocket opcodes.
const (
	OpContinuation = 0x0
	OpText         = 0x1
	OpBinary       = 0x2
	OpClose        = 0x8
	OpPing         = 0x9
	OpPong         = 0xA
)

const (
	wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	// maxFramePayload caps a single frame. KCP datagrams and lobby JSON messages
	// are far smaller; the limit only guards against a hostile client.
	maxFramePayload = 4 << 20
	// maxMessageSize caps a reassembled fragmented message.
	maxMessageSize = 8 << 20
)

var (
	// ErrClosed is returned once the underlying connection has been closed.
	ErrClosed = errors.New("lobbyws: connection closed")
	// ErrProtocol signals a malformed WebSocket frame.
	ErrProtocol = errors.New("lobbyws: protocol error")
)

// Conn is a server-side WebSocket connection.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader

	writeMu sync.Mutex
	closeMu sync.Mutex
	closed  bool

	// onPongMu guards onPong, which ReadMessage calls from the read goroutine
	// while SetPongHandler may be called from the serving goroutine.
	onPongMu sync.Mutex
	onPong   func()
}

type frameHeader struct {
	fin     bool
	opcode  int
	masked  bool
	maskKey [4]byte
	length  int
}

// IsUpgrade reports whether the request looks like a WebSocket upgrade.
func IsUpgrade(r *http.Request) bool {
	if r == nil {
		return false
	}
	return headerHasToken(r.Header, "Connection", "upgrade") &&
		strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket")
}

// Upgrade completes the WebSocket handshake, hijacking the underlying
// connection. On success the caller owns the returned Conn; on failure the
// response writer is still usable and the caller may write an HTTP error.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !IsUpgrade(r) {
		return nil, errors.New("lobbyws: not a websocket upgrade request")
	}
	if strings.TrimSpace(r.Header.Get("Sec-WebSocket-Version")) != "13" {
		return nil, errors.New("lobbyws: unsupported websocket version")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, errors.New("lobbyws: missing Sec-WebSocket-Key")
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("lobbyws: response writer does not support hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("lobbyws: hijack: %w", err)
	}
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + computeAccept(key) + "\r\n\r\n"
	if _, err := rw.WriteString(response); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("lobbyws: write handshake: %w", err)
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("lobbyws: flush handshake: %w", err)
	}
	return &Conn{conn: conn, br: rw.Reader}, nil
}

func computeAccept(key string) string {
	sum := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func headerHasToken(header http.Header, name string, token string) bool {
	for _, value := range header.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// RemoteAddr returns the remote network address of the connection.
func (c *Conn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

// SetReadDeadline sets the deadline for future reads.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// SetWriteDeadline sets the deadline for future writes.
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// SetPongHandler registers a callback invoked whenever a ping or pong control
// frame is read. ReadMessage consumes control frames internally and never
// returns them to the caller, so this is the only way for a keepalive loop to
// observe that a peer is still alive. It is safe to call from another goroutine.
func (c *Conn) SetPongHandler(h func()) {
	c.onPongMu.Lock()
	c.onPong = h
	c.onPongMu.Unlock()
}

func (c *Conn) firePong() {
	c.onPongMu.Lock()
	h := c.onPong
	c.onPongMu.Unlock()
	if h != nil {
		h()
	}
}

// ReadMessage reads the next complete data message, transparently handling
// fragmentation and replying to ping frames. Control close frames are returned
// to the caller with OpClose and their payload.
func (c *Conn) ReadMessage() (int, []byte, error) {
	var (
		messageOp int
		payload   []byte
	)
	for {
		header, err := c.readFrameHeader()
		if err != nil {
			return 0, nil, err
		}
		if header.opcode >= 0x8 {
			data, err := c.readPayload(header)
			if err != nil {
				return 0, nil, err
			}
			switch header.opcode {
			case OpClose:
				return OpClose, data, nil
			case OpPing:
				c.firePong()
				if err := c.writeFrame(OpPong, data, true); err != nil {
					return 0, nil, err
				}
				continue
			case OpPong:
				c.firePong()
				continue
			default:
				return 0, nil, ErrProtocol
			}
		}
		if header.opcode == OpContinuation {
			if messageOp == 0 {
				return 0, nil, ErrProtocol
			}
		} else {
			if messageOp != 0 {
				return 0, nil, ErrProtocol
			}
			messageOp = header.opcode
		}
		data, err := c.readPayload(header)
		if err != nil {
			return 0, nil, err
		}
		payload = append(payload, data...)
		if len(payload) > maxMessageSize {
			return 0, nil, ErrProtocol
		}
		if header.fin {
			return messageOp, payload, nil
		}
	}
}

func (c *Conn) readFrameHeader() (frameHeader, error) {
	var header frameHeader
	first, err := c.br.ReadByte()
	if err != nil {
		return header, err
	}
	second, err := c.br.ReadByte()
	if err != nil {
		return header, err
	}
	header.fin = first&0x80 != 0
	if first&0x70 != 0 {
		return header, ErrProtocol // RSV bits must be zero
	}
	header.opcode = int(first & 0x0F)
	header.masked = second&0x80 != 0
	length := int64(second & 0x7F)
	switch length {
	case 126:
		var buf [2]byte
		if _, err := io.ReadFull(c.br, buf[:]); err != nil {
			return header, err
		}
		length = int64(binary.BigEndian.Uint16(buf[:]))
	case 127:
		var buf [8]byte
		if _, err := io.ReadFull(c.br, buf[:]); err != nil {
			return header, err
		}
		value := binary.BigEndian.Uint64(buf[:])
		if value > maxMessageSize {
			return header, ErrProtocol
		}
		length = int64(value)
	}
	if length < 0 || length > maxFramePayload {
		return header, ErrProtocol
	}
	header.length = int(length)
	if header.masked {
		if _, err := io.ReadFull(c.br, header.maskKey[:]); err != nil {
			return header, err
		}
	}
	if header.opcode >= 0x8 {
		if !header.fin {
			return header, ErrProtocol
		}
		if header.length > 125 {
			return header, ErrProtocol
		}
	}
	return header, nil
}

func (c *Conn) readPayload(header frameHeader) ([]byte, error) {
	if header.length == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, header.length)
	if _, err := io.ReadFull(c.br, buf); err != nil {
		return nil, err
	}
	if header.masked {
		for i := range buf {
			buf[i] ^= header.maskKey[i%4]
		}
	}
	return buf, nil
}

// WriteMessage writes a single unfragmented data frame (OpText or OpBinary).
func (c *Conn) WriteMessage(opcode int, payload []byte) error {
	return c.writeFrame(opcode, payload, true)
}

// WriteText writes a text frame.
func (c *Conn) WriteText(text string) error { return c.WriteMessage(OpText, []byte(text)) }

// WriteBinary writes a binary frame.
func (c *Conn) WriteBinary(payload []byte) error { return c.WriteMessage(OpBinary, payload) }

// WritePing writes a ping control frame.
func (c *Conn) WritePing(payload []byte) error { return c.writeFrame(OpPing, payload, true) }

// WritePong writes a pong control frame.
func (c *Conn) WritePong(payload []byte) error { return c.writeFrame(OpPong, payload, true) }

// WriteClose writes a close control frame and then closes the connection.
func (c *Conn) WriteClose(code int, reason string) error {
	payload := make([]byte, 0, 2+len(reason))
	payload = append(payload, byte(code>>8), byte(code))
	payload = append(payload, reason...)
	err := c.writeFrame(OpClose, payload, true)
	_ = c.Close()
	return err
}

func (c *Conn) writeFrame(opcode int, payload []byte, fin bool) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.isClosed() {
		return ErrClosed
	}
	header := make([]byte, 0, 10)
	first := byte(opcode) & 0x0F
	if fin {
		first |= 0x80
	}
	header = append(header, first)
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(length))
	case length <= 0xFFFF:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127)
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(length))
		header = append(header, buf[:]...)
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	if length > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func (c *Conn) isClosed() bool {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	return c.closed
}

// Close closes the underlying network connection. It is safe to call multiple
// times.
func (c *Conn) Close() error {
	c.closeMu.Lock()
	if c.closed {
		c.closeMu.Unlock()
		return nil
	}
	c.closed = true
	c.closeMu.Unlock()
	return c.conn.Close()
}
