package core

import (
	"context"
	"errors"
	"strings"
	"time"

	"gensoulkyo/runtime/account"
)

// codeAccountsUnavailable is returned when the lobby was started without an
// account subsystem (no database and no explicit service). It is distinct from
// `unauthorized` so the client can tell "we cannot sign you in right now" apart
// from "those credentials are wrong".
const codeAccountsUnavailable = "accounts_unavailable"

// accounts returns the configured account subsystem, or nil.
func (s *Service) accounts() *account.Service {
	return s.accountService
}

// AccountStatus is a small operator-facing snapshot of the account subsystem.
type AccountStatus struct {
	Configured       bool   `json:"configured"`
	RegistrationOpen bool   `json:"registration_open"`
	MailEnabled      bool   `json:"mail_enabled"`
	PasswordAlg      string `json:"password_algorithm"`
}

// AccountStatus reports whether registration and e-mail delivery are wired.
func (s *Service) AccountStatus() AccountStatus {
	service := s.accounts()
	if service == nil {
		return AccountStatus{PasswordAlg: account.PasswordAlgorithm}
	}
	return AccountStatus{
		Configured:       true,
		RegistrationOpen: true,
		MailEnabled:      service.MailEnabled(),
		PasswordAlg:      account.PasswordAlgorithm,
	}
}

// RequestEmailCode mails a registration verification code.
//
// The SMTP round-trip happens here, deliberately outside Service.mu: it can
// take seconds, and every lobby request shares this Service.
func (s *Service) RequestEmailCode(req AccountEmailCodeRequest) (*AccountEmailCodeResponse, error) {
	service := s.accounts()
	if service == nil {
		return nil, newError(codeAccountsUnavailable, "the lobby has no account service configured")
	}
	receipt, err := service.SendVerificationCode(context.Background(), req.Email)
	if err != nil {
		return nil, mapAccountError(err)
	}
	now := s.clock()
	return &AccountEmailCodeResponse{
		Email:           receipt.Email,
		ExpiresAt:       receipt.ExpiresAt,
		ResendAfter:     receipt.ResendAfter,
		ExpiresInSecond: secondsBetween(now, receipt.ExpiresAt),
		ResendInSecond:  secondsBetween(now, receipt.ResendAfter),
		ServerTime:      now,
	}, nil
}

// RegisterAccount verifies the e-mail code, creates the account and signs it in.
func (s *Service) RegisterAccount(req AccountRegisterRequest) (*AuthSession, error) {
	service := s.accounts()
	if service == nil {
		return nil, newError(codeAccountsUnavailable, "the lobby has no account service configured")
	}
	stored, err := service.Register(context.Background(), account.RegisterRequest{
		Username:    req.Username,
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		Code:        req.Code,
	})
	if err != nil {
		return nil, mapAccountError(err)
	}
	session, revoked, err := s.attachAccountSession(stored, accountDeviceID(req.Platform))
	if err != nil {
		return nil, err
	}
	s.notifyRevokedSessions(revoked)
	return session, nil
}

// LoginAccount signs an existing account in. A successful sign-in supersedes any
// live session for the same account, exactly like LoginExternal: the previous
// token is reported to the revocation hook after Service.mu is released.
func (s *Service) LoginAccount(req AccountLoginRequest) (*AuthSession, error) {
	service := s.accounts()
	if service == nil {
		return nil, newError(codeAccountsUnavailable, "the lobby has no account service configured")
	}
	stored, err := service.Authenticate(context.Background(), req.Email, req.Password)
	if err != nil {
		return nil, mapAccountError(err)
	}
	session, revoked, err := s.attachAccountSession(stored, accountDeviceID(req.Platform))
	if err != nil {
		return nil, err
	}
	s.notifyRevokedSessions(revoked)
	return session, nil
}

