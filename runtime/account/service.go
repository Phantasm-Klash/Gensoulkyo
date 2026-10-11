package account

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Wire-visible error codes. The client maps these to player-facing sentences
// (see SpellKard `src/core/game/auth_messages.ts`), so they are part of the
// protocol and must not be renamed casually.
const (
	CodeInvalidRequest       = "invalid_request"
	CodeUsernameInvalid      = "account_username_invalid"
	CodeEmailInvalid         = "account_email_invalid"
	CodePasswordWeak         = "account_password_weak"
	CodeUsernameTaken        = "account_username_taken"
	CodeEmailTaken           = "account_email_taken"
	CodeEmailNotVerified     = "account_email_not_verified"
	CodeVerificationRequired = "verification_code_required"
	CodeVerificationInvalid  = "verification_code_invalid"
	CodeVerificationExpired  = "verification_code_expired"
	CodeVerificationRate     = "verification_rate_limited"
	CodeMailUnavailable      = "mail_unavailable"
	CodeUnauthorized         = "unauthorized"
	CodeUnavailable          = "accounts_unavailable"
)

// Error is a coded account failure. Callers map Code onto the transport's error
// envelope; Message is for logs and never shown to a player verbatim.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// ErrorCode extracts the coded part of err, or "internal" for an untyped error.
func ErrorCode(err error) string {
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return "internal"
}

