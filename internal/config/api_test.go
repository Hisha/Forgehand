package config

import (
	"strings"
	"testing"
	"time"
)

func TestAPIIsDisabledByDefault(t *testing.T) {
	t.Setenv(APIAddressEnvironment, "")
	t.Setenv(APISessionTTLEnvironment, "")

	configuration, enabled, err := API()
	if err != nil {
		t.Fatalf("API() returned error: %v", err)
	}
	if enabled {
		t.Fatal("the remote API listener is enabled by default")
	}
	if configuration.Address != DefaultAPIAddress {
		t.Fatalf("address = %q, want %q", configuration.Address, DefaultAPIAddress)
	}
	if configuration.SessionTTL != DefaultAPISessionTTL {
		t.Fatalf("session TTL = %s, want %s", configuration.SessionTTL, DefaultAPISessionTTL)
	}
}

func TestAPIIsDisabledWhenTheAddressIsOnlyWhitespace(t *testing.T) {
	t.Setenv(APIAddressEnvironment, "   ")

	_, enabled, err := API()
	if err != nil {
		t.Fatalf("API() returned error: %v", err)
	}
	if enabled {
		t.Fatal("the remote API listener is enabled by a whitespace address")
	}
}

func TestAPIAcceptsALoopbackAddress(t *testing.T) {
	cases := []string{
		"127.0.0.1:8787",
		"127.0.0.2:9000",
		"localhost:8787",
		"[::1]:8787",
	}

	for _, address := range cases {
		t.Setenv(APIAddressEnvironment, address)

		configuration, enabled, err := API()
		if err != nil {
			t.Fatalf("API() returned error for %q: %v", address, err)
		}
		if !enabled {
			t.Fatalf("the listener is disabled for %q", address)
		}
		if configuration.Address == "" {
			t.Fatalf("the listener address is empty for %q", address)
		}
	}
}

func TestAPIRefusesANonLoopbackAddress(t *testing.T) {
	cases := []string{
		"0.0.0.0:8787",
		"192.0.2.10:8787",
		"[::]:8787",
		"example.invalid:8787",
	}

	for _, address := range cases {
		t.Setenv(APIAddressEnvironment, address)

		_, enabled, err := API()
		if err == nil {
			t.Fatalf("API() accepted the non-loopback address %q", address)
		}
		if enabled {
			t.Fatalf("the listener was enabled for the non-loopback address %q", address)
		}
		if !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("the error for %q does not explain the loopback restriction: %v", address, err)
		}
	}
}

func TestAPIRefusesAMalformedAddress(t *testing.T) {
	cases := []string{
		"127.0.0.1",
		"127.0.0.1:not-a-port",
		"127.0.0.1:0",
		"127.0.0.1:70000",
		":8787",
	}

	for _, address := range cases {
		t.Setenv(APIAddressEnvironment, address)

		if _, _, err := API(); err == nil {
			t.Fatalf("API() accepted the malformed address %q", address)
		}
	}
}

func TestAPISessionTTLIsValidated(t *testing.T) {
	t.Setenv(APIAddressEnvironment, "127.0.0.1:8787")

	t.Setenv(APISessionTTLEnvironment, "30m")
	configuration, enabled, err := API()
	if err != nil {
		t.Fatalf("API() returned error: %v", err)
	}
	if !enabled {
		t.Fatal("the listener is disabled")
	}
	if configuration.SessionTTL != 30*time.Minute {
		t.Fatalf("session TTL = %s, want 30m", configuration.SessionTTL)
	}

	for _, value := range []string{"not-a-duration", "1m", "30d"} {
		t.Setenv(APISessionTTLEnvironment, value)
		if _, _, err := API(); err == nil {
			t.Fatalf("API() accepted the session TTL %q", value)
		}
	}
}
