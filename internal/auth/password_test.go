package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordProducesUniqueSaltsAndVerifies(t *testing.T) {
	password := "correct horse battery staple"

	first, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() returned error: %v", err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() returned error: %v", err)
	}

	if first.Encode() == second.Encode() {
		t.Fatal("two hashes of the same password are identical; the salt is not unique")
	}
	if strings.Contains(first.Encode(), password) {
		t.Fatal("the encoded hash contains the plaintext password")
	}
	if !strings.HasPrefix(first.Encode(), "$argon2id$") {
		t.Fatalf("encoded hash %q does not use argon2id", first.Encode())
	}

	if err := VerifyPassword(first.Encode(), password); err != nil {
		t.Fatalf("VerifyPassword() rejected the correct password: %v", err)
	}
	if err := VerifyPassword(second.Encode(), password); err != nil {
		t.Fatalf("VerifyPassword() rejected the correct password: %v", err)
	}
}

func TestVerifyPasswordRejectsWrongPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword() returned error: %v", err)
	}

	if err := VerifyPassword(hash.Encode(), "correct horse battery stapl"); err == nil {
		t.Fatal("VerifyPassword() accepted a wrong password")
	}
	if err := VerifyPassword(hash.Encode(), ""); err == nil {
		t.Fatal("VerifyPassword() accepted an empty password")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	cases := []string{
		"",
		"not-a-hash",
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo",
		"$argon2id$v=16$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=0,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=19456,t=2$c2FsdHNhbHQ$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=19456,t=2,p=1$not base64$aGFzaGhhc2hoYXNo",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$not base64",
	}

	for _, encoded := range cases {
		if err := VerifyPassword(encoded, "correct horse battery staple"); err == nil {
			t.Fatalf("VerifyPassword() accepted the malformed hash %q", encoded)
		}
	}
}

func TestParsePasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword() returned error: %v", err)
	}

	parsed, err := ParsePasswordHash(hash.Encode())
	if err != nil {
		t.Fatalf("ParsePasswordHash() returned error: %v", err)
	}
	if parsed.MemoryKiB != hash.MemoryKiB || parsed.Iterations != hash.Iterations || parsed.Parallelism != hash.Parallelism {
		t.Fatalf("parsed parameters = %+v, want %+v", parsed, hash)
	}
	if string(parsed.Salt) != string(hash.Salt) {
		t.Fatal("parsed salt differs from the original salt")
	}
}

func TestHashPasswordRejectsWeakPasswords(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("HashPassword() accepted a password below the minimum length")
	}
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordLength+1)); err == nil {
		t.Fatal("HashPassword() accepted a password above the maximum length")
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("HashPassword() accepted an empty password")
	}
}

func TestValidatePasswordStrengthAcceptsLongPassphrase(t *testing.T) {
	if err := ValidatePasswordStrength(strings.Repeat("x", MinPasswordLength)); err != nil {
		t.Fatalf("ValidatePasswordStrength() rejected a valid passphrase: %v", err)
	}
}
