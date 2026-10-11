// Package account implements the lobby's e-mail + password account system:
// credential storage, e-mail verification codes and SMTP delivery of those
// codes.
//
// The package deliberately depends on the standard library only. The lobby is
// built by the nakama-pluginbuilder image from a module whose go.mod/go.sum are
// never uploaded to the host (see tools/deploy_lobby.sh), so pulling in a new
// module dependency would make the build unreproducible. Password hashing
// therefore uses a local PBKDF2-HMAC-SHA256 implementation (RFC 8018 §5.2)
// rather than golang.org/x/crypto/bcrypt.
//
// Everything here is engine-agnostic: no Laya, no HTTP, no Nakama types. The
// lobby layer (runtime/core) and the transport layer (runtime/httpapi) only see
// the interfaces declared in store.go, mailer.go and service.go.
package account

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

const (
	// PasswordAlgorithm is the identifier embedded in every encoded hash. It is
	// part of the on-disk format: changing it invalidates nothing, because the
	// algorithm is read back from the stored value rather than assumed.
	PasswordAlgorithm = "pbkdf2-sha256"

	// PasswordIterations is the PBKDF2 work factor used for new hashes. It is
	// stored alongside each hash so it can be raised later without locking out
	// existing accounts.
	PasswordIterations = 210000

	passwordSaltBytes = 16
	passwordKeyBytes  = 32

	// MinPasswordLength is the shortest password the lobby accepts.
	MinPasswordLength = 8
	// MaxPasswordLength caps the input so a hostile client cannot make PBKDF2
	// burn an unbounded amount of CPU per request.
	MaxPasswordLength = 256
)

var (
	// ErrPasswordTooShort is returned by HashPassword/ValidatePassword.
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")
	// ErrPasswordTooLong is returned by HashPassword/ValidatePassword.
	ErrPasswordTooLong = errors.New("password must be at most 256 bytes")
	// ErrPasswordHashMalformed marks a stored hash this build cannot parse.
	ErrPasswordHashMalformed = errors.New("password hash is malformed")
)

// ValidatePassword enforces the password policy. It is separate from hashing so
// the caller can reject a weak password before spending ~100ms of PBKDF2 work.
func ValidatePassword(password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return ErrPasswordTooShort
	}
	if len(password) > MaxPasswordLength {
		return ErrPasswordTooLong
	}
	return nil
}

// HashPassword derives a new salted hash and returns it in the self-describing
// form "pbkdf2-sha256$<iterations>$<salt-b64>$<key-b64>".
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2SHA256([]byte(password), salt, PasswordIterations, passwordKeyBytes)
	return encodePasswordHash(PasswordIterations, salt, key), nil
}

// VerifyPassword reports whether password matches encoded. It never returns an
// error: a malformed stored hash is a mismatch, not a crash. The comparison is
// constant time so a wrong password cannot be narrowed down byte by byte.
func VerifyPassword(password, encoded string) bool {
	iterations, salt, want, err := decodePasswordHash(encoded)
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iterations, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// PasswordHashAlgorithm returns the algorithm id stored in encoded, or "" when
// encoded is not a hash this build produced.
func PasswordHashAlgorithm(encoded string) string {
	parts := strings.Split(strings.TrimSpace(encoded), "$")
	if len(parts) != 4 {
		return ""
	}
	return parts[0]
}

func encodePasswordHash(iterations int, salt, key []byte) string {
	return strings.Join([]string{
		PasswordAlgorithm,
		strconv.Itoa(iterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$")
}

func decodePasswordHash(encoded string) (int, []byte, []byte, error) {
	parts := strings.Split(strings.TrimSpace(encoded), "$")
	if len(parts) != 4 || parts[0] != PasswordAlgorithm {
		return 0, nil, nil, ErrPasswordHashMalformed
	}
	iterations, err := strconv.Atoi(parts[1])
	// The upper bound keeps a tampered row from turning a login attempt into an
	// infinite loop.
	if err != nil || iterations <= 0 || iterations > 5_000_000 {
		return 0, nil, nil, ErrPasswordHashMalformed
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) == 0 {
		return 0, nil, nil, ErrPasswordHashMalformed
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(key) == 0 {
		return 0, nil, nil, ErrPasswordHashMalformed
	}
	return iterations, salt, key, nil
}

// pbkdf2SHA256 implements PBKDF2 (RFC 8018 §5.2) with HMAC-SHA-256 as the
// pseudo-random function. It is written against the standard library only and
// reuses its buffers, so a single hash does not allocate once per iteration.
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	if iterations <= 0 || keyLen <= 0 {
		return nil
	}
	mac := hmac.New(sha256.New, password)
	hashLen := mac.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	derived := make([]byte, 0, blocks*hashLen)
	u := make([]byte, hashLen)
	t := make([]byte, hashLen)
	blockIndex := make([]byte, 4)
	for block := 1; block <= blocks; block++ {
		blockIndex[0] = byte(block >> 24)
		blockIndex[1] = byte(block >> 16)
		blockIndex[2] = byte(block >> 8)
		blockIndex[3] = byte(block)
		mac.Reset()
		mac.Write(salt)
		mac.Write(blockIndex)
		// Sum(u[:0]) appends into u's backing array, so no per-round allocation.
		t = mac.Sum(t[:0])
		copy(u, t)
		for round := 2; round <= iterations; round++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(u[:0])
			for i := range t {
				t[i] ^= u[i]
			}
		}
		derived = append(derived, t...)
	}
	return derived[:keyLen]
}
