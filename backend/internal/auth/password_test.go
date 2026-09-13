package auth

import (
	"errors"
	"strings"
	"testing"
)

// These tests need no database, and no TestMain: the package derives keys and
// parses strings, nothing else. They do pay the real cost — every Hash and
// every successful Verify allocates 64 MiB and holds it — so cases that only
// need a well-formed string build one by hand at a trivial cost instead.

// testPassword is the one the round-trip cases hash. It is recognisable on
// purpose, so the leak test can assert its absence from every error string.
const testPassword = "correct horse battery staple"

// mustHash fails the test rather than returning an error, so the cases that
// only care about what comes after hashing stay readable.
func mustHash(t *testing.T, password string) string {
	t.Helper()

	encoded, err := Hash(password)
	if err != nil {
		t.Fatalf("Hash(%q) returned an unexpected error: %v", password, err)
	}
	return encoded
}

// mustVerify asserts the outcome of a Verify that is expected to parse.
func mustVerify(t *testing.T, password, encoded string, want bool) {
	t.Helper()

	got, err := Verify(password, encoded)
	if err != nil {
		t.Fatalf("Verify() returned an unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("Verify() = %t, want %t", got, want)
	}
}

func TestHashVerifyRoundTrip(t *testing.T) {
	mustVerify(t, testPassword, mustHash(t, testPassword), true)
}

// TestHashEncodesItsParameters pins the stored format. It is what a reader of
// the users table sees, and what a future cost increase relies on being there.
func TestHashEncodesItsParameters(t *testing.T) {
	encoded := mustHash(t, testPassword)

	const wantPrefix = "$argon2id$v=19$m=65536,t=3,p=4$"
	if !strings.HasPrefix(encoded, wantPrefix) {
		t.Errorf("Hash() = %q, want the prefix %q", encoded, wantPrefix)
	}
	if n := len(strings.Split(encoded, "$")); n != 6 {
		t.Errorf("Hash() split on $ gave %d fields, want 6 — the PHC format is five fields after the leading separator", n)
	}
}

// TestVerifyRejectsWrongPassword also asserts the nil error. A non-nil one here
// would leave the caller unable to tell a bad password from a damaged row, and
// the sensible reaction to those two differs.
func TestVerifyRejectsWrongPassword(t *testing.T) {
	encoded := mustHash(t, testPassword)

	tests := []struct {
		name     string
		password string
	}{
		{"a different password", "correct horse battery stapl3"},
		{"the empty password", ""},
		{"a prefix of the right one", testPassword[:len(testPassword)-1]},
		{"the right one with trailing space", testPassword + " "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustVerify(t, tt.password, encoded, false)
		})
	}
}

// TestHashUsesDistinctSalts is what stops the table from revealing which
// accounts share a password. Two hashes of the same input must differ, and both
// must still verify.
func TestHashUsesDistinctSalts(t *testing.T) {
	first := mustHash(t, testPassword)
	second := mustHash(t, testPassword)

	if first == second {
		t.Fatalf("two hashes of the same password are identical — the salt is not random")
	}

	mustVerify(t, testPassword, first, true)
	mustVerify(t, testPassword, second, true)
}

// TestVerifyReadsParametersFromTheHash is the test that earns the encoding.
// This hash was written at a cost nothing in this package uses; it verifies
// only because Verify derives with the parameters it found in the string. Raise
// the constants tomorrow and yesterday's rows keep working for the same reason.
func TestVerifyReadsParametersFromTheHash(t *testing.T) {
	// Deliberately cheap, both to prove the point and to keep the test fast.
	const legacy = "$argon2id$v=19$m=64,t=1,p=1$YSBsZWdhY3kgc2FsdCEhIQ$JJtn+zm8rLhjHfm2iRYzrul2eflQBA3Y6hm9v0UuW38"

	mustVerify(t, testPassword, legacy, true)
	mustVerify(t, "something else", legacy, false)
}

