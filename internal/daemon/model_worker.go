package daemon

import (
	"context"
	"fmt"
	"os"

	"github.com/Hisha/Forgehand/internal/config"
	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/repocontext"
	"github.com/Hisha/Forgehand/internal/state"
)

// modelRuntime holds the dependencies a model-backed worker needs: a provider
// client and a bounded repository-context builder.
type modelRuntime struct {
	provider     provider.Provider
	providerID   string
	defaultModel string
	context      *repocontext.Builder
}

// newModelRuntime constructs a model runtime. It returns nil when no provider
// is configured, so the daemon runs without model support and coding
// executions fail closed instead of silently doing nothing.
func newModelRuntime(p provider.Provider, contextBuilder *repocontext.Builder) *modelRuntime {
	if p == nil {
		return nil
	}
	return &modelRuntime{
		provider:   p,
		providerID: provider.ProviderID,
		context:    contextBuilder,
	}
}

// buildModelRuntime constructs the daemon's model runtime from configuration.
// It returns nil when no provider base URL is configured.
func buildModelRuntime(providerConfig config.ProviderConfig, db *state.Database) *modelRuntime {
	if !providerConfig.Configured() {
		return nil
	}
	client := provider.NewClient(
		providerConfig.BaseURL,
		providerConfig.APIKey,
		providerConfig.Model,
		providerConfig.Timeout,
		providerConfig.MaxTokens,
	)
	builder := repocontext.NewBuilder(db)
	if providerConfig.MaxContextChars > 0 {
		builder = builder.WithLimits(0, 0, providerConfig.MaxContextChars)
	}
	runtime := newModelRuntime(client, builder)
	runtime.defaultModel = providerConfig.Model
	return runtime
}

// runModelWorker executes a single coding execution through the configured
// provider. It builds bounded repository context, calls the provider with the
// worker context (so cancellation and timeouts apply), and commits the result
// atomically only on success.
//
// On a provider error the execution is failed. On context cancellation the
// worker does nothing and relies on handleWorkerStop to reconcile a
// still-RUNNING execution; a canceled execution must never commit a result.
func (r *modelRuntime) runModelWorker(
	ctx context.Context,
	store *state.Database,
	execution state.SessionExecution,
) {
	session, err := store.GetSession(ctx, execution.SessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "forgehand: load session %d: %v\n", execution.SessionID, err)
		failExecutionWithReason(store, execution.ID, state.FailureKindWorker, "load coding session: "+err.Error())
		return
	}

	projectID := int64(0)
	if session.ProjectID != nil {
		projectID = *session.ProjectID
	}

	repoContext, err := r.context.Build(ctx, session.ID, projectID, session.Task)
	if err != nil {
		if ctx.Err() != nil {
			handleWorkerStop(store, execution, ctx)
			return
		}
		fmt.Fprintf(os.Stderr, "forgehand: build context for execution %d: %v\n", execution.ID, err)
		failExecutionWithReason(store, execution.ID, state.FailureKindRepositoryContext, "build repository context: "+err.Error())
		return
	}
	for _, warning := range repoContext.Warnings {
		fmt.Printf("Execution %d context warning: %s\n", execution.ID, warning)
	}

	response, err := r.provider.Complete(ctx, provider.ChatRequest{
		System: repoContext.System,
		User:   repoContext.User,
		Model:  session.Model,
	})
	if err != nil {
		if ctx.Err() != nil {
			handleWorkerStop(store, execution, ctx)
			return
		}
		fmt.Fprintf(os.Stderr, "forgehand: execution %d provider error: %v\n", execution.ID, err)
		failExecutionWithReason(store, execution.ID, state.FailureKindProvider, "provider request failed: "+err.Error())
		return
	}

	model := response.Model
	if model == "" {
		model = session.Model
	}

	if err := retryOnBusy(ctx, func() error {
		return store.CompleteModelExecution(
			context.Background(),
			execution.ID,
			r.providerID,
			model,
			response.Content,
		)
	}); err != nil {
		if ctx.Err() != nil {
			handleWorkerStop(store, execution, ctx)
			return
		}
		fmt.Fprintf(os.Stderr, "forgehand: complete model execution %d: %v\n", execution.ID, err)
		handleWorkerStopWithFailure(store, execution, ctx, state.FailureKindWorker, "persist model result: "+err.Error())
		return
	}

	fmt.Printf("Execution %d completed\n", execution.ID)
}
