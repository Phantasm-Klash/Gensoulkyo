package lobbyws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"gensoulkyo/runtime/battlespawn"
	"gensoulkyo/runtime/core"
)

// Options configures the lobby WebSocket server.
type Options struct {
	Service *core.Service
	// Spawner, when set, starts a per-match battle server process when a room
	// match begins. When nil the lobby reuses the allocation returned by core.
	Spawner *battlespawn.Spawner
	// LobbyEndpoint is the host:port passed to the battle server so it can POST
	// its result back (for example "127.0.0.1:7350").
	LobbyEndpoint string
	// BattleRuleset overrides the ruleset passed to the battle server.
	BattleRuleset string
	// MatchTTL is an optional safety net that kills a battle server process after
	// the given duration.
	MatchTTL time.Duration
	// PongWait / PingPeriod / WriteWait tune the WebSocket keepalive. Zero uses
	// the Default* values. Tests shrink them to exercise the reaper quickly.
	PongWait   time.Duration
	PingPeriod time.Duration
	WriteWait  time.Duration
	Logger     *log.Logger
}

// Server hosts the WebSocket lobby, the battle relay and the internal battle
// result callback.
type Server struct {
	service       *core.Service
	spawner       *battlespawn.Spawner
	lobbyEndpoint string
	ruleset       string
	matchTTL      time.Duration
	pongWait      time.Duration
	pingPeriod    time.Duration
	writeWait     time.Duration
	logger        *log.Logger

	mu      sync.Mutex
	clients map[*client]struct{}
	rooms   map[string]map[*client]struct{}
	matches map[string]*matchEntry
}

type matchEntry struct {
	roomCode       string
	endpoint       string
	battleServerID string
}

// New builds a lobby server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}
	// A zero MatchTTL used to mean "no TTL at all": every spawned battle server
	// then relied solely on its own `--max-ticks` budget to exit. Defaulting it
	// here (and again in the spawner) makes the wall-clock backstop impossible to
	// forget, so a wedged match can never pin a process indefinitely.
	matchTTL := opts.MatchTTL
	if matchTTL <= 0 {
		matchTTL = battlespawn.DefaultMatchTTL
	}
	pongWait := opts.PongWait
	if pongWait <= 0 {
		pongWait = DefaultPongWait
	}
	pingPeriod := opts.PingPeriod
	if pingPeriod <= 0 || pingPeriod >= pongWait {
		pingPeriod = pongWait * 5 / 12
	}
	writeWait := opts.WriteWait
	if writeWait <= 0 {
		writeWait = DefaultWriteWait
	}
	return &Server{
		service:       opts.Service,
		spawner:       opts.Spawner,
		lobbyEndpoint: strings.TrimSpace(opts.LobbyEndpoint),
		ruleset:       strings.TrimSpace(opts.BattleRuleset),
		matchTTL:      matchTTL,
		pongWait:      pongWait,
		pingPeriod:    pingPeriod,
		writeWait:     writeWait,
		logger:        logger,
		clients:       map[*client]struct{}{},
		rooms:         map[string]map[*client]struct{}{},
		matches:       map[string]*matchEntry{},
	}
}

type outbound struct {
	opcode  int
	payload []byte
}

type client struct {
	server *Server
	conn   *Conn

	token  string
	userID string
	name   string

	roomCode string

	send      chan outbound
	closed    chan struct{}
	closeOnce sync.Once
}

// HandleLobby upgrades an HTTP request to the lobby WebSocket and serves it.
func (s *Server) HandleLobby(w http.ResponseWriter, r *http.Request) {
	if s.service == nil {
		http.Error(w, "lobby service unavailable", http.StatusServiceUnavailable)
		return
	}
	if !IsUpgrade(r) {
		http.Error(w, "websocket upgrade required", http.StatusUpgradeRequired)
		return
	}
	conn, err := Upgrade(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c := &client{
		server: s,
		conn:   conn,
		send:   make(chan outbound, 128),
		closed: make(chan struct{}),
	}
	s.register(c)
	go c.writePump()
	c.readLoop()
}

func (s *Server) register(c *client) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) unregister(c *client) {
	s.mu.Lock()
	delete(s.clients, c)
	if c.roomCode != "" {
		if set := s.rooms[c.roomCode]; set != nil {
			delete(set, c)
			if len(set) == 0 {
				delete(s.rooms, c.roomCode)
			}
		}
	}
	s.mu.Unlock()
}

