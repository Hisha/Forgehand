package state

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/discovery"
)

type DiscoveryObservation struct {
	ID         int64
	ProjectID  int64
	CommitHash string
	Summary    discovery.Summary
	ObservedAt time.Time
}

func (d *Database) CreateDiscoveryObservation(
	ctx context.Context,
	projectID int64,
	summary discovery.Summary,
) (DiscoveryObservation, error) {
	if projectID <= 0 {
		return DiscoveryObservation{}, fmt.Errorf("project ID must be positive")
	}
	summary.CommitHash = strings.TrimSpace(summary.CommitHash)
	if summary.CommitHash == "" {
		return DiscoveryObservation{}, fmt.Errorf("discovery commit hash must not be empty")
	}
	if summary.TrackedFiles < 0 || summary.UnclassifiedFiles < 0 {
		return DiscoveryObservation{}, fmt.Errorf("discovery file counts must not be negative")
	}

	encoded, err := json.Marshal(summary)
	if err != nil {
		return DiscoveryObservation{}, fmt.Errorf("encode discovery summary: %w", err)
	}
	observedAt := time.Now().UTC()
	result, err := d.db.ExecContext(ctx, `
INSERT INTO discovery_observations (
	project_id,
	commit_hash,
	summary_json,
	observed_at
)
VALUES (?, ?, ?, ?)
`,
		projectID,
		summary.CommitHash,
		string(encoded),
		observedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return DiscoveryObservation{}, fmt.Errorf("create discovery observation: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return DiscoveryObservation{}, fmt.Errorf("read discovery observation id: %w", err)
	}

	return DiscoveryObservation{
		ID:         id,
		ProjectID:  projectID,
		CommitHash: summary.CommitHash,
		Summary:    summary,
		ObservedAt: observedAt,
	}, nil
}

func (d *Database) ListDiscoveryObservations(
	ctx context.Context,
	projectID int64,
) ([]DiscoveryObservation, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("project ID must be positive")
	}

	rows, err := d.db.QueryContext(ctx, `
SELECT
	id,
	project_id,
	commit_hash,
	summary_json,
	observed_at
FROM discovery_observations
WHERE project_id = ?
ORDER BY id
`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list discovery observations: %w", err)
	}
	defer rows.Close()

	var observations []DiscoveryObservation
	for rows.Next() {
		var observation DiscoveryObservation
		var encoded string
		var observedAt string
		if err := rows.Scan(
			&observation.ID,
			&observation.ProjectID,
			&observation.CommitHash,
			&encoded,
			&observedAt,
		); err != nil {
			return nil, fmt.Errorf("scan discovery observation: %w", err)
		}
		if err := json.Unmarshal([]byte(encoded), &observation.Summary); err != nil {
			return nil, fmt.Errorf("decode discovery observation %d: %w", observation.ID, err)
		}
		if observation.Summary.CommitHash != observation.CommitHash {
			return nil, fmt.Errorf("discovery observation %d commit hash does not match summary", observation.ID)
		}
		observation.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return nil, fmt.Errorf("parse discovery observation %d observed_at: %w", observation.ID, err)
		}
		observations = append(observations, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list discovery observations: %w", err)
	}
	return observations, nil
}
