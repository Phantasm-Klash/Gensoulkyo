package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	phkv1 "github.com/phantasm-klash/phk-protocol/gen/go/phk/v1"
)

func TestCoreVersionConstantsFollowPhKProtocolManifest(t *testing.T) {
	if ProtocolVersion != phkv1.ProtocolVersion {
		t.Fatalf("protocol version drift: core=%d manifest=%d", ProtocolVersion, phkv1.ProtocolVersion)
	}
	if BusinessAPIVersion != phkv1.BusinessAPIVersion {
		t.Fatalf("business api version drift: core=%s manifest=%s", BusinessAPIVersion, phkv1.BusinessAPIVersion)
	}
	if BattleAPIVersion != phkv1.BattleAPIVersion {
		t.Fatalf("battle api version drift: core=%s manifest=%s", BattleAPIVersion, phkv1.BattleAPIVersion)
	}
	if RulesetVersion != phkv1.RulesetVersion {
		t.Fatalf("ruleset version drift: core=%s manifest=%s", RulesetVersion, phkv1.RulesetVersion)
	}
}

func TestCoreDependsOnRequiredProtocolFields(t *testing.T) {
	required := map[string][]string{
		"BattleTicket":           {"match_id", "user_id", "player_id", "battle_server_id", "endpoint", "deck_snapshot_hash", "ruleset_version", "expires_at_ms", "business_session_id"},
		"SignedBattleTicket":     {"ticket", "signature_alg", "key_id", "signature"},
		"BattleResult":           {"version", "match_id", "mode_id", "result_hash", "replay_id", "player_ids", "settled_at_ms"},
		"BattleModeAction":       {"version", "match_id", "player_id", "tick", "seq", "action_id", "action_type", "payload_json", "client_result_authoritative"},
		"SignedBattleResult":     {"result", "signature_alg", "key_id", "signature"},
		"BattleServerAllocation": {"match_id", "mode_id", "battle_server_id", "endpoint", "players", "server_seed", "mode_config_hash", "allocated_at_ms"},
		"BusinessSecureEnvelope": {"version", "session_id", "seq", "timestamp_ms", "nonce", "op_code", "key_id", "auth_tag"},
	}
	for messageName, fields := range required {
		for _, field := range fields {
			if !phkv1.HasMessageField(messageName, field) {
				t.Fatalf("protocol manifest missing %s.%s", messageName, field)
			}
		}
	}
}

func TestMatchEntryRequestsExposeClientVersionContract(t *testing.T) {
	requests := []any{
		JoinQueueRequest{ClientVersion: currentVersionStamp()},
		CreateRoomRequest{ClientVersion: currentVersionStamp()},
		JoinRoomRequest{ClientVersion: currentVersionStamp()},
	}
	for _, request := range requests {
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("marshal %T: %v", request, err)
		}
		var roundTrip map[string]any
		if err := json.Unmarshal(encoded, &roundTrip); err != nil {
			t.Fatalf("unmarshal %T: %v", request, err)
		}
		version, ok := roundTrip["client_version"].(map[string]any)
		if !ok {
			t.Fatalf("%T must expose client_version: %s", request, encoded)
		}
		if version["protocol_version"] == nil || version["business_api_version"] == "" || version["battle_api_version"] == "" || version["ruleset_version"] == "" {
			t.Fatalf("%T client_version missing version gates: %+v", request, version)
		}
	}
}

func TestBattleTicketConsumeRequestExposesServiceVersionContract(t *testing.T) {
	req := BattleTicketConsumeRequest{
		Version:        currentVersionStamp(),
		TicketID:       "ticket-contract",
		MatchID:        "match-contract",
		BattleServerID: "battle-contract",
		ModeConfigHash: "sha256:123456",
		TicketNonceHex: "abcdef",
	}
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal consume request: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal consume request: %v", err)
	}
	version, ok := roundTrip["version"].(map[string]any)
	if !ok {
		t.Fatalf("consume request must expose version stamp: %s", encoded)
	}
	if version["protocol_version"] == nil || version["business_api_version"] == "" || version["battle_api_version"] == "" || version["ruleset_version"] == "" {
		t.Fatalf("consume request version missing battle gates: %+v", version)
	}
	if roundTrip["mode_config_hash"] != "sha256:123456" {
		t.Fatalf("consume request must expose required mode_config_hash echo: %s", encoded)
	}
}

