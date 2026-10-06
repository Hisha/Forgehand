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
