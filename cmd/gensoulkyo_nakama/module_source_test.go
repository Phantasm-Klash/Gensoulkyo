package main

import (
	"os"
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
