package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"gensoulkyo/runtime/core"
)

func TestNakamaBindingSourceListsRuntimeEntrypoints(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	text := string(source)
	for _, expected := range []string{
		"//go:build nakama",
		"func InitModule(",
		"var rpcIDs = runtimeRPCIDs()",
		"func runtimeRPCIDs()",
		"initializer.RegisterRpc",
		"nakamaapi.New",
		"nakamaapi.NewWithDatabase",
		"core.ContractClientRPCOperations()",
		"core.ServiceCallbackOperations()",
		"runtime.RUNTIME_CTX_SESSION_ID",
		"runtime.RUNTIME_CTX_USER_ID",
		"runtime.RUNTIME_CTX_MODE",
		"runtime.RUNTIME_CTX_VARS",
		"PayloadError: payloadError(payload)",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Nakama binding source missing %q", expected)
		}
	}
}

func TestNakamaBindingRPCRegistryIsDerivedFromCoreContracts(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "module.go", string(source), 0)
	if err != nil {
		t.Fatalf("parse module source: %v", err)
	}
	registry := findFuncDecl(file, "runtimeRPCIDs")
	if registry == nil {
		t.Fatalf("runtimeRPCIDs registry helper not found")
	}
	registryText := string(source[registry.Pos()-1 : registry.End()-1])
	for _, expected := range []string{
		"core.ContractClientRPCOperations()",
		"core.ServiceCallbackOperations()",
		"append([]string{}, core.ContractClientRPCOperations()...)",
		"seen := map[string]struct{}{}",
		"if _, ok := seen[id]; ok",
		"continue",
	} {
		if !strings.Contains(registryText, expected) {
			t.Fatalf("runtimeRPCIDs must derive from core contracts and de-duplicate service callbacks; missing %q in:\n%s", expected, registryText)
		}
	}
}

func TestNakamaBindingRegistersEveryCoreServiceCallback(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	registryText := runtimeRegistryFunctionText(t, string(source))
	expected := append([]string{}, core.ContractClientRPCOperations()...)
	seen := map[string]int{}
	for _, id := range expected {
		seen[id]++
	}
	for _, callback := range core.ServiceCallbackOperations() {
		if !strings.Contains(registryText, "core.ServiceCallbackOperations()") {
			t.Fatalf("Nakama RPC registry must include service callback %q through core.ServiceCallbackOperations()", callback)
		}
		if seen[callback] > 0 {
			t.Fatalf("client RPC contract must not duplicate service callback %q: rpc_contract=%+v callbacks=%+v", callback, core.ContractClientRPCOperations(), core.ServiceCallbackOperations())
		}
		seen[callback]++
	}
}

func TestNakamaBindingRegistersEveryCoreClientRPCOperation(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	registryText := runtimeRegistryFunctionText(t, string(source))
	if !strings.Contains(registryText, "core.ContractClientRPCOperations()") {
		t.Fatalf("Nakama RPC registry must include every core client RPC operation through core.ContractClientRPCOperations():\n%s", registryText)
	}
	registered := map[string]int{}
	for _, operation := range core.ContractClientRPCOperations() {
		registered[operation]++
	}
	for _, operation := range core.ContractClientWSSOperations() {
		if !sliceContains(core.ContractClientRPCOperations(), operation) {
			t.Fatalf("Nakama binding can only derive WSS operations that are also RPC operations; WSS-only operation %q would need explicit binding policy", operation)
		}
	}
	for operation, count := range registered {
		if count != 1 {
			t.Fatalf("client RPC contract contains duplicate operation %q: %+v", operation, core.ContractClientRPCOperations())
		}
	}
}

func TestNakamaBindingDoesNotRegisterDisallowedClientOperations(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	registryText := runtimeRegistryFunctionText(t, string(source))
	if !strings.Contains(registryText, "core.ContractClientRPCOperations()") {
		t.Fatalf("Nakama RPC registry must derive client operations from core contract:\n%s", registryText)
	}
	for _, disallowed := range core.ContractDisallowedClientOperations() {
		if core.IsServiceCallbackOperation(disallowed) {
			continue
		}
		if sliceContains(core.ContractClientRPCOperations(), disallowed) {
			t.Fatalf("Nakama RPC registry must not expose disallowed client operation %q through core RPC contract: rpc_contract=%+v", disallowed, core.ContractClientRPCOperations())
		}
	}
}

