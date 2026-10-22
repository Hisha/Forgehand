package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateMasterKeyCreatesRestrictedFile(t *testing.T) {
	dir := t.TempDir()

	key, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey() returned error: %v", err)
	}
	if len(key) != MasterKeyLength {
		t.Fatalf("master key length = %d, want %d", len(key), MasterKeyLength)
	}

	path := filepath.Join(dir, masterKeyFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat master key: %v", err)
	}
	if info.Mode().Perm() != masterKeyFileMode {
		t.Fatalf("master key mode = %#o, want %#o", info.Mode().Perm(), masterKeyFileMode)
	}
}

func TestLoadOrCreateMasterKeyIsStable(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey() returned error: %v", err)
	}
	second, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey() returned error: %v", err)
	}

	if string(first) != string(second) {
		t.Fatal("the master key changed between loads")
	}
}

func TestLoadOrCreateMasterKeyRefusesWorldReadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, masterKeyFileName)

	key := make([]byte, MasterKeyLength)
	for index := range key {
		key[index] = byte(index)
	}
	if err := os.WriteFile(path, []byte("0123456789abcdefghijklmnopqrstuv"), 0o644); err != nil {
		t.Fatalf("write master key: %v", err)
	}

	if _, err := LoadOrCreateMasterKey(dir); err == nil {
		t.Fatal("LoadOrCreateMasterKey() accepted a world readable master key")
	}
}

func TestEncryptSecretRoundTrip(t *testing.T) {
	key, err := LoadOrCreateMasterKey(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey() returned error: %v", err)
	}

	secret := []byte("a shared totp secret")
	ciphertext, err := key.EncryptSecret("alice", secret)
	if err != nil {
		t.Fatalf("EncryptSecret() returned error: %v", err)
	}
	if ciphertext == "" {
		t.Fatal("EncryptSecret() produced an empty ciphertext")
	}

	plaintext, err := key.DecryptSecret("alice", ciphertext)
	if err != nil {
		t.Fatalf("DecryptSecret() returned error: %v", err)
	}
	if string(plaintext) != string(secret) {
		t.Fatalf("decrypted secret = %q, want %q", plaintext, secret)
	}
}

func TestEncryptSecretProducesDistinctCiphertexts(t *testing.T) {
	key, err := LoadOrCreateMasterKey(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey() returned error: %v", err)
	}

	secret := []byte("a shared totp secret")
	first, err := key.EncryptSecret("alice", secret)
	if err != nil {
		t.Fatalf("EncryptSecret() returned error: %v", err)
	}
	second, err := key.EncryptSecret("alice", secret)
	if err != nil {
		t.Fatalf("EncryptSecret() returned error: %v", err)
	}

	if first == second {
		t.Fatal("two encryptions of the same secret produced the same ciphertext")
	}
}

func TestDecryptSecretRejectsAnotherAccount(t *testing.T) {
	key, err := LoadOrCreateMasterKey(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey() returned error: %v", err)
	}

	ciphertext, err := key.EncryptSecret("alice", []byte("a shared totp secret"))
	if err != nil {
		t.Fatalf("EncryptSecret() returned error: %v", err)
	}

	if _, err := key.DecryptSecret("mallory", ciphertext); err == nil {
		t.Fatal("DecryptSecret() decrypted a ciphertext with another account's key")
	}
}

func TestDecryptSecretRejectsTamperedCiphertext(t *testing.T) {
	key, err := LoadOrCreateMasterKey(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey() returned error: %v", err)
	}

	ciphertext, err := key.EncryptSecret("alice", []byte("a shared totp secret"))
	if err != nil {
		t.Fatalf("EncryptSecret() returned error: %v", err)
	}

	tampered := []byte(ciphertext)
	tampered[0] = 'A'
	if tampered[0] == ciphertext[0] {
		tampered[0] = 'B'
	}

	if _, err := key.DecryptSecret("alice", string(tampered)); err == nil {
		t.Fatal("DecryptSecret() accepted a tampered ciphertext")
	}
}

func TestDecryptSecretRejectsMalformedCiphertext(t *testing.T) {
	key, err := LoadOrCreateMasterKey(t.TempDir())
	if err != nil {
		t.Fatalf("LoadOrCreateMasterKey() returned error: %v", err)
	}

	for _, value := range []string{"", "not base64!", "AAAA"} {
		if _, err := key.DecryptSecret("alice", value); err == nil {
			t.Fatalf("DecryptSecret(%q) accepted a malformed ciphertext", value)
		}
	}
}
