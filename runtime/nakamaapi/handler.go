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
)

type Handler struct {
	service         *core.Service
	envelopeGuard   *security.BusinessEnvelopeGuard
	requireEnvelope bool
}

type Option func(*Handler)

type RPCRequest struct {
	ID          string
	SessionID   string
	UserID      string
	DisplayName string
	Payload     map[string]any
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
	if rpcSkipsEnvelope(rpcID) {
		if response := handler.ensureExternalRPCSession(request); !response.OK {
			return response
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
	case "activity.claim":
		return handler.call(func() (any, error) { return handler.service.ClaimActivity(request.SessionID, body) })
	case "battle.servers":
		return successResponse(handler.service.BattleServers())
	case "battle.allocation":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.BattleAllocation(request.SessionID, matchID) })
	case "battle.ticket":
		matchID := fieldString(body, "match_id", "matchId")
		return handler.call(func() (any, error) { return handler.service.BattleTicket(request.SessionID, matchID) })
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
	case "matchmaking.join":
		var req core.JoinQueueRequest
		if err := decodeBody(body, &req); err != nil {
			return errorResponse(http.StatusBadRequest, CodeInvalidRequest, err.Error())
		}
		return handler.call(func() (any, error) { return handler.service.JoinQueue(message.SessionID, req) })
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