// NewError builds a coded account error.
func NewError(code string, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// RegisterRequest is a self-service sign-up.
type RegisterRequest struct {
	Username    string
	Email       string
	Password    string
	DisplayName string
	// Code is the e-mail verification code the player received.
	Code string
}

// CodeReceipt describes a dispatched verification code.
type CodeReceipt struct {
	Email       string
	ExpiresAt   time.Time
	ResendAfter time.Time
}

// Config assembles a Service. Only Store is required; every other field falls
// back to a documented default so tests can shrink the timings.
type Config struct {
	Store  Store
	Mailer Mailer
	Clock  func() time.Time

	VerificationTTL time.Duration
	ResendCooldown  time.Duration
	HourlySendLimit int
	MaxAttempts     int
	// CodeSecret keys the HMAC that protects pending codes in memory. A random
	// value is generated when it is empty.
	CodeSecret string
}

// Service owns credential verification, e-mail verification codes and their
// rate limits.
//
// Locking: mu guards only the in-memory pending-code and rate-limit tables. It
// is never held across password hashing or SMTP delivery — both are slow, and
// the lobby has a single Service shared by every request.
type Service struct {
	store    Store
	mailer   Mailer
	clock    func() time.Time
	codeKey  []byte
	ttl      time.Duration
	cooldown time.Duration
	hourly   int
	attempts int

	mu      sync.Mutex
	pending map[string]*pendingCode
	sends   map[string][]time.Time

	dummyOnce sync.Once
	dummyHash string
}

type pendingCode struct {
	salt      []byte
	digest    []byte
	issuedAt  time.Time
	expiresAt time.Time
	attempts  int
}

// NewService builds the account service.
func NewService(config Config) *Service {
	store := config.Store
	if store == nil {
		store = NewMemoryStore()
	}
	mailer := config.Mailer
	if mailer == nil {
		mailer = UnavailableMailer{}
	}
	clock := config.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	service := &Service{
		store:    store,
		mailer:   mailer,
		clock:    clock,
		ttl:      config.VerificationTTL,
		cooldown: config.ResendCooldown,
		hourly:   config.HourlySendLimit,
		attempts: config.MaxAttempts,
		pending:  map[string]*pendingCode{},
		sends:    map[string][]time.Time{},
	}
	if service.ttl <= 0 {
		service.ttl = DefaultVerificationTTL
	}
	if service.cooldown <= 0 {
		service.cooldown = DefaultResendCooldown
	}
	if service.hourly <= 0 {
		service.hourly = DefaultHourlySendLimit
	}
	if service.attempts <= 0 {
		service.attempts = MaxVerificationAttempts
	}
	service.codeKey = []byte(config.CodeSecret)
	if len(service.codeKey) == 0 {
		if secret, err := randomSalt(32); err == nil {
			service.codeKey = secret
		}
	}
	return service
}

// Mailer reports the configured mail transport, for status endpoints.
func (s *Service) Mailer() Mailer { return s.mailer }

// MailEnabled reports whether verification e-mail can actually be delivered.
func (s *Service) MailEnabled() bool { return s.mailer != nil && s.mailer.Enabled() }

// Store exposes the underlying store (used by the operator seed tool).
func (s *Service) Store() Store { return s.store }

// SendVerificationCode issues a code for an e-mail address and delivers it.
//
// The address must not already belong to an account: telling the player
// "already registered" at this point is friendlier than letting them fill in
// the whole form and then rejecting it. It does mean the endpoint can be used
// to probe for registered addresses, which is an accepted trade-off for a game
// lobby.
func (s *Service) SendVerificationCode(ctx context.Context, email string) (CodeReceipt, error) {
	address := strings.TrimSpace(email)
	if err := ValidateEmail(address); err != nil {
		return CodeReceipt{}, NewError(CodeEmailInvalid, "%s", err.Error())
	}
	normalized := NormalizeEmail(address)
	if existing, err := s.store.AccountByEmail(ctx, normalized); err != nil {
		if !errors.Is(err, ErrAccountNotFound) {
			return CodeReceipt{}, NewError(CodeUnavailable, "%s", err.Error())
		}
	} else if existing != nil {
		return CodeReceipt{}, NewError(CodeEmailTaken, "that e-mail is already registered")
	}
	if !s.MailEnabled() {
		return CodeReceipt{}, NewError(CodeMailUnavailable, "the lobby has no SMTP relay configured")
	}

	pending, code, err := s.beginSend(normalized)
	if err != nil {
		return CodeReceipt{}, err
	}
	if err := s.mailer.SendVerificationCode(ctx, address, code, s.ttl); err != nil {
		// A code nobody received must not count as pending, otherwise the
		// cooldown blocks the retry the player is about to make.
		s.dropPending(normalized, pending)
		return CodeReceipt{}, NewError(CodeMailUnavailable, "%s", err.Error())
	}
	return CodeReceipt{
		Email:       normalized,
		ExpiresAt:   pending.expiresAt,
		ResendAfter: pending.issuedAt.Add(s.cooldown),
	}, nil
}

// Register verifies the e-mail code and creates the account.
func (s *Service) Register(ctx context.Context, req RegisterRequest) (*Account, error) {
	username := strings.TrimSpace(req.Username)
	if err := ValidateUsername(username); err != nil {
		return nil, NewError(CodeUsernameInvalid, "%s", err.Error())
	}
	address := strings.TrimSpace(req.Email)
	if err := ValidateEmail(address); err != nil {
		return nil, NewError(CodeEmailInvalid, "%s", err.Error())
	}
	if err := ValidatePassword(req.Password); err != nil {
		return nil, NewError(CodePasswordWeak, "%s", err.Error())
	}
	code := strings.TrimSpace(req.Code)
	if err := ValidateVerificationCode(code); err != nil {
		return nil, NewError(CodeVerificationRequired, "%s", err.Error())
	}
	if err := s.consumeCode(NormalizeEmail(address), code); err != nil {
		return nil, err
	}

	// Hashing is deliberately after the code check (so a wrong code costs no
	// CPU) and outside every lock (so it cannot stall the lobby).
	hash, err := HashPassword(req.Password)
	if err != nil {
		return nil, NewError(CodePasswordWeak, "%s", err.Error())
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = username
	}
	stored, err := s.store.CreateAccount(ctx, NewAccount{
		AccountID:     NewAccountID(),
		Username:      username,
		Email:         address,
		DisplayName:   displayName,
		PasswordHash:  hash,
		EmailVerified: true,
		CreatedAt:     s.clock(),
	})
	switch {
	case errors.Is(err, ErrEmailTaken):
		return nil, NewError(CodeEmailTaken, "that e-mail is already registered")
	case errors.Is(err, ErrUsernameTaken):
		return nil, NewError(CodeUsernameTaken, "that username is already taken")
	case err != nil:
		return nil, NewError(CodeUnavailable, "%s", err.Error())
	}
	return stored, nil
}

// Authenticate verifies an e-mail/password pair.
func (s *Service) Authenticate(ctx context.Context, email, password string) (*Account, error) {
	address := strings.TrimSpace(email)
	if err := ValidateEmail(address); err != nil {
		return nil, NewError(CodeUnauthorized, "wrong e-mail or password")
	}
	stored, err := s.store.AccountByEmail(ctx, address)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			// Burn a comparable amount of CPU so a missing address cannot be
			// told apart from a wrong password by response latency.
			VerifyPassword(password, s.decoyHash())
			return nil, NewError(CodeUnauthorized, "wrong e-mail or password")
		}
		return nil, NewError(CodeUnavailable, "%s", err.Error())
	}
	if !VerifyPassword(password, stored.PasswordHash) {
		return nil, NewError(CodeUnauthorized, "wrong e-mail or password")
	}
	if !stored.EmailVerified {
		return nil, NewError(CodeEmailNotVerified, "verify your e-mail before signing in")
	}
	// A failed bookkeeping write must not fail the sign-in, but the returned
	// account has to reflect what was stored when it succeeded.
	now := s.clock()
	if err := s.store.TouchLogin(ctx, stored.AccountID, now); err == nil {
		stored.LastLoginAt = now
	}
	return stored, nil
}

