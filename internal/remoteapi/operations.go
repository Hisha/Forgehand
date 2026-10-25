package remoteapi

import (
	"context"
	"errors"

	"github.com/Hisha/Forgehand/internal/state"
)

// ErrSessionConflict reports that a session cannot be submitted because its
// persisted lifecycle state is not eligible for a new execution.
var ErrSessionConflict = errors.New("session state conflicts with the requested operation")

// ErrExecutionUnavailable reports that the daemon has no configured coding
// worker. It is intentionally generic so provider configuration is not
// disclosed through the network API.
var ErrExecutionUnavailable = errors.New("coding worker is unavailable")

// SessionOperations is the narrow daemon-owned capability exposed to the
// remote API. Implementations must use Forgehand's authoritative persistence
// and dispatcher; handlers never launch workers themselves.
type SessionOperations interface {
	CreateCodingSession(ctx context.Context, projectID int64, title, task string) (state.Session, error)
	QueueSessionExecution(ctx context.Context, sessionID int64) (state.SessionExecution, error)
	ListSessionExecutionRecords(ctx context.Context, sessionID int64, limit int) ([]state.SessionExecutionRecord, bool, error)
	GetSessionExecutionRecord(ctx context.Context, sessionID, executionID int64, contentOffset, contentLimit int) (state.SessionExecutionRecord, error)
}
