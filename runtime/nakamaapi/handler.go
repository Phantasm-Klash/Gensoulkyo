package nakamaapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/security"
)

const (
	CodeInvalidRequest           = "invalid_request"
	CodeBusinessEnvelopeRequired = "business_envelope_required"
	CodeServiceOriginRequired    = "service_origin_required"
)

type Handler struct {
	service         *core.Service
	envelopeGuard   *security.BusinessEnvelopeGuard
	requireEnvelope bool
}

type Option func(*Handler)

type RPCRequest struct {
	ID           string
	SessionID    string
	UserID       string
	DisplayName  string
	Service      bool
	Payload      map[string]any
	PayloadError string
}

type WSSMessage struct {
	Name        string
	SessionID   string
	UserID      string
	DisplayName string
	Payload     map[string]any
}

type Response struct {
	OK        bool   `json:"ok"`
	Status    int    `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
	Payload   any    `json:"payload,omitempty"`
}

func New(service *core.Service, options ...Option) *Handler {
	handler := &Handler{
		service:         service,
		envelopeGuard:   security.NewBusinessEnvelopeGuard(),
		requireEnvelope: true,
	}
	for _, option := range options {
		option(handler)
	}
	if handler.service == nil {
		handler.service = core.NewService(core.Config{})
	}
	if handler.envelopeGuard == nil {
		handler.envelopeGuard = security.NewBusinessEnvelopeGuard()
	}
	return handler
}

func WithBusinessEnvelopeGuard(guard *security.BusinessEnvelopeGuard) Option {
	return func(handler *Handler) {
		if guard != nil {
			handler.envelopeGuard = guard
		}
	}
}

func WithRequiredBusinessEnvelope(required bool) Option {
	return func(handler *Handler) {
		handler.requireEnvelope = required
	}
}

func (handler *Handler) HandleRPC(request RPCRequest) Response {
	rpcID := normalizeName(request.ID)
	if rpcID == "" {
		return errorResponse(http.StatusBadRequest, CodeInvalidRequest, "rpc id is required")
	}
	if response := payloadErrorResponse(request.PayloadError); !response.OK {
		return response
	}
	if rpcSkipsEnvelope(rpcID) {
		if response := handler.ensureExternalRPCSession(request); !response.OK {
			return response
		}
	} else if rpcRequiresServiceOrigin(rpcID) {
		if !request.Service {
			handler.auditRejectedServiceOnlyRPC(request, rpcID)
			return errorResponse(http.StatusForbidden, CodeServiceOriginRequired, fmt.Sprintf("rpc %q requires service-to-service origin", request.ID))
		}
		if strings.TrimSpace(request.SessionID) != "" || strings.TrimSpace(request.UserID) != "" {
			handler.auditRejectedServiceOnlyRPC(request, rpcID)
			return errorResponse(http.StatusForbidden, CodeServiceOriginRequired, fmt.Sprintf("rpc %q service-origin callback must not include player session context", request.ID))
		}
		if serviceOriginPayloadHasBusinessEnvelope(request.Payload) {
			handler.auditRejectedServiceOnlyRPC(request, rpcID)
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, fmt.Sprintf("rpc %q service-origin callback must not include business envelope payload", request.ID))
		}
	} else {
		if response := handler.ensureExternalRPCSession(request); !response.OK {
			return response
		}
		if response := handler.validateRPCEnvelope(request); !response.OK {
			return response
		}
	}
	body := requestBody(request.Payload)
	switch rpcID {
	case "auth.anonymous":
		var req core.AnonymousLoginRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.LoginAnonymous(req) })
	case "bootstrap":
		return handler.call(func() (any, error) { return handler.service.Bootstrap(request.SessionID) })
	case "inventory.get", "inventory":
		return handler.call(func() (any, error) { return handler.service.Inventory(request.SessionID) })
	case "cards.upgrade":
		var req core.CardUpgradeRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.UpgradeCard(request.SessionID, req) })
	case "decks.list", "decks":
		return handler.call(func() (any, error) { return handler.service.Decks(request.SessionID) })
	case "decks.save":
		var req core.SaveDeckRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.SaveDeck(request.SessionID, req) })
	case "chests.list", "chests":
		return handler.call(func() (any, error) { return handler.service.Chests(request.SessionID) })
	case "chests.open":
		var req core.ChestOpenRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.OpenChest(request.SessionID, req) })
	case "presence.heartbeat":
		var req core.PresenceHeartbeatRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.Heartbeat(request.SessionID, req) })
	case "business.event", "business.event.settlement":
		if forbidden := core.ForbiddenClientField(body); forbidden != "" {
			return errorResponse(http.StatusForbidden, "forbidden_field", fmt.Sprintf("client cannot submit %s", forbidden))
		}
		req, response := businessEventRequest(body, rpcID)
		if !response.OK {
			return response
		}
		return handler.call(func() (any, error) { return handler.service.BusinessEvent(request.SessionID, req) })
	case "business.contract":
		return handler.call(func() (any, error) { return handler.service.BusinessContract(request.SessionID) })
	case "matchmaking.join":
		var req core.JoinQueueRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.JoinQueue(request.SessionID, req) })
	case "matchmaking.ticket":
		ticketID := fieldString(body, "ticket_id", "ticketId")
		return handler.call(func() (any, error) { return handler.service.QueueTicket(request.SessionID, ticketID) })
	case "matchmaking.cancel":
		ticketID := fieldString(body, "ticket_id", "ticketId")
		return handler.call(func() (any, error) { return handler.service.CancelTicket(request.SessionID, ticketID) })
	case "rooms.create":
		var req core.CreateRoomRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.CreateRoom(request.SessionID, req) })
	case "rooms.list", "rooms":
		return handler.call(func() (any, error) { return handler.service.ListRooms(request.SessionID) })
	case "rooms.get":
		roomCode := fieldString(body, "room_code", "roomCode")
		return handler.call(func() (any, error) { return handler.service.Room(request.SessionID, roomCode) })
	case "rooms.rules":
		roomCode := fieldString(body, "room_code", "roomCode")
		return handler.call(func() (any, error) { return handler.service.RoomRules(request.SessionID, roomCode) })
	case "rooms.join":
		roomCode := fieldString(body, "room_code", "roomCode")
		var req core.JoinRoomRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.JoinRoom(request.SessionID, roomCode, req) })
	case "rooms.leave":
		roomCode := fieldString(body, "room_code", "roomCode")
		return handler.call(func() (any, error) { return handler.service.LeaveRoom(request.SessionID, roomCode) })
	case "rooms.message", "rooms.chat", "rooms.announcement":
		if forbidden := core.ForbiddenClientField(body); forbidden != "" {
			return errorResponse(http.StatusForbidden, "forbidden_field", fmt.Sprintf("client cannot submit %s", forbidden))
		}
		req, response := lobbyMessageRequest(body, rpcID)
		if !response.OK {
			return response
		}
		return handler.call(func() (any, error) { return handler.service.LobbyMessage(request.SessionID, req) })
	case "match.ready", "matches.ready":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.ReadyMatch(request.SessionID, matchID) })
	case "match.disconnect", "matches.disconnect":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.DisconnectMatch(request.SessionID, matchID) })
	case "match.reconnect", "matches.reconnect":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.ReconnectMatch(request.SessionID, matchID) })
	case "activity.claim":
		return handler.call(func() (any, error) { return handler.service.ClaimActivity(request.SessionID, body) })
	case "battle.servers.register":
		var req core.RegisterBattleServerRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.RegisterBattleServer(req) })
	case "battle.servers.heartbeat":
		var req core.BattleServerHeartbeatRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.BattleServerHeartbeat(req) })
	case "battle.servers.offline":
		var req core.BattleServerOfflineRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.BattleServerOffline(req) })
	case "battle.servers":
		return successResponse(handler.service.BattleServers())
	case "business.envelope.audit.status":
		return successResponse(handler.EnvelopeSnapshot())
	case "battle.audit.status":
		return successResponse(handler.service.BattleLifecycleAuditStatus())
	case "lobby.audit.status":
		return successResponse(handler.service.LobbyLifecycleAuditStatus())
	case "battle.allocation":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.BattleAllocation(request.SessionID, matchID) })
	case "battle.ticket":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.BattleTicket(request.SessionID, matchID) })
	case "battle.ticket.consume":
		var req core.BattleTicketConsumeRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.ConsumeBattleTicket(req) })
	case "replay.get", "replay":
		replayID := fieldString(body, "replay_id", "replayId")
		return handler.call(func() (any, error) { return handler.service.Replay(request.SessionID, replayID) })
	case "battle.result.submit":
		var req core.BattleResultSubmitRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.SubmitBattleResult(req) })
	default:
		return errorResponse(http.StatusNotFound, "not_found", fmt.Sprintf("rpc %q is not registered", request.ID))
	}
}

func (handler *Handler) HandleWSSMessage(message WSSMessage) Response {
	name := normalizeName(message.Name)
	if name == "" {
		return errorResponse(http.StatusBadRequest, CodeInvalidRequest, "message name is required")
	}
	if rpcRequiresServiceOrigin(name) {
		handler.auditRejectedServiceOnlyWSS(message, name)
		return errorResponse(http.StatusForbidden, CodeServiceOriginRequired, fmt.Sprintf("wss message %q is service-origin RPC only", message.Name))
	}
	if response := handler.ensureExternalWSSSession(message); !response.OK {
		return response
	}
	if response := handler.validateWSSEnvelope(message); !response.OK {
		return response
	}
	body := requestBody(message.Payload)
	switch name {
	case "presence.heartbeat":
		var req core.PresenceHeartbeatRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.Heartbeat(message.SessionID, req) })
	case "business.event", "business.event.settlement":
		if forbidden := core.ForbiddenClientField(body); forbidden != "" {
			return errorResponse(http.StatusForbidden, "forbidden_field", fmt.Sprintf("client cannot submit %s", forbidden))
		}
		req, response := businessEventRequest(body, name)
		if !response.OK {
			return response
		}
		return handler.call(func() (any, error) { return handler.service.BusinessEvent(message.SessionID, req) })
	case "business.contract":
		return handler.call(func() (any, error) { return handler.service.BusinessContract(message.SessionID) })
	case "matchmaking.join":
		var req core.JoinQueueRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.JoinQueue(message.SessionID, req) })
	case "matchmaking.ticket":
		ticketID := fieldString(body, "ticket_id", "ticketId")
		return handler.call(func() (any, error) { return handler.service.QueueTicket(message.SessionID, ticketID) })
	case "matchmaking.cancel":
		ticketID := fieldString(body, "ticket_id", "ticketId")
		return handler.call(func() (any, error) { return handler.service.CancelTicket(message.SessionID, ticketID) })
	case "rooms.create":
		var req core.CreateRoomRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.CreateRoom(message.SessionID, req) })
	case "rooms.list", "rooms":
		return handler.call(func() (any, error) { return handler.service.ListRooms(message.SessionID) })
	case "rooms.get":
		roomCode := fieldString(body, "room_code", "roomCode")
		return handler.call(func() (any, error) { return handler.service.Room(message.SessionID, roomCode) })
	case "rooms.rules":
		roomCode := fieldString(body, "room_code", "roomCode")
		return handler.call(func() (any, error) { return handler.service.RoomRules(message.SessionID, roomCode) })
	case "rooms.join":
		roomCode := fieldString(body, "room_code", "roomCode")
		var req core.JoinRoomRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.JoinRoom(message.SessionID, roomCode, req) })
	case "rooms.leave":
		roomCode := fieldString(body, "room_code", "roomCode")
		return handler.call(func() (any, error) { return handler.service.LeaveRoom(message.SessionID, roomCode) })
	case "rooms.message", "rooms.chat", "rooms.announcement":
		if forbidden := core.ForbiddenClientField(body); forbidden != "" {
			return errorResponse(http.StatusForbidden, "forbidden_field", fmt.Sprintf("client cannot submit %s", forbidden))
		}
		req, response := lobbyMessageRequest(body, name)
		if !response.OK {
			return response
		}
		return handler.call(func() (any, error) { return handler.service.LobbyMessage(message.SessionID, req) })
	case "match.ready", "matches.ready":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.ReadyMatch(message.SessionID, matchID) })
	case "match.disconnect", "matches.disconnect":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.DisconnectMatch(message.SessionID, matchID) })
	case "match.reconnect", "matches.reconnect":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.ReconnectMatch(message.SessionID, matchID) })
	case "battle.servers":
		return successResponse(handler.service.BattleServers())
	case "battle.allocation":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.BattleAllocation(message.SessionID, matchID) })
	case "battle.ticket":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.BattleTicket(message.SessionID, matchID) })
	case "replay.get", "replay":
		replayID := fieldString(body, "replay_id", "replayId")
		return handler.call(func() (any, error) { return handler.service.Replay(message.SessionID, replayID) })
	case "business.envelope.audit.status":
		return successResponse(handler.EnvelopeSnapshot())
	case "battle.audit.status":
		return successResponse(handler.service.BattleLifecycleAuditStatus())
	case "lobby.audit.status":
		return successResponse(handler.service.LobbyLifecycleAuditStatus())
	default:
		return errorResponse(http.StatusNotFound, "not_found", fmt.Sprintf("wss message %q is not registered", message.Name))
	}
}

func (handler *Handler) EnvelopeSnapshot() security.BusinessEnvelopeGuardSnapshot {
	return handler.envelopeGuard.Snapshot()
}

func (handler *Handler) validateRPCEnvelope(request RPCRequest) Response {
	envelope, ok := security.BusinessEnvelopeRequestFromNakamaRPCPayload(request.SessionID, request.ID, request.UserID, request.Payload)
	if !ok {
		if handler.requireEnvelope {
			handler.auditMissingEnvelope(request.SessionID, request.UserID, security.BusinessEnvelopeTransportNakamaRPC, endpointName("rpc", request.ID))
			return errorResponse(http.StatusBadRequest, CodeBusinessEnvelopeRequired, "business envelope is required for Nakama RPC")
		}
		return successResponse(nil)
	}
	result := security.ValidateBusinessEnvelopeRequest(handler.envelopeGuard, envelope)
	if result.OK {
		return successResponse(nil)
	}
	return envelopeErrorResponse(result)
}

func (handler *Handler) ensureExternalRPCSession(request RPCRequest) Response {
	return handler.ensureExternalSession(request.UserID, request.SessionID, request.DisplayName)
}

func (handler *Handler) ensureExternalWSSSession(message WSSMessage) Response {
	return handler.ensureExternalSession(message.UserID, message.SessionID, message.DisplayName)
}

func (handler *Handler) ensureExternalSession(userID string, sessionID string, displayName string) Response {
	userID = strings.TrimSpace(userID)
	sessionID = strings.TrimSpace(sessionID)
	if userID == "" || sessionID == "" {
		return successResponse(nil)
	}
	_, err := handler.service.LoginExternal(core.ExternalSessionRequest{
		UserID:       userID,
		SessionToken: sessionID,
		DisplayName:  displayName,
		Provider:     "nakama",
	})
	if err != nil {
		return coreErrorResponse(err)
	}
	return successResponse(nil)
}

func (handler *Handler) validateWSSEnvelope(message WSSMessage) Response {
	envelope, ok := security.BusinessEnvelopeRequestFromNakamaWSSPayload(message.SessionID, message.Name, message.UserID, message.Payload)
	if !ok {
		if handler.requireEnvelope {
			handler.auditMissingEnvelope(message.SessionID, message.UserID, security.BusinessEnvelopeTransportNakamaWSS, endpointName("wss", message.Name))
			return errorResponse(http.StatusBadRequest, CodeBusinessEnvelopeRequired, "business envelope is required for Nakama WSS")
		}
		return successResponse(nil)
	}
	result := security.ValidateBusinessEnvelopeRequest(handler.envelopeGuard, envelope)
	if result.OK {
		return successResponse(nil)
	}
	return envelopeErrorResponse(result)
}

func (handler *Handler) auditMissingEnvelope(sessionID string, userID string, transport string, endpoint string) {
	_ = security.ValidateBusinessEnvelopeRequest(handler.envelopeGuard, security.BusinessEnvelopeRequest{
		SessionID: strings.TrimSpace(sessionID),
		Transport: transport,
		Endpoint:  endpoint,
		UserID:    strings.TrimSpace(userID),
		Op:        endpoint,
	})
}

func (handler *Handler) auditRejectedServiceOnlyWSS(message WSSMessage, name string) {
	_ = security.ValidateBusinessEnvelopeRequest(handler.envelopeGuard, security.BusinessEnvelopeRequest{
		SessionID: "blocked-service-wss:" + name,
		Transport: security.BusinessEnvelopeTransportNakamaWSS,
		Endpoint:  endpointName("wss", name),
		UserID:    strings.TrimSpace(message.UserID),
		Op:        endpointName("wss", name),
	})
}

func (handler *Handler) auditRejectedServiceOnlyRPC(request RPCRequest, rpcID string) {
	_ = security.ValidateBusinessEnvelopeRequest(handler.envelopeGuard, security.BusinessEnvelopeRequest{
		SessionID: "blocked-service-rpc:" + rpcID,
		Transport: security.BusinessEnvelopeTransportNakamaRPC,
		Endpoint:  endpointName("rpc", rpcID),
		UserID:    strings.TrimSpace(request.UserID),
		Op:        endpointName("rpc", rpcID),
	})
}

func (handler *Handler) call(fn func() (any, error)) Response {
	payload, err := fn()
	if err != nil {
		return coreErrorResponse(err)
	}
	return successResponse(payload)
}

func successResponse(payload any) Response {
	return Response{OK: true, Status: http.StatusOK, Payload: payload}
}

func errorResponse(status int, code string, message string) Response {
	return Response{OK: false, Status: status, ErrorCode: code, Message: message}
}

func payloadErrorResponse(message string) Response {
	message = strings.TrimSpace(message)
	if message == "" {
		return successResponse(nil)
	}
	return errorResponse(http.StatusBadRequest, CodeInvalidRequest, message)
}

func envelopeErrorResponse(result security.BusinessEnvelopeRequestResult) Response {
	message := result.Message
	if message == "" {
		message = result.Reason
	}
	return errorResponse(result.Status, result.Code, message)
}

func coreErrorResponse(err error) Response {
	status := http.StatusBadRequest
	code := core.ErrorCode(err)
	switch code {
	case "unauthorized":
		status = http.StatusUnauthorized
	case "not_found":
		status = http.StatusNotFound
	case "match_state_invalid", "reconnect_expired", "room_unavailable":
		status = http.StatusConflict
	case "forbidden_field":
		status = http.StatusForbidden
	case "battle_server_unavailable":
		status = http.StatusServiceUnavailable
	}
	return errorResponse(status, code, err.Error())
}

func rpcSkipsEnvelope(rpcID string) bool {
	return rpcID == "auth.anonymous"
}

func rpcRequiresServiceOrigin(rpcID string) bool {
	return core.IsServiceCallbackOperation(rpcID)
}

func normalizeName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", ".")
	name = strings.ReplaceAll(name, ":", ".")
	name = strings.Trim(name, ".")
	return strings.ToLower(name)
}

func endpointName(prefix string, name string) string {
	normalized := normalizeName(name)
	if normalized == "" {
		return prefix
	}
	return prefix + "." + normalized
}

func requestBody(payload map[string]any) map[string]any {
	for _, key := range []string{"body", "request", "data"} {
		if body, ok := mapValue(payload[key]); ok {
			return body
		}
	}
	body := map[string]any{}
	for key, value := range payload {
		if key == security.BusinessEnvelopePayloadKey {
			continue
		}
		body[key] = value
	}
	return body
}

func serviceOriginPayloadHasBusinessEnvelope(payload map[string]any) bool {
	if _, ok := payload[security.BusinessEnvelopePayloadKey]; ok {
		return true
	}
	if serviceOriginPayloadContainsEnvelopeField(payload) {
		return true
	}
	for _, key := range []string{"body", "request", "data"} {
		if body, ok := mapValue(payload[key]); ok {
			if _, nested := body[security.BusinessEnvelopePayloadKey]; nested {
				return true
			}
			if serviceOriginPayloadContainsEnvelopeField(body) {
				return true
			}
		}
	}
	return false
}

func serviceOriginPayloadContainsEnvelopeField(payload map[string]any) bool {
	for _, key := range security.BusinessEnvelopeServiceCallbackFieldAliases() {
		if _, ok := payload[key]; ok {
			return true
		}
	}
	if value, ok := payload["version"]; ok {
		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(typed) != ""
		case []byte:
			return strings.TrimSpace(string(typed)) != ""
		}
	}
	return false
}

func decodeBody(body map[string]any, target any) error {
	if target == nil {
		return errors.New("decode target is nil")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func mapValue(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		result := make(map[string]any, len(typed))
		for key, field := range typed {
			result[key] = field
		}
		return result, true
	default:
		return nil, false
	}
}

func fieldString(body map[string]any, keys ...string) string {
	for _, key := range keys {
		switch typed := body[key].(type) {
		case string:
			if trimmed := strings.TrimSpace(typed); trimmed != "" {
				return trimmed
			}
		case []byte:
			if trimmed := strings.TrimSpace(string(typed)); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func lobbyMessageRequest(body map[string]any, name string) (core.LobbyMessageRequest, Response) {
	var req core.LobbyMessageRequest
	if err := decodeBody(body, &req); err != nil {
		return req, errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
	}
	normalized := normalizeName(name)
	switch normalized {
	case "rooms.chat":
		req.Kind = "chat"
	case "rooms.announcement":
		req.Kind = "announcement"
	}
	return req, successResponse(nil)
}

func businessEventRequest(body map[string]any, name string) (core.BusinessEventRequest, Response) {
	if field := firstUnexpectedBusinessEventRequestField(body); field != "" {
		return core.BusinessEventRequest{}, errorResponse(http.StatusBadRequest, CodeInvalidRequest, fmt.Sprintf("business event lookup request cannot include %s", field))
	}
	var req core.BusinessEventRequest
	if err := decodeBody(body, &req); err != nil {
		return req, errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
	}
	if normalizeName(name) == "business.event.settlement" {
		if kind := strings.TrimSpace(req.Kind); kind != "" && kind != "settlement" {
			return req, errorResponse(http.StatusBadRequest, CodeInvalidRequest, "business.event.settlement requires settlement kind")
		}
		req.Kind = "settlement"
	}
	return req, successResponse(nil)
}

func firstUnexpectedBusinessEventRequestField(body map[string]any) string {
	for key := range body {
		switch normalizeName(key) {
		case "kind", "ticket_id", "ticket.id", "ticketid", "room_code", "room.code", "roomcode", "match_id", "match.id", "matchid":
			continue
		default:
			return key
		}
	}
	return ""
}