// LogoutAccount drops a session token. It is idempotent: signing out an unknown
// or already-revoked token succeeds, because the caller's intent is satisfied
// either way.
func (s *Service) LogoutAccount(sessionToken string) error {
	token := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sessionToken), "Bearer "))
	if token == "" {
		return newError(codeUnauthorized, "missing session token")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	userID, ok := s.sessionToUser[token]
	if !ok {
		return nil
	}
	delete(s.sessionToUser, token)
	if user := s.users[userID]; user != nil && user.SessionToken == token {
		user.SessionToken = ""
	}
	return nil
}

// attachAccountSession binds an account to a live lobby user and issues a fresh
// session token. The returned superseded sessions must be dispatched by the
// caller once Service.mu is released.
func (s *Service) attachAccountSession(stored *account.Account, deviceID string) (*AuthSession, []revokedSession, error) {
	if stored == nil || strings.TrimSpace(stored.AccountID) == "" {
		return nil, nil, newError(codeUnauthorized, "account is required")
	}
	displayName := strings.TrimSpace(stored.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(stored.Username)
	}
	if displayName == "" {
		displayName = "Player"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock()
	var revoked []revokedSession
	user := s.users[stored.AccountID]
	if user == nil {
		user = newUserState(stored.AccountID, displayName, deviceID, now)
		s.users[stored.AccountID] = user
	} else {
		if previous := user.SessionToken; previous != "" {
			revoked = append(revoked, revokedSession{userID: user.UserID, sessionToken: previous})
			delete(s.sessionToUser, previous)
		}
		user.DisplayName = displayName
		user.LastSeenAt = now
	}
	token := s.nextIDLocked("session")
	user.SessionToken = token
	s.sessionToUser[token] = user.UserID
	return &AuthSession{
		UserID:       user.UserID,
		SessionToken: token,
		DisplayName:  user.DisplayName,
		CreatedAt:    user.CreatedAt,
	}, revoked, nil
}

// newUserState builds the per-user lobby state a freshly signed-in identity
// starts from. It is the same shape LoginAnonymous/LoginExternal construct.
func newUserState(userID, displayName, deviceID string, now time.Time) *userState {
	user := &userState{
		UserID:             userID,
		DeviceID:           strings.TrimSpace(deviceID),
		DisplayName:        displayName,
		CreatedAt:          now,
		LastSeenAt:         now,
		Wallet:             defaultWallet(),
		Inventory:          defaultInventory(now),
		Decks:              map[string]DeckRecord{},
		ActiveDeckID:       defaultDeckID,
		Chests:             defaultChests(),
		ChestPity:          map[string]ChestPityState{},
		ChestOpenings:      []ChestOpeningRecord{},
		LastChestResults:   []ChestOpenResult{},
		Tasks:              defaultTasks(),
		Events:             defaultEvents(),
		Leaderboards:       defaultLeaderboards(),
		Certification:      defaultCertificationProfile("", now),
		CheckinCycleDay:    1,
		CheckinClaimedDays: map[string]int{},
		ShopPurchased:      map[string]int{},
		ShopInventory:      map[string]int{},
	}
	initGachaStateLocked(user, now)
	user.Certification.UserID = user.UserID
	defaultDeck := defaultDeckRecord(now)
	user.Decks[defaultDeck.DeckID] = defaultDeck
	return user
}

// accountDeviceID labels the originating platform for presence purposes.
func accountDeviceID(platform string) string {
	trimmed := strings.TrimSpace(platform)
	if trimmed == "" {
		return "account"
	}
	return trimmed
}

// mapAccountError translates an account-layer error into the lobby's coded
// error type so ErrorCode() keeps working end to end.
func mapAccountError(err error) error {
	if err == nil {
		return nil
	}
	var coded *account.Error
	if errors.As(err, &coded) {
		return newError(coded.Code, "%s", coded.Message)
	}
	return newError(codeAccountsUnavailable, "%s", err.Error())
}

// secondsBetween rounds a duration down to whole seconds, never negative.
func secondsBetween(from, to time.Time) int {
	delta := to.Sub(from)
	if delta <= 0 {
		return 0
	}
	return int(delta / time.Second)
}
