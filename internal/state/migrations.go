package state

import (
	"context"
	"database/sql"
	"fmt"
)

type migration struct {
	version int
	sql     string
}

var migrations = []migration{
	{
		version: 1,
		sql: `
CREATE TABLE daemon_runs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	started_at TEXT NOT NULL,
	stopped_at TEXT,
	pid INTEGER NOT NULL,
	version TEXT NOT NULL,
	shutdown_clean INTEGER NOT NULL DEFAULT 0
);
`,
	},
	{
		version: 2,
		sql: `
CREATE TABLE sessions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	title TEXT NOT NULL,
	state TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE INDEX idx_sessions_state
ON sessions(state);
`,
	},
	{
		version: 3,
		sql: `
CREATE TABLE session_executions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id INTEGER NOT NULL,
	status TEXT NOT NULL,
	current_step INTEGER NOT NULL DEFAULT 0,
	total_steps INTEGER NOT NULL,
	started_at TEXT NOT NULL,
	completed_at TEXT,
	FOREIGN KEY(session_id) REFERENCES sessions(id)
);

CREATE INDEX idx_session_executions_session_id
ON session_executions(session_id);
`,
	},
	{
		version: 4,
		sql: `
CREATE TABLE projects (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	root_path TEXT NOT NULL UNIQUE,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
`,
	},
	{
		version: 5,
		sql: `
CREATE TABLE repository_snapshots (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id INTEGER NOT NULL,
	head_commit TEXT,
	branch TEXT,
	is_detached INTEGER NOT NULL,
	is_dirty INTEGER NOT NULL,
	tracked_files INTEGER NOT NULL,
	untracked_files INTEGER NOT NULL,
	observed_at TEXT NOT NULL,
	FOREIGN KEY(project_id) REFERENCES projects(id)
);

CREATE INDEX idx_repository_snapshots_project_id
ON repository_snapshots(project_id);
`,
	},
	{
		version: 6,
		sql: `
CREATE TABLE discovery_observations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id INTEGER NOT NULL,
	commit_hash TEXT NOT NULL,
	summary_json TEXT NOT NULL,
	observed_at TEXT NOT NULL,
	FOREIGN KEY(project_id) REFERENCES projects(id)
);

CREATE INDEX idx_discovery_observations_project_id
ON discovery_observations(project_id);
`,
	},
	{
		version: 7,
		sql: `
CREATE TABLE project_inventories (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id INTEGER NOT NULL,
	commit_hash TEXT NOT NULL,
	head_commit TEXT NOT NULL,
	is_dirty INTEGER NOT NULL DEFAULT 0,
	engine_version INTEGER NOT NULL,
	tracked_files INTEGER NOT NULL,
	truncated INTEGER NOT NULL DEFAULT 0,
	summary_json TEXT NOT NULL,
	observed_at TEXT NOT NULL,
	FOREIGN KEY(project_id) REFERENCES projects(id)
);

CREATE INDEX idx_project_inventories_project_id
ON project_inventories(project_id);

CREATE TABLE inventory_files (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	inventory_id INTEGER NOT NULL,
	path TEXT NOT NULL,
	blob_hash TEXT NOT NULL,
	mode TEXT NOT NULL,
	size INTEGER NOT NULL,
	language TEXT NOT NULL DEFAULT '',
	category TEXT NOT NULL,
	FOREIGN KEY(inventory_id) REFERENCES project_inventories(id)
);

CREATE INDEX idx_inventory_files_inventory_id
ON inventory_files(inventory_id);
`,
	},
	{
		version: 8,
		sql: `
CREATE TABLE component_observations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id INTEGER NOT NULL,
	commit_hash TEXT NOT NULL,
	head_commit TEXT NOT NULL,
	is_dirty INTEGER NOT NULL DEFAULT 0,
	engine_version INTEGER NOT NULL,
	summary_json TEXT NOT NULL,
	observed_at TEXT NOT NULL,
	FOREIGN KEY(project_id) REFERENCES projects(id)
);

CREATE INDEX idx_component_observations_project_id
ON component_observations(project_id);

CREATE TABLE components (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	observation_id INTEGER NOT NULL,
	kind TEXT NOT NULL,
	identity TEXT NOT NULL,
	path TEXT NOT NULL,
	language TEXT NOT NULL DEFAULT '',
	internal INTEGER NOT NULL DEFAULT 0,
	evidence_json TEXT NOT NULL,
	FOREIGN KEY(observation_id) REFERENCES component_observations(id),
	UNIQUE(observation_id, kind, identity)
);

CREATE INDEX idx_components_observation
ON components(observation_id);

CREATE TABLE dependencies (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	observation_id INTEGER NOT NULL,
	kind TEXT NOT NULL,
	source TEXT NOT NULL,
	target TEXT NOT NULL,
	target_module TEXT NOT NULL DEFAULT '',
	language TEXT NOT NULL DEFAULT 'Go',
	evidence_json TEXT NOT NULL,
	FOREIGN KEY(observation_id) REFERENCES component_observations(id),
	UNIQUE(observation_id, kind, source, target)
);

CREATE INDEX idx_dependencies_observation
ON dependencies(observation_id);
`,
	},
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER NOT NULL
);
`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	var current int

	err := db.QueryRowContext(ctx,
		`SELECT version FROM schema_version LIMIT 1`,
	).Scan(&current)

	switch {
	case err == sql.ErrNoRows:
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_version(version) VALUES (0)`,
		); err != nil {
			return fmt.Errorf("initialize schema version: %w", err)
		}

	case err != nil:
		return fmt.Errorf("read schema version: %w", err)
	}

	for _, migration := range migrations {
		if migration.version <= current {
			continue
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", migration.version, err)
		}

		if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", migration.version, err)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE schema_version SET version = ?`,
			migration.version,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", migration.version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", migration.version, err)
		}

		current = migration.version
	}

	return nil
}
