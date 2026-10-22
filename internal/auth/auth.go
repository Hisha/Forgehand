// Package auth implements the authentication primitives required by the
// Forgehand remote API: Argon2id password hashing, RFC 6238 TOTP, single-use
// recovery codes, authenticated encryption of TOTP secrets at rest, session
// token issuance, and login throttling.
//
// The package deliberately contains no storage concerns and no network
// concerns. Persistence lives in internal/state and transport lives in
// internal/remoteapi. Every function in this package fails closed: an
// unparseable secret, an unverifiable hash, or an unexpected algorithm is
// treated as a failure rather than being silently repaired.
package auth

import "errors"

// ErrInvalidCredentials is returned by verification helpers when a credential
// does not match. It intentionally carries no detail about which part of a
// credential pair failed, so that callers cannot use it to distinguish a wrong
// password from a wrong one-time code.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrReplay is returned when a one-time code is presented that was already
// accepted for the account.
var ErrReplay = errors.New("one-time code has already been used")

// ErrTooManyAttempts is returned by the throttling helper when the caller
// exceeded the configured failure budget.
var ErrTooManyAttempts = errors.New("too many failed authentication attempts")