func (s *Server) setRoom(c *client, roomCode string) {
	s.mu.Lock()
	if c.roomCode != "" && c.roomCode != roomCode {
		if set := s.rooms[c.roomCode]; set != nil {
			delete(set, c)
			if len(set) == 0 {
				delete(s.rooms, c.roomCode)
			}
		}
	}
	c.roomCode = roomCode
	if roomCode != "" {
		if s.rooms[roomCode] == nil {
			s.rooms[roomCode] = map[*client]struct{}{}
		}
		s.rooms[roomCode][c] = struct{}{}
	}
	s.mu.Unlock()
}

func (s *Server) roomClients(roomCode string) []*client {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := s.rooms[roomCode]
	out := make([]*client, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	return out
}

func (s *Server) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// Default keepalive parameters for the lobby WebSocket. A peer that vanishes
// without a TCP FIN/RST (process killed, network drop, NAT timeout) would
// otherwise block ReadMessage forever, leaking the client, its goroutine and its
// room membership. The server pings every PingPeriod and drops the connection
// when no frame has arrived for PongWait.
const (
	// DefaultPongWait is how long we tolerate silence before reaping a client.
	DefaultPongWait = 60 * time.Second
	// DefaultPingPeriod must be < DefaultPongWait so a live client answers in time.
	DefaultPingPeriod = 25 * time.Second
	// DefaultWriteWait bounds a single write so a stalled peer cannot block the pump.
	DefaultWriteWait = 10 * time.Second
)

func (c *client) readLoop() {
	defer func() {
		c.close()
	}()
	// Reset the read deadline on every frame (including the pongs that answer
	// our keepalive pings), so only a genuinely silent peer times out.
	_ = c.conn.SetReadDeadline(time.Now().Add(c.server.pongWait))
	c.conn.SetPongHandler(func() {
		_ = c.conn.SetReadDeadline(time.Now().Add(c.server.pongWait))
	})
	for {
		opcode, payload, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(c.server.pongWait))
		switch opcode {
		case OpClose:
			_ = c.conn.WriteClose(1000, "")
			return
		case OpText:
			c.handleText(payload)
		case OpBinary:
			c.sendError("unsupported_frame", "binary frames are only accepted on the battle relay")
		}
	}
}

func (c *client) writePump() {
	ping := time.NewTicker(c.server.pingPeriod)
	defer ping.Stop()
	for {
		select {
		case <-c.closed:
			_ = c.conn.Close()
			return
		case <-ping.C:
			// A failed ping means the peer is gone; stop the pump and let the
			// read side (which is blocked on its deadline) unwind too.
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.server.writeWait))
			if err := c.conn.WritePing(nil); err != nil {
				c.close()
				_ = c.conn.Close()
				return
			}
		case msg := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.server.writeWait))
			if err := c.conn.WriteMessage(msg.opcode, msg.payload); err != nil {
				c.close()
				_ = c.conn.Close()
				return
			}
		}
	}
}

func (c *client) close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.server.unregister(c)
	})
}

func (c *client) enqueue(msg outbound) {
	select {
	case <-c.closed:
	case c.send <- msg:
	default:
		// Slow consumer: drop rather than block the whole lobby.
	}
}

func (c *client) sendEnvelope(msgType int, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	data, err := json.Marshal(envelope{Type: msgType, Payload: raw})
	if err != nil {
		return
	}
	c.enqueue(outbound{opcode: OpText, payload: data})
}

func (c *client) sendError(code string, message string) {
	c.sendEnvelope(TypeError, ErrorStatus{Code: code, Message: message})
}

func (c *client) authenticated() bool { return c.token != "" }

