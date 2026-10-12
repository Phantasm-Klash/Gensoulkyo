package main

import (
	"os"
	"strings"
	"testing"
)

// TestNakamaBindingRegistersTheExtraRPCSurfaces pins the two registration lists
// that sit outside the business operation contract:
//
//   - the pre-auth account operations, which cannot carry a signed business
//     envelope and therefore must not appear in the contract (whose published
//     guard fields require BusinessEnvelopeRequired), and
//   - the alias spellings the client's REST route table also publishes, so the
//     REST and RPC transports stay interchangeable instead of the RPC one
//     answering 404 for a route the REST one serves.
func TestNakamaBindingRegistersTheExtraRPCSurfaces(t *testing.T) {
	source, err := os.ReadFile("module.go")
	if err != nil {
		t.Fatalf("read module source: %v", err)
	}
	text := string(source)

	registryText := runtimeRegistryFunctionText(t, text)
	for _, expected := range []string{"rpcPreAuthIDs", "rpcAliasIDs"} {
		if !strings.Contains(registryText, expected) {
			t.Fatalf("runtimeRPCIDs must register %s:\n%s", expected, registryText)
		}
	}

	for _, id := range []string{
		"auth.anonymous",
		"auth.emailCode",
		"auth.register",
		"auth.login",
		"chests.get",
		"rooms.messages",
	} {
		if !strings.Contains(text, `"`+id+`"`) {
			t.Fatalf("module.go must register extra rpc id %q", id)
		}
	}
}