func TestBattleTicketExposesSignedModeConfigHashContract(t *testing.T) {
	ticket := BattleTicket{
		Version:             currentVersionStamp(),
		TicketID:            "ticket-contract",
		MatchID:             "match-contract",
		UserID:              "user-contract",
		PlayerID:            "player-contract",
		ModeID:              "pvp_duel",
		BattleServerID:      "battle-contract",
		Endpoint:            "127.0.0.1:7909",
		DeckSnapshotHash:    "sha256:deck123",
		ModeConfigHash:      "sha256:mode123",
		RulesetVersion:      RulesetVersion,
		TicketNonceHex:      "abcdef",
		ServerAuthoritative: true,
	}
	encoded, err := json.Marshal(ticket)
	if err != nil {
		t.Fatalf("marshal battle ticket: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal battle ticket: %v", err)
	}
	if roundTrip["mode_config_hash"] != "sha256:mode123" {
		t.Fatalf("battle ticket must expose signed mode_config_hash for battle-server validation: %s", encoded)
	}
}

func TestPresenceHeartbeatResponseExposesBattleDescriptorContract(t *testing.T) {
	response := PresenceHeartbeatResponse{
		OK:             true,
		UserID:         "user-contract",
		PresenceStatus: "in_match",
		SessionStatus:  "authenticated",
		MatchID:        "match-contract",
		MatchStatus:    "running",
		BattleAllocation: &BattleServerAllocation{
			OK:                  true,
			Version:             currentVersionStamp(),
			MatchID:             "match-contract",
			ModeID:              "pvp_duel",
			BattleServerID:      "battle-contract",
			Endpoint:            "127.0.0.1:7909",
			ServerAuthoritative: true,
		},
		BattleTicket: &SignedBattleTicket{
			OK: true,
			Ticket: BattleTicket{
				Version:             currentVersionStamp(),
				TicketID:            "ticket-contract",
				MatchID:             "match-contract",
				UserID:              "user-contract",
				PlayerID:            "player-contract",
				BattleServerID:      "battle-contract",
				Endpoint:            "127.0.0.1:7909",
				ModeConfigHash:      "sha256:mode123",
				ServerAuthoritative: true,
			},
			SignatureAlg:        "ED25519",
			KeyID:               "dev-key",
			SignatureHex:        "abcdef",
			ServerAuthoritative: true,
		},
		ServerAuthoritative: true,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal heartbeat response: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal heartbeat response: %v", err)
	}
	allocation, ok := roundTrip["battle_allocation"].(map[string]any)
	if !ok || allocation["match_id"] != "match-contract" || allocation["battle_server_id"] != "battle-contract" || allocation["server_authoritative"] != true {
		t.Fatalf("heartbeat response must expose low-frequency battle allocation descriptor: %s", encoded)
	}
	signedTicket, ok := roundTrip["battle_ticket"].(map[string]any)
	if !ok || signedTicket["signature_alg"] != "ED25519" || signedTicket["server_authoritative"] != true {
		t.Fatalf("heartbeat response must expose signed battle ticket descriptor: %s", encoded)
	}
	ticket, ok := signedTicket["ticket"].(map[string]any)
	if !ok || ticket["match_id"] != "match-contract" || ticket["user_id"] != "user-contract" || ticket["battle_server_id"] != "battle-contract" || ticket["mode_config_hash"] != "sha256:mode123" || ticket["server_authoritative"] != true {
		t.Fatalf("heartbeat signed ticket must stay match/user/server bound: %s", encoded)
	}
}

func TestBattleResultExposesFullServiceVersionContract(t *testing.T) {
	result := BattleResult{
		Version:     currentVersionStamp(),
		MatchID:     "match-contract",
		ModeID:      "pvp_duel",
		ResultHash:  "sha256:abc123",
		ReplayID:    "replay-contract",
		PlayerIDs:   []string{"p1", "p2"},
		SettledAtMS: 1782800000000,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal battle result: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("unmarshal battle result: %v", err)
	}
	version, ok := roundTrip["version"].(map[string]any)
	if !ok {
		t.Fatalf("battle result must expose version stamp: %s", encoded)
	}
	if version["protocol_version"] == nil || version["business_api_version"] == "" || version["battle_api_version"] == "" || version["ruleset_version"] == "" {
		t.Fatalf("battle result version missing service gates: %+v", version)
	}
}

