package config

import (
	"crypto/sha256"
	"fmt"
	"net/url"
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
	// ProviderMaxContextCharsEnvironment configures the character budget for the
	// assembled repository-context prompt. When unset, the repocontext package
	// default is used.
	ProviderMaxContextCharsEnvironment = "FORGEHAND_PROVIDER_MAX_CONTEXT_CHARS"
	// ProviderCapabilitiesEnvironment is a comma-separated allow-list of model
	// responsibilities explicitly qualified by the operator. Semantic planning
	// requires "planning"; configuring a provider alone does not grant it.
	ProviderCapabilitiesEnvironment = "FORGEHAND_PROVIDER_CAPABILITIES"

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
	// MaxContextChars bounds the assembled repository-context prompt. Zero means
	// the repocontext package default applies.
	MaxContextChars int
	Capabilities    []string
}

func (c ProviderConfig) HasCapability(name string) bool {
	for _, capability := range c.Capabilities {
		if capability == name {
			return true
		}
	}
	return false
}

// Configured reports whether a provider base URL is set. Without a base URL the
// daemon runs without model support.
func (c ProviderConfig) Configured() bool {
	return strings.TrimSpace(c.BaseURL) != ""
}

// Fingerprint identifies the non-secret provider configuration used for an
// evaluation without exposing its endpoint or credentials in durable state.
func (c ProviderConfig) Fingerprint() string {
	value := fmt.Sprintf("base=%s\ntimeout=%s\nmax_tokens=%d\nmax_context_chars=%d", c.BaseURL, c.Timeout, c.MaxTokens, c.MaxContextChars)
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", sum)
}

// DisplayEndpoint returns a credential-free endpoint suitable for operator
// interfaces. Provider requests continue using the original configured URL.
func (c ProviderConfig) DisplayEndpoint() string {
	parsed, err := url.Parse(c.BaseURL)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// Provider resolves the provider configuration from the environment. It returns
// an error only when an explicitly configured value is invalid; an unconfigured
// provider is not an error.
func Provider() (ProviderConfig, error) {
	return providerConfig("")
}

// ProviderForModel resolves the same provider settings while using an
// explicitly selected model. It lets standalone evaluations select a model
// without changing the daemon's configured default model.
func ProviderForModel(model string) (ProviderConfig, error) {
	return providerConfig(strings.TrimSpace(model))
}

func providerConfig(modelOverride string) (ProviderConfig, error) {
	model := modelOverride
	if model == "" {
		model = strings.TrimSpace(os.Getenv(ProviderModelEnvironment))
	}
	config := ProviderConfig{
		BaseURL:   strings.TrimSpace(os.Getenv(ProviderBaseURLEnvironment)),
		Model:     model,
		APIKey:    strings.TrimSpace(os.Getenv(ProviderAPIKeyEnvironment)),
		Timeout:   DefaultProviderTimeout,
		MaxTokens: DefaultProviderMaxTokens,
	}
	seenCapabilities := map[string]bool{}
	for _, value := range strings.Split(os.Getenv(ProviderCapabilitiesEnvironment), ",") {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seenCapabilities[value] {
			continue
		}
		switch value {
		case "planning", "interpretation", "coding":
			seenCapabilities[value] = true
			config.Capabilities = append(config.Capabilities, value)
		default:
			return ProviderConfig{}, fmt.Errorf("%s contains unsupported capability %q", ProviderCapabilitiesEnvironment, value)
		}
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

	if configured := strings.TrimSpace(os.Getenv(ProviderMaxContextCharsEnvironment)); configured != "" {
		maxContextChars, err := strconv.Atoi(configured)
		if err != nil {
			return ProviderConfig{}, fmt.Errorf(
				"parse %s as an integer: %w",
				ProviderMaxContextCharsEnvironment,
				err,
			)
		}
		if maxContextChars <= 0 {
			return ProviderConfig{}, fmt.Errorf(
				"%s must be positive",
				ProviderMaxContextCharsEnvironment,
			)
		}
		config.MaxContextChars = maxContextChars
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
