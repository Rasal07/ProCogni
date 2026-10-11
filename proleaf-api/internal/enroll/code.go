// Package enroll owns the format of enrollment codes: how they are generated,
// how they are normalized when typed back in, and how they are hashed for
// storage.
//
// It sits on its own so that admin/ (which issues codes) and parent/ (which
// consumes them) agree without importing one another.
package enroll

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
)

// alphabet is Crockford base32 without I, L, O and U: no character pair that a
// parent reading a printed slip can confuse, and no accidental words.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// codeLength of 12 over this alphabet is 60 bits of entropy, which is far more
// than a 14-day single-use code needs and costs nothing to type in groups of
// four.
const codeLength = 12

var ErrInvalidCode = errors.New("enroll: code is not well formed")

// Generate returns a new code in display form, e.g. "K7M2-9QHT-4RXB".
func Generate() (string, error) {
	raw := make([]byte, codeLength)
	max := big.NewInt(int64(len(alphabet)))
	for i := range raw {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		raw[i] = alphabet[n.Int64()]
	}
	s := string(raw)
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12], nil
}

// Normalize strips separators, upper-cases, and folds the characters people
// substitute by hand: O for 0, I and L for 1.
func Normalize(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.NewReplacer("-", "", " ", "", "O", "0", "I", "1", "L", "1", "U", "V").Replace(s)
	if len(s) != codeLength {
		return "", ErrInvalidCode
	}
	for _, r := range s {
		if !strings.ContainsRune(alphabet, r) {
			return "", ErrInvalidCode
		}
	}
	return s, nil
}

// Hash is what the database stores. Keyed, so a leaked table is not a list of
// usable codes.
func Hash(pepper []byte, normalized string) string {
	m := hmac.New(sha256.New, pepper)
	m.Write([]byte(normalized))
	return hex.EncodeToString(m.Sum(nil))
}

// HashRaw normalizes then hashes, for the common case of a code typed by a user.
func HashRaw(pepper []byte, raw string) (string, error) {
	n, err := Normalize(raw)
	if err != nil {
		return "", err
	}
	return Hash(pepper, n), nil
}
