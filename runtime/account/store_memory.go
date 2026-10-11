package account

import (
	"context"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-process Store. It backs the tests and a lobby started
// without a database URL (where accounts are intentionally ephemeral).
type MemoryStore struct {
	mu       sync.RWMutex
	byID     map[string]*Account
	byEmail  map[string]string
	byHandle map[string]string
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:     map[string]*Account{},
		byEmail:  map[string]string{},
		byHandle: map[string]string{},
	}
}

// CreateAccount inserts a new account.
func (s *MemoryStore) CreateAccount(_ context.Context, account NewAccount) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byEmail[NormalizeEmail(account.Email)]; exists {
		return nil, ErrEmailTaken
	}
	if _, exists := s.byHandle[NormalizeUsername(account.Username)]; exists {
		return nil, ErrUsernameTaken
	}
	stored := s.insertLocked(account)
	return stored, nil
}

// UpsertAccount creates the account, or replaces the credentials of the account
// already registered under the same e-mail.
func (s *MemoryStore) UpsertAccount(_ context.Context, account NewAccount) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	email := NormalizeEmail(account.Email)
	handle := NormalizeUsername(account.Username)
	if existingID, exists := s.byEmail[email]; exists {
		if owner, taken := s.byHandle[handle]; taken && owner != existingID {
			return nil, ErrUsernameTaken
		}
		stored := s.byID[existingID]
		stored.Username = account.Username
		stored.DisplayName = account.DisplayName
		stored.PasswordHash = account.PasswordHash
		stored.EmailVerified = account.EmailVerified
		stored.UpdatedAt = time.Now().UTC()
		s.byHandle[handle] = existingID
		return cloneAccount(stored), nil
	}
	if _, taken := s.byHandle[handle]; taken {
		return nil, ErrUsernameTaken
	}
	return s.insertLocked(account), nil
}

// AccountByEmail looks an account up by e-mail.
func (s *MemoryStore) AccountByEmail(_ context.Context, email string) (*Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byEmail[NormalizeEmail(email)]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return cloneAccount(s.byID[id]), nil
}

// AccountByUsername looks an account up by username.
func (s *MemoryStore) AccountByUsername(_ context.Context, username string) (*Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byHandle[NormalizeUsername(username)]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return cloneAccount(s.byID[id]), nil
}

// AccountByID looks an account up by its id.
func (s *MemoryStore) AccountByID(_ context.Context, accountID string) (*Account, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored, ok := s.byID[strings.TrimSpace(accountID)]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return cloneAccount(stored), nil
}

// TouchLogin records a successful sign-in.
func (s *MemoryStore) TouchLogin(_ context.Context, accountID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.byID[strings.TrimSpace(accountID)]
	if !ok {
		return ErrAccountNotFound
	}
	stored.LastLoginAt = at
	return nil
}

// Len reports how many accounts are stored. Test helper.
func (s *MemoryStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

func (s *MemoryStore) insertLocked(account NewAccount) *Account {
	createdAt := account.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	stored := &Account{
		AccountID:     account.AccountID,
		Username:      account.Username,
		Email:         account.Email,
		DisplayName:   account.DisplayName,
		PasswordHash:  account.PasswordHash,
		EmailVerified: account.EmailVerified,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	s.byID[stored.AccountID] = stored
	s.byEmail[NormalizeEmail(stored.Email)] = stored.AccountID
	s.byHandle[NormalizeUsername(stored.Username)] = stored.AccountID
	return cloneAccount(stored)
}

func cloneAccount(stored *Account) *Account {
	if stored == nil {
		return nil
	}
	copied := *stored
	return &copied
}
