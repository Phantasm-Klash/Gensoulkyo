package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"gensoulkyo/runtime/core"
	"gensoulkyo/runtime/httpapi"
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
	server := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("Gensoulkyo %s listening on http://%s", core.ServerVersion, *addr)
	log.Fatal(server.ListenAndServe())
}
