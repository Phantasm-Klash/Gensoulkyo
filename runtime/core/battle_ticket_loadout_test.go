package core

import (
	"encoding/json"
	"strconv"
	"testing"
)

// W-1: the signed battle ticket must carry the opening hand (<=4 catalog
// cards), the opening energy and the authoritative lobby seed as decimal text.
func TestBattleTicketIssuesOpeningLoadout(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Loadout Alice")
	bob := mustLogin(t, service, "Loadout Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")

	ticket, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("battle ticket: %v", err)
	}
	if len(ticket.Ticket.HandCardIDs) != cardHandLimit {
		t.Fatalf("opening hand size = %d, want %d (%v)", len(ticket.Ticket.HandCardIDs), cardHandLimit, ticket.Ticket.HandCardIDs)
	}
	for _, cardID := range ticket.Ticket.HandCardIDs {
		if _, ok := serverCardCatalog[cardID]; !ok {
			t.Fatalf("hand card %q is not in the server catalog", cardID)
		}
	}
	// The hand is the deck prefix in draw order (initialHand skips empty ids).
	service.mu.Lock()
	match := service.matches[matchID]
	service.mu.Unlock()
	if match == nil {
		t.Fatal("match missing after queue fill")
	}
	player := match.Players[alice.UserID]
	if player == nil {
		t.Fatal("player missing after queue fill")
	}
	wantHand, _ := initialHand(player.DeckSnapshot)
	if len(wantHand) != len(ticket.Ticket.HandCardIDs) {
		t.Fatalf("hand = %v, want deck prefix %v", ticket.Ticket.HandCardIDs, wantHand)
	}
	for index, cardID := range wantHand {
		if ticket.Ticket.HandCardIDs[index] != cardID {
			t.Fatalf("hand[%d] = %q, want deck prefix %q", index, ticket.Ticket.HandCardIDs[index], cardID)
		}
	}
	if ticket.Ticket.InitialEnergy != cardStartingEnergy {
		t.Fatalf("initial energy = %v, want %v", ticket.Ticket.InitialEnergy, cardStartingEnergy)
	}
	wantSeed := strconv.FormatInt(seedFrom(matchID), 10)
	if ticket.Ticket.ServerSeedDecimal != wantSeed {
		t.Fatalf("server_seed = %q, want lobby seedFrom(%q) = %q", ticket.Ticket.ServerSeedDecimal, matchID, wantSeed)
	}
	if ticket.Ticket.ServerSeedDecimal != strconv.FormatInt(match.ServerSeed, 10) {
		t.Fatalf("server_seed = %q must equal match.ServerSeed = %d", ticket.Ticket.ServerSeedDecimal, match.ServerSeed)
	}
	if !verifySignedBattleTicket(t, ticket) {
		t.Fatal("issued ticket signature must verify over the marshaled ticket JSON")
	}
}

// W-1: server_seed must serialize as a decimal JSON string so seeds above 2^53
// survive the JS client pass-through, and the ED25519 signature must cover the
// opening loadout fields.
func TestBattleTicketServerSeedIsDecimalStringCoveredBySignature(t *testing.T) {
	service := NewService(Config{})
	alice := mustLogin(t, service, "Seed Alice")
	bob := mustLogin(t, service, "Seed Bob")
	matchID := matchTwoPlayers(t, service, alice, bob, "pvp_duel")

	ticket, err := service.BattleTicket(alice.SessionToken, matchID)
	if err != nil {
		t.Fatalf("battle ticket: %v", err)
	}
	payload, err := json.Marshal(ticket.Ticket)
	if err != nil {
		t.Fatalf("marshal ticket: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal ticket: %v", err)
	}
	seed, ok := decoded["server_seed"].(string)
	if !ok {
		t.Fatalf("server_seed must be a JSON string, got %T (%v)", decoded["server_seed"], decoded["server_seed"])
	}
	parsed, err := strconv.ParseInt(seed, 10, 64)
	if err != nil {
		t.Fatalf("server_seed %q must be a decimal int64 text: %v", seed, err)
	}
	service.mu.Lock()
	match := service.matches[matchID]
	service.mu.Unlock()
	if parsed != match.ServerSeed {
		t.Fatalf("server_seed text %q parses to %d, want match.ServerSeed %d", seed, parsed, match.ServerSeed)
	}

	// Tampering with any opening-loadout field must break the signature.
	tampered := *ticket
	tampered.Ticket.HandCardIDs = []string{"finale_arc", "finale_arc", "finale_arc", "finale_arc"}
	if verifySignedBattleTicket(t, &tampered) {
		t.Fatal("hand tampering must invalidate the ticket signature")
	}
	tampered = *ticket
	tampered.Ticket.InitialEnergy = 99.0
	if verifySignedBattleTicket(t, &tampered) {
		t.Fatal("energy tampering must invalidate the ticket signature")
	}
	tampered = *ticket
	tampered.Ticket.ServerSeedDecimal = "1"
	if verifySignedBattleTicket(t, &tampered) {
		t.Fatal("seed tampering must invalidate the ticket signature")
	}
}

// W-1: seedFrom stays exact through decimal text for match ids whose seed
// exceeds 2^53 (a JS number would narrow those).
func TestLobbySeedDecimalTextRoundTripsForLargeSeeds(t *testing.T) {
	matchID := ""
	for index := 0; index < 100000 && matchID == ""; index++ {
		candidate := "seed-overshoot-" + strconv.Itoa(index)
		if seedFrom(candidate) > 1<<53 {
			matchID = candidate
		}
	}
	if matchID == "" {
		t.Fatal("no match id with seed > 2^53 found")
	}
	seed := seedFrom(matchID)
	text := strconv.FormatInt(seed, 10)
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || parsed != seed {
		t.Fatalf("decimal text %q must round-trip int64 exactly, got %d err=%v", text, parsed, err)
	}
}
