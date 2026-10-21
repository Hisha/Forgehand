package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/config"
	"github.com/Hisha/Forgehand/internal/planner"
	"github.com/Hisha/Forgehand/internal/provider"
)

type qualificationProviderFunc func(context.Context, provider.ChatRequest) (provider.ChatResponse, error)

func (f qualificationProviderFunc) Complete(ctx context.Context, request provider.ChatRequest) (provider.ChatResponse, error) {
	return f(ctx, request)
}

func qualificationTestConfig(apiKey string) config.ProviderConfig {
	return config.ProviderConfig{BaseURL: "http://127.0.0.1:8080/v1", Model: "default", APIKey: apiKey, Timeout: time.Second, MaxTokens: 2048}
}

func successfulQualificationProvider() provider.Provider {
	return qualificationProviderFunc(func(_ context.Context, chat provider.ChatRequest) (provider.ChatResponse, error) {
		var request planner.Request
		if err := json.Unmarshal([]byte(chat.User), &request); err != nil {
			return provider.ChatResponse{}, err
		}
		proposal := qualificationProposal(request)
		content, err := json.Marshal(proposal)
		return provider.ChatResponse{Content: string(content), Model: chat.Model}, err
	})
}

func qualificationProposal(request planner.Request) planner.Proposal {
	p := planner.Proposal{Objective: request.Objective, Constraints: append([]string(nil), request.Constraints...), ReadOnly: true}
	objective := strings.ToLower(request.Objective)
	switch {
	case strings.Contains(objective, "committed files under docs"):
		p.Tasks = []planner.Task{{Key: "inventory-docs", Objective: "Inventory committed file paths under docs", Method: planner.MethodInventoryLookup, Inputs: map[string]string{"path_prefix": "docs"}, RequiredEvidence: "committed file paths and blob identities", CompletionCriteria: "all bounded docs paths are recorded"}}
	case strings.Contains(objective, "max_retries"):
		p.Tasks = []planner.Task{
			{Key: "find-declaration", Objective: "Locate the MAX_RETRIES declaration", Method: planner.MethodCommittedSearch, Inputs: map[string]string{"term": "MAX_RETRIES"}, RequiredEvidence: "committed path, blob, and matching line", CompletionCriteria: "all bounded declaration matches are recorded"},
			{Key: "trace-consumer", Objective: "Plan how to identify the implementation consumer", Method: planner.MethodIncrementalPlanning, DependsOn: []string{"find-declaration"}, RequiredEvidence: "deterministic declaration evidence for a follow-up plan", CompletionCriteria: "the consumer investigation is represented by a validated plan", RequiresModel: true},
		}
	case strings.Contains(objective, "enable_cache"):
		p.Tasks = []planner.Task{
			{Key: "find-flag", Objective: "Find the ENABLE_CACHE declaration", Method: planner.MethodCommittedSearch, Inputs: map[string]string{"term": "ENABLE_CACHE"}, RequiredEvidence: "committed path, blob, and matching line", CompletionCriteria: "the declaration matches are recorded"},
			{Key: "read-context", Objective: "Capture exact source context around the flag", Method: planner.MethodSourceExcerpt, DependsOn: []string{"find-flag"}, Inputs: map[string]string{"source_task": "find-flag"}, RequiredEvidence: "path, line range, blob, and source excerpt", CompletionCriteria: "bounded source context is recorded"},
			{Key: "identify-component", Objective: "Plan identification of the component that uses the flag", Method: planner.MethodIncrementalPlanning, DependsOn: []string{"read-context"}, RequiredEvidence: "source evidence for a validated follow-up plan", CompletionCriteria: "component usage work is represented", RequiresModel: true},
		}
	case strings.Contains(objective, "direct dependencies"):
		p.Tasks = []planner.Task{
			{Key: "resolve-component", Objective: "Resolve component example.invalid/service", Method: planner.MethodComponentLookup, Inputs: map[string]string{"identity": "example.invalid/service"}, RequiredEvidence: "component identity, declaration, and path", CompletionCriteria: "the exact component is resolved"},
			{Key: "list-relationships", Objective: "List direct dependencies and relationships", Method: planner.MethodDependencyLookup, DependsOn: []string{"resolve-component"}, Inputs: map[string]string{"identity": "example.invalid/service", "relation": "dependencies"}, RequiredEvidence: "dependency relationships with path and line evidence", CompletionCriteria: "all direct relationships are recorded"},
		}
	case strings.Contains(objective, "huntmaster"):
		p.Tasks = []planner.Task{
			{Key: "find-huntmaster", Objective: "Locate Huntmaster geography zone eligibility declarations", Method: planner.MethodCommittedSearch, Inputs: map[string]string{"term": "Huntmaster"}, RequiredEvidence: "committed path, blob, and matching line", CompletionCriteria: "eligibility declaration candidates are recorded"},
			{Key: "trace-implementation", Objective: "Plan how to trace the implementation consumer", Method: planner.MethodIncrementalPlanning, DependsOn: []string{"find-huntmaster"}, RequiredEvidence: "eligibility evidence for a validated consumer plan", CompletionCriteria: "consumer tracing is represented without modifying files", RequiresModel: true},
		}
	case strings.Contains(objective, "deploy"):
		p.Objective = "Deployment is an unsupported capability"
		p.Tasks = []planner.Task{{Key: "unsupported-deploy", Objective: "Identify deployment as unsupported", Method: planner.MethodUnsupported, Inputs: map[string]string{"capability": "deploy"}, RequiredEvidence: "unsupported capability and reason", CompletionCriteria: "deployment is blocked explicitly", BlockedReason: "deployment is not a supported operation"}}
	default:
		p.Tasks = []planner.Task{{Key: "clarify-better", Objective: "Ask what better means and which desired behavior is intended", Method: planner.MethodHumanClarification, Inputs: map[string]string{"question": "What desired behavior should improve?"}, RequiredEvidence: "human clarification decision and answer", CompletionCriteria: "the requested improvement is unambiguous", BlockedReason: "the desired behavior is ambiguous"}}
	}
	return p
}