func TestBusinessOperationContractsKeepServiceCallbacksOutOfClientList(t *testing.T) {
	clientOps := ContractClientOperations()
	clientRPCOps := ContractClientRPCOperations()
	clientWSSOps := ContractClientWSSOperations()
	disallowedClientOps := ContractDisallowedClientOperations()
	serviceCallbacks := ServiceCallbackOperations()
	if len(clientOps) == 0 || len(clientRPCOps) == 0 || len(clientWSSOps) == 0 || len(disallowedClientOps) == 0 || len(serviceCallbacks) == 0 {
		t.Fatalf("business operation contracts must not be empty: client=%+v rpc=%+v wss=%+v disallowed=%+v service=%+v", clientOps, clientRPCOps, clientWSSOps, disallowedClientOps, serviceCallbacks)
	}
	for _, callback := range serviceCallbacks {
		if !IsServiceCallbackOperation(callback) {
			t.Fatalf("service callback helper does not recognize %q", callback)
		}
		if stringSliceContains(clientOps, callback) || stringSliceContains(clientRPCOps, callback) || stringSliceContains(clientWSSOps, callback) {
			t.Fatalf("service callback %q must not be exposed as a client operation: client=%+v rpc=%+v wss=%+v service=%+v", callback, clientOps, clientRPCOps, clientWSSOps, serviceCallbacks)
		}
	}
	for _, expected := range []string{
		"battle.servers.register",
		"battle.servers.heartbeat",
		"battle.servers.offline",
		"battle.ticket.consume",
		"battle.result.submit",
	} {
		if !IsServiceCallbackOperation(expected) {
			t.Fatalf("service callback contract missing %q: %+v", expected, serviceCallbacks)
		}
	}
	for _, ops := range [][]string{clientOps, clientRPCOps, clientWSSOps} {
		for _, clientOp := range ops {
			if IsServiceCallbackOperation(clientOp) {
				t.Fatalf("client operation %q must not require service origin: client=%+v rpc=%+v wss=%+v service=%+v", clientOp, clientOps, clientRPCOps, clientWSSOps, serviceCallbacks)
			}
		}
	}
	for _, disallowed := range disallowedClientOps {
		if stringSliceContains(clientOps, disallowed) || stringSliceContains(clientRPCOps, disallowed) || stringSliceContains(clientWSSOps, disallowed) {
			t.Fatalf("disallowed client operation %q must not be exposed: client=%+v rpc=%+v wss=%+v disallowed=%+v", disallowed, clientOps, clientRPCOps, clientWSSOps, disallowedClientOps)
		}
	}
	for _, expected := range []string{
		"match.input",
		"match.snapshot",
		"match.events",
		"match.settle",
		"battle.input",
		"battle.snapshot",
		"battle.events",
		"battle.result.submit",
		"battle.ticket.consume",
		"battle.servers.register",
		"battle.servers.heartbeat",
		"battle.servers.offline",
	} {
		if !stringSliceContains(disallowedClientOps, expected) {
			t.Fatalf("disallowed client operation contract missing %q: %+v", expected, disallowedClientOps)
		}
	}
	callbackContext := ServiceCallbackContext()
	expectedCallbackContext := map[string]string{
		ServiceCallbackRuntimeModeKey:             ServiceCallbackRuntimeModeRPC,
		ServiceCallbackOriginKey:                  ServiceCallbackOriginBattleServer,
		ServiceCallbackFlagKey:                    ServiceCallbackRequiredValue,
		ServiceCallbackPlayerSessionContextKey:    ServiceCallbackDisallowedValue,
		ServiceCallbackBusinessEnvelopeAllowedKey: ServiceCallbackDisallowedValue,
	}
	if len(callbackContext) != len(expectedCallbackContext) {
		t.Fatalf("service callback context must expose only required non-secret gates: got %+v want keys %+v", callbackContext, expectedCallbackContext)
	}
	for key, expected := range expectedCallbackContext {
		if callbackContext[key] != expected {
			t.Fatalf("service callback context %q drifted: got %q want %q in %+v", key, callbackContext[key], expected, callbackContext)
		}
	}
	for key, value := range callbackContext {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			t.Fatalf("service callback context must not expose blank keys or values: %+v", callbackContext)
		}
	}
	acceptedCallbackValues := ServiceCallbackAcceptedValues()
	for _, expected := range []string{callbackContext[ServiceCallbackFlagKey], "1", "yes"} {
		if !stringSliceContains(acceptedCallbackValues, expected) {
			t.Fatalf("service callback accepted values missing %q: %+v", expected, acceptedCallbackValues)
		}
	}
	seenAcceptedCallbackValues := map[string]bool{}
	for _, value := range acceptedCallbackValues {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if value != normalized || normalized == "" {
			t.Fatalf("service callback accepted values must be non-blank normalized tokens: %+v", acceptedCallbackValues)
		}
		if normalized == ServiceCallbackDisallowedValue || normalized == callbackContext[ServiceCallbackBusinessEnvelopeAllowedKey] || normalized == callbackContext[ServiceCallbackPlayerSessionContextKey] {
			t.Fatalf("service callback accepted values must not include disallowed/player/envelope tokens: %+v context=%+v", acceptedCallbackValues, callbackContext)
		}
		if seenAcceptedCallbackValues[normalized] {
			t.Fatalf("service callback accepted values must be unique after normalization: %+v", acceptedCallbackValues)
		}
		seenAcceptedCallbackValues[normalized] = true
	}
	for _, rejected := range []string{"", "false", "0", "no", ServiceCallbackOriginBattleServer, ServiceCallbackRuntimeModeRPC} {
		if seenAcceptedCallbackValues[strings.ToLower(strings.TrimSpace(rejected))] {
			t.Fatalf("service callback accepted values must not trust rejected token %q: %+v", rejected, acceptedCallbackValues)
		}
	}
	for _, value := range []string{callbackContext[ServiceCallbackRuntimeModeKey], callbackContext[ServiceCallbackOriginKey], callbackContext[ServiceCallbackFlagKey]} {
		if value != strings.ToLower(strings.TrimSpace(value)) {
			t.Fatalf("service callback context values must stay normalized for HTTP/Nakama gates: %+v", callbackContext)
		}
	}
	for _, disallowedKey := range []string{ServiceCallbackBusinessEnvelopeAllowedKey, ServiceCallbackPlayerSessionContextKey} {
		if callbackContext[disallowedKey] != ServiceCallbackDisallowedValue {
			t.Fatalf("service callback context must keep %q disallowed: %+v", disallowedKey, callbackContext)
		}
	}
	contract := businessContractSnapshot(time.Unix(1782800000, 0))
	if contract.SettlementAuthority != settlementAuthorityServiceSignedBattleResult {
		t.Fatalf("business contract must publish service-signed settlement authority, got %+v", contract)
	}
	for _, ops := range [][]string{clientOps, clientRPCOps, clientWSSOps} {
		if !stringSliceContains(ops, "battle.servers") {
			t.Fatalf("client operation contract should expose battle server discovery: client=%+v rpc=%+v wss=%+v", clientOps, clientRPCOps, clientWSSOps)
		}
	}
	if !stringSliceContains(clientOps, "business.contract") || !stringSliceContains(clientRPCOps, "business.contract") || !stringSliceContains(clientWSSOps, "business.contract") {
		t.Fatalf("client RPC/WSS operation contracts should expose authenticated business contract snapshot: client=%+v rpc=%+v wss=%+v", clientOps, clientRPCOps, clientWSSOps)
	}
	if !stringSliceContains(clientOps, "business.event.settlement") || !stringSliceContains(clientRPCOps, "business.event.settlement") || !stringSliceContains(clientWSSOps, "business.event.settlement") {
		t.Fatalf("client RPC/WSS operation contracts should expose settlement event alias: client=%+v rpc=%+v wss=%+v", clientOps, clientRPCOps, clientWSSOps)
	}
	if !stringSliceContains(clientRPCOps, "activity.claim") || stringSliceContains(clientWSSOps, "activity.claim") {
		t.Fatalf("client RPC/WSS operation contracts should reflect handler transport support: rpc=%+v wss=%+v", clientRPCOps, clientWSSOps)
	}
	for _, diagnostic := range []string{"business.envelope.audit.status", "battle.audit.status", "lobby.audit.status"} {
		if !stringSliceContains(clientOps, diagnostic) || !stringSliceContains(clientRPCOps, diagnostic) || !stringSliceContains(clientWSSOps, diagnostic) {
			t.Fatalf("client RPC/WSS operation contracts should expose authenticated audit diagnostics %q: client=%+v rpc=%+v wss=%+v", diagnostic, clientOps, clientRPCOps, clientWSSOps)
		}
	}
	for _, serviceOnly := range []string{"battle.servers.register", "battle.servers.heartbeat", "battle.servers.offline"} {
		if stringSliceContains(clientOps, serviceOnly) || stringSliceContains(clientRPCOps, serviceOnly) || stringSliceContains(clientWSSOps, serviceOnly) {
			t.Fatalf("client operation contract must not expose service-only battle server callback %q: client=%+v rpc=%+v wss=%+v", serviceOnly, clientOps, clientRPCOps, clientWSSOps)
		}
	}
	topics := ContractBusinessNotificationTopics()
	if len(topics) == 0 {
		t.Fatalf("business notification topic contract must not be empty")
	}
	requestKinds := ContractBusinessEventRequestKinds()
	if !reflect.DeepEqual(requestKinds, ContractBusinessNotificationKinds()) {
		t.Fatalf("business event request kinds must mirror notification kinds: request=%+v notifications=%+v", requestKinds, ContractBusinessNotificationKinds())
	}
	if stringSliceContains(requestKinds, "battle.result.submit") || stringSliceContains(requestKinds, "battle.ticket.consume") {
		t.Fatalf("business event request kinds must not expose service callbacks: %+v", requestKinds)
	}
	seenKinds := map[string]bool{}
	for _, topic := range topics {
		if topic.Kind == "" || topic.Topic != "nakama_wss.business."+strings.ReplaceAll(topic.Kind, ".", "_") || topic.Transport != "nakama_wss" {
			t.Fatalf("business notification topic shape invalid: %+v", topic)
		}
		expectedRequestOp := "business.event"
		if topic.Kind == "settlement" {
			expectedRequestOp = "business.event.settlement"
		}
		if topic.ClientEventRequestOperation != expectedRequestOp || !topic.ServerPush {
			t.Fatalf("business notification topic must remain low-frequency business WSS/event contract: %+v", topic)
		}
		if topic.ClientEventRequestKind != topic.Kind {
			t.Fatalf("business notification topic must publish its fixed read request kind: %+v", topic)
		}
		if topic.Kind != "settlement" && topic.ClientEventRequestOperation == "business.event.settlement" {
			t.Fatalf("settlement alias must not be reused for non-settlement notification topics: %+v", topic)
		}
		if !topic.ServerAuthoritativeProjection {
			t.Fatalf("business notification topic must be a server-authoritative projection: %+v", topic)
		}
		for _, requestField := range []string{"kind", "ticket_id", "room_code", "match_id"} {
			if !stringSliceContains(topic.ClientRequestFields, requestField) {
				t.Fatalf("business notification topic missing allowed read request field %q: %+v", requestField, topic)
			}
		}
		for _, forbiddenField := range []string{"result_hash", "final_result", "damage", "damage_dealt", "boss_hp_after_global", "rank", "rank_score_delta", "reward_status", "settlement_key"} {
			if !stringSliceContains(topic.ForbiddenClientRequestFields, forbiddenField) {
				t.Fatalf("business notification topic missing forbidden client request field %q: %+v", forbiddenField, topic)
			}
		}
		if topic.ServiceCallback || topic.HighFrequencyBattleTickAllowed || topic.ClientResultSubmitAllowed {
			t.Fatalf("business notification topic must not authorize service callbacks, tick, or client result submit: %+v", topic)
		}
		if IsServiceCallbackOperation(topic.Kind) || stringSliceContains(disallowedClientOps, topic.Kind) {
			t.Fatalf("business notification topic kind must stay out of service/disallowed operation sets: %+v", topic)
		}
		seenKinds[topic.Kind] = true
	}
	for _, expected := range []string{"matchmaking", "battle.allocation", "battle.ticket", "settlement"} {
		if !seenKinds[expected] {
			t.Fatalf("business notification topic contract missing %q: %+v", expected, topics)
		}
	}
	for _, forbiddenField := range []string{"score", "damage_dealt", "boss_hp_after", "boss_hp_after_global", "rank", "rank_score_after", "reward_status", "battle_result_hash"} {
		if !stringSliceContains(contract.ForbiddenFields, forbiddenField) {
			t.Fatalf("business contract missing server-authoritative forbidden field %q: %+v", forbiddenField, contract.ForbiddenFields)
		}
		if ForbiddenClientField(map[string]any{"projection": []any{map[string]any{forbiddenField: "client-authored"}}}) != forbiddenField {
			t.Fatalf("forbidden client field %q must be rejected in nested business/event payloads", forbiddenField)
		}
	}
}

