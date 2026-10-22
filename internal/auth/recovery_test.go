package auth

import (
	"strings"
	"testing"
)

func TestGenerateRecoveryCodesProducesDistinctHashes(t *testing.T) {
	codes, err := GenerateRecoveryCodes(RecoveryCodeCount)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes() returned error: %v", err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("generated %d codes, want %d", len(codes), RecoveryCodeCount)
	}

	seenCodes := map[string]struct{}{}
	seenHashes := map[string]struct{}{}
	for _, code := range codes {
		if code.Code == "" {
			t.Fatal("a generated recovery code is empty")
		}
		if code.Hash == "" {
			t.Fatal("a generated recovery code has no hash")
		}
		if strings.Contains(code.Hash, code.Code) {
			t.Fatalf("the hash of %q contains the plaintext code", code.Code)
		}
		if _, exists := seenCodes[code.Code]; exists {
			t.Fatalf("the recovery code %q was generated twice", code.Code)
		}
		if _, exists := seenHashes[code.Hash]; exists {
			t.Fatalf("the recovery code hash of %q was generated twice", code.Code)
		}
		seenCodes[code.Code] = struct{}{}
		seenHashes[code.Hash] = struct{}{}
	}
}

func TestRecoveryCodeFormatIsTranscribable(t *testing.T) {
	codes, err := GenerateRecoveryCodes(1)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes() returned error: %v", err)
	}

	parts := strings.Split(codes[0].Code, "-")
	if len(parts) != RecoveryCodeGroups {
		t.Fatalf("recovery code %q has %d groups, want %d", codes[0].Code, len(parts), RecoveryCodeGroups)
	}
	for _, part := range parts {
		if len(part) != RecoveryCodeGroupLength {
			t.Fatalf("recovery code group %q has the wrong length", part)
		}
		for _, character := range part {
			if !strings.ContainsRune(recoveryCodeAlphabet, character) {
				t.Fatalf("recovery code %q contains the ambiguous character %q", codes[0].Code, character)
			}
		}
	}
}

func TestHashRecoveryCodeIgnoresPresentation(t *testing.T) {
	code := "ABCDE-FGHJK"

	plain := HashRecoveryCode(code)
	lower := HashRecoveryCode(strings.ToLower(code))
	spaced := HashRecoveryCode(" " + strings.ReplaceAll(code, "-", " ") + " ")

	if plain != lower || plain != spaced {
		t.Fatal("the recovery code hash depends on the presentation of the code")
	}
	if !VerifyRecoveryCode(code, plain) {
		t.Fatal("VerifyRecoveryCode() rejected the matching code")
	}
	if VerifyRecoveryCode("ZZZZZ-ZZZZZ", plain) {
		t.Fatal("VerifyRecoveryCode() accepted a non-matching code")
	}
}

func TestGenerateRecoveryCodesRejectsNonPositiveCount(t *testing.T) {
	if _, err := GenerateRecoveryCodes(0); err == nil {
		t.Fatal("GenerateRecoveryCodes() accepted a count of zero")
	}
}
