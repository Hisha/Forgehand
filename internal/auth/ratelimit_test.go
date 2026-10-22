package auth

import (
	"errors"
	"testing"
	"time"
)

func TestGenerateSessionTokenProducesDistinctTokens(t *testing.T) {
	first, err := GenerateSessionToken()
	if err != nil {
		t.Fatalf("GenerateSessionToken() returned error: %v", err)
	}
	second, err := GenerateSessionToken()
	if err != nil {
		t.Fatalf("GenerateSessionToken() returned error: %v", err)
	}

	if first.Token == second.Token {
		t.Fatal("two generated session tokens are identical")
	}
	if first.Hash == second.Hash {
		t.Fatal("two generated session token hashes are identical")
	}
	if first.Token == first.Hash {
		t.Fatal("the session token hash equals the token")
	}
}

func TestHashSessionTokenIsStable(t *testing.T) {
	token, err := GenerateSessionToken()
	if err != nil {
		t.Fatalf("GenerateSessionToken() returned error: %v", err)
	}

	if HashSessionToken(token.Token) != token.Hash {
		t.Fatal("HashSessionToken() is not stable")
	}
}

func TestLoginThrottleAllowsAttemptsUntilBudgetIsExhausted(t *testing.T) {
	throttle := NewLoginThrottle()
	throttle.MaxFailures = 3
	throttle.Window = time.Minute
	throttle.Lockout = time.Minute

	now := time.Now()
	throttle.SetClock(func() time.Time { return now })

	for attempt := 0; attempt < 3; attempt++ {
		if err := throttle.Allow("192.0.2.10"); err != nil {
			t.Fatalf("Allow() refused attempt %d: %v", attempt, err)
		}
		throttle.RecordFailure("192.0.2.10")
	}

	if err := throttle.Allow("192.0.2.10"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("Allow() returned %v, want ErrTooManyAttempts", err)
	}
}

func TestLoginThrottleSeparatesCallers(t *testing.T) {
	throttle := NewLoginThrottle()
	throttle.MaxFailures = 2
	throttle.Window = time.Minute
	throttle.Lockout = time.Minute

	now := time.Now()
	throttle.SetClock(func() time.Time { return now })

	throttle.RecordFailure("192.0.2.10")
	throttle.RecordFailure("192.0.2.10")

	if err := throttle.Allow("192.0.2.11"); err != nil {
		t.Fatalf("Allow() refused an unrelated caller: %v", err)
	}
}

func TestLoginThrottleReleasesAfterSuccess(t *testing.T) {
	throttle := NewLoginThrottle()
	throttle.MaxFailures = 2
	throttle.Window = time.Minute
	throttle.Lockout = time.Minute

	now := time.Now()
	throttle.SetClock(func() time.Time { return now })

	throttle.RecordFailure("192.0.2.10")
	throttle.RecordSuccess("192.0.2.10")

	if err := throttle.Allow("192.0.2.10"); err != nil {
		t.Fatalf("Allow() refused a caller after a success: %v", err)
	}
}

func TestLoginThrottleReleasesAfterTheWindow(t *testing.T) {
	throttle := NewLoginThrottle()
	throttle.MaxFailures = 2
	throttle.Window = time.Minute
	throttle.Lockout = time.Minute

	now := time.Now()
	throttle.SetClock(func() time.Time { return now })

	throttle.RecordFailure("192.0.2.10")
	throttle.RecordFailure("192.0.2.10")

	if err := throttle.Allow("192.0.2.10"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("Allow() returned %v, want ErrTooManyAttempts", err)
	}

	now = now.Add(2 * time.Minute)
	if err := throttle.Allow("192.0.2.10"); err != nil {
		t.Fatalf("Allow() refused a caller after the lockout expired: %v", err)
	}
}