func (c *client) handleText(data []byte) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		c.sendError("invalid_json", err.Error())
		return
	}
	switch env.Type {
	case TypeAuthRequest:
		c.handleAuth(env.Payload)
	case TypeBootstrapRequest:
		c.handleBootstrap(env.Payload)
	case TypeRoomCreateRequest:
		c.handleRoomCreate(env.Payload)
	case TypeRoomJoinRequest:
		c.handleRoomJoin(env.Payload)
	case TypeRoomLeaveRequest:
		c.handleRoomLeave(env.Payload)
	default:
		c.sendError("unsupported_type", "unsupported lobby message type")
	}
}

func (c *client) requireAuth() bool {
	if !c.authenticated() {
		c.sendError("unauthorized", "authenticate before sending lobby messages")
		return false
	}
	return true
}

func (c *client) handleAuth(raw json.RawMessage) {
	var req LobbyAuthRequest
	_ = json.Unmarshal(raw, &req)

	token := strings.TrimSpace(req.SessionToken)
	var snapshot *core.BootstrapSnapshot
	if token != "" {
		if bootstrapped, err := c.server.service.Bootstrap(token); err == nil {
			snapshot = bootstrapped
		}
	}
	if snapshot == nil {
		userID := strings.TrimSpace(req.UserID)
		if userID == "" {
			c.sendEnvelope(TypeAuthResponse, LobbyAuthResponse{
				Error: &ErrorStatus{Code: "unauthorized", Message: "a valid session_token or user_id is required"},
			})
			return
		}
		displayName := "Web Player"
		session, err := c.server.service.LoginExternal(core.ExternalSessionRequest{
			UserID:       userID,
			SessionToken: tokenOrRandom(token),
			DisplayName:  displayName,
			Provider:     strings.TrimSpace(req.Platform),
		})
		if err != nil {
			c.sendEnvelope(TypeAuthResponse, LobbyAuthResponse{Error: &ErrorStatus{Code: core.ErrorCode(err), Message: err.Error()}})
			return
		}
		bootstrapped, err := c.server.service.Bootstrap(session.SessionToken)
		if err != nil {
			c.sendEnvelope(TypeAuthResponse, LobbyAuthResponse{Error: &ErrorStatus{Code: core.ErrorCode(err), Message: err.Error()}})
			return
		}
		token = session.SessionToken
		snapshot = bootstrapped
	}

	c.token = snapshot.SessionToken
	c.userID = snapshot.UserID
	c.name = snapshot.DisplayName
	now := time.Now().UTC()
	c.sendEnvelope(TypeAuthResponse, LobbyAuthResponse{
		Version:      coreVersionStamp(snapshot.Ruleset),
		SessionToken: snapshot.SessionToken,
		UserID:       snapshot.UserID,
		IssuedAtMS:   now.UnixMilli(),
		ExpiresAtMS:  now.Add(time.Hour).UnixMilli(),
	})
}

func (c *client) handleBootstrap(raw json.RawMessage) {
	if !c.requireAuth() {
		return
	}
	snapshot, err := c.server.service.Bootstrap(c.token)
	if err != nil {
		c.sendEnvelope(TypeBootstrapResponse, LobbyBootstrapResponse{Error: &ErrorStatus{Code: core.ErrorCode(err), Message: err.Error()}})
		return
	}
	c.sendEnvelope(TypeBootstrapResponse, LobbyBootstrapResponse{
		Version:        coreVersionStamp(snapshot.Ruleset),
		Profile:        profileFromSnapshot(snapshot),
		RulesetVersion: snapshot.Ruleset,
		ServerFlags:    map[string]string{},
	})
}

func (c *client) handleRoomCreate(raw json.RawMessage) {
	if !c.requireAuth() {
		return
	}
	var req RoomCreateRequest
	_ = json.Unmarshal(raw, &req)
	resp, err := c.server.service.CreateRoom(c.token, core.CreateRoomRequest{
		ModeID:       strings.TrimSpace(req.ModeID),
		ModeParams:   modeParamsFromLoadout(req.Loadout, req.ModeParams),
		ActiveDeckID: "",
	})
	if err != nil {
		c.sendEnvelope(TypeRoomCreateResponse, RoomCreateResponse{Error: &ErrorStatus{Code: core.ErrorCode(err), Message: err.Error()}})
		return
	}
	c.server.setRoom(c, resp.RoomCode)
	room := c.server.roomSnapshot(c.token, resp.RoomCode)
	c.sendEnvelope(TypeRoomCreateResponse, RoomCreateResponse{
		Version:    coreVersionStamp(room.RulesetVersion),
		RoomCode:   resp.RoomCode,
		ModeID:     resp.ModeID,
		HostUserID: room.HostUserID,
		Room:       &room,
	})
	c.server.broadcastRoomState(resp.RoomCode)
}

