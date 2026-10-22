package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/hkdf"
)

const (
	// MasterKeyLength is the length of the AES-256 key that protects
	// TOTP secrets at rest.
	MasterKeyLength = 32

	// masterKeyFileName is the name of the restricted key file inside the
	// daemon state directory.
	masterKeyFileName = "auth-master.key"

	// masterKeyFileMode is the permission mode of the master key file. The
	// key must be readable only by the daemon service identity.
	masterKeyFileMode = 0o600

	// masterKeyPurpose is the HKDF info label used to derive per-account
	// encryption keys from the master key.
	masterKeyPurpose = "forgehand/auth/totp-secret/v1"
)

// MasterKey is the daemon secret that protects TOTP secrets at rest. It is
// loaded from, or created in, a permission-restricted file inside the daemon
// state directory. It is never embedded in the binary and never logged.
type MasterKey []byte

// LoadOrCreateMasterKey reads the master key from the state directory,
// creating it with cryptographically secure random bytes when it does not
// exist yet. The file is created with mode 0600 and refuses to be used when
// its permissions allow group or world access.
func LoadOrCreateMasterKey(stateDir string) (MasterKey, error) {
	if stateDir == "" {
		return nil, errors.New("state directory must not be empty")
	}

	path := filepath.Join(stateDir, masterKeyFileName)

	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("master key %q is a symbolic link; refusing to use it", path)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("master key %q is not a regular file", path)
		}
		if permissions := info.Mode().Perm(); permissions&0o077 != 0 {
			return nil, fmt.Errorf(
				"master key %q permissions %04o permit group or world access; refusing to use it",
				path,
				permissions,
			)
		}
	case errors.Is(err, os.ErrNotExist):
		key, createErr := createMasterKey(path)
		if createErr != nil {
			return nil, createErr
		}
		return key, nil
	default:
		return nil, fmt.Errorf("inspect master key: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read master key: %w", err)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, fmt.Errorf("decode master key: %w", err)
	}
	if len(decoded) != MasterKeyLength {
		return nil, fmt.Errorf("master key has an unexpected length of %d bytes", len(decoded))
	}
	return MasterKey(decoded), nil
}

func createMasterKey(path string) (MasterKey, error) {
	key := make([]byte, MasterKeyLength)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}

	encoded := []byte(base64.RawStdEncoding.EncodeToString(key))

	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, masterKeyFileMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another process created it first; read it instead of failing
			// so a concurrent start does not corrupt the store.
			return LoadOrCreateMasterKey(filepath.Dir(path))
		}
		return nil, fmt.Errorf("create master key: %w", err)
	}
	defer file.Close()

	if err := file.Chmod(masterKeyFileMode); err != nil {
		return nil, fmt.Errorf("secure master key: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		return nil, fmt.Errorf("write master key: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync master key: %w", err)
	}
	return MasterKey(key), nil
}

// DeriveKey derives a purpose-specific key from the master key. The account
// name binds the derived key to a single account so that a ciphertext for one
// account cannot be decrypted with another account's key.
func (m MasterKey) DeriveKey(purpose, account string) ([]byte, error) {
	if len(m) != MasterKeyLength {
		return nil, errors.New("master key has an unexpected length")
	}
	reader := hkdf.New(sha256.New, m, nil, []byte(masterKeyPurpose+"/"+purpose+"/"+account))
	key := make([]byte, MasterKeyLength)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("derive encryption key: %w", err)
	}
	return key, nil
}

// EncryptSecret encrypts a TOTP secret with AES-256-GCM. The account name is
// bound into the additional authenticated data, so a ciphertext cannot be
// moved to a different account. The returned value is
// base64(nonce || ciphertext || tag).
func (m MasterKey) EncryptSecret(account string, plaintext []byte) (string, error) {
	key, err := m.DeriveKey("totp", account)
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create AEAD: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	sealed := aead.Seal(nil, nonce, plaintext, []byte(account))
	return base64.RawStdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

// DecryptSecret decrypts a value produced by EncryptSecret.
func (m MasterKey) DecryptSecret(account, encoded string) ([]byte, error) {
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("encrypted secret is malformed")
	}

	key, err := m.DeriveKey("totp", account)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AEAD: %w", err)
	}
	if len(raw) < aead.NonceSize()+aead.Overhead() {
		return nil, errors.New("encrypted secret is malformed")
	}

	nonce := raw[:aead.NonceSize()]
	sealed := raw[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, sealed, []byte(account))
	if err != nil {
		return nil, errors.New("encrypted secret could not be authenticated")
	}
	return plaintext, nil
}
