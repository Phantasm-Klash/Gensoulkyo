package security

import (
	"strconv"
	"strings"
)

const (
	BusinessEnvelopeTransportHTTPFallback = "http_fallback"
	BusinessEnvelopeTransportNakamaRPC    = "nakama_rpc"
	BusinessEnvelopeTransportNakamaWSS    = "nakama_wss"

	BusinessEnvelopePayloadKey = "business_envelope"
)

type BusinessEnvelopeRequestContext struct {
	SessionID string
	Transport string
	Endpoint  string
	UserID    string
}

func BusinessEnvelopeRequestFromHTTPHeaders(headers map[string][]string, context BusinessEnvelopeRequestContext) (BusinessEnvelopeRequest, bool) {
	if !BusinessEnvelopeHeadersPresent(headers) {
		return BusinessEnvelopeRequest{}, false
	}
	request := BusinessEnvelopeRequest{
		SessionID:   strings.TrimSpace(context.SessionID),
		Version:     headerString(headers, HeaderBusinessEnvelope),
		Seq:         headerInt64(headers, HeaderBusinessSeq),
		TimestampMS: headerInt64(headers, HeaderBusinessTimestampMs),
		Nonce:       headerString(headers, HeaderBusinessNonce),
		Op:          headerString(headers, HeaderBusinessOp),
		KeyID:       headerString(headers, HeaderBusinessKeyID),
		Tag:         headerString(headers, HeaderBusinessTag),
		Mode:        headerString(headers, HeaderBusinessMode),
		Transport:   firstNonEmptyTrimmed(context.Transport, BusinessEnvelopeTransportHTTPFallback),
		Endpoint:    strings.TrimSpace(context.Endpoint),
		UserID:      strings.TrimSpace(context.UserID),
		BodyHash:    headerString(headers, HeaderBusinessBodyHash),
	}
	return request, true
}

func BusinessEnvelopeHeadersPresent(headers map[string][]string) bool {
	for _, header := range []string{
		HeaderBusinessEnvelope,
		HeaderBusinessSeq,
		HeaderBusinessTimestampMs,
		HeaderBusinessNonce,
		HeaderBusinessOp,
		HeaderBusinessKeyID,
		HeaderBusinessTag,
		HeaderBusinessMode,
		HeaderBusinessBodyHash,
	} {
		if headerString(headers, header) != "" {
			return true
		}
	}
	return false
}

func BusinessEnvelopeRequestFromNakamaRPCPayload(sessionID string, rpcID string, userID string, payload map[string]any) (BusinessEnvelopeRequest, bool) {
	endpoint := "rpc." + strings.Trim(strings.TrimSpace(rpcID), ".")
	if endpoint == "rpc." {
		endpoint = "rpc"
	}
	return BusinessEnvelopeRequestFromPayload(payload, BusinessEnvelopeRequestContext{
		SessionID: sessionID,
		Transport: BusinessEnvelopeTransportNakamaRPC,
		Endpoint:  endpoint,
		UserID:    userID,
	})
}

func BusinessEnvelopeRequestFromNakamaWSSPayload(sessionID string, messageName string, userID string, payload map[string]any) (BusinessEnvelopeRequest, bool) {
	endpoint := "wss." + strings.Trim(strings.TrimSpace(messageName), ".")
	if endpoint == "wss." {
		endpoint = "wss"
	}
	return BusinessEnvelopeRequestFromPayload(payload, BusinessEnvelopeRequestContext{
		SessionID: sessionID,
		Transport: BusinessEnvelopeTransportNakamaWSS,
		Endpoint:  endpoint,
		UserID:    userID,
	})
}

func BusinessEnvelopeRequestFromPayload(payload map[string]any, context BusinessEnvelopeRequestContext) (BusinessEnvelopeRequest, bool) {
	envelope, ok := businessEnvelopePayload(payload)
	if !ok {
		return BusinessEnvelopeRequest{}, false
	}
	request := BusinessEnvelopeRequest{
		SessionID:   firstNonEmptyTrimmed(context.SessionID, payloadString(envelope, "session_id", "business_session_id")),
		Version:     payloadString(envelope, "version", "envelope_version"),
		Seq:         payloadInt64(envelope, "seq"),
		TimestampMS: payloadInt64(envelope, "timestamp_ms", "timestampMS"),
		Nonce:       payloadString(envelope, "nonce"),
		Op:          payloadString(envelope, "op_code", "op"),
		KeyID:       payloadString(envelope, "key_id", "keyId"),
		Tag:         payloadString(envelope, "auth_tag", "tag"),
		Mode:        payloadString(envelope, "ciphertext_mode", "mode"),
		Transport:   firstNonEmptyTrimmed(context.Transport, payloadString(envelope, "transport")),
		Endpoint:    firstNonEmptyTrimmed(context.Endpoint, payloadString(envelope, "endpoint")),
		UserID:      firstNonEmptyTrimmed(context.UserID, payloadString(envelope, "user_id")),
		BodyHash:    payloadString(envelope, "body_hash", "bodyHash"),
	}
	return request, true
}

func businessEnvelopePayload(payload map[string]any) (map[string]any, bool) {
	if payload == nil {
		return nil, false
	}
	if nestedRaw, ok := payload[BusinessEnvelopePayloadKey]; ok {
		return payloadMap(nestedRaw)
	}
	if payloadContainsEnvelopeField(payload) {
		return payload, true
	}
	return nil, false
}

func payloadMap(value any) (map[string]any, bool) {
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

func payloadContainsEnvelopeField(payload map[string]any) bool {
	for _, key := range []string{"version", "envelope_version", "seq", "timestamp_ms", "nonce", "op_code", "op", "key_id", "keyId", "auth_tag", "tag", "ciphertext_mode", "mode", "body_hash", "bodyHash"} {
		if _, ok := payload[key]; ok {
			return true
		}
	}
	return false
}

func headerString(headers map[string][]string, key string) string {
	if headers == nil {
		return ""
	}
	if values, ok := headers[key]; ok {
		return firstHeaderValue(values)
	}
	for foundKey, values := range headers {
		if strings.EqualFold(foundKey, key) {
			return firstHeaderValue(values)
		}
	}
	return ""
}

func firstHeaderValue(values []string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func headerInt64(headers map[string][]string, key string) int64 {
	return parseInt64(headerString(headers, key))
}

func payloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
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

func payloadInt64(payload map[string]any, keys ...string) int64 {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			return int64(typed)
		case int32:
			return int64(typed)
		case int64:
			return typed
		case float64:
			return int64(typed)
		case string:
			if parsed := parseInt64(typed); parsed > 0 {
				return parsed
			}
		}
	}
	return 0
}

func parseInt64(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
