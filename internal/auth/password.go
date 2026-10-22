package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. The memory and iteration counts follow the OWASP
// password-storage guidance for Argon2id (m=19 MiB, t=2, p=1), which is the
// reviewed baseline for a network-facing daemon. The parameters are recorded
// in every encoded hash so that they can be raised later without invalidating
// existing credentials.
const (
	argon2MemoryKiB     = 19 * 1024
	argon2Iterations    = 2
	argon2Parallelism   = 1
	argon2SaltLength    = 16
	argon2KeyLength     = 32
	argon2Version       = 19
	argon2Algorithm     = "argon2id"
	argon2EncodedPrefix = "$argon2id$v=19$"
)

// Password length bounds. The minimum keeps trivially weak passphrases out of
// the store; the maximum bounds the work an unauthenticated caller can force
// the daemon to perform with a single request body.
const (
	MinPasswordLength = 12
	MaxPasswordLength = 256
)

// PasswordHash is an Argon2id hash in the encoded PHC-like form
// $argon2id$v=19$m=...,t=...,p=...$salt$hash. It never contains the
// plaintext password.
type PasswordHash struct {
	MemoryKiB    uint32
	Iterations   uint32
	Parallelism  uint8
	Salt         []byte
	Hash         []byte
	encodedValue string
}

// HashPassword derives an Argon2id hash of password using a freshly generated,
// cryptographically secure random salt. Two calls with the same password
// always produce different hashes.
func HashPassword(password string) (PasswordHash, error) {
	if err := ValidatePasswordStrength(password); err != nil {
		return PasswordHash{}, err
	}

	salt := make([]byte, argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return PasswordHash{}, fmt.Errorf("generate password salt: %w", err)
	}

	key := argon2.IDKey(
		[]byte(password),
		salt,
		argon2Iterations,
		argon2MemoryKiB,
		argon2Parallelism,
		argon2KeyLength,
	)

	hash := PasswordHash{
		MemoryKiB:   argon2MemoryKiB,
		Iterations:  argon2Iterations,
		Parallelism: argon2Parallelism,
		Salt:        salt,
		Hash:        key,
	}
	hash.encodedValue = hash.encode()
	return hash, nil
}

// ValidatePasswordStrength rejects passwords that are empty, too short, too
// long, or that exceed the maximum byte length the daemon will hash.
func ValidatePasswordStrength(password string) error {
	if password == "" {
		return errors.New("password must not be empty")
	}
	if len([]rune(password)) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	if len(password) > MaxPasswordLength {
		return fmt.Errorf("password must be at most %d bytes", MaxPasswordLength)
	}
	return nil
}

// Encode returns the stored representation of the hash.
func (h PasswordHash) Encode() string {
	if h.encodedValue != "" {
		return h.encodedValue
	}
	return h.encode()
}

func (h PasswordHash) encode() string {
	return fmt.Sprintf(
		"%sm=%d,t=%d,p=%d$%s$%s",
		argon2EncodedPrefix,
		h.MemoryKiB,
		h.Iterations,
		h.Parallelism,
		base64.RawStdEncoding.EncodeToString(h.Salt),
		base64.RawStdEncoding.EncodeToString(h.Hash),
	)
}

// ParsePasswordHash decodes an encoded Argon2id hash. It accepts only the
// argon2id algorithm and version 19; any other value is rejected so a weaker
// algorithm can never be introduced through a stored value.
func ParsePasswordHash(encoded string) (PasswordHash, error) {
	if !strings.HasPrefix(encoded, argon2EncodedPrefix) {
		return PasswordHash{}, errors.New("unsupported password hash format")
	}

	parts := strings.Split(strings.TrimPrefix(encoded, "$"), "$")
	// parts = ["argon2id", "v=19", "m=...,t=...,p=...", salt, hash]
	if len(parts) != 5 {
		return PasswordHash{}, errors.New("malformed password hash")
	}
	if parts[0] != argon2Algorithm {
		return PasswordHash{}, errors.New("unsupported password hash algorithm")
	}
	if parts[1] != "v=19" {
		return PasswordHash{}, errors.New("unsupported password hash version")
	}

	parameters := strings.Split(parts[2], ",")
	if len(parameters) != 3 {
		return PasswordHash{}, errors.New("malformed password hash parameters")
	}

	values := make(map[string]uint64, 3)
	for _, parameter := range parameters {
		name, raw, found := strings.Cut(parameter, "=")
		if !found {
			return PasswordHash{}, errors.New("malformed password hash parameters")
		}
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return PasswordHash{}, fmt.Errorf("malformed password hash parameter %q", name)
		}
		values[name] = parsed
	}

	memory, hasMemory := values["m"]
	iterations, hasIterations := values["t"]
	parallelism, hasParallelism := values["p"]
	if !hasMemory || !hasIterations || !hasParallelism {
		return PasswordHash{}, errors.New("incomplete password hash parameters")
	}
	if memory == 0 || iterations == 0 || parallelism == 0 {
		return PasswordHash{}, errors.New("invalid password hash parameters")
	}
	if memory > 1<<20 || iterations > 1<<20 || parallelism > 1<<16 {
		return PasswordHash{}, errors.New("password hash parameters exceed supported bounds")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return PasswordHash{}, errors.New("malformed password hash salt")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return PasswordHash{}, errors.New("malformed password hash value")
	}
	if len(salt) == 0 || len(key) == 0 {
		return PasswordHash{}, errors.New("malformed password hash")
	}

	return PasswordHash{
		MemoryKiB:    uint32(memory),
		Iterations:   uint32(iterations),
		Parallelism:  uint8(parallelism),
		Salt:         salt,
		Hash:         key,
		encodedValue: encoded,
	}, nil
}

// VerifyPassword reports whether password matches the encoded hash. It
// compares the derived key in constant time and returns ErrInvalidCredentials
// for any mismatch or malformed stored hash.
func VerifyPassword(encoded, password string) error {
	parsed, err := ParsePasswordHash(encoded)
	if err != nil {
		return ErrInvalidCredentials
	}

	derived := argon2.IDKey(
		[]byte(password),
		parsed.Salt,
		parsed.Iterations,
		parsed.MemoryKiB,
		parsed.Parallelism,
		uint32(len(parsed.Hash)),
	)

	if subtle.ConstantTimeCompare(derived, parsed.Hash) != 1 {
		return ErrInvalidCredentials
	}
	return nil
}
