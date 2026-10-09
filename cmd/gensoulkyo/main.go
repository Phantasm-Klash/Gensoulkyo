package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"gensoulkyo/runtime/battlespawn"
	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/httpapi"
	"gensoulkyo/runtime/lobbyws"
	"gensoulkyo/runtime/storage"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7350", "HTTP listen address")
	dbConfig := storage.DatabaseConfigFromEnv()
	databaseDriver := flag.String("database-driver", dbConfig.Driver, "database/sql driver name for optional persistence")
	databaseURL := flag.String("database-url", dbConfig.URL, "database connection URL for optional persistence")
	migrationsDir := flag.String("migrations-dir", "migrations", "directory containing .up.sql migrations")
	migrateUp := flag.Bool("migrate-up", false, "apply pending .up.sql migrations before serving")
	flag.Parse()

	db, err := storage.OpenDatabase(storage.DatabaseConfig{Driver: *databaseDriver, URL: *databaseURL})
	if err != nil {
		log.Fatal(err)
	}
	if db != nil {
		defer db.Close()
		if *migrateUp {
			migrations, err := storage.LoadUpMigrations(*migrationsDir)
			if err != nil {
				log.Fatal(err)
			}
			applied, err := storage.ApplyUpMigrations(db, migrations)
			if err != nil {
				log.Fatal(err)
			}
			log.Printf("Applied %d migration(s): %v", len(applied), applied)
		}
	}

	service := core.NewService(core.Config{})
	handler := httpapi.New(service)
	if db != nil {
		wired, err := httpapi.NewWithDatabase(db)
		if err != nil {
			log.Fatal(err)
		}
		service = wired.Service
		handler = wired.Handler
	}

	lobbyEndpoint := strings.TrimSpace(os.Getenv("GENSOULKYO_LOBBY_ENDPOINT"))
	if lobbyEndpoint == "" {
		lobbyEndpoint = *addr
	}
	battleRuleset := strings.TrimSpace(os.Getenv("GENSOULKYO_BATTLE_RULESET"))
	spawner := battlespawn.NewSpawner(battlespawn.Config{
		BinaryPath:    strings.TrimSpace(os.Getenv(battlespawn.EnvBinary)),
		AdvertiseHost: strings.TrimSpace(os.Getenv(battlespawn.EnvAdvertiseHost)),
		LobbyEndpoint: lobbyEndpoint,
		Ruleset:       battleRuleset,
	})
	// Every spawned battle server must have both a tick budget and a wall-clock
	// deadline, otherwise an abandoned match leaks a process (and its KCP session
	// and simulation state) until the host runs out of memory. The spawner
	// defaults both, so the lobby only needs to pass through explicit overrides.
	lobby := lobbyws.New(lobbyws.Options{
		Service:       service,
		Spawner:       spawner,
		LobbyEndpoint: lobbyEndpoint,
		BattleRuleset: battleRuleset,
		MatchTTL:      envDuration("GENSOULKYO_MATCH_TTL"),
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/lobby/ws", lobby.HandleLobby)
	mux.HandleFunc("/v1/battle/relay", lobby.HandleRelay)
	mux.HandleFunc("/internal/battle/result", lobby.HandleBattleResult)
	mux.Handle("/", handler)

	cors := httpapi.CORSConfigFromEnv()
	rootHandler := cors.Middleware(mux)

	server := &http.Server{
		Addr:              *addr,
		Handler:           rootHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf(
		"Gensoulkyo %s listening on http://%s (lobby ws %s, battle bin %s, max-ticks %d, match-ttl %s)",
		core.ServerVersion, *addr, lobbyEndpoint, spawner.Config().BinaryPath,
		spawner.Config().MaxTicks, spawner.Config().MatchTTL,
	)
	log.Fatal(server.ListenAndServe())
}

// envDuration parses a Go duration string (e.g. "15m", "90s") from the
// environment. An unset or malformed value yields 0 so the caller's default
// applies.
func envDuration(key string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}
