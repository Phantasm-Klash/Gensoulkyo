package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"gensoulkyo/runtime/account"
)

// stubMailer captures verification codes so the test can complete the
// registration round-trip without an SMTP server.
type stubMailer struct {
	mu   sync.Mutex
	code map[string]string
}

func (m *stubMailer) Enabled() bool { return true }

func (m *stubMailer) SendVerificationCode(_ context.Context, to string, code string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.code == nil {
		m.code = map[string]string{}
	}
	m.code[account.NormalizeEmail(to)] = code
	return nil
}

func (m *stubMailer) last(to string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.code[account.NormalizeEmail(to)]
}

func newAccountService(t *testing.T) (*Service, *stubMailer) {
	t.Helper()
	mailer := &stubMailer{}
	accounts := account.NewService(account.Config{
		Store:      account.NewMemoryStore(),
		Mailer:     mailer,
		CodeSecret: "core-test-secret",
	})
	return NewService(Config{Accounts: accounts}), mailer
}

func registerAccount(t *testing.T, service *Service, mailer *stubMailer, username, email, password string) *AuthSession {
	t.Helper()
	if _, err := service.RequestEmailCode(AccountEmailCodeRequest{Email: email}); err != nil {
		t.Fatalf("RequestEmailCode: %v", err)
	}
	session, err := service.RegisterAccount(AccountRegisterRequest{
		Username: username,
		Email:    email,
		Password: password,
		Code:     mailer.last(email),
		Platform: "test",
	})
	if err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}
	return session
}

func TestRegisterAccountIssuesAUsableSession(t *testing.T) {
	service, mailer := newAccountService(t)
	session := registerAccount(t, service, mailer, "wjcwqc", "wjcwqc@gmail.com", "wjc11111")

	if session.SessionToken == "" {
		t.Fatal("register returned an empty session token")
	}
	if session.DisplayName != "wjcwqc" {
		t.Fatalf("DisplayName = %q, want the username", session.DisplayName)
	}
	// The session must be a first-class lobby session, not just a token: every
	// authenticated route goes through userBySessionLocked.
	snapshot, err := service.Bootstrap(session.SessionToken)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if snapshot.UserID != session.UserID {
		t.Fatalf("Bootstrap user id = %q, want %q", snapshot.UserID, session.UserID)
	}
	if snapshot.DisplayName != "wjcwqc" {
		t.Fatalf("Bootstrap display name = %q", snapshot.DisplayName)
	}
}

func TestRequestEmailCodeReportsTheCodeWindow(t *testing.T) {
	service, _ := newAccountService(t)
	receipt, err := service.RequestEmailCode(AccountEmailCodeRequest{Email: "wjcwqc@gmail.com"})
	if err != nil {
		t.Fatalf("RequestEmailCode: %v", err)
	}
	if receipt.Email != "wjcwqc@gmail.com" {
		t.Fatalf("Email = %q", receipt.Email)
	}
	if receipt.ExpiresInSecond <= 0 {
		t.Fatalf("ExpiresInSecond = %d, want > 0", receipt.ExpiresInSecond)
	}
	if receipt.ResendInSecond <= 0 {
		t.Fatalf("ResendInSecond = %d, want > 0", receipt.ResendInSecond)
	}
	if receipt.ServerTime.IsZero() {
		t.Fatal("ServerTime must be set so the client can run a trustworthy countdown")
	}
}

