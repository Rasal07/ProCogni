package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testTokens(now time.Time) Tokens {
	return Tokens{
		Secret: []byte("a-secret-that-is-long-enough-here"),
		TTL:    time.Hour,
		Issuer: "proleaf-test",
		Now:    func() time.Time { return now },
	}
}

func TestIssueAndParseRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	tk := testTokens(now)
	id := uuid.New()

	token, expires, err := tk.Issue(id, RoleTeacher)
	if err != nil {
		t.Fatal(err)
	}
	if !expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("want expiry %v, got %v", now.Add(time.Hour), expires)
	}

	claims, err := tk.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != id.String() {
		t.Fatalf("want subject %s, got %s", id, claims.Subject)
	}
	if claims.Role != RoleTeacher {
		t.Fatalf("want teacher, got %s", claims.Role)
	}
}

func TestParseRejectsTamperedPayload(t *testing.T) {
	now := time.Now()
	tk := testTokens(now)
	token, _, err := tk.Issue(uuid.New(), RoleParent)
	if err != nil {
		t.Fatal(err)
	}

	parts := strings.Split(token, ".")
	var claims Claims
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	// Promote to admin, keeping the original signature.
	claims.Role = RoleAdmin
	forged, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(forged)

	if _, err := tk.Parse(strings.Join(parts, ".")); err == nil {
		t.Fatal("a re-signed-payload token was accepted")
	}
}

// The classic JWT failure: a token that declares no algorithm.
func TestParseRejectsAlgNone(t *testing.T) {
	tk := testTokens(time.Now())

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(Claims{
		Issuer: "proleaf-test", Subject: uuid.NewString(), Role: RoleAdmin,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	token := header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."

	if _, err := tk.Parse(token); err == nil {
		t.Fatal("an alg:none token was accepted")
	}
}

func TestParseRejectsExpired(t *testing.T) {
	issued := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	token, _, err := testTokens(issued).Issue(uuid.New(), RoleParent)
	if err != nil {
		t.Fatal(err)
	}

	later := testTokens(issued.Add(2 * time.Hour))
	if _, err := later.Parse(token); err != ErrTokenExpired {
		t.Fatalf("want ErrTokenExpired, got %v", err)
	}
}

func TestParseRejectsForeignIssuerAndSecret(t *testing.T) {
	now := time.Now()
	token, _, err := testTokens(now).Issue(uuid.New(), RoleParent)
	if err != nil {
		t.Fatal(err)
	}

	other := testTokens(now)
	other.Secret = []byte("a-different-secret-of-sufficient-len")
	if _, err := other.Parse(token); err != ErrTokenSignature {
		t.Fatalf("want ErrTokenSignature, got %v", err)
	}

	renamed := testTokens(now)
	renamed.Issuer = "someone-else"
	if _, err := renamed.Parse(token); err != ErrTokenIssuer {
		t.Fatalf("want ErrTokenIssuer, got %v", err)
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	tk := testTokens(time.Now())
	for _, token := range []string{"", "abc", "a.b", "a.b.c.d", "!!.??.$$"} {
		if _, err := tk.Parse(token); err == nil {
			t.Errorf("%q was accepted", token)
		}
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"13800138000":     "+8613800138000",
		"138 0013 8000":   "+8613800138000",
		"138-0013-8000":   "+8613800138000",
		"+8613800138000":  "+8613800138000",
		"+14155550123":    "+14155550123",
		" +8613800138000": "+8613800138000",
	}
	for in, want := range cases {
		got, err := NormalizePhone(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: want %q, got %q", in, want, got)
		}
	}

	for _, in := range []string{"", "12345", "abcdefghijk", "0013800138000", "+0123456789"} {
		if _, err := NormalizePhone(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}
