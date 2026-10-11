package enroll

import (
	"strings"
	"testing"
)

func TestGenerateShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 14 || strings.Count(code, "-") != 2 {
			t.Fatalf("unexpected display form %q", code)
		}
		if seen[code] {
			t.Fatalf("generated a duplicate within 200 draws: %q", code)
		}
		seen[code] = true

		normalized, err := Normalize(code)
		if err != nil {
			t.Fatalf("a generated code does not normalize: %q: %v", code, err)
		}
		if len(normalized) != codeLength {
			t.Fatalf("want %d characters, got %d", codeLength, len(normalized))
		}
		// The alphabet excludes the confusable characters, so normalizing a
		// freshly generated code must be a pure strip, never a substitution.
		if normalized != strings.ReplaceAll(code, "-", "") {
			t.Fatalf("normalize altered a generated code: %q -> %q", code, normalized)
		}
	}
}

func TestNormalizeFoldsConfusableCharacters(t *testing.T) {
	cases := map[string]string{
		"k7m2-9qht-4rxb": "K7M29QHT4RXB",
		"K7M2 9QHT 4RXB": "K7M29QHT4RXB",
		"K7M29QHT4RXB":   "K7M29QHT4RXB",
		// O for 0, I and L for 1, U for V: what people actually type.
		"OIL2-9QHT-4RXB": "0112" + "9QHT4RXB",
		"K7M2-9QHT-4RXU": "K7M29QHT4RXV",
	}
	for in, want := range cases {
		got, err := Normalize(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q: want %q, got %q", in, want, got)
		}
	}

	for _, in := range []string{"", "SHORT", "K7M2-9QHT-4RX", "K7M2-9QHT-4RXB-EXTRA", "!!!!-!!!!-!!!!"} {
		if _, err := Normalize(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

func TestHashIsKeyedAndStable(t *testing.T) {
	pepper := []byte("a-pepper")
	other := []byte("a-different-pepper")

	h1 := Hash(pepper, "K7M29QHT4RXB")
	h2 := Hash(pepper, "K7M29QHT4RXB")
	if h1 != h2 {
		t.Fatal("hashing is not deterministic")
	}
	if h1 == Hash(other, "K7M29QHT4RXB") {
		t.Fatal("the pepper does not affect the hash")
	}
	if strings.Contains(h1, "K7M2") {
		t.Fatal("the hash contains the code")
	}
	if len(h1) != 64 {
		t.Fatalf("want a 64-character hex digest, got %d", len(h1))
	}
}

// The two spellings of the same code must hash the same, or a parent typing
// the O in place of a 0 cannot bind.
func TestHashRawIsSpellingInsensitive(t *testing.T) {
	pepper := []byte("a-pepper")

	a, err := HashRaw(pepper, "0112-9QHT-4RXB")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashRaw(pepper, "oil2 9qht 4rxb")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("equivalent spellings hash differently")
	}
}
