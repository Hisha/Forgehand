package auth

import (
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"
)

// rfc6238Secret is the shared secret used by the RFC 6238 test vectors.
func rfc6238Secret(algorithm TOTPAlgorithm) []byte {
	var seed string
	switch algorithm {
	case TOTPAlgorithmSHA256:
		seed = "12345678901234567890123456789012"
	case TOTPAlgorithmSHA512:
		seed = "1234567890123456789012345678901234567890123456789012345678901234"
	default:
		seed = "12345678901234567890"
	}
	return []byte(seed)
}

// TestGenerateTOTPMatchesRFC6238Vectors verifies the implementation against
// the published RFC 6238 appendix B values. The codes below are the eight
// digit reference values truncated to the six digit form, which is exactly
// what an authenticator application computes.
func TestGenerateTOTPMatchesRFC6238Vectors(t *testing.T) {
	cases := []struct {
		seconds int64
		code    string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}

	secret := rfc6238Secret(TOTPAlgorithmSHA1)

	for _, testCase := range cases {
		instant := time.Unix(testCase.seconds, 0).UTC()
		counter := TOTPCounter(instant, DefaultTOTPPeriod)
		got := GenerateTOTP(secret, TOTPAlgorithmSHA1, DefaultTOTPDigits, counter)
		if got != testCase.code {
			t.Fatalf("TOTP at %d = %q, want %q", testCase.seconds, got, testCase.code)
		}
	}
}

func TestValidateTOTPAcceptsCurrentCode(t *testing.T) {
	secret, _, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() returned error: %v", err)
	}

	now := time.Now()
	code := GenerateTOTP(secret, TOTPAlgorithmSHA1, DefaultTOTPDigits, TOTPCounter(now, DefaultTOTPPeriod))

	counter, err := ValidateTOTP(code, secret, TOTPAlgorithmSHA1, -1, now)
	if err != nil {
		t.Fatalf("ValidateTOTP() rejected a valid code: %v", err)
	}
	if counter != TOTPCounter(now, DefaultTOTPPeriod) {
		t.Fatalf("accepted counter = %d, want %d", counter, TOTPCounter(now, DefaultTOTPPeriod))
	}
}

func TestValidateTOTPRejectsInvalidCode(t *testing.T) {
	secret, _, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() returned error: %v", err)
	}

	now := time.Now()
	valid := GenerateTOTP(secret, TOTPAlgorithmSHA1, DefaultTOTPDigits, TOTPCounter(now, DefaultTOTPPeriod))
	invalid := "000000"
	if valid == invalid {
		invalid = "111111"
	}

	cases := []string{
		"",
		"   ",
		invalid,
		"12345",
		"1234567",
		"abcdef",
		"12345a",
		strings.Repeat("1", MaxTOTPCodeLength+1),
	}

	for _, code := range cases {
		if _, err := ValidateTOTP(code, secret, TOTPAlgorithmSHA1, -1, now); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("ValidateTOTP(%q) returned %v, want ErrInvalidCredentials", code, err)
		}
	}
}

func TestValidateTOTPRejectsExpiredCode(t *testing.T) {
	secret, _, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() returned error: %v", err)
	}

	now := time.Now()
	expired := GenerateTOTP(secret, TOTPAlgorithmSHA1, DefaultTOTPDigits, TOTPCounter(now.Add(-10*time.Minute), DefaultTOTPPeriod))

	if _, err := ValidateTOTP(expired, secret, TOTPAlgorithmSHA1, -1, now); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ValidateTOTP() accepted an expired code: %v", err)
	}
}

func TestValidateTOTPRejectsReplay(t *testing.T) {
	secret, _, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() returned error: %v", err)
	}

	now := time.Now()
	counter := TOTPCounter(now, DefaultTOTPPeriod)
	code := GenerateTOTP(secret, TOTPAlgorithmSHA1, DefaultTOTPDigits, counter)

	accepted, err := ValidateTOTP(code, secret, TOTPAlgorithmSHA1, -1, now)
	if err != nil {
		t.Fatalf("ValidateTOTP() rejected a valid code: %v", err)
	}

	if _, err := ValidateTOTP(code, secret, TOTPAlgorithmSHA1, accepted, now); !errors.Is(err, ErrReplay) {
		t.Fatalf("ValidateTOTP() accepted a replayed code: %v", err)
	}
}

