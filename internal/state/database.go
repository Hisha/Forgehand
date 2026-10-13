package state

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Concurrent fake workers advance independent executions against one shared
// SQLite database. SQLite permits only a single writer at a time, so the
// configuration below makes writers queue instead of failing:
//
//   - journal_mode=WAL lets readers proceed while a writer holds the write
//     lock, so progress reads never block checkpoint writes.
//   - busy_timeout makes a connection wait for a held lock (rather than
//     returning SQLITE_BUSY immediately). The worker retry loop in the daemon
//     is a bounded safety net beyond this.
//   - txlock=immediate takes the write lock at BEGIN, so a transaction that
//     reads-then-writes cannot fail while upgrading a deferred lock.
//   - MaxOpenConns bounds the number of pooled connections (and therefore
//     concurrent lock waiters) against an unbounded goroutine fan-out.
const (
	dbBusyTimeoutMilliseconds = 5000
	dbMaxOpenConnections      = 8
	dbMaxIdleConnections      = 8
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

	dsn := fmt.Sprintf(
		"%s?_busy_timeout=%d&_foreign_keys=on&_journal_mode=WAL&_txlock=immediate",
		path,
		dbBusyTimeoutMilliseconds,
	)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}

	db.SetMaxOpenConns(dbMaxOpenConnections)
	db.SetMaxIdleConns(dbMaxIdleConnections)

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