func TestNakamaBindingKeepsServiceOriginRPCsFailClosed(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	text := string(source)
	if strings.Contains(text, "Service: true") || strings.Contains(text, "Service:true") {
		t.Fatalf("public Nakama RPC binding must not mark every RPC as service-origin")
	}
	for _, expected := range []string{
		"Service:      isServiceOriginRPC(ctx, rpcID)",
		"var serviceOriginRPCIDs = serviceOriginRPCIDSet()",
		"func isServiceOriginRPC(ctx context.Context, rpcID string) bool",
		"func serviceOriginRPCIDSet() map[string]struct{}",
		"core.ServiceCallbackOperations()",
		"runtimeCtxString(ctx, runtime.RUNTIME_CTX_SESSION_ID) != \"\"",
		"runtimeCtxString(ctx, runtime.RUNTIME_CTX_USER_ID) != \"\"",
		"runtimeCtxString(ctx, runtime.RUNTIME_CTX_MODE)",
		"serviceCallbackContextExpected(serviceRuntimeModeKey)",
		"mode != expectedMode",
		"runtimeCtxStringMap(ctx, runtime.RUNTIME_CTX_VARS)",
		"serviceRuntimeModeKey",
		"core.ServiceCallbackRuntimeModeKey",
		"serviceOriginVarKey",
		"core.ServiceCallbackOriginKey",
		"serviceCallbackVarKey",
		"core.ServiceCallbackFlagKey",
		"core.ServiceCallbackContext()",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Nakama binding service-origin gate missing %q", expected)
		}
	}
	if !strings.Contains(text, "NewWithDatabase(db)") {
		t.Fatalf("Nakama binding must wire Nakama *sql.DB through NewWithDatabase")
	}
	if !strings.Contains(text, "PayloadError: payloadError(payload)") || !strings.Contains(text, "json.Unmarshal([]byte(payload), &out)") {
		t.Fatalf("Nakama binding must reject malformed JSON payloads before runtime dispatch")
	}
	if strings.Contains(text, "battle.result.submit") && !strings.Contains(text, "runtimeCtxString(ctx, runtime.RUNTIME_CTX_SESSION_ID)") {
		t.Fatalf("Nakama binding must continue extracting session context for registered RPCs")
	}
	if !strings.Contains(text, "runtimeCtxString(ctx, runtime.RUNTIME_CTX_USER_ID)") {
		t.Fatalf("Nakama binding must pass user context through runtime/nakamaapi so service-origin result callbacks can reject player-scoped requests")
	}
	registryText := runtimeRegistryFunctionText(t, text)
	if !strings.Contains(registryText, "core.ServiceCallbackOperations()") {
		t.Fatalf("Nakama binding must register service-origin RPCs from core.ServiceCallbackOperations():\n%s", registryText)
	}
}

func TestNakamaBindingServiceOriginContextGateRequiresBattleCallbackVars(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "module.go", string(source), 0)
	if err != nil {
		t.Fatalf("parse module source: %v", err)
	}
	gate := findFuncDecl(file, "isServiceOriginRPC")
	if gate == nil {
		t.Fatalf("isServiceOriginRPC not found")
	}
	gateText := string(source[gate.Pos()-1 : gate.End()-1])
	for _, expected := range []string{
		"serviceOriginRPCIDs[rpcID]",
		"RUNTIME_CTX_SESSION_ID",
		"RUNTIME_CTX_USER_ID",
		"serviceCallbackContextExpected(serviceRuntimeModeKey)",
		"if !ok",
		"mode != expectedMode",
		"RUNTIME_CTX_VARS",
		"serviceCallbackContextExpected(serviceOriginVarKey)",
		"vars[serviceOriginVarKey]",
		"expectedOrigin",
		"serviceCallbackContextExpected(serviceCallbackVarKey)",
		"vars[serviceCallbackVarKey]",
		"core.ServiceCallbackAcceptedValues()",
		"callbackValue",
	} {
		if !strings.Contains(gateText, expected) {
			t.Fatalf("service-origin context gate missing %q in:\n%s", expected, gateText)
		}
	}
	if strings.Contains(gateText, "mode != \"\" && mode != \"client\"") {
		t.Fatalf("service-origin context gate must not treat any non-client mode as trusted:\n%s", gateText)
	}
	contextHelper := findFuncDecl(file, "serviceCallbackContextValue")
	if contextHelper == nil {
		t.Fatalf("serviceCallbackContextValue helper not found")
	}
	contextHelperText := string(source[contextHelper.Pos()-1 : contextHelper.End()-1])
	for _, expected := range []string{
		"serviceCallbackContextExpected(key)",
	} {
		if !strings.Contains(contextHelperText, expected) {
			t.Fatalf("service callback context helper missing %q in:\n%s", expected, contextHelperText)
		}
	}
	expectedHelper := findFuncDecl(file, "serviceCallbackContextExpected")
	if expectedHelper == nil {
		t.Fatalf("serviceCallbackContextExpected helper not found")
	}
	expectedHelperText := string(source[expectedHelper.Pos()-1 : expectedHelper.End()-1])
	for _, expected := range []string{
		"core.ServiceCallbackContext()[key]",
		"strings.ToLower",
		"strings.TrimSpace",
		"value != \"\"",
	} {
		if !strings.Contains(expectedHelperText, expected) {
			t.Fatalf("service callback expected helper missing %q in:\n%s", expected, expectedHelperText)
		}
	}
	mapHelper := findFuncDecl(file, "runtimeCtxStringMap")
	if mapHelper == nil {
		t.Fatalf("runtimeCtxStringMap helper not found")
	}
	mapHelperText := string(source[mapHelper.Pos()-1 : mapHelper.End()-1])
	for _, expected := range []string{
		"case map[string]string",
		"case map[string]any",
		"map[string]string{}",
	} {
		if !strings.Contains(mapHelperText, expected) {
			t.Fatalf("runtime context vars helper missing %q in:\n%s", expected, mapHelperText)
		}
	}
}

