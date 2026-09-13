package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// The cost RFC 9106 calls its second recommended option, and the one to pick
// when 2 GiB of memory per hash is not on the table. Each parameter is doing a
// distinct job:
//
//   - memory is the whole point of argon2id. 64 MiB per hash is what makes a
//     GPU or ASIC attack expensive, because silicon that is cheap to replicate
//     for SHA-style work is not cheap to give 64 MiB of fast memory each.
//   - time is the iteration count RFC 9106 pairs with 64 MiB. Below it the
//     memory is filled but not re-read enough to cost the attacker what the
//     memory figure suggests.
//   - parallelism matches commodity cores. It does not reduce the memory, only
//     the wall clock spent filling it.
//
// memory is the total for one hash, not per lane: four concurrent logins hold
// 256 MiB between them. That product, not the 64 MiB, is the number that has to
// fit the instance the API runs on.
const (
	hashMemory      uint32 = 64 * 1024 // KiB, so 64 MiB
	hashTime        uint32 = 3
	hashParallelism uint8  = 4

	// RFC 9106's defaults. 16 bytes of salt is past the point where a collision
	// across the user table is worth thinking about, and 32 bytes of output
	// matches the strength of everything else that will sit beside it.
	saltLength uint32 = 16
	keyLength  uint32 = 32

	// The floors Verify enforces on what it reads back. Both sit at the length
	// a hash written before a future increase would have, not at the current
	// one, so raising saltLength or keyLength does not invalidate old rows.
	minSaltLength = 16
	minKeyLength  = 16
)

var (
	// ErrInvalidHash means the stored string is not a well-formed argon2id PHC
	// string, so no comparison was possible. It is not a wrong password, and a
	// caller that conflates the two turns a corrupt row into a failed login and
	// loses the only signal that something is wrong.
	ErrInvalidHash = errors.New("invalid password hash")

	// ErrIncompatibleVersion means the string is well formed but names an
	// argon2 version this build cannot reproduce. Deriving with the version we
	// have would silently produce a different key and reject a correct
	// password.
	ErrIncompatibleVersion = errors.New("incompatible argon2 version")
)

// Hash derives an argon2id key from password and returns it encoded in the PHC
// string format, salt and parameters included:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>
//
// The parameters travel with the hash so they can be raised later without
// invalidating the rows already written at the old cost.
func Hash(password string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read a random salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, hashTime, hashMemory, hashParallelism, keyLength)

	return encode(salt, key, hashMemory, hashTime, hashParallelism), nil
}

// Verify reports whether password is the one held in encoded.
//
// A wrong password is (false, nil). A non-nil error means encoded could not be
// parsed, which is a different fact entirely: it says the stored value is
// damaged, not that the caller typed the wrong thing.
//
// The parameters are read back out of encoded rather than taken from the
// package constants, which is what lets a hash written at an older cost keep
// verifying after the constants are raised.
func Verify(password, encoded string) (bool, error) {
	salt, want, memory, time, parallelism, err := decode(encoded)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey([]byte(password), salt, time, memory, parallelism, uint32(len(want)))

	// ConstantTimeCompare, not bytes.Equal: an early-exit comparison leaks how
	// many leading bytes matched, which is enough to walk a forged hash towards
	// a real one.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// b64 is the encoding the reference implementation uses, and with it every
// other language's argon2 library: standard alphabet, no padding. Matching it
// means a hash written here can be read by anything else, and vice versa.
var b64 = base64.RawStdEncoding

func encode(salt, key []byte, memory, time uint32, parallelism uint8) string {
	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, time, parallelism,
		b64.EncodeToString(salt), b64.EncodeToString(key),
	)
}

// decode pulls a PHC string apart. Every failure returns ErrInvalidHash or
// ErrIncompatibleVersion, and no error here quotes the input: the string it was
// handed is a credential, and an error value ends up in a log.
func decode(encoded string) (salt, key []byte, memory, time uint32, parallelism uint8, err error) {
	// A leading "$" means the first field is always empty, so a well-formed
	// string splits into exactly six.
	fields := strings.Split(encoded, "$")
	if len(fields) != 6 || fields[0] != "" {
		return nil, nil, 0, 0, 0, fmt.Errorf("want 5 fields: %w", ErrInvalidHash)
	}

	// argon2i and argon2d are real variants with real uses, and neither is this
	// one. Deriving an argon2i hash with IDKey would reject a correct password.
	if fields[1] != "argon2id" {
		return nil, nil, 0, 0, 0, fmt.Errorf("not argon2id: %w", ErrInvalidHash)
	}

	var version int
	if _, err := fmt.Sscanf(fields[2], "v=%d", &version); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("unreadable version: %w", ErrInvalidHash)
	}
	if version != argon2.Version {
		return nil, nil, 0, 0, 0, fmt.Errorf("version %d: %w", version, ErrIncompatibleVersion)
	}

	// Not Sscanf: it stops at the first mismatch but is happy to leave trailing
	// junk unread, so "m=65536,t=3,p=4nonsense" would parse. Splitting and
	// parsing each value whole rejects that, and a reordered or repeated
	// parameter with it.
	memory, time, parallelism, err = parseParams(fields[3])
	if err != nil {
		return nil, nil, 0, 0, 0, err
	}

	if salt, err = b64.DecodeString(fields[4]); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("undecodable salt: %w", ErrInvalidHash)
	}
	if key, err = b64.DecodeString(fields[5]); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("undecodable key: %w", ErrInvalidHash)
	}
	// The lengths are read from the string rather than fixed, so that keyLength
	// can be raised the same way the cost parameters can. They still need a
	// floor: a truncated column would otherwise shorten the comparison instead
	// of failing it, and a one-byte key matches a wrong password once in 256.
	if len(salt) < minSaltLength || len(key) < minKeyLength {
		return nil, nil, 0, 0, 0, fmt.Errorf("salt or key too short: %w", ErrInvalidHash)
	}

	return salt, key, memory, time, parallelism, nil
}

// parseParams reads the "m=,t=,p=" field, requiring exactly those three keys,
// in that order, each with a whole number and nothing trailing.
func parseParams(field string) (memory, time uint32, parallelism uint8, err error) {
	parts := strings.Split(field, ",")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("want 3 parameters: %w", ErrInvalidHash)
	}

	values := make([]uint32, 3)
	for i, key := range []string{"m=", "t=", "p="} {
		text, ok := strings.CutPrefix(parts[i], key)
		if !ok {
			return 0, 0, 0, fmt.Errorf("parameter %d is not %q: %w", i, key, ErrInvalidHash)
		}
		// ParseUint rejects a sign, a space and any trailing character, all of
		// which Sscanf would have accepted or skipped past.
		n, parseErr := strconv.ParseUint(text, 10, 32)
		if parseErr != nil || n == 0 {
			return 0, 0, 0, fmt.Errorf(
				"parameter %q is not a positive number: %w",
				key,
				ErrInvalidHash,
			)
		}
		values[i] = uint32(n)
	}

	// argon2 takes parallelism as a uint8, so a larger value could not have
	// produced this hash and must not be truncated into one that verifies.
	if values[2] > math.MaxUint8 {
		return 0, 0, 0, fmt.Errorf("parallelism out of range: %w", ErrInvalidHash)
	}

	return values[0], values[1], uint8(values[2]), nil
}
