package nakamaapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"

	"gensoulkyo/runtime/core"
)

// notificationCodes gives each push subject a stable numeric code.
//
// The client dispatches on Subject; Code exists because Nakama's notification
// record carries one and a client that switches on integers (or filters in the
// console) needs a value that does not shift. The numbers are part of the
// contract, so never renumber them.
var notificationCodes = map[string]int{
	core.PushRoomState:   1,
	core.PushMatchStart:  2,
	core.PushMatchResult: 3,
}

// NakamaNotifier delivers lobby pushes as Nakama notifications.
//
// Nakama's realtime protocol is a closed enum: there is no way to register a
// custom server-to-client message type (Initializer has RegisterBeforeRt and
// RegisterAfterRt, both interceptors that cannot send). Notifications are the
// one server-initiated channel the socket does carry, so a push travels as a
// notification and the client dispatches on the subject.
//
// The payload is JSON-encoded into the notification content under "payload"
// rather than spread across it: Content is map[string]interface{} and Nakama
// stores it as a JSON object, so a nested string is the only way to guarantee
// the client receives exactly the bytes the lobby produced. That mirrors the
// RPC wire, where the business envelope also travels as a string.
//
// Notifications are deliberately not persistent. A client that was offline
// re-reads state from the REST endpoints (GET /v1/rooms/{code},
// GET /v1/matches/{id}/snapshot) instead of draining a backlog of stale pushes,
// which also keeps the notification store from growing with every seat change.
type NakamaNotifier struct {
	nk     runtime.NakamaModule
	logger runtime.Logger
}

// NewNakamaNotifier returns a notifier backed by the given module.
func NewNakamaNotifier(nk runtime.NakamaModule, logger runtime.Logger) *NakamaNotifier {
	return &NakamaNotifier{nk: nk, logger: logger}
}

// Notify implements core.LobbyNotifier.
func (n *NakamaNotifier) Notify(userIDs []string, subject string, payload map[string]any) error {
	if n == nil || n.nk == nil {
		return errors.New("nakama notifier is not configured")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	code, known := notificationCodes[subject]
	if !known && n.logger != nil {
		// An unmapped subject would still be delivered, but with a code the
		// client cannot route; say so once per push instead of silently sending
		// something the other end will drop.
		n.logger.Warn("nakama notifier: no code registered for subject %q", subject)
	}
	notifications := make([]*runtime.NotificationSend, 0, len(userIDs))
	for _, userID := range userIDs {
		trimmed := strings.TrimSpace(userID)
		if trimmed == "" {
			continue
		}
		notifications = append(notifications, &runtime.NotificationSend{
			UserID:     trimmed,
			Subject:    subject,
			Content:    map[string]interface{}{"payload": string(encoded)},
			Code:       code,
			Persistent: false,
		})
	}
	if len(notifications) == 0 {
		return nil
	}
	return n.nk.NotificationsSend(context.Background(), notifications)
}
