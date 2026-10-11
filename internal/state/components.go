package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/components"
)

// ComponentObservation is a persisted, commit-pinned component analysis of a
// project.
type ComponentObservation struct {
	ID            int64
	ProjectID     int64
	CommitHash    string
	HeadCommit    string
	IsDirty       bool
	EngineVersion int
	Summary       components.Summary
	ObservedAt    time.Time
	Components    []ComponentRecord
	Dependencies  []DependencyRecord
}

type ComponentRecord struct {
	Kind     string
	Identity string
	Path     string
	Language string
	Internal bool
	Evidence []components.SourceEvidence
}

type DependencyRecord struct {
	Kind         string
	Source       string
	Target       string
	TargetModule string
	Language     string
	Evidence     []components.SourceEvidence
}

// CreateComponentObservation persists an analysis and its details in a single
// transaction.
func (d *Database) CreateComponentObservation(
	ctx context.Context,
	projectID int64,
	commitHash string,
	headCommit string,
	isDirty bool,
	engineVersion int,
	result components.Result,
) (*ComponentObservation, error) {
	commitHash = strings.TrimSpace(commitHash)
	headCommit = strings.TrimSpace(headCommit)
	if projectID <= 0 {
		return nil, fmt.Errorf("project id must be positive")
	}
	if commitHash == "" {
		return nil, fmt.Errorf("commit hash must not be empty")
	}
	if headCommit == "" {
		return nil, fmt.Errorf("head commit must not be empty")
	}
	if engineVersion <= 0 {
		return nil, fmt.Errorf("engine version must be positive")
	}

	summaryJSON, err := json.Marshal(result.Summary)
	if err != nil {
		return nil, fmt.Errorf("marshal component summary: %w", err)
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin component observation transaction: %w", err)
	}

	dirty := 0
	if isDirty {
		dirty = 1
	}

	var observationID int64
	query := `
INSERT INTO component_observations (
	project_id,
	commit_hash,
	head_commit,
	is_dirty,
	engine_version,
	summary_json,
	observed_at
) VALUES (
	?, ?, ?, ?, ?, ?, ?
)
`
	execResult, err := tx.ExecContext(ctx, query,
		projectID,
		commitHash,
		headCommit,
		dirty,
		engineVersion,
		string(summaryJSON),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("insert component observation: %w", err)
	}

	observationID, err = execResult.LastInsertId()
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("get component observation id: %w", err)
	}

	if err := d.insertComponents(ctx, tx, observationID, result.Components); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := d.insertDependencies(ctx, tx, observationID, result.Dependencies); err != nil {
		_ = tx.Rollback()
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit component observation: %w", err)
	}

	return d.GetComponentObservation(ctx, observationID, true)
}
func (d *Database) insertComponents(
	ctx context.Context,
	tx *sql.Tx,
	observationID int64,
	components []components.Component,
) error {
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO components (
	observation_id,
	kind,
	identity,
	path,
	language,
	internal,
	evidence_json
) VALUES (?, ?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		return fmt.Errorf("prepare components insert: %w", err)
	}
	defer stmt.Close()

	for _, component := range components {
		internal := 0
		if component.Internal {
			internal = 1
		}
		evidenceJSON, err := json.Marshal(component.Evidence)
		if err != nil {
			return fmt.Errorf("marshal component evidence: %w", err)
		}
		if _, err := stmt.ExecContext(ctx,
			observationID,
			string(component.Kind),
			component.Identity,
			component.Path,
			component.Language,
			internal,
			string(evidenceJSON),
		); err != nil {
			return fmt.Errorf("insert component: %w", err)
		}
	}
	return nil
}

func (d *Database) insertDependencies(
	ctx context.Context,
	tx *sql.Tx,
	observationID int64,
	dependencies []components.Dependency,
) error {
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO dependencies (
	observation_id,
	kind,
	source,
	target,
	target_module,
	language,
	evidence_json
) VALUES (?, ?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		return fmt.Errorf("prepare dependencies insert: %w", err)
	}
	defer stmt.Close()

	for _, dependency := range dependencies {
		evidenceJSON, err := json.Marshal(dependency.Evidence)
		if err != nil {
			return fmt.Errorf("marshal dependency evidence: %w", err)
		}
		if _, err := stmt.ExecContext(ctx,
			observationID,
			string(dependency.Kind),
			dependency.Source,
			dependency.Target,
			dependency.TargetModule,
			dependency.Language,
			string(evidenceJSON),
		); err != nil {
			return fmt.Errorf("insert dependency: %w", err)
		}
	}
	return nil
}

