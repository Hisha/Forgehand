package state

import (
	"context"
	"testing"
)

func TestFreshDatabaseMigratesToLatestVersion(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	var version int
	if err := db.db.QueryRowContext(
		ctx,
		`SELECT version FROM schema_version LIMIT 1`,
	).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}

	if version != 6 {
		t.Fatalf("schema version = %d, want 6", version)
	}

	for _, want := range []string{
		"daemon_runs",
		"sessions",
		"session_executions",
		"projects",
		"repository_snapshots",
		"discovery_observations",
	} {
		var tableName string

		if err := db.db.QueryRowContext(
			ctx,
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
			want,
		).Scan(&tableName); err != nil {
			t.Fatalf("%s table not found: %v", want, err)
		}

		if tableName != want {
			t.Fatalf("table name = %q, want %q", tableName, want)
		}
	}
}

func TestProjectsRootPathIsUnique(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	_, err := db.db.ExecContext(ctx, `
INSERT INTO projects (
	name,
	root_path,
	created_at,
	updated_at
)
VALUES (?, ?, ?, ?)
`,
		"Forgehand",
		"/tmp/forgehand",
		"2026-10-06T00:00:00Z",
		"2026-10-06T00:00:00Z",
	)
	if err != nil {
		t.Fatalf("insert first project: %v", err)
	}

	_, err = db.db.ExecContext(ctx, `
INSERT INTO projects (
	name,
	root_path,
	created_at,
	updated_at
)
VALUES (?, ?, ?, ?)
`,
		"Duplicate Forgehand",
		"/tmp/forgehand",
		"2026-10-06T00:00:00Z",
		"2026-10-06T00:00:00Z",
	)

	if err == nil {
		t.Fatal("duplicate project root unexpectedly succeeded")
	}
}
