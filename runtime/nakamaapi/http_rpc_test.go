package nakamaapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gensoulkyo/runtime/core"
)

// newRPCTestServer mounts the Nakama-compatible HTTP RPC face on a throwaway
// core.Service, with the same secrets the deployment uses.
func newRPCTestServer(t *testing.T) (*httptest.Server, *core.Service) {
	t.Helper()
	service := core.NewService(core.Config{})
	handler := NewHTTPRPCHandler(New(service), HTTPRPCOptions{
		ServiceCallbackKey: "defaulthttpkey",
		RuntimeHTTPKey:     "defaulthttpkey",
	})
	return httptest.NewServer(handler), service
}

// postRPC issues a Nakama-shaped RPC call.
func postRPC(t *testing.T, server *httptest.Server, rpcID string, body string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v2/rpc/"+rpcID+"?unwrap=true", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth("defaulthttpkey", "")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call %s: %v", rpcID, err)
	}
	defer response.Body.Close()
	decoded := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode %s response: %v", rpcID, err)
	}
	return response.StatusCode, decoded
}

func TestHTTPRPCRegistersARemoteBattleServer(t *testing.T) {
	server, _ := newRPCTestServer(t)
	defer server.Close()

	body := `{
		"battle_server_id": "bs-104-1",
		"endpoint": "104.233.217.232:7400",
		"region": "raksmart",
		"build_id": "dev",
		"capacity": 20,
		"active_matches": 0,
		"load": 0,
		"status": "online",
		"supported_modes": ["*", "mvp_boss_race"]
	}`
	status, response := postRPC(t, server, "battle.servers.register", body, map[string]string{
		headerServiceKey: "defaulthttpkey",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if response["ok"] != true {
		t.Fatalf("register rejected: %+v", response)
	}

	// The registry is the same one `/v1/battle/servers` reads, and the agent must
	// be able to poll it for assignments right away.
	status, assignments := postRPC(t, server, "battle.agent.assignments", `{"battle_server_id":"bs-104-1"}`, map[string]string{
		headerServiceKey: "defaulthttpkey",
	})
	if status != http.StatusOK || assignments["ok"] != true {
		t.Fatalf("assignments failed: status=%d body=%+v", status, assignments)
	}
}

func TestHTTPRPCServiceKeyIsRequiredForServiceOperations(t *testing.T) {
	server, _ := newRPCTestServer(t)
	defer server.Close()

	status, response := postRPC(t, server, "battle.servers.register", `{"battle_server_id":"bs-x"}`, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the envelope carries the failure)", status)
	}
	if response["ok"] != false || response["error_code"] != CodeServiceOriginRequired {
		t.Fatalf("expected %s, got %+v", CodeServiceOriginRequired, response)
	}
}

func TestHTTPRPCRejectsAWrongServiceKey(t *testing.T) {
	server, _ := newRPCTestServer(t)
	defer server.Close()

	_, response := postRPC(t, server, "battle.servers.register", `{"battle_server_id":"bs-x"}`, map[string]string{
		headerServiceKey: "not-the-secret",
	})
	if response["ok"] != false || response["error_code"] != CodeServiceOriginRequired {
		t.Fatalf("expected %s, got %+v", CodeServiceOriginRequired, response)
	}
}

func TestHTTPRPCServiceKeyIsAcceptedAsAQueryParameter(t *testing.T) {
	server, _ := newRPCTestServer(t)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost,
		server.URL+"/v2/rpc/battle.agent.assignments?unwrap=true&service_key=defaulthttpkey",
		strings.NewReader(`{"battle_server_id":"bs-query"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.SetBasicAuth("defaulthttpkey", "")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	defer response.Body.Close()
	decoded := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The server was never registered, so the expected failure is not_found — what
	// matters is that the service-origin gate was satisfied by the query param.
	if decoded["error_code"] == CodeServiceOriginRequired {
		t.Fatalf("query-parameter service key was not honoured: %+v", decoded)
	}
}

func TestHTTPRPCRejectsServiceAuthorityForAPlayerSession(t *testing.T) {
	server, service := newRPCTestServer(t)
	defer server.Close()

	login, err := service.LoginAnonymous(core.AnonymousLoginRequest{DeviceID: "rpc-player", DisplayName: "RPC Player"})
	if err != nil {
		t.Fatalf("LoginAnonymous: %v", err)
	}
	// The runtime key travels as a query parameter here so the Authorization
	// header can carry the player session, which is the case under test.
	request, err := http.NewRequest(http.MethodPost,
		server.URL+"/v2/rpc/battle.servers.register?unwrap=true&http_key=defaulthttpkey",
		strings.NewReader(`{"battle_server_id":"bs-x"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set(headerServiceKey, "defaulthttpkey")
	request.Header.Set("Authorization", "Bearer "+login.SessionToken)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	defer response.Body.Close()
	decoded := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["ok"] != false || decoded["error_code"] != CodeServiceOriginRequired {
		t.Fatalf("a player session must not gain service authority: %+v", decoded)
	}
}

func TestHTTPRPCRequiresTheRuntimeHTTPKey(t *testing.T) {
	service := core.NewService(core.Config{})
	handler := NewHTTPRPCHandler(New(service), HTTPRPCOptions{
		ServiceCallbackKey: "defaulthttpkey",
		RuntimeHTTPKey:     "defaulthttpkey",
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v2/rpc/auth.anonymous", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
}

func TestHTTPRPCUnwrappedCallsStillReturnTheEnvelope(t *testing.T) {
	server, _ := newRPCTestServer(t)
	defer server.Close()

	// `?unwrap=true` is what both the battle agent and NakamaLobbyTransport send.
	// The plugin never returns a Go error, so Nakama passes the encoded envelope
	// through regardless — this face must behave identically or both callers break.
	status, response := postRPC(t, server, "auth.anonymous", `{"device_id":"unwrapped","display_name":"Unwrapped"}`, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if response["ok"] != true {
		t.Fatalf("anonymous login failed: %+v", response)
	}
	payload, ok := response["payload"].(map[string]any)
	if !ok {
		t.Fatalf("expected the session payload inside the envelope, got %T", response["payload"])
	}
	if token, _ := payload["session_token"].(string); token == "" {
		t.Fatalf("envelope payload is missing the session token: %+v", payload)
	}
}

func TestHTTPRPCReturnsEnvelopeForUnknownRPCOrBadBody(t *testing.T) {
	server, _ := newRPCTestServer(t)
	defer server.Close()

	// An id the dispatcher cannot route takes the authenticated-client path, so
	// the envelope gate answers before the switch's default is reached. The face
	// deliberately does not keep its own copy of the dispatch table: a second
	// list would drift from `HandleRPC` and could start 404-ing operations that
	// actually work.
	_, unknown := postRPC(t, server, "does.not.exist", `{}`, nil)
	if unknown["ok"] != false || unknown["error_code"] != CodeBusinessEnvelopeRequired {
		t.Fatalf("expected %s for an unroutable rpc, got %+v", CodeBusinessEnvelopeRequired, unknown)
	}

	_, malformed := postRPC(t, server, "auth.anonymous", `{not json`, nil)
	if malformed["ok"] != false || malformed["error_code"] != CodeInvalidRequest {
		t.Fatalf("expected %s for a malformed body, got %+v", CodeInvalidRequest, malformed)
	}

	// A JSON array is not a Nakama RPC payload object.
	_, arrayBody := postRPC(t, server, "auth.anonymous", `[1,2,3]`, nil)
	if arrayBody["ok"] != false || arrayBody["error_code"] != CodeInvalidRequest {
		t.Fatalf("expected %s for a non-object body, got %+v", CodeInvalidRequest, arrayBody)
	}
}

func TestHTTPRPCRejectsNonPostAndMissingID(t *testing.T) {
	server, _ := newRPCTestServer(t)
	defer server.Close()

	get, err := http.Get(server.URL + "/v2/rpc/auth.anonymous")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	get.Body.Close()
	if get.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", get.StatusCode)
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v2/rpc/", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.SetBasicAuth("defaulthttpkey", "")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty id status = %d, want 400", response.StatusCode)
	}
}

func TestHTTPRPCHandlerMatchesOnlyItsOwnPrefix(t *testing.T) {
	handler := NewHTTPRPCHandler(New(core.NewService(core.Config{})), HTTPRPCOptions{})
	if !handler.Matches("/v2/rpc/bootstrap") {
		t.Fatal("the handler must own /v2/rpc/")
	}
	if handler.Matches("/v1/bootstrap") || handler.Matches("/v2/rpc") {
		t.Fatal("the handler must not own /v1 or the bare prefix")
	}
	if handler.PathPrefix() != "/v2/rpc/" {
		t.Fatalf("PathPrefix = %q", handler.PathPrefix())
	}
}
