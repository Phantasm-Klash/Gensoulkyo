package account

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Account is a stored e-mail/password identity. PasswordHash is only populated
// on the lookup paths that need to verify a credential; list-shaped responses
// must not expose it.
type Account struct {
	AccountID     string
	Username      string
	Email         string
	DisplayName   string
	EmailVerified bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastLoginAt   time.Time
	PasswordHash  string
}

// NewAccount is the insert payload for Store.CreateAccount.
type NewAccount struct {
	AccountID     string
	Username      string
	Email         string
	DisplayName   string
	PasswordHash  string
	EmailVerified bool
	CreatedAt     time.Time
}

// Store persists accounts. Implementations must translate their backend's
// unique-constraint failure into ErrEmailTaken / ErrUsernameTaken so callers
// never have to inspect driver-specific errors.
type Store interface {
	// CreateAccount inserts a new account, or returns ErrEmailTaken /
	// ErrUsernameTaken when the e-mail or the username is already registered.
	CreateAccount(ctx context.Context, account NewAccount) (*Account, error)
	// AccountByEmail returns the account for an e-mail (case-insensitive), or
	// ErrAccountNotFound.
	AccountByEmail(ctx context.Context, email string) (*Account, error)
	// AccountByUsername returns the account for a username (case-insensitive),
	// or ErrAccountNotFound.
	AccountByUsername(ctx context.Context, username string) (*Account, error)
	// AccountByID returns the account for an account id, or ErrAccountNotFound.
	AccountByID(ctx context.Context, accountID string) (*Account, error)
	// UpsertAccount creates the account, or overwrites the credentials of the
	// existing account with the same e-mail. It backs the operator seed tool.
	UpsertAccount(ctx context.Context, account NewAccount) (*Account, error)
	// TouchLogin records a successful sign-in time.
	TouchLogin(ctx context.Context, accountID string, at time.Time) error
}

var (
	// ErrAccountNotFound is returned by the lookup methods.
	ErrAccountNotFound = errors.New("account not found")
	// ErrEmailTaken is returned when an e-mail is already registered.
	ErrEmailTaken = errors.New("e-mail is already registered")
	// ErrUsernameTaken is returned when a username is already registered.
	ErrUsernameTaken = errors.New("username is already taken")
)

const (
	// MinUsernameLength / MaxUsernameLength bound the display handle.
	MinUsernameLength = 3
	MaxUsernameLength = 24
	// MaxEmailLength caps the stored address.
	MaxEmailLength = 254
)

// usernamePattern mirrors the rule the client already advertises: 3-24 letters,
// digits or underscores.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,24}$`)

// emailPattern is a deliberately conservative structural check. It is not an
// RFC 5322 parser: the authority on whether an address exists is the
// verification e-mail, not a regular expression.
var emailPattern = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}$`)

// NormalizeEmail lower-cases and trims an address so lookups are stable.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NormalizeUsername trims a username. Uniqueness is enforced case-insensitively
// so "Wjcwqc" cannot shadow "wjcwqc".
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// ValidateEmail reports whether an address is structurally plausible.
func ValidateEmail(email string) error {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return errors.New("e-mail is required")
	}
	if len(trimmed) > MaxEmailLength {
		return errors.New("e-mail is too long")
	}
	if !emailPattern.MatchString(trimmed) {
		return errors.New("e-mail is not a valid address")
	}
	return nil
}

// ValidateUsername enforces the 3-24 [A-Za-z0-9_] rule.
func ValidateUsername(username string) error {
	trimmed := strings.TrimSpace(username)
	if trimmed == "" {
		return errors.New("username is required")
	}
	if !usernamePattern.MatchString(trimmed) {
		return errors.New("username must be 3-24 letters, digits or underscores")
	}
	return nil
}

// ValidateVerificationCode checks the shape of a submitted code. The value is
// compared against the pending code separately.
func ValidateVerificationCode(code string) error {
	trimmed := strings.TrimSpace(code)
	if len(trimmed) != VerificationCodeLength {
		return errors.New("verification code must be 6 digits")
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] < '0' || trimmed[i] > '9' {
			return errors.New("verification code must be 6 digits")
		}
	}
	return nil
}
