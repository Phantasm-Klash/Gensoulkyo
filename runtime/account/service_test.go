package account

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// captureMailer records the codes it was asked to deliver so tests can replay
// them, and can be told to fail.
type captureMailer struct {
	mu       sync.Mutex
	sent     map[string][]string
	failNext bool
	disabled bool
}

func newCaptureMailer() *captureMailer {
	return &captureMailer{sent: map[string][]string{}}
}

func (m *captureMailer) Enabled() bool { return !m.disabled }

func (m *captureMailer) SendVerificationCode(_ context.Context, to string, code string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failNext {
		m.failNext = false
		return errors.New("smtp is down")
	}
	m.sent[NormalizeEmail(to)] = append(m.sent[NormalizeEmail(to)], code)
	return nil
}

// last returns the most recent code delivered to an address.
func (m *captureMailer) last(to string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	codes := m.sent[NormalizeEmail(to)]
	if len(codes) == 0 {
		return ""
	}
	return codes[len(codes)-1]
}

// count reports how many codes were delivered to an address.
func (m *captureMailer) count(to string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent[NormalizeEmail(to)])
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestService(t *testing.T, mailer Mailer, clock *testClock) (*Service, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	service := NewService(Config{
		Store:           store,
		Mailer:          mailer,
		Clock:           clock.Now,
		VerificationTTL: 10 * time.Minute,
		ResendCooldown:  time.Minute,
		HourlySendLimit: 3,
		MaxAttempts:     3,
		CodeSecret:      "test-secret",
	})
	return service, store
}

func TestSendVerificationCodeDeliversAndRegisters(t *testing.T) {
	mailer := newCaptureMailer()
	clock := newTestClock()
	service, store := newTestService(t, mailer, clock)
	ctx := context.Background()

	receipt, err := service.SendVerificationCode(ctx, "Wjcwqc@Gmail.com ")
	if err != nil {
		t.Fatalf("SendVerificationCode: %v", err)
	}
	if receipt.Email != "wjcwqc@gmail.com" {
		t.Fatalf("receipt.Email = %q, want the normalised address", receipt.Email)
	}
	code := mailer.last("wjcwqc@gmail.com")
	if len(code) != VerificationCodeLength {
		t.Fatalf("delivered code = %q, want %d digits", code, VerificationCodeLength)
	}

	created, err := service.Register(ctx, RegisterRequest{
		Username: "wjcwqc",
		Email:    "wjcwqc@gmail.com",
		Password: "wjc11111",
		Code:     code,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !strings.HasPrefix(created.AccountID, "acct_") {
		t.Fatalf("AccountID = %q", created.AccountID)
	}
	if created.DisplayName != "wjcwqc" {
		t.Fatalf("DisplayName = %q, want the username fallback", created.DisplayName)
	}
	if !created.EmailVerified {
		t.Fatal("a registered account must be marked verified")
	}
	if created.PasswordHash == "" || strings.Contains(created.PasswordHash, "wjc11111") {
		t.Fatalf("PasswordHash = %q, want a derived hash", created.PasswordHash)
	}
	if store.Len() != 1 {
		t.Fatalf("store.Len() = %d, want 1", store.Len())
	}
}

func TestRegisterRequiresTheCodeThatWasSent(t *testing.T) {
	mailer := newCaptureMailer()
	clock := newTestClock()
	service, _ := newTestService(t, mailer, clock)
	ctx := context.Background()

	base := RegisterRequest{Username: "wjcwqc", Email: "wjcwqc@gmail.com", Password: "wjc11111"}

	// No code was requested at all.
	if _, err := service.Register(ctx, base); ErrorCode(err) != CodeVerificationRequired {
		t.Fatalf("err = %v, want %s", err, CodeVerificationRequired)
	}

	if _, err := service.SendVerificationCode(ctx, base.Email); err != nil {
		t.Fatalf("SendVerificationCode: %v", err)
	}
	wrong := base
	wrong.Code = "000000"
	if mailer.last(base.Email) == wrong.Code {
		wrong.Code = "111111"
	}
	if _, err := service.Register(ctx, wrong); ErrorCode(err) != CodeVerificationInvalid {
		t.Fatalf("err = %v, want %s", err, CodeVerificationInvalid)
	}
	// A wrong code must not burn the pending code.
	if _, err := service.Register(ctx, RegisterRequest{
		Username: base.Username, Email: base.Email, Password: base.Password,
		Code: mailer.last(base.Email),
	}); err != nil {
		t.Fatalf("Register with the correct code after one failure: %v", err)
	}
}

func TestRegisterRejectsAnExpiredCode(t *testing.T) {
	mailer := newCaptureMailer()
	clock := newTestClock()
	service, _ := newTestService(t, mailer, clock)
	ctx := context.Background()

	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); err != nil {
		t.Fatalf("SendVerificationCode: %v", err)
	}
	code := mailer.last("wjcwqc@gmail.com")
	clock.Advance(11 * time.Minute)

	_, err := service.Register(ctx, RegisterRequest{
		Username: "wjcwqc", Email: "wjcwqc@gmail.com", Password: "wjc11111", Code: code,
	})
	if ErrorCode(err) != CodeVerificationExpired {
		t.Fatalf("err = %v, want %s", err, CodeVerificationExpired)
	}
}

func TestRegisterBurnsTheCodeAfterTooManyWrongAttempts(t *testing.T) {
	mailer := newCaptureMailer()
	clock := newTestClock()
	service, _ := newTestService(t, mailer, clock)
	ctx := context.Background()

	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); err != nil {
		t.Fatalf("SendVerificationCode: %v", err)
	}
	code := mailer.last("wjcwqc@gmail.com")
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := 0; i < 3; i++ {
		if _, err := service.Register(ctx, RegisterRequest{
			Username: "wjcwqc", Email: "wjcwqc@gmail.com", Password: "wjc11111", Code: wrong,
		}); ErrorCode(err) != CodeVerificationInvalid {
			t.Fatalf("attempt %d: err = %v, want %s", i, err, CodeVerificationInvalid)
		}
	}
	// The third failure burnt the code, so even the right one is now rejected.
	if _, err := service.Register(ctx, RegisterRequest{
		Username: "wjcwqc", Email: "wjcwqc@gmail.com", Password: "wjc11111", Code: code,
	}); ErrorCode(err) != CodeVerificationRequired {
		t.Fatalf("err = %v, want %s after the code was burnt", err, CodeVerificationRequired)
	}
}