func TestBattleModeActionFixtureContract(t *testing.T) {
	action := struct {
		Version                   int    `json:"version"`
		MatchID                   string `json:"match_id"`
		PlayerID                  string `json:"player_id"`
		Tick                      int    `json:"tick"`
		Seq                       int    `json:"seq"`
		ActionID                  string `json:"action_id"`
		ActionType                string `json:"action_type"`
		PayloadJSON               string `json:"payload_json"`
		ClientResultAuthoritative bool   `json:"client_result_authoritative"`
	}{
		Version:                   phkv1.ProtocolVersion,
		MatchID:                   phkv1.BattleModeActionMatchID,
		PlayerID:                  phkv1.BattleModeActionPlayerID,
		Tick:                      phkv1.BattleModeActionTick,
		Seq:                       phkv1.BattleModeActionSeq,
		ActionID:                  phkv1.BattleModeActionActionID,
		ActionType:                phkv1.BattleModeActionActionType,
		PayloadJSON:               phkv1.BattleModeActionPayloadJSON,
		ClientResultAuthoritative: false,
	}
	if action.MatchID == "" || action.PlayerID == "" || action.ActionID == "" || action.ActionType == "" {
		t.Fatalf("battle mode action fixture missing identity fields: %+v", action)
	}
	if action.Tick <= 0 || action.Seq <= 0 {
		t.Fatalf("battle mode action fixture must carry positive tick/seq: %+v", action)
	}
	if action.ClientResultAuthoritative {
		t.Fatalf("battle mode action fixture must not allow client-authored results: %+v", action)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(action.PayloadJSON), &payload); err != nil {
		t.Fatalf("battle mode action payload_json must be parseable JSON: %v", err)
	}
	if payload["card_id"] == "" || payload["round_index"] == nil {
		t.Fatalf("battle mode action payload missing expected mode-action fields: %+v", payload)
	}
	encoded, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("marshal battle mode action fixture: %v", err)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("round-trip battle mode action fixture: %v", err)
	}
	if got, ok := roundTrip["client_result_authoritative"].(bool); !ok || got {
		t.Fatalf("wire action must include client_result_authoritative=false, got %#v", roundTrip["client_result_authoritative"])
	}
	payloadJSON, ok := roundTrip["payload_json"].(string)
	if !ok {
		t.Fatalf("wire action payload_json must be a JSON string field, got %#v", roundTrip["payload_json"])
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("wire action payload_json string must parse as JSON: %v", err)
	}
}

