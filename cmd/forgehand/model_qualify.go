package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/config"
	"github.com/Hisha/Forgehand/internal/planner"
	"github.com/Hisha/Forgehand/internal/provider"
)

const (
	defaultQualificationRuns    = 5
	maxQualificationRuns        = 100
	defaultQualificationTimeout = 15 * time.Minute
)

type qualificationProviderFactory func(config.ProviderConfig, string) provider.Provider

type qualificationInfrastructureError struct{ message string }

func (e *qualificationInfrastructureError) Error() string { return e.message }

func modelCommand(args []string) error {
	return runModelCommand(args, io.Writer(os.Stdout), config.ProviderForModel, func(cfg config.ProviderConfig, model string) provider.Provider {
		return provider.NewClient(cfg.BaseURL, cfg.APIKey, model, cfg.Timeout, cfg.MaxTokens)
	})
}

func runModelCommand(args []string, stdout io.Writer, loadConfig func(string) (config.ProviderConfig, error), makeProvider qualificationProviderFactory) error {
	if len(args) == 0 || args[0] != "qualify" {
		return fmt.Errorf("usage: forgehand model qualify --model <id> --capability planning --runs <n> --output <path> [--timeout <duration>]")
	}
	model, capability, output := "", "", ""
	runs, timeout := defaultQualificationRuns, defaultQualificationTimeout
	for i := 1; i < len(args); i++ {
		if i+1 >= len(args) {
			return fmt.Errorf("missing value for %s", args[i])
		}
		value := args[i+1]
		switch args[i] {
		case "--model":
			model = strings.TrimSpace(value)
		case "--capability":
			capability = strings.ToLower(strings.TrimSpace(value))
		case "--runs":
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("parse --runs: %w", err)
			}
			runs = parsed
		case "--output":
			output = strings.TrimSpace(value)
		case "--timeout":
			parsed, err := time.ParseDuration(value)
			if err != nil {
				return fmt.Errorf("parse --timeout: %w", err)
			}
			timeout = parsed
		default:
			return fmt.Errorf("unknown model qualify option %q", args[i])
		}
		i++
	}
	if model == "" {
		return fmt.Errorf("--model is required")
	}
	if capability == "" {
		return fmt.Errorf("--capability is required")
	}
	if capability != planner.CapabilityPlanning {
		return fmt.Errorf("unsupported qualification capability %q; supported: planning", capability)
	}
	if runs < 1 || runs > maxQualificationRuns {
		return fmt.Errorf("--runs must be between 1 and %d", maxQualificationRuns)
	}
	if output == "" {
		return fmt.Errorf("--output is required")
	}
	if timeout <= 0 || timeout > time.Hour {
		return fmt.Errorf("--timeout must be greater than zero and no more than 1h")
	}

	cfg, err := loadConfig(model)
	if err != nil {
		return &qualificationInfrastructureError{message: "provider configuration: " + err.Error()}
	}
	if !cfg.Configured() {
		return &qualificationInfrastructureError{message: "provider is not configured; set " + config.ProviderBaseURLEnvironment}
	}
	client := makeProvider(cfg, model)
	if client == nil {
		return &qualificationInfrastructureError{message: "provider factory returned no provider"}
	}
	fingerprint := qualificationProviderFingerprint(cfg)
	base := planner.QualificationRequest(model, fingerprint, capability)
	limits := planner.ModelLimits{MaxTokens: cfg.MaxTokens}
	decomposer := &planner.ModelDecomposer{Provider: client, Model: model, Limits: limits}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	report := planner.Qualify(ctx, decomposer, base, model, fingerprint, capability, planner.RepresentativeQualificationCases(), runs)
	redactQualificationSecrets(&report, cfg.APIKey)
	if err := planner.WriteQualificationReport(output, report); err != nil {
		return &qualificationInfrastructureError{message: err.Error()}
	}
	printQualificationSummary(stdout, report, output)
	if report.Recommendation == planner.RecommendationInconclusive {
		return &qualificationInfrastructureError{message: fmt.Sprintf("qualification infrastructure was inconclusive (%d infrastructure failure(s)); report: %s", report.Aggregate.InfrastructureFailures, output)}
	}
	return nil
}

func redactQualificationSecrets(report *planner.QualificationReport, secrets ...string) {
	for runIndex := range report.Runs {
		for errorIndex, message := range report.Runs[runIndex].ValidationErrors {
			for _, secret := range secrets {
				if secret != "" {
					message = strings.ReplaceAll(message, secret, "[REDACTED]")
				}
			}
			report.Runs[runIndex].ValidationErrors[errorIndex] = message
		}
	}
}

func qualificationProviderFingerprint(cfg config.ProviderConfig) string {
	return cfg.Fingerprint()
}

func printQualificationSummary(w io.Writer, report planner.QualificationReport, output string) {
	fmt.Fprintf(w, "Model qualification: %s\n", report.Recommendation)
	fmt.Fprintf(w, "Model: %s\nCapability: %s\n", report.Model, report.Capability)
	fmt.Fprintf(w, "Cases: %d · repetitions: %d · passing runs: %d/%d\n", report.CaseCount, report.Repetitions, report.Aggregate.PassingRuns, report.Aggregate.ExpectedRuns)
	for _, consistency := range report.Consistency {
		fmt.Fprintf(w, "  %-28s %d/%d pass (%.0f%%)\n", consistency.Case, consistency.PassingAttempts, consistency.Attempts, consistency.PassRate*100)
	}
	if report.Aggregate.ModelFailures > 0 {
		fmt.Fprintf(w, "Model qualification failures: %d\n", report.Aggregate.ModelFailures)
	}
	if report.Aggregate.InfrastructureFailures > 0 {
		fmt.Fprintf(w, "Infrastructure failures: %d\n", report.Aggregate.InfrastructureFailures)
	}
	fmt.Fprintf(w, "Report: %s\n", output)
}
