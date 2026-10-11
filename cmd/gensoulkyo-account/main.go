// Command gensoulkyo-account creates or resets an e-mail + password account
// directly in the lobby database.
//
// It exists for operator tasks that cannot go through the self-service flow:
// seeding the first account on a fresh deployment, and rotating a password when
// the owner has lost access to the mailbox that would receive a reset code.
//
// Usage (inside the pluginbuilder image, from the Gensoulkyo working copy):
//
//	GENSOULKYO_DATABASE_URL=postgres://... \
//	  go run ./cmd/gensoulkyo-account \
//	    -username wjcwqc -email wjcwqc@gmail.com -password 'secret'
//
// Prefer GENSOULKYO_SEED_PASSWORD over -password: a command-line flag is
// visible to every local user through `ps`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"gensoulkyo/runtime/account"
	"gensoulkyo/runtime/storage"
)

func main() {
	dbConfig := storage.DatabaseConfigFromEnv()
	databaseDriver := flag.String("database-driver", dbConfig.Driver, "database/sql driver name")
	databaseURL := flag.String("database-url", dbConfig.URL, "database connection URL")
	username := flag.String("username", "", "account username (3-24 letters, digits or underscores)")
	email := flag.String("email", "", "account e-mail; this is also the sign-in identifier")
	password := flag.String("password", "", "account password (or set GENSOULKYO_SEED_PASSWORD)")
	displayName := flag.String("display-name", "", "display name; defaults to the username")
	verified := flag.Bool("verified", true, "mark the e-mail as verified so the account can sign in")
	flag.Parse()

	secret := *password
	if env := strings.TrimSpace(os.Getenv("GENSOULKYO_SEED_PASSWORD")); env != "" {
		secret = env
	}
	if err := validateInputs(*username, *email, secret); err != nil {
		log.Fatal(err)
	}

	db, err := storage.OpenDatabase(storage.DatabaseConfig{Driver: *databaseDriver, URL: *databaseURL})
	if err != nil {
		log.Fatal(err)
	}
	if db == nil {
		log.Fatal("a database is required: set GENSOULKYO_DATABASE_URL or pass -database-url")
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("database is unreachable: %v", err)
	}

	store, err := account.NewSQLStore(db)
	if err != nil {
		log.Fatal(err)
	}

	// Keep the existing account id when the address is already registered:
	// the lobby keys per-user state (and the derived player id) off it, so a
	// reset must not look like a brand-new player.
	accountID := account.NewAccountID()
	switch existing, err := store.AccountByEmail(ctx, *email); {
	case err == nil && existing != nil:
		accountID = existing.AccountID
	case err != nil && !errors.Is(err, account.ErrAccountNotFound):
		log.Fatal(err)
	}

	hash, err := account.HashPassword(secret)
	if err != nil {
		log.Fatal(err)
	}
	handle := strings.TrimSpace(*username)
	label := strings.TrimSpace(*displayName)
	if label == "" {
		label = handle
	}
	stored, err := store.UpsertAccount(ctx, account.NewAccount{
		AccountID:     accountID,
		Username:      handle,
		Email:         strings.TrimSpace(*email),
		DisplayName:   label,
		PasswordHash:  hash,
		EmailVerified: *verified,
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("account ready: id=%s username=%s email=%s email_verified=%t\n",
		stored.AccountID, stored.Username, stored.Email, stored.EmailVerified)
}

func validateInputs(username, email, password string) error {
	if err := account.ValidateUsername(username); err != nil {
		return err
	}
	if err := account.ValidateEmail(email); err != nil {
		return err
	}
	if err := account.ValidatePassword(password); err != nil {
		return err
	}
	return nil
}
