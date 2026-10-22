package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// APIAddressEnvironment configures the network listener for the remote
	// API. The listener is disabled when the variable is unset or empty, so
	// the daemon never exposes a network interface by default.
	APIAddressEnvironment = "FORGEHAND_API_ADDRESS"

	// APISessionTTLEnvironment configures how long an authenticated remote
	// session remains valid.
	APISessionTTLEnvironment = "FORGEHAND_API_SESSION_TTL"

	// DefaultAPIAddress is the loopback address used when the API is enabled
	// without an explicit address.
	DefaultAPIAddress = "127.0.0.1:8787"

	// DefaultAPISessionTTL is the default lifetime of a remote session.
	DefaultAPISessionTTL = 12 * time.Hour

	// MinAPISessionTTL and MaxAPISessionTTL bound the configured session
	// lifetime so that a misconfiguration cannot create an effectively
	// permanent session.
	MinAPISessionTTL = 5 * time.Minute
	MaxAPISessionTTL = 7 * 24 * time.Hour
)

// APIConfig describes the remote API listener.
type APIConfig struct {
	// Address is the host:port the listener binds to.
	Address string
	// SessionTTL is the lifetime of an issued remote session.
	SessionTTL time.Duration
}

// API returns the remote API configuration. The second return value reports
// whether the network listener is enabled at all.
//
// The listener is disabled by default. When it is enabled, the address must
// resolve to a loopback interface. Binding to a routable interface is
// refused because the public-exposure release gate in the Forgehand security
// principles has not been satisfied; deployments behind a reverse proxy are
// supported by proxying to the loopback listener.
func API() (APIConfig, bool, error) {
	config := APIConfig{
		Address:    DefaultAPIAddress,
		SessionTTL: DefaultAPISessionTTL,
	}

	if raw := strings.TrimSpace(os.Getenv(APISessionTTLEnvironment)); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil {
			return APIConfig{}, false, fmt.Errorf(
				"parse %s as a duration: %w",
				APISessionTTLEnvironment,
				err,
			)
		}
		if ttl < MinAPISessionTTL || ttl > MaxAPISessionTTL {
			return APIConfig{}, false, fmt.Errorf(
				"%s must be between %s and %s",
				APISessionTTLEnvironment,
				MinAPISessionTTL,
				MaxAPISessionTTL,
			)
		}
		config.SessionTTL = ttl
	}

	configured := strings.TrimSpace(os.Getenv(APIAddressEnvironment))
	if configured == "" {
		return config, false, nil
	}

	address, err := ValidateAPIAddress(configured)
	if err != nil {
		return APIConfig{}, false, err
	}
	config.Address = address
	return config, true, nil
}

// ValidateAPIAddress checks that the configured listener address is a
// loopback host and port. A routable address is refused with a message that
// names the unmet release gate, so an operator cannot expose the daemon to a
// network by changing a single environment variable.
func ValidateAPIAddress(value string) (string, error) {
	if strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("API address contains a NUL byte")
	}

	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return "", fmt.Errorf("API address %q must be in host:port form: %w", value, err)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "", errors.New("API address must name a loopback host")
	}

	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("API address %q must use a port between 1 and 65535", value)
	}

	if !isLoopbackHost(host) {
		return "", fmt.Errorf(
			"API address %q is not a loopback address; remote exposure is blocked until the public-exposure release gate in docs/SECURITY-PRINCIPLES.md section 14.5 is satisfied",
			value,
		)
	}

	return net.JoinHostPort(host, strconv.Itoa(portNumber)), nil
}

// isLoopbackHost reports whether the host component designates the loopback
// interface. Only literal loopback addresses and the "localhost" name are
// accepted; a host name is never resolved, so a DNS answer can never move
// the listener onto a routable interface.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	if address == nil {
		return false
	}
	return address.IsLoopback()
}
