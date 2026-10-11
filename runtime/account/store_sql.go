package account

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// accountColumns is the projection every read shares, so the scan order stays
// in one place.
const accountColumns = `account_id, username, email, display_name, password_hash,
	email_verified, created_at, updated_at, COALESCE(last_login_at, 'epoch'::timestamptz)`

const insertAccountSQL = `
INSERT INTO gensoulkyo_accounts (
    account_id,
    username,
    username_normalized,
    email,
    email_normalized,
    display_name,
    password_hash,
    email_verified,
    created_at,
    updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)`

const upsertAccountSQL = insertAccountSQL + `
ON CONFLICT (email_normalized) DO UPDATE SET
    username = EXCLUDED.username,
    username_normalized = EXCLUDED.username_normalized,
    display_name = EXCLUDED.display_name,
    password_hash = EXCLUDED.password_hash,
    email_verified = EXCLUDED.email_verified,
    updated_at = EXCLUDED.updated_at
RETURNING ` + accountColumns

// SQLStore is the Postgres-backed Store.
type SQLStore struct {
	db *sql.DB
}

// NewSQLStore validates the handle and returns the store.
func NewSQLStore(db *sql.DB) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("database is required for the SQL account store")
	}
	return &SQLStore{db: db}, nil
}

// CreateAccount inserts a new account.
func (s *SQLStore) CreateAccount(ctx context.Context, account NewAccount) (*Account, error) {
	createdAt := account.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, insertAccountSQL,
		account.AccountID,
		account.Username,
		NormalizeUsername(account.Username),
		account.Email,
		NormalizeEmail(account.Email),
		account.DisplayName,
		account.PasswordHash,
		account.EmailVerified,
		createdAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, s.classifyConflict(ctx, account)
		}
		return nil, err
	}
	return s.AccountByID(ctx, account.AccountID)
}

// UpsertAccount creates the account, or replaces the credentials of the account
// already registered under the same e-mail.
func (s *SQLStore) UpsertAccount(ctx context.Context, account NewAccount) (*Account, error) {
	createdAt := account.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	row := s.db.QueryRowContext(ctx, upsertAccountSQL,
		account.AccountID,
		account.Username,
		NormalizeUsername(account.Username),
		account.Email,
		NormalizeEmail(account.Email),
		account.DisplayName,
		account.PasswordHash,
		account.EmailVerified,
		createdAt,
	)
	stored, err := scanAccount(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUsernameTaken
		}
		return nil, err
	}
	return stored, nil
}

// AccountByEmail looks an account up by e-mail.
func (s *SQLStore) AccountByEmail(ctx context.Context, email string) (*Account, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM gensoulkyo_accounts WHERE email_normalized = $1`,
		NormalizeEmail(email))
	return scanAccount(row)
}

// AccountByUsername looks an account up by username.
func (s *SQLStore) AccountByUsername(ctx context.Context, username string) (*Account, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM gensoulkyo_accounts WHERE username_normalized = $1`,
		NormalizeUsername(username))
	return scanAccount(row)
}

// AccountByID looks an account up by its id.
func (s *SQLStore) AccountByID(ctx context.Context, accountID string) (*Account, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+accountColumns+` FROM gensoulkyo_accounts WHERE account_id = $1`,
		strings.TrimSpace(accountID))
	return scanAccount(row)
}

// TouchLogin records a successful sign-in.
func (s *SQLStore) TouchLogin(ctx context.Context, accountID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE gensoulkyo_accounts SET last_login_at = $2, updated_at = now() WHERE account_id = $1`,
		strings.TrimSpace(accountID), at)
	return err
}

// classifyConflict decides which unique index the insert tripped. Re-reading is
// the portable option: the driver-specific error text is not part of any
// contract, and this path only runs when an insert already failed.
func (s *SQLStore) classifyConflict(ctx context.Context, account NewAccount) error {
	if existing, err := s.AccountByEmail(ctx, account.Email); err == nil && existing != nil {
		return ErrEmailTaken
	}
	if existing, err := s.AccountByUsername(ctx, account.Username); err == nil && existing != nil {
		return ErrUsernameTaken
	}
	return ErrEmailTaken
}

func scanAccount(row *sql.Row) (*Account, error) {
	var (
		account   Account
		createdAt time.Time
		updatedAt time.Time
		lastLogin time.Time
	)
	err := row.Scan(
		&account.AccountID,
		&account.Username,
		&account.Email,
		&account.DisplayName,
		&account.PasswordHash,
		&account.EmailVerified,
		&createdAt,
		&updatedAt,
		&lastLogin,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAccountNotFound
		}
		return nil, err
	}
	account.CreatedAt = createdAt
	account.UpdatedAt = updatedAt
	if !lastLogin.IsZero() && lastLogin.Year() > 1 {
		account.LastLoginAt = lastLogin
	}
	return &account, nil
}

// sqlStateError is implemented by pgx's *pgconn.PgError. Matching on the
// interface keeps this file free of a driver import.
type sqlStateError interface {
	SQLState() string
}

func isUniqueViolation(err error) bool {
	var state sqlStateError
	if errors.As(err, &state) {
		return state.SQLState() == "23505"
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate key value")
}