func TestModelQualifyArgumentValidation(t *testing.T) {
	load := func(string) (config.ProviderConfig, error) { return qualificationTestConfig(""), nil }
	factory := func(config.ProviderConfig, string) provider.Provider { return successfulQualificationProvider() }
	for _, args := range [][]string{{}, {"qualify"}, {"qualify", "--model", "m", "--capability", "coding", "--runs", "1", "--output", "x"}, {"qualify", "--model", "m", "--capability", "planning", "--runs", "0", "--output", "x"}} {
		if err := runModelCommand(args, &bytes.Buffer{}, load, factory); err == nil {
			t.Fatalf("args %#v unexpectedly succeeded", args)
		}
	}
}

func TestModelQualifyRequiresProviderConfiguration(t *testing.T) {
	err := runModelCommand([]string{"qualify", "--model", "worker", "--capability", "planning", "--runs", "1", "--output", filepath.Join(t.TempDir(), "report.json")}, &bytes.Buffer{}, func(string) (config.ProviderConfig, error) { return config.ProviderConfig{}, nil }, func(config.ProviderConfig, string) provider.Provider { return nil })
	var infrastructure *qualificationInfrastructureError
	if !errors.As(err, &infrastructure) {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestModelQualifySuccessWritesQualifiedReport(t *testing.T) {
	output := filepath.Join(t.TempDir(), "reports", "planning.json")
	var stdout bytes.Buffer
	err := runModelCommand([]string{"qualify", "--model", "worker", "--capability", "planning", "--runs", "1", "--output", output}, &stdout, func(model string) (config.ProviderConfig, error) {
		if model != "worker" {
			t.Fatalf("model override = %q", model)
		}
		return qualificationTestConfig("secret-not-reported"), nil
	}, func(config.ProviderConfig, string) provider.Provider { return successfulQualificationProvider() })
	if err != nil {
		t.Fatal(err)
	}
	report := readQualificationReport(t, output)
	if report.Recommendation != planner.RecommendationQualified || report.Aggregate.PassingRuns != report.Aggregate.ExpectedRuns {
		t.Fatalf("report = %#v", report)
	}
	if !strings.Contains(stdout.String(), "QUALIFIED") {
		t.Fatalf("summary = %q", stdout.String())
	}
	data, _ := os.ReadFile(output)
	if bytes.Contains(data, []byte("secret-not-reported")) || bytes.Contains(data, []byte("Authorization")) {
		t.Fatalf("report leaked secret: %s", data)
	}
}

func TestModelQualifyInvalidResponsesAreQualificationFailureWithZeroExit(t *testing.T) {
	output := filepath.Join(t.TempDir(), "report.json")
	invalid := qualificationProviderFunc(func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{Content: `{}`}, nil
	})
	err := runModelCommand([]string{"qualify", "--model", "worker", "--capability", "planning", "--runs", "1", "--output", output}, &bytes.Buffer{}, func(string) (config.ProviderConfig, error) { return qualificationTestConfig(""), nil }, func(config.ProviderConfig, string) provider.Provider { return invalid })
	if err != nil {
		t.Fatalf("model qualification failure returned infrastructure exit: %v", err)
	}
	report := readQualificationReport(t, output)
	if report.Recommendation != planner.RecommendationNotQualified || report.Aggregate.ModelFailures == 0 || report.Aggregate.InfrastructureFailures != 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestModelQualifyProviderFailureIsNonzeroAndRedacted(t *testing.T) {
	const secret = "api-secret-value"
	output := filepath.Join(t.TempDir(), "report.json")
	failing := qualificationProviderFunc(func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		return provider.ChatResponse{}, errors.New("provider echoed " + secret)
	})
	err := runModelCommand([]string{"qualify", "--model", "worker", "--capability", "planning", "--runs", "2", "--output", output}, &bytes.Buffer{}, func(string) (config.ProviderConfig, error) { return qualificationTestConfig(secret), nil }, func(config.ProviderConfig, string) provider.Provider { return failing })
	var infrastructure *qualificationInfrastructureError
	if !errors.As(err, &infrastructure) {
		t.Fatalf("error = %T %v", err, err)
	}
	data, readErr := os.ReadFile(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if bytes.Contains(data, []byte(secret)) || !bytes.Contains(data, []byte("[REDACTED]")) {
		t.Fatalf("redaction failed: %s", data)
	}
}

func TestModelQualifyProviderTimeoutIsInfrastructureFailure(t *testing.T) {
	output := filepath.Join(t.TempDir(), "report.json")
	blocking := qualificationProviderFunc(func(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		<-ctx.Done()
		return provider.ChatResponse{}, ctx.Err()
	})
	err := runModelCommand([]string{"qualify", "--model", "worker", "--capability", "planning", "--runs", "1", "--output", output, "--timeout", "20ms"}, &bytes.Buffer{}, func(string) (config.ProviderConfig, error) { return qualificationTestConfig(""), nil }, func(config.ProviderConfig, string) provider.Provider { return blocking })
	var infrastructure *qualificationInfrastructureError
	if !errors.As(err, &infrastructure) {
		t.Fatalf("error = %T %v", err, err)
	}
	report := readQualificationReport(t, output)
	if report.Recommendation != planner.RecommendationInconclusive || report.Aggregate.InfrastructureFailures != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func readQualificationReport(t *testing.T, path string) planner.QualificationReport {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report planner.QualificationReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	return report
}
