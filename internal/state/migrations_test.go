package state

import (
	"context"
	"testing"
)

func TestFreshDatabaseMigratesToVersionOne(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	var version int
	if err := db.db.QueryRowContext(
		ctx,
		`SELECT version FROM schema_version LIMIT 1`,
	).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}

	if version != 1 {
		t.Fatalf("schema version = %d, want 1", version)
	}

	var tableName string
	if err := db.db.QueryRowContext(
		ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='daemon_runs'`,
	).Scan(&tableName); err != nil {
		t.Fatalf("daemon_runs table not found: %v", err)
	}

	if tableName != "daemon_runs" {
		t.Fatalf("table name = %q, want daemon_runs", tableName)
	}
}
