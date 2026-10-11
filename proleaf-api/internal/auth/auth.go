// Package auth issues and verifies identity: phone + OTP in, bearer token out,
// role middleware on the way back in.
//
// The token carries an account id and a role and nothing else. Anything a
// handler needs beyond that is looked up server-side.
package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Role string

const (
	RoleTeacher Role = "teacher"
	RoleParent  Role = "parent"
	RoleAdmin   Role = "admin"
)

func (r Role) Valid() bool {
	switch r {
	case RoleTeacher, RoleParent, RoleAdmin:
		return true
	}
	return false
}

type Account struct {
	ID          uuid.UUID  `json:"id"`
	Phone       string     `json:"phone"`
	Role        Role       `json:"role"`
	DisplayName string     `json:"display_name"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
}

func (a Account) Disabled() bool { return a.DisabledAt != nil }

// Identity is what a handler gets: the authenticated actor, already checked
// against the accounts table on this request.
type Identity struct {
	AccountID uuid.UUID
	Role      Role
}

type ctxKey int

const identityKey ctxKey = iota

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// FromContext returns the authenticated identity. The ok result is false on
// unauthenticated routes; handlers behind RequireX can rely on it being true.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}

// MustFromContext panics when there is no identity, which can only happen if a
// handler was mounted without its role middleware. Failing loudly in dev beats
// serving student data to nobody in particular.
func MustFromContext(ctx context.Context) Identity {
	id, ok := FromContext(ctx)
	if !ok {
		panic("auth: handler requires an identity but none is in context; middleware is missing")
	}
	return id
}

var (
	ErrAccountNotFound  = errors.New("auth: account not found")
	ErrNoChallenge      = errors.New("auth: no challenge")
	ErrChallengeExpired = errors.New("auth: challenge expired")
	ErrTooManyAttempts  = errors.New("auth: too many attempts")
	ErrCodeMismatch     = errors.New("auth: code mismatch")
	ErrResendTooSoon    = errors.New("auth: resend too soon")
	ErrInvalidPhone     = errors.New("auth: invalid phone")
)

var (
	phoneStrip    = regexp.MustCompile(`[\s\-()]`)
	e164          = regexp.MustCompile(`^\+[1-9]\d{7,14}$`)
	cnBareMobile  = regexp.MustCompile(`^1[3-9]\d{9}$`)
	defaultRegion = "+86"
)

// NormalizePhone produces the single stored form of a number. A bare 11-digit
// mainland mobile is accepted and prefixed, because that is how people will
// type it; everything else must already be E.164.
func NormalizePhone(raw string) (string, error) {
	s := phoneStrip.ReplaceAllString(strings.TrimSpace(raw), "")
	switch {
	case cnBareMobile.MatchString(s):
		return defaultRegion + s, nil
	case e164.MatchString(s):
		return s, nil
	default:
		return "", ErrInvalidPhone
	}
}
