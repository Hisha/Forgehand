package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters. RFC 6238 with the de-facto interoperable defaults:
// SHA-1 HMAC, six digits, and a 30 second time step. SHA-1 is used as the
// HMAC function because it is what the overwhelming majority of
// standards-compatible authenticator applications expect; the security of
// the construction comes from the HMAC key, not from the digest.
const (
	DefaultTOTPDigits    = 6
	DefaultTOTPPeriod    = 30 * time.Second
	DefaultTOTPAlgorithm = "SHA1"

	// TOTPSkewSteps is the number of time steps accepted on either side of
	// the current step. A value of one accepts a code for at most 90 seconds
	// of wall-clock time, which is the narrow window required by the
	// Forgehand security principles.
	TOTPSkewSteps = 1

	// TOTPSecretLength is the length in bytes of a generated TOTP secret.
	// 160 bits is the size mandated by RFC 4226 for HOTP and matches the
	// key size used by common authenticator applications.
	TOTPSecretLength = 20

	// MaxTOTPCodeLength bounds the accepted code length so that a caller
	// cannot force an unbounded comparison.
	MaxTOTPCodeLength = 16
)

// TOTPAlgorithm identifies the HMAC function used by a TOTP secret.
type TOTPAlgorithm string

const (
	TOTPAlgorithmSHA1   TOTPAlgorithm = "SHA1"
	TOTPAlgorithmSHA256 TOTPAlgorithm = "SHA256"
	TOTPAlgorithmSHA512 TOTPAlgorithm = "SHA512"
)

// ParseTOTPAlgorithm maps a provisioning label onto a supported algorithm.
// The comparison is case-insensitive because authenticator applications and
// provisioning URIs are inconsistent about casing.
func ParseTOTPAlgorithm(value string) (TOTPAlgorithm, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "SHA1", "SHA-1":
		return TOTPAlgorithmSHA1, nil
	case "SHA256", "SHA-256":
		return TOTPAlgorithmSHA256, nil
	case "SHA512", "SHA-512":
		return TOTPAlgorithmSHA512, nil
	default:
		return "", fmt.Errorf("unsupported TOTP algorithm %q", value)
	}
}

func (a TOTPAlgorithm) new() func() hash.Hash {
	switch a {
	case TOTPAlgorithmSHA256:
		return sha256.New
	case TOTPAlgorithmSHA512:
		return sha512.New
	default:
		return sha1.New
	}
}

// GenerateTOTPSecret returns a new cryptographically secure TOTP secret
// together with its base32 encoding, which is the representation used by
// authenticator applications.
func GenerateTOTPSecret() (secret []byte, encoded string, err error) {
	secret = make([]byte, TOTPSecretLength)
	if _, err := rand.Read(secret); err != nil {
		return nil, "", fmt.Errorf("generate TOTP secret: %w", err)
	}
	return secret, base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret), nil
}

// DecodeTOTPSecret decodes a base32 encoded TOTP secret. Padding variations
// and lower case input are accepted because authenticator applications and
// manual entry flows differ in how they present secrets.
func DecodeTOTPSecret(encoded string) ([]byte, error) {
	normalized := strings.ToUpper(strings.TrimSpace(encoded))
	normalized = strings.ReplaceAll(normalized, " ", "")
	normalized = strings.TrimRight(normalized, "=")
	if normalized == "" {
		return nil, fmt.Errorf("TOTP secret must not be empty")
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized)
	if err != nil {
		return nil, fmt.Errorf("decode TOTP secret: %w", err)
	}
	if len(secret) < 10 {
		return nil, fmt.Errorf("TOTP secret is too short")
	}
	return secret, nil
}

// EncodeTOTPSecret returns the base32 representation of a raw TOTP secret.
func EncodeTOTPSecret(secret []byte) string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
}

// ProvisioningURI builds the otpauth:// URI that authenticator applications
// consume. The issuer is Forgehand; the label carries the account name.
func ProvisioningURI(issuer, account, secretBase32 string, algorithm TOTPAlgorithm) string {
	label := url.PathEscape(issuer + ":" + account)
	query := url.Values{}
	query.Set("secret", secretBase32)
	query.Set("issuer", issuer)
	query.Set("algorithm", string(algorithm))
	query.Set("digits", fmt.Sprintf("%d", DefaultTOTPDigits))
	query.Set("period", fmt.Sprintf("%d", int(DefaultTOTPPeriod/time.Second)))
	return "otpauth://totp/" + label + "?" + query.Encode()
}

// GenerateTOTP computes the RFC 6238 code for a counter value.
func GenerateTOTP(secret []byte, algorithm TOTPAlgorithm, digits int, counter int64) string {
	if digits <= 0 {
		digits = DefaultTOTPDigits
	}
	if len(secret) == 0 {
		return ""
	}

	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(counter))

	mac := hmac.New(algorithm.new(), secret)
	mac.Write(message[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])

	modulus := uint32(1)
	for i := 0; i < digits; i++ {
		modulus *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%modulus)
}

// TOTPCounter returns the RFC 6238 time step counter for an instant.
func TOTPCounter(at time.Time, period time.Duration) int64 {
	if period <= 0 {
		period = DefaultTOTPPeriod
	}
	return at.Unix() / int64(period/time.Second)
}

// ValidateTOTP verifies a submitted one-time code.
//
// It returns the counter of the accepted step so the caller can persist it
// and reject a replay of the same code. lastAcceptedCounter is the highest
// counter previously accepted for the account; a candidate whose counter is
// not strictly greater is refused as a replay.
//
// The comparison of the submitted code against the expected code is constant
// time, and every candidate step in the skew window is evaluated so the
// result does not depend on which step matched.
func ValidateTOTP(
	code string,
	secret []byte,
	algorithm TOTPAlgorithm,
	lastAcceptedCounter int64,
	now time.Time,
) (int64, error) {
	normalized := strings.TrimSpace(code)
	if normalized == "" {
		return 0, ErrInvalidCredentials
	}
	if len(normalized) > MaxTOTPCodeLength {
		return 0, ErrInvalidCredentials
	}
	for _, character := range normalized {
		if character < '0' || character > '9' {
			return 0, ErrInvalidCredentials
		}
	}

	if len(secret) == 0 {
		return 0, ErrInvalidCredentials
	}

	current := TOTPCounter(now, DefaultTOTPPeriod)
	accepted := int64(-1)
	matched := false

	for offset := int64(-TOTPSkewSteps); offset <= TOTPSkewSteps; offset++ {
		candidate := current + offset
		if candidate < 0 {
			continue
		}
		expected := GenerateTOTP(secret, algorithm, DefaultTOTPDigits, candidate)
		if subtle.ConstantTimeCompare([]byte(expected), []byte(normalized)) == 1 {
			matched = true
			if candidate > accepted {
				accepted = candidate
			}
		}
	}

	if !matched {
		return 0, ErrInvalidCredentials
	}
	if accepted <= lastAcceptedCounter {
		return 0, ErrReplay
	}
	return accepted, nil
}
