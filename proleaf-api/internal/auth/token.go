package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Tokens issues and parses compact HS256 JWTs.
//
// Hand-rolled rather than pulled from a library on purpose: the skeleton needs
// exactly one algorithm with no negotiation, and "alg: none" is the classic way
// a JWT library gets misused. Parse here accepts HS256 and nothing else.
type Tokens struct {
	Secret []byte
	TTL    time.Duration
	Issuer string
	Now    func() time.Time // nil means time.Now
}

type Claims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Role      Role   `json:"role"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	TokenID   string `json:"jti"`
}

var (
	ErrTokenMalformed = errors.New("auth: token malformed")
	ErrTokenSignature = errors.New("auth: token signature invalid")
	ErrTokenExpired   = errors.New("auth: token expired")
	ErrTokenIssuer    = errors.New("auth: token issuer mismatch")
)

var b64 = base64.RawURLEncoding

func (t Tokens) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Issue returns a signed token and the moment it stops being valid.
func (t Tokens) Issue(accountID uuid.UUID, role Role) (string, time.Time, error) {
	now := t.now().UTC()
	exp := now.Add(t.TTL)
	claims := Claims{
		Issuer:    t.Issuer,
		Subject:   accountID.String(),
		Role:      role,
		IssuedAt:  now.Unix(),
		ExpiresAt: exp.Unix(),
		TokenID:   uuid.NewString(),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	header := b64.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	signing := header + "." + b64.EncodeToString(payload)
	return signing + "." + b64.EncodeToString(t.sign(signing)), exp, nil
}

// Parse verifies the signature, algorithm, issuer and expiry, in that order.
func (t Tokens) Parse(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrTokenMalformed
	}
	rawHeader, err := b64.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrTokenMalformed
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(rawHeader, &header); err != nil {
		return Claims{}, ErrTokenMalformed
	}
	if header.Alg != "HS256" {
		return Claims{}, ErrTokenSignature
	}

	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return Claims{}, ErrTokenMalformed
	}
	if !hmac.Equal(sig, t.sign(parts[0]+"."+parts[1])) {
		return Claims{}, ErrTokenSignature
	}

	rawPayload, err := b64.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrTokenMalformed
	}
	var claims Claims
	if err := json.Unmarshal(rawPayload, &claims); err != nil {
		return Claims{}, ErrTokenMalformed
	}
	if t.Issuer != "" && claims.Issuer != t.Issuer {
		return Claims{}, ErrTokenIssuer
	}
	if !claims.Role.Valid() {
		return Claims{}, ErrTokenMalformed
	}
	if _, err := uuid.Parse(claims.Subject); err != nil {
		return Claims{}, ErrTokenMalformed
	}
	if t.now().UTC().Unix() >= claims.ExpiresAt {
		return Claims{}, ErrTokenExpired
	}
	return claims, nil
}

func (t Tokens) sign(s string) []byte {
	m := hmac.New(sha256.New, t.Secret)
	m.Write([]byte(s))
	return m.Sum(nil)
}
