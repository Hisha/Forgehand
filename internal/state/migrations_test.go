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

	if version != 3 {
		t.Fatalf("schema version = %d, want 3", version)
	}

	for _, want := range []string{"daemon_runs", "sessions", "session_executions"} {
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