func (d *Database) GetComponentObservation(
	ctx context.Context,
	id int64,
	includeDetails bool,
) (*ComponentObservation, error) {
	if id <= 0 {
		return nil, fmt.Errorf("observation id must be positive")
	}

	row := d.db.QueryRowContext(ctx, `
SELECT
	id,
	project_id,
	commit_hash,
	head_commit,
	is_dirty,
	engine_version,
	summary_json,
	observed_at
FROM component_observations
WHERE id = ?
`, id)

	var (
		record     ComponentObservation
		isDirty    int
		encoded    string
		observedAt string
	)

	if err := row.Scan(
		&record.ID,
		&record.ProjectID,
		&record.CommitHash,
		&record.HeadCommit,
		&isDirty,
		&record.EngineVersion,
		&encoded,
		&observedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load component observation %d: %w", id, err)
	}

	if err := json.Unmarshal([]byte(encoded), &record.Summary); err != nil {
		return nil, fmt.Errorf("decode component observation %d summary: %w", id, err)
	}
	record.IsDirty = isDirty != 0
	parsed, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return nil, fmt.Errorf("parse component observation %d observed_at: %w", id, err)
	}
	record.ObservedAt = parsed

	if includeDetails {
		components, err := d.listComponentsForObservation(ctx, record.ID)
		if err != nil {
			return nil, err
		}
		record.Components = components
		deps, err := d.listDependenciesForObservation(ctx, record.ID)
		if err != nil {
			return nil, err
		}
		record.Dependencies = deps
	}

	return &record, nil
}

func (d *Database) LatestComponentObservation(
	ctx context.Context,
	projectID int64,
	includeDetails bool,
) (*ComponentObservation, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("project id must be positive")
	}

	row := d.db.QueryRowContext(ctx, `
SELECT id
FROM component_observations
WHERE project_id = ?
ORDER BY id DESC
LIMIT 1
`, projectID)

	var id int64
	if err := row.Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load latest component observation: %w", err)
	}

	return d.GetComponentObservation(ctx, id, includeDetails)
}

func (d *Database) listComponentsForObservation(
	ctx context.Context,
	observationID int64,
) ([]ComponentRecord, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
	kind,
	identity,
	path,
	language,
	internal,
	evidence_json
FROM components
WHERE observation_id = ?
ORDER BY path, kind, identity
`, observationID)
	if err != nil {
		return nil, fmt.Errorf("list components for observation %d: %w", observationID, err)
	}
	defer rows.Close()

	var result []ComponentRecord
	for rows.Next() {
		var (
			record    ComponentRecord
			internal  int
			encodedEv string
		)
		if err := rows.Scan(
			&record.Kind,
			&record.Identity,
			&record.Path,
			&record.Language,
			&internal,
			&encodedEv,
		); err != nil {
			return nil, fmt.Errorf("scan component: %w", err)
		}
		if err := json.Unmarshal([]byte(encodedEv), &record.Evidence); err != nil {
			return nil, fmt.Errorf("decode component evidence: %w", err)
		}
		record.Internal = internal != 0
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list components: %w", err)
	}
	return result, nil
}

func (d *Database) listDependenciesForObservation(
	ctx context.Context,
	observationID int64,
) ([]DependencyRecord, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
	kind,
	source,
	target,
	target_module,
	language,
	evidence_json
FROM dependencies
WHERE observation_id = ?
ORDER BY kind, source, target, target_module
`, observationID)
	if err != nil {
		return nil, fmt.Errorf("list dependencies for observation %d: %w", observationID, err)
	}
	defer rows.Close()

	var result []DependencyRecord
	for rows.Next() {
		var (
			record    DependencyRecord
			encodedEv string
		)
		if err := rows.Scan(
			&record.Kind,
			&record.Source,
			&record.Target,
			&record.TargetModule,
			&record.Language,
			&encodedEv,
		); err != nil {
			return nil, fmt.Errorf("scan dependency: %w", err)
		}
		if err := json.Unmarshal([]byte(encodedEv), &record.Evidence); err != nil {
			return nil, fmt.Errorf("decode dependency evidence: %w", err)
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list dependencies: %w", err)
	}
	return result, nil
}