func (c *client) handleRoomJoin(raw json.RawMessage) {
	if !c.requireAuth() {
		return
	}
	var req RoomJoinRequest
	_ = json.Unmarshal(raw, &req)
	resp, err := c.server.service.JoinRoom(c.token, req.RoomCode, core.JoinRoomRequest{
		ModeParams: modeParamsFromLoadout(req.Loadout, nil),
	})
	if err != nil {
		c.sendEnvelope(TypeRoomJoinResponse, RoomJoinResponse{Error: &ErrorStatus{Code: core.ErrorCode(err), Message: err.Error()}})
		return
	}
	c.server.setRoom(c, resp.RoomCode)
	room := c.server.roomSnapshot(c.token, resp.RoomCode)
	c.sendEnvelope(TypeRoomJoinResponse, RoomJoinResponse{
		Version:    coreVersionStamp(room.RulesetVersion),
		RoomCode:   resp.RoomCode,
		ModeID:     room.ModeID,
		HostUserID: room.HostUserID,
		Players:    room.Players,
		Room:       &room,
	})
	c.server.broadcastRoomState(resp.RoomCode)
	if resp.MatchID != "" {
		c.server.startMatchAsync(resp.RoomCode, resp.MatchID)
	}
}

func (c *client) handleRoomLeave(raw json.RawMessage) {
	if !c.requireAuth() {
		return
	}
	var req RoomLeaveRequest
	_ = json.Unmarshal(raw, &req)
	roomCode := strings.TrimSpace(req.RoomCode)
	if roomCode == "" {
		roomCode = c.roomCode
	}
	if roomCode == "" {
		c.sendError("invalid_request", "room_code is required")
		return
	}
	if _, err := c.server.service.LeaveRoom(c.token, roomCode); err != nil {
		c.sendError(core.ErrorCode(err), err.Error())
		return
	}
	room := c.server.roomSnapshot(c.token, roomCode)
	c.server.setRoom(c, "")
	c.sendEnvelope(TypeRoomState, room)
	c.server.broadcastRoomState(roomCode)
}

func (s *Server) roomSnapshot(sessionToken string, roomCode string) RoomStateMessage {
	snapshot, err := s.service.Room(sessionToken, roomCode)
	if err != nil {
		return RoomStateMessage{RoomCode: roomCode}
	}
	return roomStateMessageFromSnapshot(*snapshot)
}

func (s *Server) broadcastRoomState(roomCode string) {
	if roomCode == "" {
		return
	}
	for _, c := range s.roomClients(roomCode) {
		c.sendEnvelope(TypeRoomState, s.roomSnapshot(c.token, roomCode))
	}
}

func (s *Server) startMatchAsync(roomCode string, matchID string) {
	if roomCode == "" || matchID == "" {
		return
	}
	s.mu.Lock()
	if _, exists := s.matches[matchID]; exists {
		s.mu.Unlock()
		return
	}
	s.matches[matchID] = &matchEntry{roomCode: roomCode}
	s.mu.Unlock()
	go s.startMatch(roomCode, matchID)
}

