package auth_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/testsupport"
	"github.com/proleaf/proleaf-api/pkg/sms"
)

var codeIn = regexp.MustCompile(`\b\d{6}\b`)

type otpRig struct {
	otp     auth.OTP
	capture *sms.Capture
	world   *testsupport.World
	now     time.Time
}

func newOTPRig() *otpRig {
	w := testsupport.NewWorld()
	r := &otpRig{
		capture: &sms.Capture{},
		world:   w,
		now:     time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC),
	}
	r.otp = auth.OTP{
		Store:          testsupport.AuthStore{W: w},
		Sender:         r.capture,
		Pepper:         []byte("pepper"),
		TTL:            5 * time.Minute,
		MaxAttempts:    3,
		ResendInterval: time.Minute,
		Length:         6,
		Now:            func() time.Time { return r.now },
	}
	return r
}

func (r *otpRig) requestCode(t *testing.T, phone string) string {
	t.Helper()
	if _, err := r.otp.Request(context.Background(), phone); err != nil {
		t.Fatal(err)
	}
	normalized, err := auth.NormalizePhone(phone)
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := r.capture.Last(normalized)
	if !ok {
		t.Fatal("no message sent")
	}
	code := codeIn.FindString(msg.Message)
	if code == "" {
		t.Fatalf("no code in %q", msg.Message)
	}
	return code
}

func TestOTPVerifyCreatesParentAccount(t *testing.T) {
	r := newOTPRig()
	code := r.requestCode(t, "13800138000")

	acct, err := r.otp.Verify(context.Background(), "13800138000", code)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Role != auth.RoleParent {
		t.Fatalf("want a parent account, got %q", acct.Role)
	}
	if acct.Phone != "+8613800138000" {
		t.Fatalf("phone was not normalized: %q", acct.Phone)
	}
}

// An existing account keeps its role: signing in never escalates.
func TestOTPVerifyKeepsExistingRole(t *testing.T) {
	r := newOTPRig()
	r.world.AddAccount(auth.RoleAdmin, "+8613800138000")
	code := r.requestCode(t, "13800138000")

	acct, err := r.otp.Verify(context.Background(), "13800138000", code)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Role != auth.RoleAdmin {
		t.Fatalf("want the stored admin role, got %q", acct.Role)
	}
}

func TestOTPCodeIsSingleUse(t *testing.T) {
	r := newOTPRig()
	code := r.requestCode(t, "13800138000")

	if _, err := r.otp.Verify(context.Background(), "13800138000", code); err != nil {
		t.Fatal(err)
	}
	if _, err := r.otp.Verify(context.Background(), "13800138000", code); !errors.Is(err, auth.ErrNoChallenge) {
		t.Fatalf("want ErrNoChallenge on replay, got %v", err)
	}
}

func TestOTPExpires(t *testing.T) {
	r := newOTPRig()
	code := r.requestCode(t, "13800138000")

	r.now = r.now.Add(6 * time.Minute)
	if _, err := r.otp.Verify(context.Background(), "13800138000", code); !errors.Is(err, auth.ErrChallengeExpired) {
		t.Fatalf("want ErrChallengeExpired, got %v", err)
	}
}

func TestOTPAttemptsAreCapped(t *testing.T) {
	r := newOTPRig()
	code := r.requestCode(t, "13800138000")

	for i := 0; i < 3; i++ {
		if _, err := r.otp.Verify(context.Background(), "13800138000", "000000"); !errors.Is(err, auth.ErrCodeMismatch) {
			t.Fatalf("attempt %d: want ErrCodeMismatch, got %v", i+1, err)
		}
	}
	// The cap holds even for the correct code: the challenge is spent.
	if _, err := r.otp.Verify(context.Background(), "13800138000", code); !errors.Is(err, auth.ErrTooManyAttempts) {
		t.Fatalf("want ErrTooManyAttempts, got %v", err)
	}
}

func TestOTPResendIsRateLimited(t *testing.T) {
	r := newOTPRig()
	r.requestCode(t, "13800138000")

	if _, err := r.otp.Request(context.Background(), "13800138000"); !errors.Is(err, auth.ErrResendTooSoon) {
		t.Fatalf("want ErrResendTooSoon, got %v", err)
	}

	r.now = r.now.Add(61 * time.Second)
	if _, err := r.otp.Request(context.Background(), "13800138000"); err != nil {
		t.Fatalf("resend after the interval should work: %v", err)
	}
}

// A code issued for one number must not verify against another, even though
// both hashes use the same pepper.
func TestOTPCodeIsBoundToItsPhone(t *testing.T) {
	r := newOTPRig()
	code := r.requestCode(t, "13800138000")
	r.requestCode(t, "13900139000")

	if _, err := r.otp.Verify(context.Background(), "13900139000", code); !errors.Is(err, auth.ErrCodeMismatch) {
		t.Fatalf("a code verified against the wrong phone: %v", err)
	}
}

func TestOTPRejectsBadPhone(t *testing.T) {
	r := newOTPRig()
	if _, err := r.otp.Request(context.Background(), "nonsense"); !errors.Is(err, auth.ErrInvalidPhone) {
		t.Fatalf("want ErrInvalidPhone, got %v", err)
	}
}

// The plaintext code never reaches the store.
func TestOTPStoresOnlyAHash(t *testing.T) {
	r := newOTPRig()
	code := r.requestCode(t, "13800138000")

	for _, ch := range r.world.Challenges {
		if ch.CodeHash == code {
			t.Fatal("the plaintext code was stored")
		}
		if len(ch.CodeHash) != 64 {
			t.Fatalf("want a 64-character hex hash, got %q", ch.CodeHash)
		}
	}
}