func TestNakamaBindingDocumentsCoreServiceCallbackContext(t *testing.T) {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read Nakama binding README: %v", err)
	}
	text := string(readme)
	context := core.ServiceCallbackContext()
	for _, expected := range []string{
		core.ServiceCallbackOriginKey + "=" + context[core.ServiceCallbackOriginKey],
		core.ServiceCallbackFlagKey + "=" + context[core.ServiceCallbackFlagKey],
		"`runtime.RUNTIME_CTX_MODE` is `" + context[core.ServiceCallbackRuntimeModeKey],
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Nakama binding README must document core service callback context %q", expected)
		}
	}
	for _, accepted := range core.ServiceCallbackAcceptedValues() {
		if !strings.Contains(text, accepted) {
			t.Fatalf("Nakama binding README must document accepted service callback flag %q", accepted)
		}
	}
	for _, callback := range core.ServiceCallbackOperations() {
		if !strings.Contains(text, callback) {
			t.Fatalf("Nakama binding README must document service callback operation %q", callback)
		}
	}
}

func TestNakamaTagBuildComposeProfileDocumentsTemporarySDKPin(t *testing.T) {
	compose, err := os.ReadFile("../../docker-compose.yml")
	if err != nil {
		t.Fatalf("read docker compose: %v", err)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read Nakama binding README: %v", err)
	}
	for _, expected := range []string{
		"nakama-tag-build",
		"NAKAMA_COMMON_VERSION",
		"GOSUMDB",
		"v1.34.0",
		"go mod edit -replace github.com/phantasm-klash/phk-protocol=/workspace/PhK-Protocol",
		"go get github.com/heroiclabs/nakama-common/runtime@$${NAKAMA_COMMON_VERSION}",
		"go test -tags nakama ./cmd/gensoulkyo_nakama ./runtime/...",
		"go build -tags nakama -buildmode=plugin",
	} {
		if !strings.Contains(string(compose), expected) {
			t.Fatalf("Nakama tag-build compose profile missing %q", expected)
		}
	}
	for _, expected := range []string{
		"docker-compose --profile nakama-tag-build run --rm nakama-tag-build",
		"-e GOPROXY=https://goproxy.cn,direct -e GOSUMDB=off",
		"without mutating the repository's `go.mod`/`go.sum`",
		"github.com/heroiclabs/nakama-common/runtime",
		"`v1.34.0`",
	} {
		if !strings.Contains(string(readme), expected) {
			t.Fatalf("Nakama binding README missing %q", expected)
		}
	}
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func runtimeRegistryFunctionText(t *testing.T, source string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "module.go", source, 0)
	if err != nil {
		t.Fatalf("parse module source: %v", err)
	}
	registry := findFuncDecl(file, "runtimeRPCIDs")
	if registry == nil {
		t.Fatalf("runtimeRPCIDs registry helper not found")
	}
	return string(source[registry.Pos()-1 : registry.End()-1])
}

func sliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