// startMatch spawns the per-match battle server (when configured), binds the
// allocation to its endpoint, auto-readies every connected room member and
// broadcasts the match start message.
func (s *Server) startMatch(roomCode string, matchID string) {
	if s.spawner != nil {
		allocation, ok := s.service.BattleAllocationForMatch(matchID)
		if ok {
			playerIDs := make([]string, 0, len(allocation.Players))
			for _, player := range allocation.Players {
				playerIDs = append(playerIDs, player.PlayerID)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			proc, err := s.spawner.Spawn(ctx, battlespawn.SpawnRequest{
				MatchID:       matchID,
				Seed:          uint64(allocation.ServerSeed),
				PlayerIDs:     playerIDs,
				LobbyEndpoint: s.lobbyEndpoint,
				Ruleset:       s.ruleset,
				TTL:           s.matchTTL,
			})
			cancel()
			if err != nil {
				s.logf("lobbyws: battle server spawn failed for match %s: %v", matchID, err)
			} else {
				battleServerID := "battle-" + matchID
				if binding, err := s.service.BindBattleServerAllocation(matchID, battleServerID, proc.Endpoint); err != nil {
					s.logf("lobbyws: bind allocation failed for match %s: %v", matchID, err)
				} else {
					s.mu.Lock()
					if entry := s.matches[matchID]; entry != nil {
						entry.endpoint = binding.Endpoint
						entry.battleServerID = binding.BattleServerID
					}
					s.mu.Unlock()
				}
			}
		}
	}
	for _, c := range s.roomClients(roomCode) {
		if _, err := s.service.ReadyMatch(c.token, matchID); err != nil {
			s.logf("lobbyws: auto-ready failed for user %s on match %s: %v", c.userID, matchID, err)
		}
	}
	s.broadcastRoomState(roomCode)
	s.broadcastMatchStart(roomCode, matchID)
}

func (s *Server) broadcastMatchStart(roomCode string, matchID string) {
	allocation, _ := s.service.BattleAllocationForMatch(matchID)
	for _, c := range s.roomClients(roomCode) {
		event, ticket, err := s.service.MatchStart(c.token, matchID)
		if err != nil {
			continue
		}
		message := matchStartMessageFromEvent(*event, ticket, allocation)
		c.sendEnvelope(TypeMatchStart, message)
	}
}

// HandleBattleResult receives the C++ battle server result callback, settles the
// match through core, pushes MatchResultMessage to the room and reaps the
// process.
func (s *Server) HandleBattleResult(w http.ResponseWriter, r *http.Request) {
	if s.service == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error_code": "unavailable"})
		return
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error_code": "method_not_allowed"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error_code": "invalid_request", "message": err.Error()})
		return
	}
	var callback core.BattleResultCallback
	if err := json.Unmarshal(body, &callback); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error_code": "invalid_json", "message": err.Error()})
		return
	}
	resp, err := s.service.ApplyBattleResultCallback(callback)
	if err != nil {
		status := http.StatusBadRequest
		switch core.ErrorCode(err) {
		case "not_found":
			status = http.StatusNotFound
		case "match_state_invalid":
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]any{"ok": false, "error_code": core.ErrorCode(err), "message": err.Error()})
		return
	}

	if !resp.Duplicate {
		s.mu.Lock()
		entry := s.matches[callback.MatchID]
		s.mu.Unlock()
		if entry != nil && entry.roomCode != "" {
			s.broadcast(entry.roomCode, TypeMatchResult, MatchResultMessage{
				Version:             coreVersionStamp(""),
				MatchID:             resp.MatchID,
				WinnerPlayerID:      resp.WinnerPlayerID,
				Points:              resp.Points,
				ReplayID:            resp.ReplayID,
				ServerAuthoritative: true,
				ModeID:              resp.ModeID,
				SettledAtMS:         resp.ServerTime.UnixMilli(),
			})
		}
	}
	if s.spawner != nil {
		_ = s.spawner.Kill(callback.MatchID)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) broadcast(roomCode string, msgType int, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	data, err := json.Marshal(envelope{Type: msgType, Payload: raw})
	if err != nil {
		return
	}
	for _, c := range s.roomClients(roomCode) {
		c.enqueue(outbound{opcode: OpText, payload: data})
	}
}