func TestBattleSnapshotFixtureContract(t *testing.T) {
	snapshot := struct {
		Version      int                `json:"version"`
		MatchID      string             `json:"match_id"`
		SnapshotTick int                `json:"snapshot_tick"`
		SnapshotKind string             `json:"snapshot_kind"`
		StateHash    string             `json:"state_hash"`
		Players      []battlePlayerWire `json:"players"`
		BulletsDelta []battleBulletWire `json:"bullets_delta"`
		ModeState    map[string]any     `json:"mode_state"`
		EventCursor  int                `json:"event_cursor"`
	}{
		Version:      phkv1.ProtocolVersion,
		MatchID:      phkv1.BattleSnapshotMatchID,
		SnapshotTick: phkv1.BattleSnapshotSnapshotTick,
		SnapshotKind: phkv1.BattleSnapshotSnapshotKind,
		StateHash:    phkv1.BattleSnapshotStateHash,
		Players: []battlePlayerWire{
			{PlayerID: "p1", XMilli: 120000, YMilli: 300000, Connected: true, HandSize: 4},
		},
		BulletsDelta: []battleBulletWire{
			{BulletID: "b-001", Op: "spawn", XMilli: 120000, YMilli: 300000, VXMilli: 0, VYMilli: -350, RadiusMilli: 5000, PatternID: "opening_fan", Color: "blue"},
		},
		ModeState: map[string]any{
			"boss_hp_preview": 42,
			"duel_status":     "running",
		},
		EventCursor: phkv1.BattleSnapshotEventCursor,
	}
	if snapshot.MatchID == "" || snapshot.StateHash == "" || snapshot.SnapshotKind == "" {
		t.Fatalf("battle snapshot fixture missing identity fields: %+v", snapshot)
	}
	if snapshot.SnapshotTick <= 0 || snapshot.EventCursor <= 0 {
		t.Fatalf("battle snapshot fixture must carry positive tick/cursor: %+v", snapshot)
	}
	if len(snapshot.Players) == 0 || len(snapshot.BulletsDelta) == 0 || len(snapshot.ModeState) == 0 {
		t.Fatalf("battle snapshot fixture missing projected state: %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal battle snapshot fixture: %v", err)
	}
	roundTrip := assertWireFieldsMatchManifest(t, "BattleSnapshot", encoded)
	if _, ok := roundTrip["players"].([]any); !ok {
		t.Fatalf("wire snapshot players must be an array, got %#v", roundTrip["players"])
	}
	if _, ok := roundTrip["bullets_delta"].([]any); !ok {
		t.Fatalf("wire snapshot bullets_delta must be an array, got %#v", roundTrip["bullets_delta"])
	}
	if _, ok := roundTrip["mode_state"].(map[string]any); !ok {
		t.Fatalf("wire snapshot mode_state must be an object, got %#v", roundTrip["mode_state"])
	}
	assertWireFieldsMatchManifest(t, "BattlePlayerSnapshot", mustMarshalJSON(t, snapshot.Players[0]))
	assertWireFieldsMatchManifest(t, "BattleBulletDelta", mustMarshalJSON(t, snapshot.BulletsDelta[0]))
}

