package state

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Database struct {
	db *sql.DB
}

type DaemonRun struct {
	ID            int64
	StartedAt     time.Time
	StoppedAt     *time.Time
	PID           int
	Version       string
	ShutdownClean bool
}

func Open(ctx context.Context) (*Database, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return OpenAt(ctx, dir)
}

func OpenAt(ctx context.Context, dir string) (*Database, error) {
	if err := EnsureDirAt(dir); err != nil {
		return nil, err
	}

	path := filepath.Join(filepath.Clean(dir), "forgehand.db")

	dsn := path + "?_pragma=foreign_keys(1)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping state database: %w", err)
	}

	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	return &Database{db: db}, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}

func (d *Database) StartDaemonRun(
	ctx context.Context,
	pid int,
	version string,
) (int64, error) {
	result, err := d.db.ExecContext(ctx, `
INSERT INTO daemon_runs (
	started_at,
	pid,
	version,
	shutdown_clean
)
VALUES (?, ?, ?, 0)
`,
		time.Now().UTC().Format(time.RFC3339Nano),
		pid,
		version,
	)
	if err != nil {
		return 0, fmt.Errorf("record daemon start: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get daemon run id: %w", err)
	}

	return id, nil
}

func (d *Database) FinishDaemonRun(
	ctx context.Context,
	id int64,
) error {
	_, err := d.db.ExecContext(ctx, `
UPDATE daemon_runs
SET stopped_at = ?,
    shutdown_clean = 1
WHERE id = ?
`,
		time.Now().UTC().Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return fmt.Errorf("record daemon shutdown: %w", err)
	}

	return nil
}

func (d *Database) LastDaemonRun(
	ctx context.Context,
) (*DaemonRun, error) {
	row := d.db.QueryRowContext(ctx, `
SELECT
	id,
	started_at,
	stopped_at,
	pid,
	version,
	shutdown_clean
FROM daemon_runs
ORDER BY id DESC
LIMIT 1
`)

	var run DaemonRun
	var startedAt string
	var stoppedAt sql.NullString
	var clean int

	err := row.Scan(
		&run.ID,
		&startedAt,
		&stoppedAt,
		&run.PID,
		&run.Version,
		&clean,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read last daemon run: %w", err)
	}

	run.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return nil, fmt.Errorf("parse daemon start time: %w", err)
	}

	if stoppedAt.Valid {
		t, err := time.Parse(time.RFC3339Nano, stoppedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse daemon stop time: %w", err)
		}
		run.StoppedAt = &t
	}

	run.ShutdownClean = clean != 0

	return &run, nil
}