// HandleRelay upgrades a WebSocket and relays raw KCP datagrams (binary frames)
// between the client and the match's battle server UDP socket. Text frames are
// not part of the relay transport.
func (s *Server) HandleRelay(w http.ResponseWriter, r *http.Request) {
	matchID := strings.TrimSpace(r.URL.Query().Get("match_id"))
	if matchID == "" {
		http.Error(w, "match_id is required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	entry := s.matches[matchID]
	endpoint := ""
	if entry != nil {
		endpoint = entry.endpoint
	}
	s.mu.Unlock()
	if endpoint == "" {
		if allocation, ok := s.service.BattleAllocationForMatch(matchID); ok {
			endpoint = allocation.Endpoint
		}
	}
	if endpoint == "" {
		http.Error(w, "battle server endpoint unavailable", http.StatusServiceUnavailable)
		return
	}
	udpAddr, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		http.Error(w, "invalid battle server endpoint", http.StatusInternalServerError)
		return
	}
	udpConn, err := net.DialUDP("udp", nil, udpAddr)
	if err != nil {
		http.Error(w, "battle server unreachable", http.StatusBadGateway)
		return
	}
	wsConn, err := Upgrade(w, r)
	if err != nil {
		_ = udpConn.Close()
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var once sync.Once
	stop := func() {
		once.Do(func() {
			_ = udpConn.Close()
			_ = wsConn.Close()
		})
	}
	defer stop()

	go func() {
		buffer := make([]byte, 65536)
		for {
			n, err := udpConn.Read(buffer)
			if err != nil {
				stop()
				return
			}
			if err := wsConn.WriteBinary(buffer[:n]); err != nil {
				stop()
				return
			}
		}
	}()

	// Mirror the lobby keepalive: a client that disappears mid-match would
	// otherwise pin this relay (plus its UDP socket and the reader goroutine
	// above) until the battle server happens to write into a dead socket.
	_ = wsConn.SetReadDeadline(time.Now().Add(s.pongWait))
	wsConn.SetPongHandler(func() {
		_ = wsConn.SetReadDeadline(time.Now().Add(s.pongWait))
	})
	go func() {
		ticker := time.NewTicker(s.pingPeriod)
		defer ticker.Stop()
		for range ticker.C {
			_ = wsConn.SetWriteDeadline(time.Now().Add(s.writeWait))
			if err := wsConn.WritePing(nil); err != nil {
				stop()
				return
			}
		}
	}()

	for {
		opcode, payload, err := wsConn.ReadMessage()
		if err != nil {
			return
		}
		_ = wsConn.SetReadDeadline(time.Now().Add(s.pongWait))
		switch opcode {
		case OpBinary:
			if _, err := udpConn.Write(payload); err != nil {
				return
			}
		case OpText:
			_ = wsConn.WriteText(`{"type":100,"payload":{"code":"unsupported_frame","message":"text frames are not part of the battle relay"}}`)
		case OpClose:
			return
		}
	}
}

func profileFromSnapshot(snapshot *core.BootstrapSnapshot) LobbyPlayerProfile {
	ratingCode := snapshot.Certification.RatingCode
	return LobbyPlayerProfile{
		UserID:      snapshot.UserID,
		DisplayName: snapshot.DisplayName,
		CharacterID: "balanced",
		Level:       1,
		RatingCode:  ratingCode,
		Loadout: LoadoutRef{
			UserID:      snapshot.UserID,
			CharacterID: "balanced",
			StageID:     "starlit_lanes",
			RatingCode:  ratingCode,
		},
	}
}

func modeParamsFromLoadout(loadout LoadoutRef, extra map[string]string) map[string]any {
	params := map[string]any{}
	for key, value := range extra {
		params[key] = value
	}
	if stageID := strings.TrimSpace(loadout.StageID); stageID != "" {
		params["stage_id"] = stageID
	}
	if characterID := strings.TrimSpace(loadout.CharacterID); characterID != "" {
		params["character_id"] = characterID
	}
	if ratingCode := strings.TrimSpace(loadout.RatingCode); ratingCode != "" {
		params["rating_code"] = ratingCode
	}
	return params
}

func tokenOrRandom(token string) string {
	if strings.TrimSpace(token) != "" {
		return token
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "ws-" + time.Now().UTC().Format("20060102150405")
	}
	return "ws-" + hex.EncodeToString(buf)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// Close stops every tracked battle server process and closes client
// connections. It is used for graceful shutdown.
func (s *Server) Close() {
	s.mu.Lock()
	clients := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		_ = c.conn.WriteClose(1001, "server shutdown")
	}
	if s.spawner != nil {
		s.spawner.KillAll()
	}
}