func TestBattleEventFixtureContract(t *testing.T) {
	event := struct {
		Version             int    `json:"version"`
		MatchID             string `json:"match_id"`
		Cursor              int    `json:"cursor"`
		Tick                int    `json:"tick"`
		Type                string `json:"type"`
		PlayerID            string `json:"player_id"`
		PayloadJSON         string `json:"payload_json"`
		ServerAuthoritative bool   `json:"server_authoritative"`
	}{
		Version:             phkv1.ProtocolVersion,
		MatchID:             phkv1.BattleEventMatchID,
		Cursor:              phkv1.BattleEventCursor,
		Tick:                phkv1.BattleEventTick,
		Type:                phkv1.BattleEventType,
		PlayerID:            "p1",
		PayloadJSON:         `{"bullet_id":"b-001","pattern_id":"opening_fan"}`,
		ServerAuthoritative: phkv1.BattleEventServerAuthoritative,
	}
	if event.MatchID == "" || event.Type == "" || event.PlayerID == "" {
		t.Fatalf("battle event fixture missing identity fields: %+v", event)
	}
	if event.Cursor <= 0 || event.Tick <= 0 {
		t.Fatalf("battle event fixture must carry positive cursor/tick: %+v", event)
	}
	if !event.ServerAuthoritative {
		t.Fatalf("battle event fixture must be server authoritative: %+v", event)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
		t.Fatalf("battle event payload_json must be parseable JSON: %v", err)
	}
	if payload["bullet_id"] == "" || payload["pattern_id"] == "" {
		t.Fatalf("battle event payload missing bullet audit fields: %+v", payload)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal battle event fixture: %v", err)
	}
	roundTrip := assertWireFieldsMatchManifest(t, "BattleEvent", encoded)
	payloadJSON, ok := roundTrip["payload_json"].(string)
	if !ok {
		t.Fatalf("wire event payload_json must be a JSON string field, got %#v", roundTrip["payload_json"])
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("wire event payload_json string must parse as JSON: %v", err)
	}
	if got, ok := roundTrip["server_authoritative"].(bool); !ok || !got {
		t.Fatalf("wire event must include server_authoritative=true, got %#v", roundTrip["server_authoritative"])
	}
}

