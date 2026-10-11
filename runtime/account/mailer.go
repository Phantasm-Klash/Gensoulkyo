package account

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// ErrMailUnavailable is returned when no SMTP transport is configured, or when
// the configured transport could not deliver the message. The caller surfaces
// it as the wire code `mail_unavailable` so the client can say "we could not
// send the e-mail" instead of silently waiting for a code that never arrives.
var ErrMailUnavailable = errors.New("mail transport is unavailable")

// Mailer delivers e-mail verification codes. Implementations must be safe for
// concurrent use: the lobby serves many HTTP requests in parallel.
type Mailer interface {
	// SendVerificationCode delivers code to the given address. It must return
	// an error (wrapping ErrMailUnavailable is conventional) when delivery
	// fails, so the caller can drop the pending code instead of leaving the
	// user waiting for a message that will never come.
	SendVerificationCode(ctx context.Context, to string, code string, ttl time.Duration) error
	// Enabled reports whether the mailer can attempt delivery at all.
	Enabled() bool
}

// SMTPConfig describes the outbound SMTP relay.
//
// The lobby is behind a cloud firewall that blocks port 25 (verified on the
// gateway host: smtp.ym.163.com:25 times out, 465/994/587 connect), so the
// default is implicit TLS on 465.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	// UseSSL dials an implicit-TLS connection (SMTPS, normally port 465).
	UseSSL bool
	// UseSTARTTLS upgrades a plaintext connection in-band (normally port 587).
	UseSTARTTLS bool
	// Timeout bounds the whole delivery attempt.
	Timeout time.Duration
	// Brand is the product name shown in the message subject and body.
	Brand string
}

// DefaultSMTPPort is the implicit-TLS port used when none is configured.
const DefaultSMTPPort = 465

// DefaultMailBrand is the fallback product name used in outbound mail.
const DefaultMailBrand = "纷争幻想"

// SMTPConfigFromEnv reads the relay configuration from the environment.
//
// `GENSOULKYO_SMTP_*` wins; the older `GOTOUHOU_SMTP_*` keys used by the
// hourly progress mailer are accepted as a fallback so one set of credentials
// can serve both. Nothing here logs or echoes the password.
func SMTPConfigFromEnv() SMTPConfig {
	get := func(keys ...string) string {
		for _, key := range keys {
			if value := strings.TrimSpace(os.Getenv(key)); value != "" {
				return value
			}
		}
		return ""
	}
	config := SMTPConfig{
		Host:     get("GENSOULKYO_SMTP_HOST", "GOTOUHOU_SMTP_HOST"),
		Username: get("GENSOULKYO_SMTP_USER", "GOTOUHOU_SMTP_USER"),
		Password: get("GENSOULKYO_SMTP_PASSWORD", "GOTOUHOU_SMTP_PASSWORD"),
		From:     get("GENSOULKYO_SMTP_FROM", "GOTOUHOU_MAIL_FROM", "GOTOUHOU_SMTP_USER", "GENSOULKYO_SMTP_USER"),
		Brand:    get("GENSOULKYO_MAIL_BRAND"),
	}
	if raw := get("GENSOULKYO_SMTP_PORT", "GOTOUHOU_SMTP_PORT"); raw != "" {
		if port, err := strconv.Atoi(raw); err == nil && port > 0 && port < 65536 {
			config.Port = port
		}
	}
	if config.Port == 0 {
		config.Port = DefaultSMTPPort
	}
	if config.Brand == "" {
		config.Brand = DefaultMailBrand
	}
	config.UseSSL = truthy(get("GENSOULKYO_SMTP_SSL")) || (config.Port == 465 && !explicitFalse(get("GENSOULKYO_SMTP_SSL")))
	config.UseSTARTTLS = truthy(get("GENSOULKYO_SMTP_STARTTLS", "GOTOUHOU_SMTP_STARTTLS"))
	return config
}

// Enabled reports whether the configuration has enough information to dial.
func (config SMTPConfig) Enabled() bool {
	return strings.TrimSpace(config.Host) != "" && strings.TrimSpace(config.From) != ""
}

// SMTPMailer delivers verification codes over SMTP.
type SMTPMailer struct {
	config SMTPConfig
}

// NewSMTPMailer builds a mailer for the given relay configuration.
func NewSMTPMailer(config SMTPConfig) *SMTPMailer {
	if config.Port == 0 {
		config.Port = DefaultSMTPPort
	}
	if config.Brand == "" {
		config.Brand = DefaultMailBrand
	}
	return &SMTPMailer{config: config}
}

// Enabled reports whether a relay host and sender are configured.
func (m *SMTPMailer) Enabled() bool {
	return m != nil && m.config.Enabled()
}

// Config returns a copy of the mailer's configuration, for status reporting.
func (m *SMTPMailer) Config() SMTPConfig {
	if m == nil {
		return SMTPConfig{}
	}
	return m.config
}