// decoyHash lazily derives a real hash for the timing-equalisation path.
func (s *Service) decoyHash() string {
	s.dummyOnce.Do(func() {
		if hash, err := HashPassword("decoy-password-value"); err == nil {
			s.dummyHash = hash
			return
		}
		s.dummyHash = PasswordAlgorithm + "$1$AAAA$AAAA"
	})
	return s.dummyHash
}

// beginSend rate-limits the request and records the pending code. It returns
// the plaintext code for the caller to deliver.
func (s *Service) beginSend(normalized string) (*pendingCode, string, error) {
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()

	history := pruneSendHistory(s.sends[normalized], now)
	s.sends[normalized] = history
	if len(history) >= s.hourly {
		return nil, "", NewError(CodeVerificationRate, "too many verification e-mails requested; try again later")
	}
	if last := s.pending[normalized]; last != nil {
		if wait := s.cooldown - now.Sub(last.issuedAt); wait > 0 {
			return nil, "", NewError(CodeVerificationRate,
				"wait %d seconds before requesting another code", int(wait.Seconds())+1)
		}
	}

	code, err := generateVerificationCode()
	if err != nil {
		return nil, "", NewError(CodeUnavailable, "could not generate a verification code")
	}
	salt, err := randomSalt(16)
	if err != nil {
		return nil, "", NewError(CodeUnavailable, "could not generate a verification code")
	}
	pending := &pendingCode{
		salt:      salt,
		digest:    hashVerificationCode(string(s.codeKey), salt, code),
		issuedAt:  now,
		expiresAt: now.Add(s.ttl),
	}
	s.pending[normalized] = pending
	s.sends[normalized] = append(history, now)
	return pending, code, nil
}

// dropPending removes a pending code only when it is still the one we stored,
// so a slow failed delivery cannot delete a newer code.
func (s *Service) dropPending(normalized string, pending *pendingCode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.pending[normalized]; current == pending {
		delete(s.pending, normalized)
	}
}

// consumeCode verifies and burns the pending code for an address.
func (s *Service) consumeCode(normalized, code string) error {
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()

	pending := s.pending[normalized]
	if pending == nil {
		return NewError(CodeVerificationRequired, "request a verification code first")
	}
	if !now.Before(pending.expiresAt) {
		delete(s.pending, normalized)
		return NewError(CodeVerificationExpired, "the verification code has expired; request a new one")
	}
	if !verificationCodeMatches(string(s.codeKey), pending.salt, code, pending.digest) {
		pending.attempts++
		if pending.attempts >= s.attempts {
			delete(s.pending, normalized)
			return NewError(CodeVerificationInvalid, "too many incorrect codes; request a new one")
		}
		return NewError(CodeVerificationInvalid, "the verification code is incorrect")
	}
	delete(s.pending, normalized)
	return nil
}

// pruneSendHistory drops timestamps older than the one-hour window.
func pruneSendHistory(history []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-time.Hour)
	kept := history[:0]
	for _, stamp := range history {
		if stamp.After(cutoff) {
			kept = append(kept, stamp)
		}
	}
	return kept
}

// NewAccountID returns a fresh opaque account identifier.
func NewAccountID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "acct_" + hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return "acct_" + hex.EncodeToString(buf)
}