func TestGoldenReplaySummaryFixtureContract(t *testing.T) {
	summary := struct {
		Version         int    `json:"version"`
		ReplayID        string `json:"replay_id"`
		MatchID         string `json:"match_id"`
		OwnerUserID     string `json:"owner_user_id"`
		InputCount      int    `json:"input_count"`
		EventCount      int    `json:"event_count"`
		InputStreamHash string `json:"input_stream_hash"`
		EventStreamHash string `json:"event_stream_hash"`
		FinalStateHash  string `json:"final_state_hash"`
		FinalTick       int    `json:"final_tick"`
	}{
		Version:         phkv1.ProtocolVersion,
		ReplayID:        phkv1.GoldenReplaySummaryReplayID,
		MatchID:         phkv1.GoldenReplaySummaryMatchID,
		OwnerUserID:     phkv1.GoldenReplaySummaryOwnerUserID,
		InputCount:      phkv1.GoldenReplaySummaryInputCount,
		EventCount:      phkv1.GoldenReplaySummaryEventCount,
		InputStreamHash: phkv1.GoldenReplaySummaryInputStreamHash,
		EventStreamHash: phkv1.GoldenReplaySummaryEventStreamHash,
		FinalStateHash:  phkv1.GoldenReplaySummaryFinalStateHash,
		FinalTick:       phkv1.GoldenReplaySummaryFinalTick,
	}
	if summary.ReplayID == "" || summary.MatchID == "" || summary.OwnerUserID == "" {
		t.Fatalf("golden replay summary fixture missing identity fields: %+v", summary)
	}
	if summary.InputCount <= 0 || summary.EventCount <= 0 || summary.FinalTick <= 0 {
		t.Fatalf("golden replay summary fixture must carry positive counts/tick: %+v", summary)
	}
	for _, value := range []string{summary.InputStreamHash, summary.EventStreamHash, summary.FinalStateHash} {
		if !strings.HasPrefix(value, "sha256:") {
			t.Fatalf("golden replay summary hash must be sha256-prefixed: %+v", summary)
		}
	}
	assertWireFieldsMatchManifest(t, "ReplayInputStreamSummary", mustMarshalJSON(t, summary))
}

