package account

import (
	"encoding/hex"
	"strings"
	"testing"
)

// TestPBKDF2SHA256MatchesKnownVectors pins the hand-rolled PBKDF2 to the
// published PBKDF2-HMAC-SHA256 vectors for ("password", "salt"). Without this,
// a subtle mistake in the block loop would still round-trip against itself and
// every other test would pass.
func TestPBKDF2SHA256MatchesKnownVectors(t *testing.T) {
	vectors := []struct {
		iterations int
		want       string
	}{
		{1, "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"},
		{2, "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43"},
		{4096, "c5e478d59288c841aa530db6845c4c8d962893a001ce4e11a4963873aa98134a"},
	}
	for _, vector := range vectors {
		got := pbkdf2SHA256([]byte("password"), []byte("salt"), vector.iterations, 32)
		if hex.EncodeToString(got) != vector.want {
			t.Errorf("pbkdf2SHA256(iterations=%d) = %s, want %s",
				vector.iterations, hex.EncodeToString(got), vector.want)
		}
	}
}

func TestPBKDF2SHA256HandlesKeyLengthNotAMultipleOfTheBlockSize(t *testing.T) {
	// 20 bytes forces the "last block is truncated" path.
	got := pbkdf2SHA256([]byte("password"), []byte("salt"), 2, 20)
	if len(got) != 20 {
		t.Fatalf("len = %d, want 20", len(got))
	}
	if !strings.HasPrefix(hex.EncodeToString(got), "ae4d0c95af6b46d32d0adff928f06dd0") {
		t.Fatalf("unexpected prefix: %s", hex.EncodeToString(got))
	}
}

func TestHashPasswordRoundTrips(t *testing.T) {
	encoded, err := HashPassword("wjc11111")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(encoded, PasswordAlgorithm+"$") {
		t.Fatalf("encoded = %q, want the %s prefix", encoded, PasswordAlgorithm)
	}
	if PasswordHashAlgorithm(encoded) != PasswordAlgorithm {
		t.Fatalf("PasswordHashAlgorithm(%q) = %q", encoded, PasswordHashAlgorithm(encoded))
	}
	if !VerifyPassword("wjc11111", encoded) {
		t.Fatal("VerifyPassword rejected the correct password")
	}
	if VerifyPassword("wjc11112", encoded) {
		t.Fatal("VerifyPassword accepted a wrong password")
	}
}

func TestHashPasswordSaltsEveryHash(t *testing.T) {
	first, err := HashPassword("wjc11111")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := HashPassword("wjc11111")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Fatal("two hashes of the same password are identical: the salt is not random")
	}
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	cases := []string{
		"",
		"not-a-hash",
		"pbkdf2-sha256$210000$onlythree",
		"bcrypt$210000$c2FsdA$a2V5",
		"pbkdf2-sha256$0$c2FsdA$a2V5",
		"pbkdf2-sha256$999999999$c2FsdA$a2V5",
		"pbkdf2-sha256$210000$$a2V5",
		"pbkdf2-sha256$210000$c2FsdA$",
		"pbkdf2-sha256$abc$c2FsdA$a2V5",
	}
	for _, encoded := range cases {
		if VerifyPassword("wjc11111", encoded) {
			t.Errorf("VerifyPassword accepted malformed hash %q", encoded)
		}
	}
}

func TestValidatePasswordPolicy(t *testing.T) {
	if err := ValidatePassword("wjc11111"); err != nil {
		t.Fatalf("8 characters must be accepted: %v", err)
	}
	if err := ValidatePassword("short"); err != ErrPasswordTooShort {
		t.Fatalf("err = %v, want ErrPasswordTooShort", err)
	}
	if err := ValidatePassword(strings.Repeat("a", MaxPasswordLength+1)); err != ErrPasswordTooLong {
		t.Fatalf("err = %v, want ErrPasswordTooLong", err)
	}
	if _, err := HashPassword("short"); err != ErrPasswordTooShort {
		t.Fatalf("HashPassword err = %v, want ErrPasswordTooShort", err)
	}
}
