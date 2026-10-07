package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type RepositorySnapshot struct {
	ID             int64
	ProjectID      int64
	HeadCommit     string
	Branch         string
	Detached       bool
	Dirty          bool
	TrackedFiles   int
	UntrackedFiles int
	ObservedAt     time.Time
}

func (d *Database) CreateRepositorySnapshot(
	ctx context.Context,
	snapshot RepositorySnapshot,
) (RepositorySnapshot, error) {
	if snapshot.ProjectID <= 0 {
		return RepositorySnapshot{}, fmt.Errorf(
			"project ID must be positive",
		)
	}

	if snapshot.TrackedFiles < 0 {
		return RepositorySnapshot{}, fmt.Errorf(
			"tracked file count must not be negative",
		)
	}

	if snapshot.UntrackedFiles < 0 {
		return RepositorySnapshot{}, fmt.Errorf(
			"untracked file count must not be negative",
		)
	}

	observedAt := time.Now().UTC()

	var headCommit any
	if snapshot.HeadCommit != "" {
		headCommit = snapshot.HeadCommit
	}

	var branch any
	if snapshot.Branch != "" {
		branch = snapshot.Branch
	}

	result, err := d.db.ExecContext(ctx, `
INSERT INTO repository_snapshots (
	project_id,
	head_commit,
	branch,
	is_detached,
	is_dirty,
	tracked_files,
	untracked_files,
	observed_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`,
		snapshot.ProjectID,
		headCommit,
		branch,
		boolToInt(snapshot.Detached),
		boolToInt(snapshot.Dirty),
		snapshot.TrackedFiles,
		snapshot.UntrackedFiles,
		observedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return RepositorySnapshot{}, fmt.Errorf(
			"create repository snapshot: %w",
			err,
		)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return RepositorySnapshot{}, fmt.Errorf(
			"read repository snapshot id: %w",
			err,
		)
	}

	snapshot.ID = id
	snapshot.ObservedAt = observedAt

	return snapshot, nil
}

func (d *Database) ListRepositorySnapshots(
	ctx context.Context,
	projectID int64,
) ([]RepositorySnapshot, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
	id,
	project_id,
	head_commit,
	branch,
	is_detached,
	is_dirty,
	tracked_files,
	untracked_files,
	observed_at
FROM repository_snapshots
WHERE project_id = ?
ORDER BY id
`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"list repository snapshots: %w",
			err,
		)
	}
	defer rows.Close()

	var snapshots []RepositorySnapshot

	for rows.Next() {
		var (
			snapshot   RepositorySnapshot
			headCommit sql.NullString
			branch     sql.NullString
			detached   int
			dirty      int
			observedAt string
		)

		if err := rows.Scan(
			&snapshot.ID,
			&snapshot.ProjectID,
			&headCommit,
			&branch,
			&detached,
			&dirty,
			&snapshot.TrackedFiles,
			&snapshot.UntrackedFiles,
			&observedAt,
		); err != nil {
			return nil, fmt.Errorf(
				"scan repository snapshot: %w",
				err,
			)
		}

		if headCommit.Valid {
			snapshot.HeadCommit = headCommit.String
		}

		if branch.Valid {
			snapshot.Branch = branch.String
		}

		snapshot.Detached = detached != 0
		snapshot.Dirty = dirty != 0

		snapshot.ObservedAt, err = time.Parse(
			time.RFC3339Nano,
			observedAt,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"parse repository snapshot %d observed_at: %w",
				snapshot.ID,
				err,
			)
		}

		snapshots = append(snapshots, snapshot)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"list repository snapshots: %w",
			err,
		)
	}

	return snapshots, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}

	return 0
}
