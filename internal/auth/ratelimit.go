package auth

import (
	"fmt"
	"sync"
	"time"
)

// LoginThrottle bounds how often an unauthenticated network caller may
// attempt a login.
//
// It tracks a per-caller budget: repeated failures from one network peer are
// throttled regardless of which account is targeted, which limits password
// spraying across many accounts from a single source.
//
// Per-account throttling is deliberately not implemented here. Account state
// is persisted by the account store so that a daemon restart cannot be used
// to reset an account's failure budget.
type LoginThrottle struct {
	mu sync.Mutex

	// MaxFailures is the number of consecutive failures allowed from a
	// single caller inside the failure window.
	MaxFailures int
	// Window is the sliding window over which failures are counted.
	Window time.Duration
	// Lockout is how long a caller stays throttled after exceeding the
	// failure budget.
	Lockout time.Duration

	now   func() time.Time
	peers map[string]*peerBudget
}

type peerBudget struct {
	failures    []time.Time
	lockedUntil time.Time
}

// NewLoginThrottle returns a throttle with the default budgets: twenty
// failures from one caller within fifteen minutes lock that caller out for
// fifteen minutes.
func NewLoginThrottle() *LoginThrottle {
	return &LoginThrottle{
		MaxFailures: 20,
		Window:      15 * time.Minute,
		Lockout:     15 * time.Minute,
		now:         time.Now,
		peers:       map[string]*peerBudget{},
	}
}

// SetClock replaces the time source. It exists so that tests can drive the
// throttle deterministically.
func (t *LoginThrottle) SetClock(now func() time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.now = now
}

// Allow reports whether a login attempt from peer may proceed. It returns
// ErrTooManyAttempts when the caller's failure budget is exhausted.
func (t *LoginThrottle) Allow(peer string) error {
	if peer == "" {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	budget := t.peers[peer]
	if budget == nil {
		return nil
	}
	if t.now().Before(budget.lockedUntil) {
		return fmt.Errorf("%w: caller is temporarily throttled", ErrTooManyAttempts)
	}
	return nil
}

// RecordFailure records a failed authentication attempt from peer.
func (t *LoginThrottle) RecordFailure(peer string) {
	if peer == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	budget := t.peers[peer]
	if budget == nil {
		budget = &peerBudget{}
		t.peers[peer] = budget
	}

	budget.failures = append(budget.failures, now)
	budget.failures = trimFailures(budget.failures, now.Add(-t.Window))
	if len(budget.failures) >= t.MaxFailures {
		budget.lockedUntil = now.Add(t.Lockout)
		budget.failures = nil
	}
}

// RecordSuccess clears the failure history for peer.
func (t *LoginThrottle) RecordSuccess(peer string) {
	if peer == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.peers, peer)
}

// LockedUntil reports the time until which a caller is locked out, if any.
func (t *LoginThrottle) LockedUntil(peer string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	budget := t.peers[peer]
	if budget == nil {
		return time.Time{}, false
	}
	return budget.lockedUntil, true
}

func trimFailures(failures []time.Time, cutoff time.Time) []time.Time {
	kept := failures[:0]
	for _, failure := range failures {
		if failure.After(cutoff) {
			kept = append(kept, failure)
		}
	}
	return kept
}