type battlePlayerWire struct {
	PlayerID  string `json:"player_id"`
	XMilli    int    `json:"x_milli"`
	YMilli    int    `json:"y_milli"`
	Connected bool   `json:"connected"`
	HandSize  int    `json:"hand_size"`
}

type battleBulletWire struct {
	BulletID    string `json:"bullet_id"`
	Op          string `json:"op"`
	XMilli      int    `json:"x_milli"`
	YMilli      int    `json:"y_milli"`
	VXMilli     int    `json:"vx_milli"`
	VYMilli     int    `json:"vy_milli"`
	RadiusMilli int    `json:"radius_milli"`
	PatternID   string `json:"pattern_id"`
	Color       string `json:"color"`
}

func assertWireFieldsMatchManifest(t *testing.T, messageName string, encoded []byte) map[string]any {
	t.Helper()
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("round-trip %s fixture: %v", messageName, err)
	}
	required := phkv1.RequiredMessageFields(messageName)
	if len(roundTrip) != len(required) {
		t.Fatalf("%s wire field count drift: got %d keys=%v want %d fields=%v", messageName, len(roundTrip), mapKeys(roundTrip), len(required), required)
	}
	for _, field := range required {
		if _, ok := roundTrip[field]; !ok {
			t.Fatalf("%s wire fixture missing manifest field %q; keys=%v", messageName, field, mapKeys(roundTrip))
		}
	}
	for field := range roundTrip {
		if !phkv1.HasMessageField(messageName, field) {
			t.Fatalf("%s wire fixture has field outside manifest %q; manifest=%v", messageName, field, required)
		}
	}
	return roundTrip
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return encoded
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
