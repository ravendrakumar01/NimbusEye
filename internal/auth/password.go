// Package auth implements password hashing and session management.
//
// Decisions worth stating, because each has a weaker alternative that looks fine:
//
//   - Argon2id, not bcrypt or a fast hash. This application is internet-facing
//     and a password is the only thing in front of a full multi-cloud inventory.
//   - Session tokens are random and opaque, not JWTs. A stateless token cannot be
//     revoked; a row in a table can. Logout, "sign out everywhere" and disabling a
//     user all need that.
//   - Only a hash of the token is stored, so a database dump cannot be replayed as
//     a live session.
//   - Login always performs the full hash comparison, even for an unknown address,
//     so response time does not reveal whether an account exists.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters.
//
// 64 MiB and three passes is above the OWASP floor and comfortable on a small
// server: a login is rare, so the cost is paid once per sign-in rather than per
// request. These are recorded in the encoded hash, so raising them later does not
// invalidate existing passwords — an old hash verifies with its own parameters and
// can be upgraded on next login.
const (
	argonMemory  uint32 = 64 * 1024
	argonTime    uint32 = 3
	argonKeyLen  uint32 = 32
	argonSaltLen int    = 16
)

func argonThreads() uint8 {
	// Two lanes unless the machine has only one core.
	if runtime.NumCPU() < 2 {
		return 1
	}
	return 2
}

var (
	// ErrMismatch is returned when a password does not match. Deliberately
	// indistinguishable from "no such user" at the caller's level.
	ErrMismatch = errors.New("auth: password does not match")
	// ErrMalformedHash means the stored value is not a hash this code understands.
	ErrMalformedHash = errors.New("auth: stored password hash is malformed")
)

// HashPassword returns an encoded Argon2id hash in the standard PHC string
// format, so the parameters travel with the hash.
func HashPassword(password string) (string, error) {
	if len(password) < 12 {
		// Enforced here rather than only in the handler: every path that sets a
		// password goes through this function, including the bootstrap CLI.
		return "", errors.New("auth: password must be at least 12 characters")
	}
	if len(password) > 1024 {
		// An unbounded password is a denial-of-service vector: Argon2 cost grows
		// with input length.
		return "", errors.New("auth: password must be 1024 characters or fewer")
	}

	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: cannot generate salt: %w", err)
	}
	threads := argonThreads()
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, threads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against an encoded hash.
func VerifyPassword(encoded, password string) error {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return err
	}
	got := argon2.IDKey([]byte(password), salt,
		params.time, params.memory, params.threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	var p argonParams
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return p, nil, nil, ErrMalformedHash
	}
	if version != argon2.Version {
		return p, nil, nil, fmt.Errorf("auth: unsupported argon2 version %d", version)
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return p, nil, nil, ErrMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return p, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}

// dummyHash is compared against when no user matches, so a failed login costs the
// same time whether or not the address exists.
var dummyHash string

func init() {
	h, err := HashPassword("not-a-real-password-placeholder")
	if err != nil {
		panic("auth: cannot build dummy hash: " + err.Error())
	}
	dummyHash = h
}

// VerifyAgainstDummy burns the same work as a real verification. Called on the
// unknown-user path so login timing carries no signal.
func VerifyAgainstDummy(password string) {
	_ = VerifyPassword(dummyHash, password)
}

/* ------------------------------------------------------------- tokens */

// TokenBytes is the length of a session token before encoding. 32 bytes of
// CSPRNG output is beyond brute force and leaves no reason to make it longer.
const TokenBytes = 32

// NewToken returns a session token and the hash to store for it.
//
// The plaintext goes to the browser and is never written down server-side; the
// hash is what the database holds, so a dump of the sessions table cannot be
// replayed.
func NewToken() (token string, hash []byte, err error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("auth: cannot generate token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	sum := HashToken(token)
	return token, sum, nil
}

// HashToken returns the stored form of a session token.
//
// A plain SHA-256 rather than a password hash: the input is 256 bits of entropy
// from a CSPRNG, so there is nothing to brute force and no reason to pay Argon2's
// cost on every single request.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// NewCSRFToken returns a random value for the double-submit cookie.
func NewCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: cannot generate csrf token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SameCSRF compares two CSRF values in constant time.
func SameCSRF(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