func TestSendVerificationCodeRateLimits(t *testing.T) {
	mailer := newCaptureMailer()
	clock := newTestClock()
	service, _ := newTestService(t, mailer, clock)
	ctx := context.Background()

	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); ErrorCode(err) != CodeVerificationRate {
		t.Fatalf("immediate resend err = %v, want %s", err, CodeVerificationRate)
	}
	clock.Advance(61 * time.Second)
	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); err != nil {
		t.Fatalf("send after cooldown: %v", err)
	}
	// Hourly limit is 3 in the test config; one more succeeds, then it stops.
	clock.Advance(61 * time.Second)
	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); err != nil {
		t.Fatalf("third send: %v", err)
	}
	clock.Advance(61 * time.Second)
	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); ErrorCode(err) != CodeVerificationRate {
		t.Fatalf("fourth send err = %v, want %s", err, CodeVerificationRate)
	}
	if got := mailer.count("wjcwqc@gmail.com"); got != 3 {
		t.Fatalf("delivered %d codes, want 3", got)
	}
}

func TestSendVerificationCodeRejectsARegisteredAddress(t *testing.T) {
	mailer := newCaptureMailer()
	clock := newTestClock()
	service, _ := newTestService(t, mailer, clock)
	ctx := context.Background()

	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); err != nil {
		t.Fatalf("SendVerificationCode: %v", err)
	}
	if _, err := service.Register(ctx, RegisterRequest{
		Username: "wjcwqc", Email: "wjcwqc@gmail.com", Password: "wjc11111",
		Code: mailer.last("wjcwqc@gmail.com"),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); ErrorCode(err) != CodeEmailTaken {
		t.Fatalf("err = %v, want %s", err, CodeEmailTaken)
	}
}

func TestSendVerificationCodeFailsWhenNoRelayIsConfigured(t *testing.T) {
	clock := newTestClock()
	service, _ := newTestService(t, UnavailableMailer{}, clock)
	if _, err := service.SendVerificationCode(context.Background(), "wjcwqc@gmail.com"); ErrorCode(err) != CodeMailUnavailable {
		t.Fatalf("err = %v, want %s", err, CodeMailUnavailable)
	}
}

