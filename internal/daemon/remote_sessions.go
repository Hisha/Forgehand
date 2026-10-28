package daemon

import (
	"context"
	"fmt"

	"github.com/Hisha/Forgehand/internal/provider"
	"github.com/Hisha/Forgehand/internal/remoteapi"
	"github.com/Hisha/Forgehand/internal/state"
)

// remoteSessionOperations adapts the authenticated HTTP API to the daemon's
// authoritative session persistence and dispatcher. It contains no worker or
// scheduling implementation of its own.
type remoteSessionOperations struct {
	db             *state.Database
	dispatcher     *dispatcher
	statusProvider remoteapi.DaemonStatusOperations
}

func (o *remoteSessionOperations) DaemonStatus(ctx context.Context) (remoteapi.DaemonStatusResponse, error) {
	if o == nil || o.statusProvider == nil {
		return remoteapi.DaemonStatusResponse{}, remoteapi.ErrExecutionUnavailable
	}
	return o.statusProvider.DaemonStatus(ctx)
}

func (o *remoteSessionOperations) CreateCodingSession(
	ctx context.Context,
	projectID int64,
	title, task string,
) (state.Session, error) {
	if o == nil || o.db == nil || o.dispatcher == nil || o.dispatcher.model == nil {
		return state.Session{}, remoteapi.ErrExecutionUnavailable
	}
	return o.db.CreateCodingSession(
		ctx,
		title,
		projectID,
		task,
		provider.ProviderID,
		o.dispatcher.model.defaultModel,
	)
}

func (o *remoteSessionOperations) QueueSessionExecution(
	ctx context.Context,
	sessionID int64,
) (state.SessionExecution, error) {
	if o == nil || o.db == nil || o.dispatcher == nil {
		return state.SessionExecution{}, remoteapi.ErrExecutionUnavailable
	}
	execution, err := queueSessionForExecution(ctx, o.db, o.dispatcher, sessionID)
	if err == nil {
		return execution, nil
	}

	// QueueSessionExecution performs the state check and transition in one
	// transaction. A session that is no longer READY is a stable HTTP conflict,
	// including the loser of concurrent duplicate submissions.
	session, readErr := o.db.GetSession(ctx, sessionID)
	if readErr == nil && session.State != state.SessionStateReady {
		return state.SessionExecution{}, fmt.Errorf("%w: session is %s", remoteapi.ErrSessionConflict, session.State)
	}
	return state.SessionExecution{}, err
}

func (o *remoteSessionOperations) ListSessionExecutionRecords(
	ctx context.Context,
	sessionID int64,
	limit int,
) ([]state.SessionExecutionRecord, bool, error) {
	if o == nil || o.db == nil {
		return nil, false, remoteapi.ErrExecutionUnavailable
	}
	return o.db.ListSessionExecutionRecords(ctx, sessionID, limit)
}

func (o *remoteSessionOperations) GetSessionExecutionRecord(
	ctx context.Context,
	sessionID, executionID int64,
	contentOffset, contentLimit int,
) (state.SessionExecutionRecord, error) {
	if o == nil || o.db == nil {
		return state.SessionExecutionRecord{}, remoteapi.ErrExecutionUnavailable
	}
	return o.db.GetSessionExecutionRecord(ctx, sessionID, executionID, contentOffset, contentLimit)
}

func queueSessionForExecution(
	ctx context.Context,
	db *state.Database,
	dispatcher *dispatcher,
	sessionID int64,
	resources ...string,
) (state.SessionExecution, error) {
	permit, err := acquireExecutionAdmission(dispatcher)
	if err != nil {
		return state.SessionExecution{}, err
	}
	defer permit.release()

	totalSteps, err := executionStepCount(ctx, db, sessionID)
	if err != nil {
		return state.SessionExecution{}, err
	}
	execution, err := db.QueueSessionExecution(ctx, sessionID, totalSteps, resources...)
	if err != nil {
		return state.SessionExecution{}, err
	}
	if dispatcher != nil {
		dispatcher.notify()
	}
	return execution, nil
}

func executionStepCount(ctx context.Context, db *state.Database, sessionID int64) (int, error) {
	totalSteps := fakeWorkerSteps
	session, err := db.GetSession(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	switch session.Kind {
	case "investigation":
		return 1, nil
	case "work-plan":
		plan, err := db.GetWorkPlanBySession(ctx, sessionID)
		if err != nil {
			return 0, err
		}
		if plan == nil {
			return 0, fmt.Errorf("session %d has no work plan", sessionID)
		}
		return len(plan.Tasks), nil
	default:
		return totalSteps, nil
	}
}