func TestValidateTOTPAllowsOnlyNarrowSkewWindow(t *testing.T) {
	secret, _, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() returned error: %v", err)
	}

	now := time.Now()
	current := TOTPCounter(now, DefaultTOTPPeriod)

	// One step in the past and one step in the future are inside the
	// accepted window.
	previous := GenerateTOTP(secret, TOTPAlgorithmSHA1, DefaultTOTPDigits, current-1)
	if _, err := ValidateTOTP(previous, secret, TOTPAlgorithmSHA1, -1, now); err != nil {
		t.Fatalf("ValidateTOTP() rejected a code one step old: %v", err)
	}

	next := GenerateTOTP(secret, TOTPAlgorithmSHA1, DefaultTOTPDigits, current+1)
	if _, err := ValidateTOTP(next, secret, TOTPAlgorithmSHA1, -1, now); err != nil {
		t.Fatalf("ValidateTOTP() rejected a code one step ahead: %v", err)
	}

	// Two steps away is outside the window.
	tooOld := GenerateTOTP(secret, TOTPAlgorithmSHA1, DefaultTOTPDigits, current-2)
	if _, err := ValidateTOTP(tooOld, secret, TOTPAlgorithmSHA1, -1, now); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ValidateTOTP() accepted a code two steps old: %v", err)
	}

	tooNew := GenerateTOTP(secret, TOTPAlgorithmSHA1, DefaultTOTPDigits, current+2)
	if _, err := ValidateTOTP(tooNew, secret, TOTPAlgorithmSHA1, -1, now); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("ValidateTOTP() accepted a code two steps ahead: %v", err)
	}
}

func TestGenerateTOTPSecretUsesSecureRandomness(t *testing.T) {
	first, firstEncoded, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() returned error: %v", err)
	}
	_, secondEncoded, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() returned error: %v", err)
	}

	if firstEncoded == secondEncoded {
		t.Fatal("two generated secrets are identical")
	}
	if len(first) != TOTPSecretLength {
		t.Fatalf("secret length = %d, want %d", len(first), TOTPSecretLength)
	}

	decoded, err := DecodeTOTPSecret(firstEncoded)
	if err != nil {
		t.Fatalf("DecodeTOTPSecret() returned error: %v", err)
	}
	if string(decoded) != string(first) {
		t.Fatal("the decoded secret differs from the generated secret")
	}
}

func TestDecodeTOTPSecretRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"", "   ", "not-base32!!", "AAAA"} {
		if _, err := DecodeTOTPSecret(value); err == nil {
			t.Fatalf("DecodeTOTPSecret(%q) accepted a malformed secret", value)
		}
	}
}

func TestDecodeTOTPSecretAcceptsPaddedAndLowerCaseInput(t *testing.T) {
	secret, encoded, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() returned error: %v", err)
	}
	_ = encoded

	padded := base32.StdEncoding.EncodeToString(secret)
	decoded, err := DecodeTOTPSecret(strings.ToLower(padded))
	if err != nil {
		t.Fatalf("DecodeTOTPSecret() returned error: %v", err)
	}
	if string(decoded) != string(secret) {
		t.Fatal("the decoded secret differs from the generated secret")
	}
}

func TestProvisioningURIUsesIssuerAndSecret(t *testing.T) {
	_, encoded, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() returned error: %v", err)
	}

	uri := ProvisioningURI("Forgehand", "alice", encoded, TOTPAlgorithmSHA1)
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("provisioning URI %q does not use the otpauth scheme", uri)
	}
	if !strings.Contains(uri, "secret="+encoded) {
		t.Fatalf("provisioning URI %q does not carry the secret", uri)
	}
	if !strings.Contains(uri, "issuer=Forgehand") {
		t.Fatalf("provisioning URI %q does not carry the issuer", uri)
	}
	if !strings.Contains(uri, "algorithm=SHA1") {
		t.Fatalf("provisioning URI %q does not carry the algorithm", uri)
	}
}

func TestParseTOTPAlgorithmRejectsUnknownValues(t *testing.T) {
	if _, err := ParseTOTPAlgorithm("MD5"); err == nil {
		t.Fatal("ParseTOTPAlgorithm() accepted an unsupported algorithm")
	}
	for _, value := range []string{"", "sha1", "SHA-256", "sha512"} {
		if _, err := ParseTOTPAlgorithm(value); err != nil {
			t.Fatalf("ParseTOTPAlgorithm(%q) returned error: %v", value, err)
		}
	}
}