func TestFailedDeliveryDoesNotConsumeTheCooldown(t *testing.T) {
	mailer := newCaptureMailer()
	mailer.failNext = true
	clock := newTestClock()
	service, _ := newTestService(t, mailer, clock)
	ctx := context.Background()

	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); ErrorCode(err) != CodeMailUnavailable {
		t.Fatalf("err = %v, want %s", err, CodeMailUnavailable)
	}
	// The player must be able to retry immediately after a transport failure.
	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); err != nil {
		t.Fatalf("retry after a failed delivery: %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	mailer := newCaptureMailer()
	clock := newTestClock()
	service, _ := newTestService(t, mailer, clock)
	ctx := context.Background()

	if _, err := service.SendVerificationCode(ctx, "wjcwqc@gmail.com"); err != nil {
		t.Fatalf("SendVerificationCode: %v", err)
	}
	created, err := service.Register(ctx, RegisterRequest{
		Username: "wjcwqc", Email: "wjcwqc@gmail.com", Password: "wjc11111",
		Code: mailer.last("wjcwqc@gmail.com"),
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Sign-in is case-insensitive on the address.
	signedIn, err := service.Authenticate(ctx, " WJCWQC@GMAIL.COM", "wjc11111")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if signedIn.AccountID != created.AccountID {
		t.Fatalf("AccountID = %q, want %q", signedIn.AccountID, created.AccountID)
	}
	if signedIn.LastLoginAt.IsZero() {
		t.Fatal("a successful sign-in must record last_login_at")
	}

	if _, err := service.Authenticate(ctx, "wjcwqc@gmail.com", "wrong-password"); ErrorCode(err) != CodeUnauthorized {
		t.Fatalf("wrong password err = %v, want %s", err, CodeUnauthorized)
	}
	if _, err := service.Authenticate(ctx, "nobody@gmail.com", "wjc11111"); ErrorCode(err) != CodeUnauthorized {
		t.Fatalf("unknown address err = %v, want %s", err, CodeUnauthorized)
	}
	if _, err := service.Authenticate(ctx, "not-an-address", "wjc11111"); ErrorCode(err) != CodeUnauthorized {
		t.Fatalf("malformed address err = %v, want %s", err, CodeUnauthorized)
	}
}

func TestAuthenticateRejectsAnUnverifiedAccount(t *testing.T) {
	clock := newTestClock()
	store := NewMemoryStore()
	hash, err := HashPassword("wjc11111")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := store.CreateAccount(context.Background(), NewAccount{
		AccountID: "acct_manual", Username: "wjcwqc", Email: "wjcwqc@gmail.com",
		PasswordHash: hash, EmailVerified: false, CreatedAt: clock.Now(),
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	service := NewService(Config{Store: store, Mailer: newCaptureMailer(), Clock: clock.Now})

	if _, err := service.Authenticate(context.Background(), "wjcwqc@gmail.com", "wjc11111"); ErrorCode(err) != CodeEmailNotVerified {
		t.Fatalf("err = %v, want %s", err, CodeEmailNotVerified)
	}
}

func TestRegisterRejectsDuplicateCredentials(t *testing.T) {
	mailer := newCaptureMailer()
	clock := newTestClock()
	service, _ := newTestService(t, mailer, clock)
	ctx := context.Background()

	register := func(username, email string) error {
		if _, err := service.SendVerificationCode(ctx, email); err != nil {
			return err
		}
		_, err := service.Register(ctx, RegisterRequest{
			Username: username, Email: email, Password: "wjc11111",
			Code: mailer.last(email),
		})
		return err
	}
	if err := register("wjcwqc", "wjcwqc@gmail.com"); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if err := register("other", "wjcwqc@gmail.com"); ErrorCode(err) != CodeEmailTaken {
		t.Fatalf("duplicate e-mail err = %v, want %s", err, CodeEmailTaken)
	}
	if err := register("WJCWQC", "other@gmail.com"); ErrorCode(err) != CodeUsernameTaken {
		t.Fatalf("duplicate username err = %v, want %s (case-insensitive)", err, CodeUsernameTaken)
	}
}

func TestRegisterValidatesItsInputs(t *testing.T) {
	mailer := newCaptureMailer()
	clock := newTestClock()
	service, _ := newTestService(t, mailer, clock)
	ctx := context.Background()

	cases := []struct {
		name string
		req  RegisterRequest
		want string
	}{
		{"short username", RegisterRequest{Username: "ab", Email: "a@b.com", Password: "wjc11111", Code: "123456"}, CodeUsernameInvalid},
		{"username with a space", RegisterRequest{Username: "wjc wqc", Email: "a@b.com", Password: "wjc11111", Code: "123456"}, CodeUsernameInvalid},
		{"bad e-mail", RegisterRequest{Username: "wjcwqc", Email: "nope", Password: "wjc11111", Code: "123456"}, CodeEmailInvalid},
		{"short password", RegisterRequest{Username: "wjcwqc", Email: "a@b.com", Password: "short", Code: "123456"}, CodePasswordWeak},
		{"short code", RegisterRequest{Username: "wjcwqc", Email: "a@b.com", Password: "wjc11111", Code: "12"}, CodeVerificationRequired},
		{"non-numeric code", RegisterRequest{Username: "wjcwqc", Email: "a@b.com", Password: "wjc11111", Code: "abcdef"}, CodeVerificationRequired},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := service.Register(ctx, testCase.req); ErrorCode(err) != testCase.want {
				t.Fatalf("err = %v, want %s", err, testCase.want)
			}
		})
	}
}

func TestGeneratedCodesAreDigits(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := generateVerificationCode()
		if err != nil {
			t.Fatalf("generateVerificationCode: %v", err)
		}
		if len(code) != VerificationCodeLength {
			t.Fatalf("code = %q, want %d digits", code, VerificationCodeLength)
		}
		for _, r := range code {
			if r < '0' || r > '9' {
				t.Fatalf("code = %q contains a non-digit", code)
			}
		}
		seen[code] = true
	}
	if len(seen) < 100 {
		t.Fatalf("only %d distinct codes in 200 draws: the generator is not random", len(seen))
	}
}

func TestNewAccountIDIsUniqueAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		id := NewAccountID()
		if !strings.HasPrefix(id, "acct_") {
			t.Fatalf("id = %q, want the acct_ prefix", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestValidateEmailAndUsername(t *testing.T) {
	valid := []string{"wjcwqc@gmail.com", "a.b+c@sub.domain.co", "WJC@QQ.COM"}
	for _, email := range valid {
		if err := ValidateEmail(email); err != nil {
			t.Errorf("ValidateEmail(%q) = %v, want nil", email, err)
		}
	}
	invalid := []string{"", "nope", "a@b", "@b.com", "a@.com", "a b@c.com"}
	for _, email := range invalid {
		if err := ValidateEmail(email); err == nil {
			t.Errorf("ValidateEmail(%q) = nil, want an error", email)
		}
	}
	if err := ValidateUsername("wjcwqc_01"); err != nil {
		t.Errorf("ValidateUsername: %v", err)
	}
	for _, username := range []string{"", "ab", strings.Repeat("a", 25), "wjc-wqc", "wjc.wqc"} {
		if err := ValidateUsername(username); err == nil {
			t.Errorf("ValidateUsername(%q) = nil, want an error", username)
		}
	}
}

func TestUpsertAccountKeepsTheAccountID(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	first, err := store.UpsertAccount(ctx, NewAccount{
		AccountID: "acct_keep", Username: "wjcwqc", Email: "wjcwqc@gmail.com",
		PasswordHash: "hash-a", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	second, err := store.UpsertAccount(ctx, NewAccount{
		AccountID: "acct_other", Username: "wjcwqc", Email: "wjcwqc@gmail.com",
		PasswordHash: "hash-b", EmailVerified: true,
	})
	if err != nil {
		t.Fatalf("UpsertAccount (second): %v", err)
	}
	if second.AccountID != first.AccountID {
		t.Fatalf("AccountID = %q, want the original %q", second.AccountID, first.AccountID)
	}
	if second.PasswordHash != "hash-b" {
		t.Fatalf("PasswordHash = %q, want the replacement", second.PasswordHash)
	}
	if store.Len() != 1 {
		t.Fatalf("store.Len() = %d, want 1", store.Len())
	}
}

func TestMemoryStoreReportsNotFound(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	if _, err := store.AccountByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("err = %v, want ErrAccountNotFound", err)
	}
	if _, err := store.AccountByUsername(ctx, "nobody"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("err = %v, want ErrAccountNotFound", err)
	}
	if _, err := store.AccountByID(ctx, "acct_missing"); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("err = %v, want ErrAccountNotFound", err)
	}
	if err := store.TouchLogin(ctx, "acct_missing", time.Now()); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("err = %v, want ErrAccountNotFound", err)
	}
}
