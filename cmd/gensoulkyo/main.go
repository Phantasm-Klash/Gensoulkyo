package main

import (
	"database/sql"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"gensoulkyo/runtime/account"
	"gensoulkyo/runtime/battlespawn"
	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/httpapi"
	"gensoulkyo/runtime/lobbyws"
	"gensoulkyo/runtime/nakamaapi"
	"gensoulkyo/runtime/security"
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

	// --- shared business state -------------------------------------------------
	// Both HTTP faces (`/v1/...` for the client and battle callbacks, `/v2/rpc/...`
	// for the Nakama protocol) MUST be served by one core.Service. Two Services
	// would each own their own in-memory registries, so a battle server
	// registered over `/v2/rpc` would be invisible to the match allocation
	// performed over `/v1`, and every match would silently fall back to the
	// local spawner.
	battleRepo, lobbyRepo, demRepo, guard := sharedBusinessState(db)

	// E-mail + password accounts. Credentials live in Postgres so they survive a
	// restart; verification codes stay in memory because they are short-lived by
	// design. Without a database the lobby still starts, but accounts are
	// process-local (documented on the log line below).
	mailer := account.NewSMTPMailer(account.SMTPConfigFromEnv())
	var accounts *account.Service
	if db != nil {
		store, err := account.NewSQLStore(db)
		if err != nil {
			log.Fatal(err)
		}
		accounts = account.NewService(account.Config{Store: store, Mailer: mailer})
	} else {
		log.Printf("account: no database configured; accounts are in-memory and will not survive a restart")
		accounts = account.NewService(account.Config{Mailer: mailer})
	}
	if mailer.Enabled() {
		config := mailer.Config()
		// Host/port only: never log the relay password.
		log.Printf("account: SMTP relay %s:%d (ssl=%t starttls=%t) as %s",
			config.Host, config.Port, config.UseSSL, config.UseSTARTTLS, config.From)
	} else {
		log.Printf("account: SMTP relay is not configured; registration e-mail cannot be sent")
	}

	// The battle-callback master secret MUST be stable across restarts: every
	// battle server's callback secret is derived from it
	// (hex(HMAC-SHA256(master, battleServerID))), so a per-process random master
	// silently rotates all of them on restart and every in-flight server gets
	// 403 until it re-registers. Deployments inject it through the same 0600
	// EnvironmentFile as the other secrets. When it is absent we still fall back
	// to a random per-process secret (core.NewService), but say so loudly here
	// instead of silently breaking callbacks.
	callbackSecret := envOr("GENSOULKYO_CALLBACK_SECRET", "")
	if callbackSecret == "" {
		log.Printf("callback-auth: GENSOULKYO_CALLBACK_SECRET is not set; the lobby will generate a random per-process battle-callback master secret, so every battle server gets 403 after a restart until it re-registers. Set it (64 hex chars) in the lobby EnvironmentFile (e.g. /etc/gotouhou/gensoulkyo-lobby.env).")
	}

	service := core.NewService(core.Config{
		BattleLifecycleAuditRepo: battleRepo,
		LobbyLifecycleAuditRepo:  lobbyRepo,
		DemRepository:            demRepo,
		Accounts:                 accounts,
		CallbackSecret:           callbackSecret,
	})
	handler := httpapi.NewWithOptions(service, httpapi.WithBusinessEnvelopeGuard(guard))

	// The Nakama-compatible RPC face. Out-of-process battle agents speak only
	// this protocol, so without it a remote agent cannot register, heartbeat or
	// poll for assignments.
	rpcHandler := nakamaapi.NewHTTPRPCHandler(
		nakamaapi.New(service, nakamaapi.WithBusinessEnvelopeGuard(guard)),
		nakamaapi.HTTPRPCOptions{
			ServiceCallbackKey: envOr("GENSOULKYO_SERVICE_CALLBACK_KEY", "defaulthttpkey"),
			RuntimeHTTPKey:     envOr("GENSOULKYO_RUNTIME_HTTP_KEY", "defaulthttpkey"),
		},
	)

	lobbyEndpoint := strings.TrimSpace(os.Getenv("GENSOULKYO_LOBBY_ENDPOINT"))
	if lobbyEndpoint == "" {
		lobbyEndpoint = *addr
	}
	battleRuleset := strings.TrimSpace(os.Getenv("GENSOULKYO_BATTLE_RULESET"))

	// The local spawner is opt-in: it only exists when a battle binary is
	// configured. Leaving it unset is how a deployment delegates matches to
	// registered remote battle servers (the RAKsmart agent), because
	// `lobbyws.startMatch` otherwise spawns a local process and rebinds the
	// allocation to it, so a remote server would never receive an assignment.
	var spawner *battlespawn.Spawner
	battleBin := strings.TrimSpace(os.Getenv(battlespawn.EnvBinary))
	if battleBin != "" {
		spawner = battlespawn.NewSpawner(battlespawn.Config{
			BinaryPath:    battleBin,
			AdvertiseHost: strings.TrimSpace(os.Getenv(battlespawn.EnvAdvertiseHost)),
			LobbyEndpoint: lobbyEndpoint,
			Ruleset:       battleRuleset,
		})
	}

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
	// Wire single sign-on: when a sign-in supersedes an existing session token,
	// core notifies the lobby so it can disconnect the superseded connection.
	// This must happen after both objects exist; the setter avoids a
	// construction cycle between Service and Server.
	service.SetSessionRevokedHook(lobby.OnSessionRevoked)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/lobby/ws", lobby.HandleLobby)
	mux.HandleFunc("/v1/battle/relay", lobby.HandleRelay)
	mux.HandleFunc("/internal/battle/result", lobby.HandleBattleResult)
	mux.Handle(rpcHandler.PathPrefix(), rpcHandler)
	mux.Handle("/", handler)

	cors := httpapi.CORSConfigFromEnv()
	rootHandler := cors.Middleware(mux)

	server := &http.Server{
		Addr:              *addr,
		Handler:           rootHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf(
		"Gensoulkyo %s listening on http://%s (lobby ws %s, battle spawner %s, match-ttl %s)",
		core.ServerVersion, *addr, lobbyEndpoint, spawnerMode(spawner),
		envDuration("GENSOULKYO_MATCH_TTL"),
	)
	log.Fatal(server.ListenAndServe())
}

// sharedBusinessState builds the audit repositories and the business-envelope
// guard that both HTTP faces share. A nil database leaves the repositories nil,
// which core treats as "auditing disabled".
func sharedBusinessState(db *sql.DB) (core.BattleLifecycleAuditRepository, core.LobbyLifecycleAuditRepository, core.DemRepository, *security.BusinessEnvelopeGuard) {
	if db == nil {
		return nil, nil, nil, security.NewBusinessEnvelopeGuard()
	}
	sink, err := security.NewSQLBusinessEnvelopeAuditSink(db)
	if err != nil {
		log.Fatal(err)
	}
	battleRepo, err := core.NewSQLBattleLifecycleAuditRepository(db)
	if err != nil {
		log.Fatal(err)
	}
	lobbyRepo, err := core.NewSQLLobbyLifecycleAuditRepository(db)
	if err != nil {
		log.Fatal(err)
	}
	demRepo, err := core.NewSQLDemStore(db)
	if err != nil {
		log.Fatal(err)
	}
	return battleRepo, lobbyRepo, demRepo, security.NewBusinessEnvelopeGuard(security.WithBusinessEnvelopeAuditSink(sink))
}

// spawnerMode describes where matches will run, for the startup log line.
func spawnerMode(spawner *battlespawn.Spawner) string {
	if spawner == nil {
		return "disabled (matches go to registered battle servers)"
	}
	return "local (" + spawner.Config().BinaryPath + ")"
}

// envOr returns the trimmed environment value, or fallback when it is unset.
func envOr(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
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
