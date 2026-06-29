package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
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
		"nakamaapi.NewWithDatabase",
		"runtime.RUNTIME_CTX_SESSION_ID",
		"runtime.RUNTIME_CTX_USER_ID",
		"PayloadError: payloadError(payload)",
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
		"business.envelope.audit.status",
		"battle.audit.status",
		"lobby.audit.status",
		"battle.allocation",
		"battle.ticket",
		"replay.get",
		"battle.result.submit",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("Nakama binding source missing %q", expected)
		}
	}
}

func TestNakamaBindingRPCRegistryIsExact(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	got := extractRPCIDs(t, string(source))
	want := []string{
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
		"business.envelope.audit.status",
		"battle.audit.status",
		"lobby.audit.status",
		"battle.allocation",
		"battle.ticket",
		"replay.get",
		"battle.result.submit",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected Nakama RPC registry:\n got: %#v\nwant: %#v", got, want)
	}
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("duplicate Nakama RPC registration for %q", id)
		}
		seen[id] = true
	}
}

func TestNakamaBindingKeepsPublicResultSubmitFailClosed(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	text := string(source)
	for _, forbidden := range []string{
		"Service: true",
		"Service:true",
		"Service:     true",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("public Nakama RPC binding must not mark client RPCs as service-origin; found %q", forbidden)
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
}

func extractRPCIDs(t *testing.T, source string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "module.go", source, 0)
	if err != nil {
		t.Fatalf("parse module source: %v", err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Names) == 0 || valueSpec.Names[0].Name != "rpcIDs" || len(valueSpec.Values) != 1 {
				continue
			}
			literal, ok := valueSpec.Values[0].(*ast.CompositeLit)
			if !ok {
				t.Fatalf("rpcIDs must be declared as a composite literal")
			}
			out := make([]string, 0, len(literal.Elts))
			for _, element := range literal.Elts {
				basic, ok := element.(*ast.BasicLit)
				if !ok || basic.Kind != token.STRING {
					t.Fatalf("rpcIDs contains non-string element %#v", element)
				}
				out = append(out, strings.Trim(basic.Value, `"`))
			}
			return out
		}
	}
	t.Fatalf("rpcIDs registry not found in module.go")
	return nil
}
