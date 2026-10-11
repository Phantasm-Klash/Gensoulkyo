package account

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"math/big"
	"time"
)

const (
	// VerificationCodeLength is the number of decimal digits in an e-mail code.
	VerificationCodeLength = 6
	// DefaultVerificationTTL is how long a code stays usable after it is sent.
	DefaultVerificationTTL = 10 * time.Minute
	// DefaultResendCooldown is the minimum gap between two code requests for the
	// same address. It stops a "send code" button from being used as a mail bomb.
	DefaultResendCooldown = 60 * time.Second
	// DefaultHourlySendLimit caps how many codes one address may request per hour.
	DefaultHourlySendLimit = 5
	// MaxVerificationAttempts caps how many wrong codes one pending code accepts
	// before it is burnt.
	MaxVerificationAttempts = 5
)

// generateVerificationCode returns a uniformly random code with
// VerificationCodeLength decimal digits (leading zeroes included).
func generateVerificationCode() (string, error) {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(VerificationCodeLength), nil)
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", VerificationCodeLength, value.Int64()), nil
}

// hashVerificationCode binds a code to a random salt under the service's
// process-local secret. The plaintext code never has to be kept: verification
// recomputes this digest and compares in constant time.
func hashVerificationCode(secret string, salt []byte, code string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(salt)
	mac.Write([]byte{0x1f})
	mac.Write([]byte(code))
	return mac.Sum(nil)
}

// verificationCodeMatches is the constant-time comparison used on verify.
func verificationCodeMatches(secret string, salt []byte, code string, want []byte) bool {
	return subtle.ConstantTimeCompare(hashVerificationCode(secret, salt, code), want) == 1
}

// randomSalt returns n cryptographically random bytes.
func randomSalt(n int) ([]byte, error) {
	salt := make([]byte, n)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return salt, nil
}
