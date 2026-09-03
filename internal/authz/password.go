package authz

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"crypto/pbkdf2"
)

// Password hashing.
//
// PBKDF2-HMAC-SHA256 is used because it is in the standard library (Go 1.24+),
// and this project deliberately carries no dependency it does not need. bcrypt
// or argon2id would be preferable on memory-hardness grounds, but both live in
// golang.org/x/crypto; PBKDF2 at a high iteration count is an accepted choice
// (OWASP lists 600 000 iterations for HMAC-SHA256) and the stored format below
// records its own parameters, so raising the count later re-hashes on next sign
// in rather than invalidating everyone's password.

const (
	// DefaultPasswordIterations is the OWASP-recommended work factor for
	// PBKDF2-HMAC-SHA256. Roughly 200 ms on a modern core — noticeable only at
	// sign-in, which is exactly where it should be.
	DefaultPasswordIterations = 600_000

	pbkdf2SaltLen = 16
	pbkdf2KeyLen  = 32
	pbkdf2Scheme  = "pbkdf2-sha256"

	// MinPasswordLength is the shortest password accepted. Length is the only
	// composition rule enforced: complexity rules push people towards
	// predictable substitutions without adding real entropy.
	MinPasswordLength = 12
)

// ErrPasswordTooShort is returned by HashPassword for passwords below
// MinPasswordLength.
var ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)

// pbkdf2Iterations is the work factor new hashes are made with. It is a
// variable rather than a constant only so test suites can lower it — see
// SetPasswordIterations.
var pbkdf2Iterations = DefaultPasswordIterations

// SetPasswordIterations overrides the work factor for hashes created from now
// on, returning the previous value.
//
// It exists for tests: at the production factor, a suite that signs in a few
// dozen times spends minutes deriving keys, and under the race detector rather
// longer. **Nothing in production may call it** — lowering the factor weakens
// every password hashed afterwards.
//
// Existing hashes are unaffected either way: each records the factor it was
// made with, and VerifyPassword uses that, which is also what allows the
// production factor to be raised later without invalidating anyone's password.
func SetPasswordIterations(n int) int {
	previous := pbkdf2Iterations
	if n > 0 {
		pbkdf2Iterations = n
	}
	return previous
}

// HashPassword derives a storable hash from a plaintext password.
func HashPassword(password string) (string, error) {
	if len([]rune(password)) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}
	return hashPassword(password, pbkdf2Iterations)
}

// hashPassword is HashPassword with an explicit work factor, so tests can run
// at a cost that does not dominate the suite.
func hashPassword(password string, iterations int) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, pbkdf2KeyLen)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}
	return strings.Join([]string{
		pbkdf2Scheme,
		strconv.Itoa(iterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

// VerifyPassword reports whether password matches the stored hash.
//
// A malformed or unknown-scheme hash yields false rather than an error: to the
// caller there is nothing to distinguish it from a wrong password, and treating
// it as an error would let a corrupted row turn a failed sign-in into a 500.
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != pbkdf2Scheme {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) != pbkdf2KeyLen {
		// The length is checked rather than taken from the stored value: PBKDF2
		// output is prefix-truncatable, so deriving len(want) bytes would make a
		// truncated digest verify against the full password.
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, pbkdf2KeyLen)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NewSessionToken returns a fresh opaque session token: 32 bytes of CSPRNG
// output, URL-safe so it can live in a cookie unescaped.
func NewSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.New("generate session token")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashSessionToken maps a token to the value stored in the database.
//
// A plain SHA-256 is right here, unlike for passwords: the token already has
// 256 bits of entropy, so there is no dictionary to attack and no reason to pay
// a work factor on every authenticated request. The hash exists so that a
// database dump does not contain usable sessions.
func HashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
