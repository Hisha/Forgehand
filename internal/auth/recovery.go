package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	// RecoveryCodeCount is the number of recovery codes issued when an
	// account is provisioned.
	RecoveryCodeCount = 10

	// RecoveryCodeGroups and RecoveryCodeGroupLength describe the human
	// readable shape of a recovery code: two groups of five characters.
	RecoveryCodeGroups      = 2
	RecoveryCodeGroupLength = 5

	// recoveryCodeAlphabet omits characters that are easily confused when a
	// code is transcribed by hand.
	recoveryCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// RecoveryCode is a single-use recovery factor. Only the hash of the code is
// ever persisted; the plaintext is shown to the account owner exactly once.
type RecoveryCode struct {
	// Code is the plaintext recovery code. It is never stored.
	Code string
	// Hash is the SHA-256 digest of the normalised code, hex encoded.
	Hash string
}

// NormalizeRecoveryCode upper-cases a recovery code and removes the
// separators that the presentation format adds, so that a user may enter a
// code with or without dashes or spaces.
func NormalizeRecoveryCode(code string) string {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	return normalized
}

// HashRecoveryCode returns the hex encoded SHA-256 digest of a recovery code.
// Recovery codes are high-entropy random tokens rather than user-chosen
// passwords, so a fast digest is the appropriate storage primitive.
func HashRecoveryCode(code string) string {
	normalized := NormalizeRecoveryCode(code)
	digest := sha256.Sum256([]byte(normalized))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// GenerateRecoveryCodes returns a set of single-use recovery codes together
// with their hashes. The plaintext codes are returned to the caller for a
// one-time display and are never persisted.
func GenerateRecoveryCodes(count int) ([]RecoveryCode, error) {
	if count <= 0 {
		return nil, fmt.Errorf("recovery code count must be positive")
	}

	codes := make([]RecoveryCode, 0, count)
	for index := 0; index < count; index++ {
		code, err := generateRecoveryCode()
		if err != nil {
			return nil, err
		}
		codes = append(codes, RecoveryCode{
			Code: code,
			Hash: HashRecoveryCode(code),
		})
	}
	return codes, nil
}

func generateRecoveryCode() (string, error) {
	alphabetSize := len(recoveryCodeAlphabet)
	total := RecoveryCodeGroups * RecoveryCodeGroupLength

	// Rejection sampling keeps the character distribution uniform. The
	// largest multiple of the alphabet size that fits in a byte is the
	// acceptance limit.
	limit := 256 - (256 % alphabetSize)

	buffer := make([]byte, total)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate recovery code: %w", err)
	}

	characters := make([]byte, 0, total)
	for len(characters) < total {
		for _, value := range buffer {
			if int(value) >= limit {
				continue
			}
			characters = append(characters, recoveryCodeAlphabet[int(value)%alphabetSize])
			if len(characters) == total {
				break
			}
		}
		if len(characters) < total {
			extra := make([]byte, total)
			if _, err := rand.Read(extra); err != nil {
				return "", fmt.Errorf("generate recovery code: %w", err)
			}
			buffer = extra
		}
	}

	groups := make([]string, 0, RecoveryCodeGroups)
	for group := 0; group < RecoveryCodeGroups; group++ {
		start := group * RecoveryCodeGroupLength
		groups = append(groups, string(characters[start:start+RecoveryCodeGroupLength]))
	}
	return strings.Join(groups, "-"), nil
}

// VerifyRecoveryCode reports whether the presented recovery code matches the
// supplied hash. The comparison is constant time.
func VerifyRecoveryCode(code, hash string) bool {
	if hash == "" {
		return false
	}
	computed := HashRecoveryCode(code)
	return constantTimeStringEqual(computed, hash)
}

// constantTimeStringEqual compares two strings without leaking the position
// of the first difference through timing.
func constantTimeStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := 0; index < len(left); index++ {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

// FormatRecoveryCodes renders recovery codes for a one-time display.
func FormatRecoveryCodes(codes []RecoveryCode) string {
	lines := make([]string, 0, len(codes))
	for _, code := range codes {
		lines = append(lines, code.Code)
	}
	return strings.Join(lines, "\n")
}
