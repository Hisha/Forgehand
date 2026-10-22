package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// SessionTokenLength is the number of random bytes in a session token. 256
// bits of entropy is well beyond what is needed to make session guessing
// infeasible.
const SessionTokenLength = 32

// SessionToken is a bearer credential issued after a successful password and
// one-time code authentication. The plaintext token is returned to the client
// exactly once; only its digest is stored.
type SessionToken struct {
	// Token is the plaintext bearer credential. It is never persisted.
	Token string
	// Hash is the hex encoded SHA-256 digest of the token.
	Hash string
}

// GenerateSessionToken returns a new cryptographically strong session token.
func GenerateSessionToken() (SessionToken, error) {
	buffer := make([]byte, SessionTokenLength)
	if _, err := rand.Read(buffer); err != nil {
		return SessionToken{}, fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)
	return SessionToken{
		Token: token,
		Hash:  HashSessionToken(token),
	}, nil
}

// HashSessionToken returns the hex encoded SHA-256 digest of a session token.
func HashSessionToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
