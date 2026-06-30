//go:build nakama

package main

import (
	"context"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
)

func TestServiceOriginRPCContextGate(t *testing.T) {
	base := context.WithValue(context.Background(), runtime.RUNTIME_CTX_MODE, "rpc")
	trustedVars := map[string]string{
		serviceOriginVarKey:   serviceCallbackContextValue(serviceOriginVarKey),
		serviceCallbackVarKey: "true",
	}
	if isServiceOriginRPC(base, "battle.result.submit") {
		t.Fatalf("service-origin RPC must require trusted battle callback vars")
	}
	withVars := context.WithValue(base, runtime.RUNTIME_CTX_VARS, trustedVars)
	if !isServiceOriginRPC(withVars, "battle.result.submit") {
		t.Fatalf("trusted battle callback vars should allow service-origin RPC")
	}
	withAnyVars := context.WithValue(base, runtime.RUNTIME_CTX_VARS, map[string]any{
		serviceOriginVarKey:   serviceCallbackContextValue(serviceOriginVarKey),
		serviceCallbackVarKey: "yes",
	})
	if !isServiceOriginRPC(withAnyVars, "battle.ticket.consume") {
		t.Fatalf("trusted battle callback vars from map[string]any should allow service-origin RPC")
	}
	withNumericCallback := context.WithValue(base, runtime.RUNTIME_CTX_VARS, map[string]string{
		serviceOriginVarKey:   serviceCallbackContextValue(serviceOriginVarKey),
		serviceCallbackVarKey: "1",
	})
	if !isServiceOriginRPC(withNumericCallback, "battle.servers.heartbeat") {
		t.Fatalf("numeric battle callback var should allow service-origin RPC")
	}
	for name, vars := range map[string]map[string]string{
		"wrong origin":     {serviceOriginVarKey: "player", serviceCallbackVarKey: "true"},
		"missing callback": {serviceOriginVarKey: serviceCallbackContextValue(serviceOriginVarKey)},
		"false callback":   {serviceOriginVarKey: serviceCallbackContextValue(serviceOriginVarKey), serviceCallbackVarKey: "false"},
	} {
		untrusted := context.WithValue(base, runtime.RUNTIME_CTX_VARS, vars)
		if isServiceOriginRPC(untrusted, "battle.result.submit") {
			t.Fatalf("%s vars must not allow service-origin RPC", name)
		}
	}
	withPlayerSession := context.WithValue(withVars, runtime.RUNTIME_CTX_SESSION_ID, "player-session")
	if isServiceOriginRPC(withPlayerSession, "battle.result.submit") {
		t.Fatalf("player-scoped context must never become service-origin")
	}
	withPlayerUser := context.WithValue(withVars, runtime.RUNTIME_CTX_USER_ID, "player-user")
	if isServiceOriginRPC(withPlayerUser, "battle.result.submit") {
		t.Fatalf("player user context must never become service-origin")
	}
	nonRPCMode := context.WithValue(context.WithValue(context.Background(), runtime.RUNTIME_CTX_MODE, "match"), runtime.RUNTIME_CTX_VARS, trustedVars)
	if isServiceOriginRPC(nonRPCMode, "battle.result.submit") {
		t.Fatalf("non-rpc runtime modes must not be treated as service-origin callbacks")
	}
	if isServiceOriginRPC(withVars, "bootstrap") {
		t.Fatalf("non-callback RPC must not be allowed by service-origin vars")
	}
}
