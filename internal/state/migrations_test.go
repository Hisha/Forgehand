package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Hisha/Forgehand/internal/components"
	"github.com/Hisha/Forgehand/internal/discovery"
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

	if version != 8 {
		t.Fatalf("schema version = %d, want 8", version)
	}

	for _, want := range []string{
		"daemon_runs",
		"sessions",
		"session_executions",
		"projects",
		"repository_snapshots",
		"discovery_observations",
		"project_inventories",
		"inventory_files",
		"component_observations",
		"components",
		"dependencies",
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

// TestSchemaV6DataPreservedWhenMigratingToV7 constructs a schema-v6 database,
// populates it with existing records, and verifies that opening it applies the
// v7 migration without altering the pre-existing rows.
func TestSchemaV6DataPreservedWhenMigratingToV7(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "forgehand.db")

	raw, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}

	if _, err := raw.Exec(`CREATE TABLE schema_version (version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_version(version) VALUES (0)`); err != nil {
		t.Fatalf("initialize schema version: %v", err)
	}
	for _, migration := range migrations {
		if migration.version > 6 {
			break
		}
		if _, err := raw.Exec(migration.sql); err != nil {
			t.Fatalf("apply migration %d: %v", migration.version, err)
		}
		if _, err := raw.Exec(`UPDATE schema_version SET version = ?`, migration.version); err != nil {
			t.Fatalf("record migration %d: %v", migration.version, err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := raw.Exec(`
INSERT INTO projects (name, root_path, created_at, updated_at)
VALUES (?, ?, ?, ?)
`, "Legacy Project", "/tmp/legacy", now, now); err != nil {
		t.Fatalf("insert legacy project: %v", err)
	}

	summaryJSON, err := json.Marshal(discovery.Summary{
		CommitHash:   "legacy-commit",
		TrackedFiles: 3,
	})
	if err != nil {
		t.Fatalf("encode legacy summary: %v", err)
	}
	if _, err := raw.Exec(`
INSERT INTO discovery_observations (project_id, commit_hash, summary_json, observed_at)
VALUES (?, ?, ?, ?)
`, 1, "legacy-commit", string(summaryJSON), now); err != nil {
		t.Fatalf("insert legacy observation: %v", err)
	}

	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	db, err := OpenAt(ctx, dir)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.db.QueryRowContext(
		ctx,
		`SELECT version FROM schema_version LIMIT 1`,
	).Scan(&version); err != nil {
		t.Fatalf("read migrated schema version: %v", err)
	}
	if version != 8 {
		t.Fatalf("migrated schema version = %d, want 8", version)
	}

	var (
		name     string
		rootPath string
	)
	if err := db.db.QueryRowContext(ctx, `
SELECT name, root_path FROM projects WHERE id = 1
`).Scan(&name, &rootPath); err != nil {
		t.Fatalf("read preserved project: %v", err)
	}
	if name != "Legacy Project" || rootPath != "/tmp/legacy" {
		t.Fatalf("preserved project = %q %q", name, rootPath)
	}

	var (
		commitHash string
		encoded    string
	)
	if err := db.db.QueryRowContext(ctx, `
SELECT commit_hash, summary_json FROM discovery_observations WHERE id = 1
`).Scan(&commitHash, &encoded); err != nil {
		t.Fatalf("read preserved observation: %v", err)
	}
	var preserved discovery.Summary
	if err := json.Unmarshal([]byte(encoded), &preserved); err != nil {
		t.Fatalf("decode preserved observation: %v", err)
	}
	if commitHash != "legacy-commit" || preserved.CommitHash != "legacy-commit" {
		t.Fatalf("preserved observation = %q %#v", commitHash, preserved)
	}

	if _, err := db.db.Exec(`
INSERT INTO project_inventories (
	project_id, commit_hash, head_commit, is_dirty, engine_version,
	tracked_files, truncated, summary_json, observed_at
)
VALUES (1, 'legacy-commit', 'legacy-commit', 0, 1, 0, 0, '{}', ?)
`, now); err != nil {
		t.Fatalf("insert into migrated project_inventories: %v", err)
	}
	if _, err := db.db.Exec(`
INSERT INTO inventory_files (inventory_id, path, blob_hash, mode, size, language, category)
VALUES (1, 'main.go', 'hash', '100644', 1, 'Go', 'source')
`); err != nil {
		t.Fatalf("insert into migrated inventory_files: %v", err)
	}
}

// TestSchemaV7DataPreservedWhenMigratingToV8 constructs a schema-v7 database,
// populates it with existing projects, inventories, and inventory-file evidence,
// and verifies that opening it applies the v8 migration without altering the
// pre-existing rows.
func TestSchemaV7DataPreservedWhenMigratingToV8(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "forgehand.db")

	raw, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}

	if _, err := raw.Exec(`CREATE TABLE schema_version (version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO schema_version(version) VALUES (0)`); err != nil {
		t.Fatalf("initialize schema version: %v", err)
	}
	for _, migration := range migrations {
		if migration.version > 7 {
			break
		}
		if _, err := raw.Exec(migration.sql); err != nil {
			t.Fatalf("apply migration %d: %v", migration.version, err)
		}
		if _, err := raw.Exec(`UPDATE schema_version SET version = ?`, migration.version); err != nil {
			t.Fatalf("record migration %d: %v", migration.version, err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := raw.Exec(`
INSERT INTO projects (name, root_path, created_at, updated_at)
VALUES (?, ?, ?, ?)
`, "Legacy Inventory Project", "/tmp/legacy-inventory", now, now); err != nil {
		t.Fatalf("insert legacy project: %v", err)
	}

	if _, err := raw.Exec(`
INSERT INTO project_inventories (
	project_id, commit_hash, head_commit, is_dirty, engine_version,
	tracked_files, truncated, summary_json, observed_at
)
VALUES (1, 'legacy-commit', 'legacy-commit', 1, 1, 1, 0,
	'{"tracked_files":1,"unclassified_files":0,"truncated":false}', ?)
`, now); err != nil {
		t.Fatalf("insert legacy inventory: %v", err)
	}
	if _, err := raw.Exec(`
INSERT INTO inventory_files (inventory_id, path, blob_hash, mode, size, language, category)
VALUES (1, 'main.go', 'legacy-blob', '100644', 17, 'Go', 'source')
`); err != nil {
		t.Fatalf("insert legacy inventory file: %v", err)
	}

	if err := raw.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	db, err := OpenAt(ctx, dir)
	if err != nil {
		t.Fatalf("open migrated database: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.db.QueryRowContext(
		ctx,
		`SELECT version FROM schema_version LIMIT 1`,
	).Scan(&version); err != nil {
		t.Fatalf("read migrated schema version: %v", err)
	}
	if version != 8 {
		t.Fatalf("migrated schema version = %d, want 8", version)
	}

	inventory, err := db.GetProjectInventory(ctx, 1, true)
	if err != nil {
		t.Fatalf("read preserved inventory: %v", err)
	}
	if inventory == nil {
		t.Fatal("preserved inventory missing after migration")
	}
	if inventory.CommitHash != "legacy-commit" || inventory.HeadCommit != "legacy-commit" {
		t.Fatalf("preserved inventory commits = %q/%q", inventory.CommitHash, inventory.HeadCommit)
	}
	if !inventory.IsDirty || inventory.TrackedFiles != 1 {
		t.Fatalf("preserved inventory flags = dirty=%v tracked=%d", inventory.IsDirty, inventory.TrackedFiles)
	}
	if len(inventory.Files) != 1 || inventory.Files[0].Path != "main.go" || inventory.Files[0].BlobHash != "legacy-blob" {
		t.Fatalf("preserved inventory files = %#v", inventory.Files)
	}

	if _, err := db.CreateComponentObservation(
		ctx,
		1,
		"legacy-commit",
		"legacy-commit",
		false,
		1,
		components.Result{},
	); err != nil {
		t.Fatalf("insert into migrated component_observations: %v", err)
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