func TestLoginAccountSupersedesThePreviousSession(t *testing.T) {
	service, mailer := newAccountService(t)
	first := registerAccount(t, service, mailer, "wjcwqc", "wjcwqc@gmail.com", "wjc11111")

	var (
		mu      sync.Mutex
		revoked []string
	)
	service.SetSessionRevokedHook(func(userID string, sessionToken string) {
		mu.Lock()
		defer mu.Unlock()
		if userID != first.UserID {
			t.Errorf("revoked userID = %q, want %q", userID, first.UserID)
		}
		revoked = append(revoked, sessionToken)
	})

	second, err := service.LoginAccount(AccountLoginRequest{Email: "wjcwqc@gmail.com", Password: "wjc11111"})
	if err != nil {
		t.Fatalf("LoginAccount: %v", err)
	}
	if second.UserID != first.UserID {
		t.Fatalf("UserID = %q, want the same account %q", second.UserID, first.UserID)
	}
	if second.SessionToken == first.SessionToken {
		t.Fatal("a second sign-in must issue a fresh token")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(revoked) != 1 || revoked[0] != first.SessionToken {
		t.Fatalf("revoked = %v, want exactly the first token %q", revoked, first.SessionToken)
	}
	// The superseded token is dead.
	if _, err := service.Bootstrap(first.SessionToken); err == nil {
		t.Fatal("the superseded session token still authenticates")
	}
	if _, err := service.Bootstrap(second.SessionToken); err != nil {
		t.Fatalf("the new session token was rejected: %v", err)
	}
}

func TestLoginAccountRejectsBadCredentials(t *testing.T) {
	service, mailer := newAccountService(t)
	registerAccount(t, service, mailer, "wjcwqc", "wjcwqc@gmail.com", "wjc11111")

	cases := []struct {
		name string
		req  AccountLoginRequest
		want string
	}{
		{"wrong password", AccountLoginRequest{Email: "wjcwqc@gmail.com", Password: "nope-nope"}, "unauthorized"},
		{"unknown account", AccountLoginRequest{Email: "nobody@gmail.com", Password: "wjc11111"}, "unauthorized"},
		{"malformed e-mail", AccountLoginRequest{Email: "wjcwqc", Password: "wjc11111"}, "unauthorized"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.LoginAccount(testCase.req)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := ErrorCode(err); got != testCase.want {
				t.Fatalf("ErrorCode = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestRegisterAccountPropagatesValidationCodes(t *testing.T) {
	service, mailer := newAccountService(t)
	if _, err := service.RequestEmailCode(AccountEmailCodeRequest{Email: "wjcwqc@gmail.com"}); err != nil {
		t.Fatalf("RequestEmailCode: %v", err)
	}
	code := mailer.last("wjcwqc@gmail.com")

	cases := []struct {
		name string
		req  AccountRegisterRequest
		want string
	}{
		{"bad username", AccountRegisterRequest{Username: "ab", Email: "a@b.com", Password: "wjc11111", Code: code}, account.CodeUsernameInvalid},
		{"bad e-mail", AccountRegisterRequest{Username: "wjcwqc", Email: "nope", Password: "wjc11111", Code: code}, account.CodeEmailInvalid},
		{"weak password", AccountRegisterRequest{Username: "wjcwqc", Email: "a@b.com", Password: "short", Code: code}, account.CodePasswordWeak},
		{"missing code", AccountRegisterRequest{Username: "wjcwqc", Email: "a@b.com", Password: "wjc11111"}, account.CodeVerificationRequired},
		{"wrong code", AccountRegisterRequest{Username: "wjcwqc", Email: "wjcwqc@gmail.com", Password: "wjc11111", Code: "000000"}, account.CodeVerificationInvalid},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.name == "wrong code" && code == "000000" {
				t.Skip("the random code collided with the wrong-code probe")
			}
			_, err := service.RegisterAccount(testCase.req)
			if got := ErrorCode(err); got != testCase.want {
				t.Fatalf("ErrorCode = %q (err %v), want %q", got, err, testCase.want)
			}
		})
	}
}

func TestLogoutAccountInvalidatesTheToken(t *testing.T) {
	service, mailer := newAccountService(t)
	session := registerAccount(t, service, mailer, "wjcwqc", "wjcwqc@gmail.com", "wjc11111")

	if err := service.LogoutAccount(session.SessionToken); err != nil {
		t.Fatalf("LogoutAccount: %v", err)
	}
	if _, err := service.Bootstrap(session.SessionToken); err == nil {
		t.Fatal("the session token still authenticates after logout")
	}
	// Signing out twice, or with an unknown token, must not be an error: the
	// caller's intent is already satisfied.
	if err := service.LogoutAccount(session.SessionToken); err != nil {
		t.Fatalf("second LogoutAccount: %v", err)
	}
	if err := service.LogoutAccount("session_does_not_exist"); err != nil {
		t.Fatalf("LogoutAccount with an unknown token: %v", err)
	}
	if err := service.LogoutAccount(""); ErrorCode(err) != "unauthorized" {
		t.Fatalf("LogoutAccount with no token: err = %v", err)
	}
}

func TestAccountRoutesReportWhenTheSubsystemIsMissing(t *testing.T) {
	service := NewService(Config{})
	if _, err := service.RequestEmailCode(AccountEmailCodeRequest{Email: "wjcwqc@gmail.com"}); ErrorCode(err) != codeAccountsUnavailable {
		t.Fatalf("err = %v, want %s", err, codeAccountsUnavailable)
	}
	if _, err := service.RegisterAccount(AccountRegisterRequest{Username: "wjcwqc", Email: "a@b.com", Password: "wjc11111", Code: "123456"}); ErrorCode(err) != codeAccountsUnavailable {
		t.Fatalf("err = %v, want %s", err, codeAccountsUnavailable)
	}
	if _, err := service.LoginAccount(AccountLoginRequest{Email: "a@b.com", Password: "wjc11111"}); ErrorCode(err) != codeAccountsUnavailable {
		t.Fatalf("err = %v, want %s", err, codeAccountsUnavailable)
	}
	status := service.AccountStatus()
	if status.Configured {
		t.Fatal("AccountStatus.Configured must be false without a subsystem")
	}
	if status.PasswordAlg != account.PasswordAlgorithm {
		t.Fatalf("PasswordAlg = %q", status.PasswordAlg)
	}
}

func TestAccountStatusReportsMailAvailability(t *testing.T) {
	service, _ := newAccountService(t)
	status := service.AccountStatus()
	if !status.Configured || !status.RegistrationOpen || !status.MailEnabled {
		t.Fatalf("status = %+v, want everything enabled", status)
	}
}
