package config

import (
	"testing"
	"time"
)

func TestProviderConfiguration(t *testing.T) {
	clearProviderEnv := func(t *testing.T) {
		t.Helper()
		for _, name := range []string{
			ProviderBaseURLEnvironment,
			ProviderModelEnvironment,
			ProviderAPIKeyEnvironment,
			ProviderTimeoutEnvironment,
			ProviderMaxTokensEnvironment,
			ProviderMaxContextCharsEnvironment,
		} {
			t.Setenv(name, "")
		}
	}

	t.Run("unconfigured provider is not an error", func(t *testing.T) {
		clearProviderEnv(t)
		config, err := Provider()
		if err != nil {
			t.Fatalf("Provider() error = %v", err)
		}
		if config.Configured() {
			t.Fatalf("expected provider to be unconfigured: %#v", config)
		}
		if config.Timeout != DefaultProviderTimeout || config.MaxTokens != DefaultProviderMaxTokens {
			t.Fatalf("unexpected defaults: %#v", config)
		}
	})

	t.Run("explicit values are resolved", func(t *testing.T) {
		clearProviderEnv(t)
		t.Setenv(ProviderBaseURLEnvironment, "http://127.0.0.1:8080/v1")
		t.Setenv(ProviderModelEnvironment, "local-model")
		t.Setenv(ProviderAPIKeyEnvironment, "secret-token")
		t.Setenv(ProviderTimeoutEnvironment, "45s")
		t.Setenv(ProviderMaxTokensEnvironment, "512")
		t.Setenv(ProviderMaxContextCharsEnvironment, "4000")

		config, err := Provider()
		if err != nil {
			t.Fatalf("Provider() error = %v", err)
		}
		if !config.Configured() {
			t.Fatal("expected provider to be configured")
		}
		if config.BaseURL != "http://127.0.0.1:8080/v1" || config.Model != "local-model" {
			t.Fatalf("unexpected base/model: %#v", config)
		}
		if config.APIKey != "secret-token" {
			t.Fatalf("unexpected api key: %#v", config)
		}
		if config.Timeout != 45*time.Second || config.MaxTokens != 512 || config.MaxContextChars != 4000 {
			t.Fatalf("unexpected limits: %#v", config)
		}
	})

	t.Run("model is required when a base URL is set", func(t *testing.T) {
		clearProviderEnv(t)
		t.Setenv(ProviderBaseURLEnvironment, "http://127.0.0.1:8080/v1")
		if _, err := Provider(); err == nil {
			t.Fatal("expected error when model is missing")
		}
	})

	t.Run("invalid values fail closed", func(t *testing.T) {
		for _, test := range []struct {
			name  string
			value string
		}{
			{ProviderTimeoutEnvironment, "not-a-duration"},
			{ProviderTimeoutEnvironment, "0s"},
			{ProviderMaxTokensEnvironment, "0"},
			{ProviderMaxTokensEnvironment, "many"},
			{ProviderMaxContextCharsEnvironment, "0"},
			{ProviderMaxContextCharsEnvironment, "-5"},
			{ProviderMaxContextCharsEnvironment, "lots"},
		} {
			t.Run(test.name+"="+test.value, func(t *testing.T) {
				clearProviderEnv(t)
				t.Setenv(test.name, test.value)
				if _, err := Provider(); err == nil {
					t.Fatalf("Provider() with %s=%q unexpectedly succeeded", test.name, test.value)
				}
			})
		}
	})

	t.Run("base URL must be http or https", func(t *testing.T) {
		clearProviderEnv(t)
		t.Setenv(ProviderBaseURLEnvironment, "ftp://example.invalid/v1")
		t.Setenv(ProviderModelEnvironment, "local-model")
		if _, err := Provider(); err == nil {
			t.Fatal("expected error for unsupported scheme")
		}
	})
}
