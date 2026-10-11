package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/pkg/sms"
)

// Challenge is one outstanding OTP. The plaintext code is never stored: only a
// keyed hash that also binds the phone, so a hash cannot be replayed against a
// different number.
type Challenge struct {
	ID         uuid.UUID
	Phone      string
	CodeHash   string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	Attempts   int
	ConsumedAt *time.Time
}

// Store is the persistence the auth package needs. Implemented by pgStore for
// production and by an in-memory fake in internal/testsupport.
type Store interface {
	InsertChallenge(ctx context.Context, c Challenge) error
	LatestChallenge(ctx context.Context, phone string) (Challenge, error)
	// RecordAttempt increments the attempt counter and returns the new value.
	RecordAttempt(ctx context.Context, id uuid.UUID) (int, error)
	ConsumeChallenge(ctx context.Context, id uuid.UUID) error

	AccountByPhone(ctx context.Context, phone string) (Account, error)
	AccountByID(ctx context.Context, id uuid.UUID) (Account, error)
	CreateAccount(ctx context.Context, phone string, role Role, displayName string) (Account, error)
}

// OTP is the issue/verify service.
//
// Role assignment on first verify: an unknown phone becomes a parent account.
// Teacher and admin accounts are provisioned out of band (POST /admin/accounts,
// or the -bootstrap-admin flag for the first one) and must exist before their
// holder can sign in, so self-service sign-in can never mint elevated access.
type OTP struct {
	Store          Store
	Sender         sms.Sender
	Pepper         []byte
	TTL            time.Duration
	MaxAttempts    int
	ResendInterval time.Duration
	Length         int
	Now            func() time.Time
}

func (o OTP) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Request issues a code for phone and hands it to the SMS gateway. It returns
// the normalized phone so the caller can log it consistently.
func (o OTP) Request(ctx context.Context, rawPhone string) (string, error) {
	phone, err := NormalizePhone(rawPhone)
	if err != nil {
		return "", err
	}
	now := o.now().UTC()

	switch last, err := o.Store.LatestChallenge(ctx, phone); {
	case err == nil:
		if now.Sub(last.IssuedAt) < o.ResendInterval {
			return phone, ErrResendTooSoon
		}
	case errors.Is(err, ErrNoChallenge):
		// first code for this number
	default:
		return phone, err
	}

	code, err := randomDigits(o.Length)
	if err != nil {
		return phone, err
	}
	ch := Challenge{
		ID:        uuid.New(),
		Phone:     phone,
		CodeHash:  o.Hash(phone, code),
		IssuedAt:  now,
		ExpiresAt: now.Add(o.TTL),
	}
	if err := o.Store.InsertChallenge(ctx, ch); err != nil {
		return phone, err
	}
	msg := fmt.Sprintf("ProLeaf verification code: %s. Valid for %d minutes.", code, int(o.TTL.Minutes()))
	if err := o.Sender.Send(ctx, phone, msg); err != nil {
		return phone, fmt.Errorf("auth: send otp: %w", err)
	}
	return phone, nil
}

// Verify consumes the outstanding challenge and returns the account, creating
// a parent account for a phone seen for the first time.
func (o OTP) Verify(ctx context.Context, rawPhone, code string) (Account, error) {
	phone, err := NormalizePhone(rawPhone)
	if err != nil {
		return Account{}, err
	}
	ch, err := o.Store.LatestChallenge(ctx, phone)
	if err != nil {
		return Account{}, err
	}
	if ch.ConsumedAt != nil {
		return Account{}, ErrNoChallenge
	}
	now := o.now().UTC()
	if !now.Before(ch.ExpiresAt) {
		return Account{}, ErrChallengeExpired
	}

	// Count the attempt before comparing, so a client that abandons the request
	// mid-verify cannot buy itself a free guess.
	attempts, err := o.Store.RecordAttempt(ctx, ch.ID)
	if err != nil {
		return Account{}, err
	}
	if attempts > o.MaxAttempts {
		return Account{}, ErrTooManyAttempts
	}
	if !hmac.Equal([]byte(o.Hash(phone, code)), []byte(ch.CodeHash)) {
		return Account{}, ErrCodeMismatch
	}
	if err := o.Store.ConsumeChallenge(ctx, ch.ID); err != nil {
		return Account{}, err
	}

	acct, err := o.Store.AccountByPhone(ctx, phone)
	if errors.Is(err, ErrAccountNotFound) {
		return o.Store.CreateAccount(ctx, phone, RoleParent, "")
	}
	if err != nil {
		return Account{}, err
	}
	if acct.Disabled() {
		return Account{}, ErrAccountNotFound
	}
	return acct, nil
}

// Hash is the stored form of a code: keyed, and bound to the phone.
func (o OTP) Hash(phone, code string) string {
	m := hmac.New(sha256.New, o.Pepper)
	m.Write([]byte(phone))
	m.Write([]byte{0})
	m.Write([]byte(code))
	return hex.EncodeToString(m.Sum(nil))
}

func randomDigits(n int) (string, error) {
	if n <= 0 {
		return "", errors.New("auth: otp length must be positive")
	}
	out := make([]byte, n)
	for i := range out {
		d, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		out[i] = byte('0' + d.Int64())
	}
	return string(out), nil
}
