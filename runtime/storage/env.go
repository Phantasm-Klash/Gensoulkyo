package storage

import (
	"net"
	"net/url"
	"os"
	"strings"
)

// Environment selects one of the two logical deployments that share the single
// Postgres instance and the single Redis instance.
//
// The project runs one Postgres container holding `nakama_prod` and
// `nakama_test`, and one Redis container whose db0 is production and db1 is
// test. That is weaker isolation than two containers, so the environment is the
// only thing allowed to decide which one a process talks to: build every
// connection string through PostgresConfig/RedisConfig instead of writing a DSN
// by hand.
type Environment string

const (
	// EnvironmentProd targets `nakama_prod` / Redis db0.
	EnvironmentProd Environment = "prod"
	// EnvironmentTest targets `nakama_test` / Redis db1.
	EnvironmentTest Environment = "test"
)

const (
	// DatabaseNameProd is the production database inside the shared Postgres.
	DatabaseNameProd = "nakama_prod"
	// DatabaseNameTest is the test database inside the shared Postgres. It holds
	// the same schema and no business data.
	DatabaseNameTest = "nakama_test"
)

const (
	envEnvironment      = "GENSOULKYO_ENV"
	envDatabaseHost     = "GENSOULKYO_DATABASE_HOST"
	envDatabasePort     = "GENSOULKYO_DATABASE_PORT"
	envDatabaseSSL      = "GENSOULKYO_DATABASE_SSLMODE"
	defaultDatabaseHost = "127.0.0.1"
	defaultDatabasePort = "5432"
	defaultSSLMode      = "disable"
)

// ParseEnvironment normalises a configured environment name. Anything that is
// not clearly a test environment resolves to production: a typo in
// GENSOULKYO_ENV must never silently point the lobby at the test database.
func ParseEnvironment(value string) Environment {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "test", "testing":
		return EnvironmentTest
	default:
		return EnvironmentProd
	}
}

// EnvironmentFromEnv reads GENSOULKYO_ENV.
func EnvironmentFromEnv() Environment {
	return ParseEnvironment(os.Getenv(envEnvironment))
}

// DatabaseName returns the database an environment is allowed to use.
func DatabaseName(env Environment) string {
	if env == EnvironmentTest {
		return DatabaseNameTest
	}
	return DatabaseNameProd
}

// PostgresConfig builds the connection config for an environment.
//
// Precedence: an explicit `GENSOULKYO_DATABASE_URL_PROD` /
// `_TEST` override wins; otherwise the parts
// (`GENSOULKYO_DATABASE_HOST`/`_PORT`/`_USER_PROD`/`_PASSWORD_PROD`/
// `_NAME_PROD`/`_SSLMODE`) are assembled. The database name is always forced to
// the one the environment owns, so a stray name in a DSN override is the only
// way to reach the wrong database and it is visible in the override itself.
func PostgresConfig(env Environment) DatabaseConfig {
	env = ParseEnvironment(string(env))
	suffix := strings.ToUpper(string(env))
	if raw := strings.TrimSpace(os.Getenv("GENSOULKYO_DATABASE_URL_" + suffix)); raw != "" {
		return NormalizeDatabaseConfig(DatabaseConfig{Driver: databaseDriverFromEnv(), URL: raw})
	}
	user := envOr("GENSOULKYO_DATABASE_USER_"+suffix, "")
	password := os.Getenv("GENSOULKYO_DATABASE_PASSWORD_" + suffix)
	host := envOr(envDatabaseHost, defaultDatabaseHost)
	port := envOr(envDatabasePort, defaultDatabasePort)
	sslMode := envOr(envDatabaseSSL, defaultSSLMode)

	target := &url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + DatabaseName(env),
	}
	if sslMode != "" {
		target.RawQuery = "sslmode=" + url.QueryEscape(sslMode)
	}
	if user != "" {
		if password != "" {
			target.User = url.UserPassword(user, password)
		} else {
			target.User = url.User(user)
		}
	}
	return NormalizeDatabaseConfig(DatabaseConfig{Driver: databaseDriverFromEnv(), URL: target.String()})
}

// PostgresConfigFromEnv is PostgresConfig(EnvironmentFromEnv()).
func PostgresConfigFromEnv() DatabaseConfig {
	return PostgresConfig(EnvironmentFromEnv())
}

func databaseDriverFromEnv() string {
	if driver := strings.TrimSpace(os.Getenv("GENSOULKYO_DATABASE_DRIVER")); driver != "" {
		return driver
	}
	return DefaultPostgresDriver
}

func envOr(key string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