// SendVerificationCode renders and delivers the verification e-mail.
func (m *SMTPMailer) SendVerificationCode(ctx context.Context, to string, code string, ttl time.Duration) error {
	if !m.Enabled() {
		return fmt.Errorf("%w: no SMTP host configured", ErrMailUnavailable)
	}
	recipient := strings.TrimSpace(to)
	if recipient == "" {
		return fmt.Errorf("%w: recipient is required", ErrMailUnavailable)
	}
	if ttl <= 0 {
		ttl = DefaultVerificationTTL
	}
	message := buildVerificationMessage(m.config.From, recipient, m.config.Brand, code, ttl)
	if err := m.deliver(ctx, recipient, message); err != nil {
		return fmt.Errorf("%w: %v", ErrMailUnavailable, err)
	}
	return nil
}

func (m *SMTPMailer) deliver(ctx context.Context, recipient string, message []byte) error {
	timeout := m.config.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	address := net.JoinHostPort(m.config.Host, strconv.Itoa(m.config.Port))
	dialer := &net.Dialer{Timeout: timeout}
	tlsConfig := &tls.Config{ServerName: m.config.Host, MinVersion: tls.VersionTLS12}

	var (
		conn net.Conn
		err  error
	)
	if m.config.UseSSL {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", address, err)
	}
	defer conn.Close()
	// A deadline covers every subsequent read/write; SMTP has no per-command
	// timeout of its own and a half-open relay would otherwise hang a handler.
	_ = conn.SetDeadline(time.Now().Add(timeout))

	client, err := smtp.NewClient(conn, m.config.Host)
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	defer client.Close()

	if !m.config.UseSSL && m.config.UseSTARTTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		} else {
			return errors.New("server does not advertise STARTTLS")
		}
	}
	if m.config.Username != "" {
		if ok, _ := client.Extension("AUTH"); ok {
			auth := smtp.PlainAuth("", m.config.Username, m.config.Password, m.config.Host)
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("auth: %w", err)
			}
		}
	}
	if err := client.Mail(m.config.From); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := client.Rcpt(recipient); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("quit: %w", err)
	}
	return nil
}

// buildVerificationMessage renders a minimal RFC 5322 text/plain message.
func buildVerificationMessage(from, to, brand, code string, ttl time.Duration) []byte {
	minutes := int(ttl.Minutes())
	if minutes <= 0 {
		minutes = 1
	}
	subject := fmt.Sprintf("%s 邮箱验证码 %s", brand, code)
	body := strings.Join([]string{
		fmt.Sprintf("你的 %s 邮箱验证码是：%s", brand, code),
		"",
		fmt.Sprintf("验证码 %d 分钟内有效，请勿转发给他人。", minutes),
		"如果这不是你本人的操作，请忽略本邮件。",
		"",
		"-- ",
		brand,
	}, "\r\n")

	headers := []string{
		"From: " + encodeAddress(from, brand),
		"To: " + to,
		"Subject: " + encodeHeader(subject),
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: base64",
	}
	encodedBody := base64.StdEncoding.EncodeToString([]byte(body))
	// RFC 2045 caps encoded lines at 76 characters.
	var wrapped strings.Builder
	for len(encodedBody) > 76 {
		wrapped.WriteString(encodedBody[:76])
		wrapped.WriteString("\r\n")
		encodedBody = encodedBody[76:]
	}
	wrapped.WriteString(encodedBody)

	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + wrapped.String() + "\r\n")
}

// encodeHeader RFC 2047-encodes a header value when it contains non-ASCII text.
func encodeHeader(value string) string {
	if isASCII(value) {
		return value
	}
	return mime.QEncoding.Encode("UTF-8", value)
}

// encodeAddress renders "Display Name <addr>" with the display name encoded.
func encodeAddress(address, displayName string) string {
	if strings.TrimSpace(displayName) == "" {
		return address
	}
	return fmt.Sprintf("%s <%s>", encodeHeader(displayName), address)
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func explicitFalse(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

// LogMailer writes codes to the log instead of sending them. It exists so a
// development lobby without SMTP credentials still exposes the full
// register/login flow.
type LogMailer struct{}

// Enabled always reports true.
func (LogMailer) Enabled() bool { return true }

// SendVerificationCode logs the code.
func (LogMailer) SendVerificationCode(_ context.Context, to string, code string, ttl time.Duration) error {
	log.Printf("account: verification code for %s is %s (valid %s)", to, code, ttl)
	return nil
}

// UnavailableMailer refuses every delivery. It is the mailer used when no SMTP
// host is configured, so the send endpoint fails loudly instead of pretending
// to have sent something.
type UnavailableMailer struct{}

// Enabled reports false.
func (UnavailableMailer) Enabled() bool { return false }

// SendVerificationCode always fails.
func (UnavailableMailer) SendVerificationCode(context.Context, string, string, time.Duration) error {
	return ErrMailUnavailable
}
