package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/security"
)

const (
	headerServiceOrigin       = "X-PhK-Service-Origin"
	headerBattleCallback      = "X-PhK-Battle-Callback"
	serviceOriginContextKey   = core.ServiceCallbackOriginKey
	serviceCallbackContextKey = core.ServiceCallbackFlagKey
)

type Handler struct {
	service       *core.Service
	envelopeGuard *security.BusinessEnvelopeGuard
}

type Option func(*Handler)

func New(service *core.Service) http.Handler {
	handler := &Handler{
		service:       service,
		envelopeGuard: security.NewBusinessEnvelopeGuard(),
	}
	return handler
}

func NewWithOptions(service *core.Service, options ...Option) http.Handler {
	handler := &Handler{
		service:       service,
		envelopeGuard: security.NewBusinessEnvelopeGuard(),
	}
	for _, option := range options {
		option(handler)
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

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.Trim(r.URL.Path, "/")
	segments := []string{}
	if path != "" {
		segments = strings.Split(path, "/")
	}
	if r.Method == http.MethodGet && path == "health" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "server_version": core.ServerVersion})
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "auth" && segments[2] == "anonymous" && r.Method == http.MethodPost {
		h.loginAnonymous(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "security" && segments[2] == "business-envelope" && r.Method == http.MethodGet {
		h.businessEnvelopeStatus(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "security" && segments[2] == "service-callback" && r.Method == http.MethodGet {
		h.serviceCallbackStatus(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "security" && segments[2] == "battle-audit" && r.Method == http.MethodGet {
		h.battleAuditStatus(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "security" && segments[2] == "lobby-audit" && r.Method == http.MethodGet {
		h.lobbyAuditStatus(w, r)
		return
	}
	if routeUsesBusinessEnvelope(r.Method, segments) {
		if status, code, message := h.validateBusinessEnvelopeHeaders(r); code != "" {
			writeJSON(w, status, map[string]any{"ok": false, "error_code": code, "message": message})
			return
		}
	}
	if len(segments) == 2 && segments[0] == "v1" && segments[1] == "bootstrap" && r.Method == http.MethodGet {
		h.bootstrap(w, r)
		return
	}
	if len(segments) == 2 && segments[0] == "v1" && segments[1] == "inventory" && r.Method == http.MethodGet {
		h.inventory(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "cards" && segments[2] == "upgrade" && r.Method == http.MethodPost {
		h.upgradeCard(w, r)
		return
	}
	if len(segments) == 2 && segments[0] == "v1" && segments[1] == "decks" && r.Method == http.MethodGet {
		h.decks(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "decks" && segments[2] == "save" && r.Method == http.MethodPost {
		h.saveDeck(w, r)
		return
	}
	if len(segments) == 2 && segments[0] == "v1" && segments[1] == "chests" && r.Method == http.MethodGet {
		h.chests(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "chests" && segments[2] == "open" && r.Method == http.MethodPost {
		h.openChest(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "presence" && segments[2] == "heartbeat" && r.Method == http.MethodPost {
		h.heartbeat(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "business" && segments[2] == "events" && r.Method == http.MethodPost {
		h.businessEvent(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "matchmaking" && segments[2] == "join" && r.Method == http.MethodPost {
		h.joinQueue(w, r)
		return
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[1] == "matchmaking" && segments[2] == "tickets" && r.Method == http.MethodGet {
		h.queueTicket(w, r, segments[3])
		return
	}
	if len(segments) == 5 && segments[0] == "v1" && segments[1] == "matchmaking" && segments[2] == "tickets" && segments[4] == "cancel" && r.Method == http.MethodPost {
		h.cancelTicket(w, r, segments[3])
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "rooms" && segments[2] == "create" && r.Method == http.MethodPost {
		h.createRoom(w, r)
		return
	}
	if len(segments) == 2 && segments[0] == "v1" && segments[1] == "rooms" && r.Method == http.MethodGet {
		h.listRooms(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "rooms" && r.Method == http.MethodGet {
		h.room(w, r, segments[2])
		return
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[1] == "rooms" && segments[3] == "rules" && r.Method == http.MethodGet {
		h.roomRules(w, r, segments[2])
		return
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[1] == "rooms" && segments[3] == "join" && r.Method == http.MethodPost {
		h.joinRoom(w, r, segments[2])
		return
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[1] == "rooms" && segments[3] == "leave" && r.Method == http.MethodPost {
		h.leaveRoom(w, r, segments[2])
		return
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[1] == "rooms" && segments[3] == "messages" && r.Method == http.MethodPost {
		h.lobbyMessage(w, r, segments[2])
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "activity" && segments[2] == "claim" && r.Method == http.MethodPost {
		h.claimActivity(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "battle" && segments[2] == "servers" && r.Method == http.MethodGet {
		h.battleServers(w, r)
		return
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[1] == "battle" && segments[2] == "servers" && segments[3] == "register" && r.Method == http.MethodPost {
		h.registerBattleServer(w, r)
		return
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[1] == "battle" && segments[2] == "servers" && segments[3] == "heartbeat" && r.Method == http.MethodPost {
		h.battleServerHeartbeat(w, r)
		return
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[1] == "battle" && segments[2] == "servers" && segments[3] == "offline" && r.Method == http.MethodPost {
		h.battleServerOffline(w, r)
		return
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[1] == "battle" && segments[2] == "tickets" && segments[3] == "consume" && r.Method == http.MethodPost {
		h.consumeBattleTicket(w, r)
		return
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[1] == "battle" && segments[2] == "results" && segments[3] == "submit" && r.Method == http.MethodPost {
		h.submitBattleResult(w, r)
		return
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "replays" && r.Method == http.MethodGet {
		h.replay(w, r, segments[2])
		return
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[1] == "matches" {
		matchID := segments[2]
		switch segments[3] {
		case "ready":
			if r.Method == http.MethodPost {
				h.ready(w, r, matchID)
				return
			}
		case "input":
			if r.Method == http.MethodPost {
				h.input(w, r, matchID)
				return
			}
		case "snapshot":
			if r.Method == http.MethodGet {
				h.snapshot(w, r, matchID)
				return
			}
		case "events":
			if r.Method == http.MethodGet {
				h.events(w, r, matchID)
				return
			}
		case "mode-action":
			if r.Method == http.MethodPost {
				h.modeAction(w, r, matchID)
				return
			}
		case "rematch":
			if r.Method == http.MethodPost {
				h.rematch(w, r, matchID)
				return
			}
		case "disconnect":
			if r.Method == http.MethodPost {
				h.disconnect(w, r, matchID)
				return
			}
		case "reconnect":
			if r.Method == http.MethodPost {
				h.reconnect(w, r, matchID)
				return
			}
		case "settle":
			if r.Method == http.MethodPost {
				h.settle(w, r, matchID)
				return
			}
		case "battle-allocation":
			if r.Method == http.MethodGet {
				h.battleAllocation(w, r, matchID)
				return
			}
		case "battle-ticket":
			if r.Method == http.MethodPost {
				h.battleTicket(w, r, matchID)
				return
			}
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error_code": "not_found", "message": "route not found"})
}

func (h *Handler) loginAnonymous(w http.ResponseWriter, r *http.Request) {
	var req core.AnonymousLoginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, err := h.service.LoginAnonymous(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) bootstrap(w http.ResponseWriter, r *http.Request) {
	resp, err := h.service.Bootstrap(sessionToken(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) inventory(w http.ResponseWriter, r *http.Request) {
	resp, err := h.service.Inventory(sessionToken(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) upgradeCard(w http.ResponseWriter, r *http.Request) {
	var req core.CardUpgradeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, err := h.service.UpgradeCard(sessionToken(r), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) decks(w http.ResponseWriter, r *http.Request) {
	resp, err := h.service.Decks(sessionToken(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) saveDeck(w http.ResponseWriter, r *http.Request) {
	var req core.SaveDeckRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, err := h.service.SaveDeck(sessionToken(r), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) chests(w http.ResponseWriter, r *http.Request) {
	resp, err := h.service.Chests(sessionToken(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) openChest(w http.ResponseWriter, r *http.Request) {
	var req core.ChestOpenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, err := h.service.OpenChest(sessionToken(r), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) heartbeat(w http.ResponseWriter, r *http.Request) {
	var req core.PresenceHeartbeatRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, err := h.service.Heartbeat(sessionToken(r), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) businessEvent(w http.ResponseWriter, r *http.Request) {
	raw := map[string]any{}
	if !decodeJSON(w, r, &raw) {
		return
	}
	if forbidden := core.ForbiddenClientField(raw); forbidden != "" {
		writeError(w, core.NewForbiddenClientFieldError(forbidden))
		return
	}
	if field := firstUnexpectedBusinessEventRequestField(raw); field != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":         false,
			"error_code": "invalid_request",
			"message":    fmt.Sprintf("business event lookup request cannot include %s", field),
		})
		return
	}
	var req core.BusinessEventRequest
	encoded, err := json.Marshal(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error_code": "invalid_json", "message": err.Error()})
		return
	}
	if err := json.Unmarshal(encoded, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error_code": "invalid_json", "message": err.Error()})
		return
	}
	resp, err := h.service.BusinessEvent(sessionToken(r), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func firstUnexpectedBusinessEventRequestField(body map[string]any) string {
	for key := range body {
		switch normalizeBusinessEventFieldName(key) {
		case "kind", "ticket_id", "ticket.id", "ticketid", "room_code", "room.code", "roomcode", "match_id", "match.id", "matchid":
			continue
		default:
			return key
		}
	}
	return ""
}

func normalizeBusinessEventFieldName(name string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(name, "-", "_")))
}

func (h *Handler) joinQueue(w http.ResponseWriter, r *http.Request) {
	var req core.JoinQueueRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, err := h.service.JoinQueue(sessionToken(r), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) queueTicket(w http.ResponseWriter, r *http.Request, ticketID string) {
	resp, err := h.service.QueueTicket(sessionToken(r), ticketID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) cancelTicket(w http.ResponseWriter, r *http.Request, ticketID string) {
	resp, err := h.service.CancelTicket(sessionToken(r), ticketID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) createRoom(w http.ResponseWriter, r *http.Request) {
	var req core.CreateRoomRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, err := h.service.CreateRoom(sessionToken(r), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) listRooms(w http.ResponseWriter, r *http.Request) {
	resp, err := h.service.ListRooms(sessionToken(r))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) room(w http.ResponseWriter, r *http.Request, roomCode string) {
	resp, err := h.service.Room(sessionToken(r), roomCode)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) roomRules(w http.ResponseWriter, r *http.Request, roomCode string) {
	resp, err := h.service.RoomRules(sessionToken(r), roomCode)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) joinRoom(w http.ResponseWriter, r *http.Request, roomCode string) {
	var req core.JoinRoomRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	resp, err := h.service.JoinRoom(sessionToken(r), roomCode, req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) leaveRoom(w http.ResponseWriter, r *http.Request, roomCode string) {
	resp, err := h.service.LeaveRoom(sessionToken(r), roomCode)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) lobbyMessage(w http.ResponseWriter, r *http.Request, roomCode string) {
	raw := map[string]any{}
	if !decodeJSON(w, r, &raw) {
		return
	}
	raw["room_code"] = roomCode
	var req core.LobbyMessageRequest
	encoded, err := json.Marshal(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error_code": "invalid_json", "message": err.Error()})
		return
	}
	if err := json.Unmarshal(encoded, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error_code": "invalid_json", "message": err.Error()})
		return
	}
	resp, err := h.service.LobbyMessage(sessionToken(r), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request, matchID string) {
	resp, err := h.service.ReadyMatch(sessionToken(r), matchID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) input(w http.ResponseWriter, r *http.Request, matchID string) {
	raw := map[string]any{}
	if !decodeJSON(w, r, &raw) {
		return
	}
	resp, err := h.service.SubmitInput(sessionToken(r), matchID, raw)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) snapshot(w http.ResponseWriter, r *http.Request, matchID string) {
	resp, err := h.service.Snapshot(sessionToken(r), matchID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) events(w http.ResponseWriter, r *http.Request, matchID string) {
	after := queryInt(r, "after", 0)
	limit := queryInt(r, "limit", 64)
	resp, err := h.service.MatchEvents(sessionToken(r), matchID, after, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) modeAction(w http.ResponseWriter, r *http.Request, matchID string) {
	raw := map[string]any{}
	if !decodeJSON(w, r, &raw) {
		return
	}
	resp, err := h.service.SubmitModeAction(sessionToken(r), matchID, raw)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) rematch(w http.ResponseWriter, r *http.Request, matchID string) {
	resp, err := h.service.RequestRematch(sessionToken(r), matchID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) disconnect(w http.ResponseWriter, r *http.Request, matchID string) {
	resp, err := h.service.DisconnectMatch(sessionToken(r), matchID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) reconnect(w http.ResponseWriter, r *http.Request, matchID string) {
	resp, err := h.service.ReconnectMatch(sessionToken(r), matchID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) settle(w http.ResponseWriter, r *http.Request, matchID string) {
	raw := map[string]any{}
	if r.Body != nil && r.ContentLength != 0 {
		if !decodeJSON(w, r, &raw) {
			return
		}
	}
	resp, err := h.service.SettleMatch(sessionToken(r), matchID, raw)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) replay(w http.ResponseWriter, r *http.Request, replayID string) {
	resp, err := h.service.Replay(sessionToken(r), replayID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) claimActivity(w http.ResponseWriter, r *http.Request) {
	raw := map[string]any{}
	if !decodeJSON(w, r, &raw) {
		return
	}
	resp, err := h.service.ClaimActivity(sessionToken(r), raw)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) battleServers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.service.BattleServers())
}

func (h *Handler) registerBattleServer(w http.ResponseWriter, r *http.Request) {
	if rejectPlayerContextForServiceRoute(w, r) {
		return
	}
	if rejectBusinessEnvelopeHeadersForServiceRoute(w, r) {
		return
	}
	var req core.RegisterBattleServerRequest
	if !decodeServiceJSON(w, r, &req) {
		return
	}
	if rejectMissingServiceOriginHeadersForServiceRoute(w, r) {
		return
	}
	resp, err := h.service.RegisterBattleServer(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) battleServerHeartbeat(w http.ResponseWriter, r *http.Request) {
	if rejectPlayerContextForServiceRoute(w, r) {
		return
	}
	if rejectBusinessEnvelopeHeadersForServiceRoute(w, r) {
		return
	}
	var req core.BattleServerHeartbeatRequest
	if !decodeServiceJSON(w, r, &req) {
		return
	}
	if rejectMissingServiceOriginHeadersForServiceRoute(w, r) {
		return
	}
	resp, err := h.service.BattleServerHeartbeat(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) battleServerOffline(w http.ResponseWriter, r *http.Request) {
	if rejectPlayerContextForServiceRoute(w, r) {
		return
	}
	if rejectBusinessEnvelopeHeadersForServiceRoute(w, r) {
		return
	}
	var req core.BattleServerOfflineRequest
	if !decodeServiceJSON(w, r, &req) {
		return
	}
	if rejectMissingServiceOriginHeadersForServiceRoute(w, r) {
		return
	}
	resp, err := h.service.BattleServerOffline(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) consumeBattleTicket(w http.ResponseWriter, r *http.Request) {
	if rejectPlayerContextForServiceRoute(w, r) {
		return
	}
	if rejectBusinessEnvelopeHeadersForServiceRoute(w, r) {
		return
	}
	var req core.BattleTicketConsumeRequest
	if !decodeServiceJSON(w, r, &req) {
		return
	}
	if rejectMissingServiceOriginHeadersForServiceRoute(w, r) {
		return
	}
	resp, err := h.service.ConsumeBattleTicket(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) submitBattleResult(w http.ResponseWriter, r *http.Request) {
	if rejectPlayerContextForServiceRoute(w, r) {
		return
	}
	if rejectBusinessEnvelopeHeadersForServiceRoute(w, r) {
		return
	}
	var req core.BattleResultSubmitRequest
	if !decodeServiceJSON(w, r, &req) {
		return
	}
	if rejectMissingServiceOriginHeadersForServiceRoute(w, r) {
		return
	}
	resp, err := h.service.SubmitBattleResult(req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) battleAllocation(w http.ResponseWriter, r *http.Request, matchID string) {
	resp, err := h.service.BattleAllocation(sessionToken(r), matchID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) battleTicket(w http.ResponseWriter, r *http.Request, matchID string) {
	resp, err := h.service.BattleTicket(sessionToken(r), matchID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) businessEnvelopeStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": h.envelopeGuard.Snapshot(),
	})
}

func (h *Handler) serviceCallbackStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"status": map[string]any{
			"service_callbacks":            core.ServiceCallbackOperations(),
			"service_callback_context":     core.ServiceCallbackContext(),
			"disallowed_client_operations": core.ContractDisallowedClientOperations(),
			"business_event_request_kinds": core.ContractBusinessEventRequestKinds(),
			"business_notification_topics": core.ContractBusinessNotificationTopics(),
			"http_headers": map[string]string{
				"service_origin":  headerServiceOrigin,
				"battle_callback": headerBattleCallback,
			},
			"accepted_callback_values":                   core.ServiceCallbackAcceptedValues(),
			"service_callback_accepted_values":           core.ServiceCallbackAcceptedValues(),
			"player_session_context_allowed":             false,
			"business_envelope_allowed":                  false,
			"service_callback_player_session_allowed":    false,
			"service_callback_business_envelope_allowed": false,
		},
	})
}

func (h *Handler) battleAuditStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": h.service.BattleLifecycleAuditStatus(),
	})
}

func (h *Handler) lobbyAuditStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"status": h.service.LobbyLifecycleAuditStatus(),
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error_code": "invalid_json", "message": err.Error()})
		return false
	}
	return true
}

func decodeServiceJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	var raw map[string]any
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error_code": "invalid_json", "message": err.Error()})
		return false
	}
	if servicePayloadHasBusinessEnvelope(raw) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":         false,
			"error_code": "invalid_request",
			"message":    "service-origin callback must not include business envelope payload",
		})
		return false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error_code": "invalid_json", "message": err.Error()})
		return false
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error_code": "invalid_json", "message": err.Error()})
		return false
	}
	return true
}

func sessionToken(r *http.Request) string {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(header, "Bearer ") {
		return strings.TrimPrefix(header, "Bearer ")
	}
	if token := strings.TrimSpace(r.Header.Get("X-Session-Token")); token != "" {
		return token
	}
	return strings.TrimSpace(r.URL.Query().Get("session_token"))
}

func queryInt(r *http.Request, key string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func routeUsesBusinessEnvelope(method string, segments []string) bool {
	if len(segments) == 0 || segments[0] != "v1" {
		return false
	}
	if isServiceCallbackRoute(method, segments) {
		return false
	}
	if len(segments) == 3 && segments[1] == "auth" && segments[2] == "anonymous" && method == http.MethodPost {
		return false
	}
	if len(segments) == 3 && segments[1] == "security" && (segments[2] == "business-envelope" || segments[2] == "service-callback") && method == http.MethodGet {
		return false
	}
	if len(segments) == 3 && segments[1] == "security" && (segments[2] == "battle-audit" || segments[2] == "lobby-audit") && method == http.MethodGet {
		return false
	}
	return true
}

func isServiceCallbackRoute(method string, segments []string) bool {
	if method != http.MethodPost || len(segments) < 4 || segments[0] != "v1" {
		return false
	}
	if len(segments) == 4 && segments[1] == "battle" && segments[2] == "servers" {
		return segments[3] == "register" || segments[3] == "heartbeat" || segments[3] == "offline"
	}
	if len(segments) == 4 && segments[1] == "battle" && segments[2] == "tickets" && segments[3] == "consume" {
		return true
	}
	if len(segments) == 4 && segments[1] == "battle" && segments[2] == "results" && segments[3] == "submit" {
		return true
	}
	return false
}

func rejectPlayerContextForServiceRoute(w http.ResponseWriter, r *http.Request) bool {
	if sessionToken(r) == "" {
		return false
	}
	writeJSON(w, http.StatusForbidden, map[string]any{
		"ok":         false,
		"error_code": "service_origin_required",
		"message":    "service-origin callback must not include player session context",
	})
	return true
}

func rejectBusinessEnvelopeHeadersForServiceRoute(w http.ResponseWriter, r *http.Request) bool {
	if !security.BusinessEnvelopeHeadersPresent(r.Header) {
		return false
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"ok":         false,
		"error_code": "invalid_request",
		"message":    "service-origin callback must not include business envelope headers",
	})
	return true
}

func rejectMissingServiceOriginHeadersForServiceRoute(w http.ResponseWriter, r *http.Request) bool {
	if serviceCallbackHeadersMatch(r.Header) {
		return false
	}
	writeJSON(w, http.StatusForbidden, map[string]any{
		"ok":         false,
		"error_code": "service_origin_required",
		"message":    "service-origin callback requires development battle callback headers",
	})
	return true
}

func serviceCallbackHeadersMatch(headers http.Header) bool {
	if normalizeServiceCallbackValue(headers.Get(headerServiceOrigin)) != serviceCallbackContextValue(serviceOriginContextKey) {
		return false
	}
	return truthyHeader(headers.Get(headerBattleCallback))
}

func serviceCallbackContextValue(key string) string {
	return normalizeServiceCallbackValue(core.ServiceCallbackContext()[key])
}

func normalizeServiceCallbackValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func truthyHeader(value string) bool {
	normalized := normalizeServiceCallbackValue(value)
	for _, accepted := range core.ServiceCallbackAcceptedValues() {
		if normalized == normalizeServiceCallbackValue(accepted) {
			return true
		}
	}
	return false
}

func servicePayloadHasBusinessEnvelope(payload map[string]any) bool {
	if _, ok := payload[security.BusinessEnvelopePayloadKey]; ok {
		return true
	}
	if servicePayloadContainsEnvelopeField(payload) {
		return true
	}
	for _, key := range []string{"body", "request", "data"} {
		if nested, ok := payload[key].(map[string]any); ok {
			if _, hasEnvelope := nested[security.BusinessEnvelopePayloadKey]; hasEnvelope {
				return true
			}
			if servicePayloadContainsEnvelopeField(nested) {
				return true
			}
		}
	}
	return false
}

func servicePayloadContainsEnvelopeField(payload map[string]any) bool {
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

func (h *Handler) validateBusinessEnvelopeHeaders(r *http.Request) (int, string, string) {
	request, ok := security.BusinessEnvelopeRequestFromHTTPHeaders(r.Header, security.BusinessEnvelopeRequestContext{
		SessionID: sessionToken(r),
		Transport: security.BusinessEnvelopeTransportHTTPFallback,
		Endpoint:  strings.TrimSpace(r.URL.Path),
	})
	if !ok {
		return 0, "", ""
	}
	result := security.ValidateBusinessEnvelopeRequest(h.envelopeGuard, request)
	if !result.OK {
		return result.Status, result.Code, result.Message
	}
	return 0, "", ""
}

func writeError(w http.ResponseWriter, err error) {
	code := core.ErrorCode(err)
	status := http.StatusBadRequest
	switch code {
	case "unauthorized":
		status = http.StatusUnauthorized
	case "not_found":
		status = http.StatusNotFound
	case "match_state_invalid", "reconnect_expired":
		status = http.StatusConflict
	case "forbidden_field":
		status = http.StatusForbidden
	case "room_unavailable":
		status = http.StatusConflict
	case "battle_server_unavailable":
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]any{"ok": false, "error_code": code, "message": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