// TestVerifyRejectsMalformedHashes covers every way the stored string can be
// wrong. Each case must report an error and must not report a match: a parser
// that returned (true, err) on junk would be an authentication bypass, and one
// that returned (false, nil) would hide a corrupt column forever.
func TestVerifyRejectsMalformedHashes(t *testing.T) {
	valid := mustHash(t, testPassword)
	fields := strings.Split(valid, "$")

	tests := []struct {
		name    string
		encoded string
		want    error
	}{
		{"empty string", "", ErrInvalidHash},
		{"not a hash at all", "hunter2", ErrInvalidHash},
		{"too few fields", strings.Join(fields[:5], "$"), ErrInvalidHash},
		{"too many fields", valid + "$extra", ErrInvalidHash},
		{"no leading separator", strings.TrimPrefix(valid, "$") + "$", ErrInvalidHash},
		{"argon2i variant", strings.Replace(valid, "$argon2id$", "$argon2i$", 1), ErrInvalidHash},
		{"argon2d variant", strings.Replace(valid, "$argon2id$", "$argon2d$", 1), ErrInvalidHash},
		{"unreadable version", strings.Replace(valid, "$v=19$", "$v=nineteen$", 1), ErrInvalidHash},
		{"older argon2 version", strings.Replace(valid, "$v=19$", "$v=16$", 1), ErrIncompatibleVersion},
		{"non-numeric memory", strings.Replace(valid, "m=65536", "m=lots", 1), ErrInvalidHash},
		{"missing parallelism", strings.Replace(valid, "m=65536,t=3,p=4", "m=65536,t=3", 1), ErrInvalidHash},
		{"reordered parameters", strings.Replace(valid, "m=65536,t=3,p=4", "t=3,m=65536,p=4", 1), ErrInvalidHash},
		// Sscanf would stop happily after p=4 and leave the rest unread.
		{"trailing junk after the parameters", strings.Replace(valid, "m=65536,t=3,p=4", "m=65536,t=3,p=4nonsense", 1), ErrInvalidHash},
		{"zero time", strings.Replace(valid, "t=3", "t=0", 1), ErrInvalidHash},
		// Padded, which RawStdEncoding rejects — the two alphabets must not be
		// quietly interchangeable or hashes stop being portable.
		{"padded base64 salt", "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHR5c2FsdA==$" + fields[5], ErrInvalidHash},
		{"invalid base64 key", strings.Join(append(append([]string{}, fields[:5]...), "not!valid!base64"), "$"), ErrInvalidHash},
		{"empty salt", "$argon2id$v=19$m=65536,t=3,p=4$$" + fields[5], ErrInvalidHash},
		{"empty key", strings.Join(fields[:5], "$") + "$", ErrInvalidHash},
		// A truncated column must fail, not shorten the comparison: Verify
		// derives a key the length of the one it read, so a two-byte key would
		// match a wrong password once in 65536.
		{"truncated key", strings.Join(fields[:5], "$") + "$" + fields[5][:3], ErrInvalidHash},
		{"truncated salt", strings.Join(fields[:4], "$") + "$" + fields[4][:3] + "$" + fields[5], ErrInvalidHash},
		// uint8 truncation would turn 260 into 4 and verify a hash argon2
		// could never have produced.
		{"parallelism past uint8", strings.Replace(valid, "p=4", "p=260", 1), ErrInvalidHash},
		{"signed parameter", strings.Replace(valid, "t=3", "t=+3", 1), ErrInvalidHash},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Verify(testPassword, tt.encoded)
			if got {
				t.Errorf("Verify() = true, want false — a malformed hash must never match")
			}
			if !errors.Is(err, tt.want) {
				t.Errorf("Verify() error = %v, want one matching %v", err, tt.want)
			}
		})
	}
}

// TestVerifyErrorsDoNotLeakSecrets guards the same property config.DBConfig
// guards for the DSN: an error value ends up in a log, and neither the password
// nor the stored hash may travel there. store.InitPool declines to wrap the pgx
// error for exactly this reason.
func TestVerifyErrorsDoNotLeakSecrets(t *testing.T) {
	valid := mustHash(t, testPassword)

	// The damaged variants an operator is most likely to actually hit. Every
	// one of them must fail to parse, so that err is never nil below.
	damaged := []string{
		"",
		testPassword,
		strings.Join(strings.Split(valid, "$")[:5], "$"),
		strings.Replace(valid, "$argon2id$", "$argon2i$", 1),
		strings.Replace(valid, "$v=19$", "$v=16$", 1),
		strings.Replace(valid, "m=65536", "m=", 1),
	}

	for _, encoded := range damaged {
		_, err := Verify(testPassword, encoded)
		if err == nil {
			t.Fatalf("Verify() with %q returned no error, want one", encoded)
		}

		msg := err.Error()
		if strings.Contains(msg, testPassword) {
			t.Errorf("error %q contains the password", msg)
		}
		// The hash itself is a credential too: it is what an offline attacker
		// needs to start guessing.
		if encoded != "" && strings.Contains(msg, encoded) {
			t.Errorf("error %q contains the stored hash", msg)
		}
	}
}
