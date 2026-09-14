package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// tokenBytes is the size of a session token before encoding. 256 bits of
// randomness puts guessing a live token out of reach, which is what lets the
// hash below be a plain digest rather than a password hash.
const tokenBytes = 32

// NewSessionToken returns a fresh session token. The value it returns is the
// only place the usable token ever exists: it goes into the cookie, and the
// database stores nothing but its hash.
//
// The encoding is base64url without padding, so the token survives a cookie
// value, a URL and a header without escaping.
func NewSessionToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the hex-encoded SHA-256 of a session token. This is what
// the sessions table holds and what a lookup matches on.
//
// SHA-256 and not argon2, which is the surprising half given Hash next door.
// A password hash is deliberately slow because passwords are short, low-entropy
// and guessable, so an attacker with the table can mount an offline search.
// A token from NewSessionToken carries 256 bits of randomness and is not
// guessable at any cost, so there is no search to slow down: argon2 here would
// add its full cost to every single authenticated request and buy nothing.
//
// The property that matters is preimage resistance, which is what stops a leak
// of the table from yielding usable tokens, and SHA-256 has it.
//
// It is deterministic and cannot fail, so it returns no error: the same token
// must always produce the same lookup key.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
