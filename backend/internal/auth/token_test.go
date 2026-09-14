package auth

import (
	"encoding/base64"
	"strings"
	"testing"
)

// These tests need no database. Everything here is a property the session
// lookup in the api package depends on.

// TestNewSessionTokenIsUnique is the assertion the whole file exists for. A
// generator that returned a constant, or one seeded from the clock, would pass
// every other test here and hand every logged-in person the same session. The
// loop is the only thing that catches it.
func TestNewSessionTokenIsUnique(t *testing.T) {
	const runs = 1000

	seen := make(map[string]struct{}, runs)
	for i := range runs {
		token, err := NewSessionToken()
		if err != nil {
			t.Fatalf("run %d: could not mint a token: %v", i, err)
		}
		if _, dup := seen[token]; dup {
			t.Fatalf("run %d: minted a token that had already been minted: %q", i, token)
		}
		seen[token] = struct{}{}
	}
}

// TestNewSessionTokenCarriesFullEntropy pins the size, which is what lets
// HashToken be a plain digest: at 256 bits there is nothing to guess. A token
// shortened to something "tidier" would quietly turn that reasoning false.
func TestNewSessionTokenCarriesFullEntropy(t *testing.T) {
	token, err := NewSessionToken()
	if err != nil {
		t.Fatalf("could not mint a token: %v", err)
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token %q does not decode as unpadded base64url: %v", token, err)
	}

	if len(raw) != tokenBytes {
		t.Errorf("token decodes to %d bytes, want %d", len(raw), tokenBytes)
	}
}

// TestNewSessionTokenSurvivesACookie covers the reason the encoding is base64url
// rather than plain base64. The token travels as a cookie value, and '+', '/'
// and '=' all need escaping there: a token that had to be percent-encoded on the
// way out and decoded on the way back would eventually be compared in the wrong
// form and fail to match its own hash.
func TestNewSessionTokenSurvivesACookie(t *testing.T) {
	for range 100 {
		token, err := NewSessionToken()
		if err != nil {
			t.Fatalf("could not mint a token: %v", err)
		}

		if i := strings.IndexAny(token, "+/= ;,\\\""); i >= 0 {
			t.Fatalf("token %q contains %q, which a cookie value cannot carry unescaped",
				token, token[i])
		}
	}
}

// TestHashTokenIsStable is what the whole lookup rests on. The token is hashed
// once at login and again on every request that presents it, and the two have to
// land on the same primary key. A hash that salted, or that mixed in the clock,
// would make every session unfindable one request after it was created.
func TestHashTokenIsStable(t *testing.T) {
	token, err := NewSessionToken()
	if err != nil {
		t.Fatalf("could not mint a token: %v", err)
	}

	first := HashToken(token)
	for i := range 10 {
		if got := HashToken(token); got != first {
			t.Fatalf("call %d: HashToken = %q, want %q", i, got, first)
		}
	}
}

// TestHashTokenDistinguishesTokens guards the other direction: a hash that
// ignored its input would be perfectly stable and would let any token open any
// session.
func TestHashTokenDistinguishesTokens(t *testing.T) {
	if HashToken("one") == HashToken("two") {
		t.Error("two different tokens hash to the same value")
	}

	// One bit apart, so a hash that compared only a prefix fails here.
	if HashToken("token-a") == HashToken("token-b") {
		t.Error("two near-identical tokens hash to the same value")
	}
}

// TestHashTokenHidesTheToken is invariant 33 as a test. The stored hash is what
// a database leak exposes; if the token could be read back out of it, storing
// the hash rather than the token would be a formality rather than a protection.
func TestHashTokenHidesTheToken(t *testing.T) {
	const token = "a-recognisable-session-token"

	hash := HashToken(token)
	if strings.Contains(hash, token) {
		t.Errorf("hash %q contains the token it was made from", hash)
	}
}

// TestHashTokenIsHex pins the encoding, because the column is text and the
// value is compared as a string. Raw digest bytes would carry NULs that Postgres
// refuses outright.
func TestHashTokenIsHex(t *testing.T) {
	hash := HashToken("any token")

	// SHA-256 is 32 bytes, two hex characters each.
	if len(hash) != 64 {
		t.Errorf("hash is %d characters, want 64", len(hash))
	}

	if strings.TrimLeft(hash, "0123456789abcdef") != "" {
		t.Errorf("hash %q contains characters outside lowercase hex", hash)
	}
}
