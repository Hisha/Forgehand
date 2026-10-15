package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// ProviderBaseURLEnvironment configures the OpenAI-compatible provider base
	// URL (for example http://127.0.0.1:8080/v1). It is required before a
	// model-backed coding session can run.
	ProviderBaseURLEnvironment = "FORGEHAND_PROVIDER_BASE_URL"
	// ProviderModelEnvironment configures the default model name.
	ProviderModelEnvironment = "FORGEHAND_PROVIDER_MODEL"
	// ProviderAPIKeyEnvironment configures the optional bearer token. It is
	// never logged.
	ProviderAPIKeyEnvironment = "FORGEHAND_PROVIDER_API_KEY"
	// ProviderTimeoutEnvironment configures the request timeout as a Go
	// duration (for example 90s or 2m).
	ProviderTimeoutEnvironment = "FORGEHAND_PROVIDER_TIMEOUT"
	// ProviderMaxTokensEnvironment configures the max_tokens request field.
	ProviderMaxTokensEnvironment = "FORGEHAND_PROVIDER_MAX_TOKENS"

	// DefaultProviderTimeout is used when no timeout is configured.
	DefaultProviderTimeout = 120 * time.Second
	// DefaultProviderMaxTokens is used when no max_tokens value is configured.
	DefaultProviderMaxTokens = 2048
)

// ProviderConfig is the resolved OpenAI-compatible provider configuration.
type ProviderConfig struct {
	BaseURL   string
	Model     string
	APIKey    string
	Timeout   time.Duration
	MaxTokens int
}

// Configured reports whether a provider base URL is set. Without a base URL the
// daemon runs without model support.
func (c ProviderConfig) Configured() bool {
	return strings.TrimSpace(c.BaseURL) != ""
}

// Provider resolves the provider configuration from the environment. It returns
// an error only when an explicitly configured value is invalid; an unconfigured
// provider is not an error.
func Provider() (ProviderConfig, error) {
	config := ProviderConfig{
		BaseURL:   strings.TrimSpace(os.Getenv(ProviderBaseURLEnvironment)),
		Model:     strings.TrimSpace(os.Getenv(ProviderModelEnvironment)),
		APIKey:    strings.TrimSpace(os.Getenv(ProviderAPIKeyEnvironment)),
		Timeout:   DefaultProviderTimeout,
		MaxTokens: DefaultProviderMaxTokens,
	}

	if configured := strings.TrimSpace(os.Getenv(ProviderTimeoutEnvironment)); configured != "" {
		timeout, err := time.ParseDuration(configured)
		if err != nil {
			return ProviderConfig{}, fmt.Errorf(
				"parse %s as a duration: %w",
				ProviderTimeoutEnvironment,
				err,
			)
		}
		if timeout <= 0 {
			return ProviderConfig{}, fmt.Errorf(
				"%s must be positive",
				ProviderTimeoutEnvironment,
			)
		}
		config.Timeout = timeout
	}

	if configured := strings.TrimSpace(os.Getenv(ProviderMaxTokensEnvironment)); configured != "" {
		maxTokens, err := strconv.Atoi(configured)
		if err != nil {
			return ProviderConfig{}, fmt.Errorf(
				"parse %s as an integer: %w",
				ProviderMaxTokensEnvironment,
				err,
			)
		}
		if maxTokens <= 0 {
			return ProviderConfig{}, fmt.Errorf(
				"%s must be positive",
				ProviderMaxTokensEnvironment,
			)
		}
		config.MaxTokens = maxTokens
	}

	if config.Configured() {
		if config.Model == "" {
			return ProviderConfig{}, fmt.Errorf(
				"%s is required when %s is set",
				ProviderModelEnvironment,
				ProviderBaseURLEnvironment,
			)
		}
		if err := validateProviderBaseURL(config.BaseURL); err != nil {
			return ProviderConfig{}, err
		}
	}

	return config, nil
}

func validateProviderBaseURL(value string) error {
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s contains a NUL byte", ProviderBaseURLEnvironment)
	}
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
		return fmt.Errorf("%s must be an http or https URL", ProviderBaseURLEnvironment)
	}
	return nil
}
