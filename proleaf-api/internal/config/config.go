// Package config loads runtime configuration from the environment.
//
// Rule: no secrets in code, and no silent defaults for secrets outside dev.
package config

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Env      string // "dev" | "staging" | "prod"
	HTTPAddr string

	DatabaseURL string

	TokenSecret []byte
	TokenTTL    time.Duration
	TokenIssuer string

	OTPPepper         []byte
	OTPTTL            time.Duration
	OTPMaxAttempts    int
	OTPResendInterval time.Duration
	OTPLength         int

	EnrollmentCodeTTL    time.Duration
	EnrollmentCodePepper []byte

	// PolicyVersion is the consent policy currently in force. A parent whose
	// latest consent event names an older version has to re-consent.
	PolicyVersion string

	// StatsMinCohort suppresses class aggregates for classes smaller than this.
	// A mean over three students is close enough to an individual score to be
	// treated as one.
	StatsMinCohort int

	SMSProvider string // "log" | "aliyun"
}

func (c Config) IsDev() bool { return c.Env == "dev" }

// Load reads configuration from the environment. It fails rather than guessing
// when a secret is missing outside dev.
func Load() (Config, error) {
	c := Config{
		Env:               env("PROLEAF_ENV", "dev"),
		HTTPAddr:          env("PROLEAF_HTTP_ADDR", ":8080"),
		DatabaseURL:       env("PROLEAF_DATABASE_URL", "postgres://proleaf:proleaf@localhost:5432/proleaf?sslmode=disable"),
		TokenTTL:          envDuration("PROLEAF_TOKEN_TTL", 12*time.Hour),
		TokenIssuer:       env("PROLEAF_TOKEN_ISSUER", "proleaf-api"),
		OTPTTL:            envDuration("PROLEAF_OTP_TTL", 5*time.Minute),
		OTPMaxAttempts:    envInt("PROLEAF_OTP_MAX_ATTEMPTS", 5),
		OTPResendInterval: envDuration("PROLEAF_OTP_RESEND_INTERVAL", 60*time.Second),
		OTPLength:         envInt("PROLEAF_OTP_LENGTH", 6),
		EnrollmentCodeTTL: envDuration("PROLEAF_ENROLLMENT_CODE_TTL", 14*24*time.Hour),
		PolicyVersion:     env("PROLEAF_POLICY_VERSION", "2026-01-01"),
		StatsMinCohort:    envInt("PROLEAF_STATS_MIN_COHORT", 5),
		SMSProvider:       env("PROLEAF_SMS_PROVIDER", "log"),
	}

	var err error
	if c.TokenSecret, err = secret("PROLEAF_TOKEN_SECRET", c.Env); err != nil {
		return Config{}, err
	}
	if c.OTPPepper, err = secret("PROLEAF_OTP_PEPPER", c.Env); err != nil {
		return Config{}, err
	}
	if c.EnrollmentCodePepper, err = secret("PROLEAF_ENROLLMENT_CODE_PEPPER", c.Env); err != nil {
		return Config{}, err
	}

	if c.StatsMinCohort < 1 {
		return Config{}, errors.New("config: PROLEAF_STATS_MIN_COHORT must be >= 1")
	}
	return c, nil
}

// secret returns the named secret, or an ephemeral one in dev. An ephemeral
// secret invalidates every issued token on restart, which is the point: it is
// unusable in anything but local development.
func secret(key, environment string) ([]byte, error) {
	if v := os.Getenv(key); v != "" {
		if len(v) < 32 {
			return nil, fmt.Errorf("config: %s must be at least 32 bytes", key)
		}
		return []byte(v), nil
	}
	if environment != "dev" {
		return nil, fmt.Errorf("config: %s is required when PROLEAF_ENV=%s", key, environment)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	slog.Warn("generated ephemeral secret for dev; tokens will not survive a restart", "key", key)
	return b, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		slog.Warn("ignoring unparseable env value", "key", key, "value", v)
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		slog.Warn("ignoring unparseable env value", "key", key, "value", v)
	}
	return def
}
