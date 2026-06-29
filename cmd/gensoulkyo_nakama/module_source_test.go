package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
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
		"initializer.RegisterRpc",
		"nakamaapi.New",
		"security.NewSQLBusinessEnvelopeAuditSink",
		"core.NewSQLBattleLifecycleAuditRepository",
		"core.NewSQLLobbyLifecycleAuditRepository",
		"BattleLifecycleAuditRepo",
		"LobbyLifecycleAuditRepo",
		"runtime.RUNTIME_CTX_SESSION_ID",
		"runtime.RUNTIME_CTX_USER_ID",
		"auth.anonymous",
		"bootstrap",
		"matchmaking.join",
		"rooms.list",
		"rooms.rules",
		"rooms.leave",
		"rooms.message",
		"rooms.chat",
		"rooms.announcement",
		"match.ready",
		"battle.audit.status",
		"lobby.audit.status",
		"battle.allocation",
		"battle.ticket",
		"battle.result.submit",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Nakama binding source missing %q", expected)
		}
	}
}

func TestNakamaBindingSourceDeclaresExactRPCSurface(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	rpcs := stringSliceVar(t, source, "rpcIDs")
	expected := []string{
		"auth.anonymous",
		"bootstrap",
		"inventory.get",
		"cards.upgrade",
		"decks.list",
		"decks.save",
		"chests.list",
		"chests.open",
		"presence.heartbeat",
		"matchmaking.join",
		"matchmaking.ticket",
		"matchmaking.cancel",
		"rooms.create",
		"rooms.list",
		"rooms.get",
		"rooms.rules",
		"rooms.join",
		"rooms.leave",
		"rooms.message",
		"rooms.chat",
		"rooms.announcement",
		"match.ready",
		"activity.claim",
		"battle.servers",
		"battle.audit.status",
		"lobby.audit.status",
		"battle.allocation",
		"battle.ticket",
		"battle.result.submit",
	}
	if !reflect.DeepEqual(rpcs, expected) {
		t.Fatalf("rpcIDs changed unexpectedly:\n got %#v\nwant %#v", rpcs, expected)
	}
	seen := map[string]bool{}
	for _, rpc := range rpcs {
		if seen[rpc] {
			t.Fatalf("duplicate rpc id %q in rpcIDs", rpc)
		}
		seen[rpc] = true
	}
}

func TestNakamaBindingSourceWiresSQLAuditRepositoriesBehindDBGate(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	text := string(source)
	dbGateIndex := strings.Index(text, "if db != nil {")
	if dbGateIndex < 0 {
		t.Fatalf("expected audit repository wiring to be gated by db != nil")
	}
	for _, expected := range []string{
		"security.NewSQLBusinessEnvelopeAuditSink(db)",
		"core.NewSQLBattleLifecycleAuditRepository(db)",
		"core.NewSQLLobbyLifecycleAuditRepository(db)",
	} {
		index := strings.Index(text, expected)
		if index < 0 {
			t.Fatalf("Nakama binding source missing %q", expected)
		}
		if index < dbGateIndex {
			t.Fatalf("%q must stay inside db != nil gate", expected)
		}
	}
	for _, expected := range []string{
		"BattleLifecycleAuditRepo: battleAuditRepo",
		"LobbyLifecycleAuditRepo:  lobbyAuditRepo",
		"nakamaapi.WithBusinessEnvelopeGuard(guard)",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Nakama binding source missing service/handler wiring %q", expected)
		}
	}
}

func stringSliceVar(t *testing.T, source []byte, varName string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "module.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse module source: %v", err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range value.Names {
				if name.Name != varName {
					continue
				}
				if index >= len(value.Values) {
					t.Fatalf("var %s has no initializer", varName)
				}
				literal, ok := value.Values[index].(*ast.CompositeLit)
				if !ok {
					t.Fatalf("var %s is not initialized with a composite literal", varName)
				}
				out := make([]string, 0, len(literal.Elts))
				for _, element := range literal.Elts {
					basic, ok := element.(*ast.BasicLit)
					if !ok || basic.Kind != token.STRING {
						t.Fatalf("var %s contains non-string element %#v", varName, element)
					}
					text, err := strconv.Unquote(basic.Value)
					if err != nil {
						t.Fatalf("unquote %s element %q: %v", varName, basic.Value, err)
					}
					out = append(out, text)
				}
				return out
			}
		}
	}
	t.Fatalf("var %s not found", varName)
	return nil
}
