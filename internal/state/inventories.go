package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/inventory"
)

// ProjectInventory is a persisted, commit-pinned inventory of a project.
type ProjectInventory struct {
	ID            int64
	ProjectID     int64
	CommitHash    string
	HeadCommit    string
	IsDirty       bool
	EngineVersion int
	TrackedFiles  int
	Truncated     bool
	Summary       inventory.Summary
	ObservedAt    time.Time
	Files         []InventoryFile
}

type InventoryFile struct {
	Path     string
	BlobHash string
	Mode     string
	Size     int64
	Language string
	Category string
}

// CreateProjectInventory persists an inventory and its file evidence in a
// single transaction. The inventory is bound to commitHash; headCommit records
// the repository HEAD observed at inventory time.
func (d *Database) CreateProjectInventory(
	ctx context.Context,
	projectID int64,
	commitHash string,
	headCommit string,
	isDirty bool,
	engineVersion int,
	result inventory.Result,
) (ProjectInventory, error) {
	if projectID <= 0 {
		return ProjectInventory{}, fmt.Errorf("project ID must be positive")
	}
	commitHash = strings.TrimSpace(commitHash)
	if commitHash == "" {
		return ProjectInventory{}, fmt.Errorf("inventory commit hash must not be empty")
	}
	headCommit = strings.TrimSpace(headCommit)
	if headCommit == "" {
		return ProjectInventory{}, fmt.Errorf("inventory head commit must not be empty")
	}
	if engineVersion <= 0 {
		return ProjectInventory{}, fmt.Errorf("inventory engine version must be positive")
	}
	if result.Summary.TrackedFiles < 0 || result.Summary.UnclassifiedFiles < 0 {
		return ProjectInventory{}, fmt.Errorf("inventory file counts must not be negative")
	}

	encoded, err := json.Marshal(result.Summary)
	if err != nil {
		return ProjectInventory{}, fmt.Errorf("encode inventory summary: %w", err)
	}

	observedAt := time.Now().UTC()

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return ProjectInventory{}, fmt.Errorf("begin inventory transaction: %w", err)
	}
	defer tx.Rollback()

	insert, err := tx.ExecContext(ctx, `
INSERT INTO project_inventories (
	project_id,
	commit_hash,
	head_commit,
	is_dirty,
	engine_version,
	tracked_files,
	truncated,
	summary_json,
	observed_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		projectID,
		commitHash,
		headCommit,
		boolToInt(isDirty),
		engineVersion,
		result.Summary.TrackedFiles,
		boolToInt(result.Summary.Truncated),
		string(encoded),
		observedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return ProjectInventory{}, fmt.Errorf("create project inventory: %w", err)
	}

	inventoryID, err := insert.LastInsertId()
	if err != nil {
		return ProjectInventory{}, fmt.Errorf("read project inventory id: %w", err)
	}

	statement, err := tx.PrepareContext(ctx, `
INSERT INTO inventory_files (
	inventory_id,
	path,
	blob_hash,
	mode,
	size,
	language,
	category
)
VALUES (?, ?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		return ProjectInventory{}, fmt.Errorf("prepare inventory file insert: %w", err)
	}
	defer statement.Close()

	for _, file := range result.Files {
		if _, err := statement.ExecContext(
			ctx,
			inventoryID,
			file.Path,
			file.BlobHash,
			file.Mode,
			file.Size,
			file.Language,
			string(file.Category),
		); err != nil {
			return ProjectInventory{}, fmt.Errorf("insert inventory file %q: %w", file.Path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return ProjectInventory{}, fmt.Errorf("commit inventory transaction: %w", err)
	}

	return ProjectInventory{
		ID:            inventoryID,
		ProjectID:     projectID,
		CommitHash:    commitHash,
		HeadCommit:    headCommit,
		IsDirty:       isDirty,
		EngineVersion: engineVersion,
		TrackedFiles:  result.Summary.TrackedFiles,
		Truncated:     result.Summary.Truncated,
		Summary:       result.Summary,
		ObservedAt:    observedAt,
		Files:         nil,
	}, nil
}

// GetProjectInventory loads a single inventory by ID. When includeFiles is
// true, the file evidence rows are loaded in deterministic path order.
func (d *Database) GetProjectInventory(
	ctx context.Context,
	id int64,
	includeFiles bool,
) (*ProjectInventory, error) {
	if id <= 0 {
		return nil, fmt.Errorf("inventory ID must be positive")
	}

	row := d.db.QueryRowContext(ctx, `
SELECT
	id,
	project_id,
	commit_hash,
	head_commit,
	is_dirty,
	engine_version,
	tracked_files,
	truncated,
	summary_json,
	observed_at
FROM project_inventories
WHERE id = ?
`, id)

	record, err := scanProjectInventory(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if includeFiles {
		files, err := d.listInventoryFiles(ctx, record.ID)
		if err != nil {
			return nil, err
		}
		record.Files = files
	}

	return record, nil
}

// LatestProjectInventory loads the most recently created inventory for a
// project, or nil when the project has never been inventoried.
func (d *Database) LatestProjectInventory(
	ctx context.Context,
	projectID int64,
	includeFiles bool,
) (*ProjectInventory, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("project ID must be positive")
	}

	row := d.db.QueryRowContext(ctx, `
SELECT
	id,
	project_id,
	commit_hash,
	head_commit,
	is_dirty,
	engine_version,
	tracked_files,
	truncated,
	summary_json,
	observed_at
FROM project_inventories
WHERE project_id = ?
ORDER BY id DESC
LIMIT 1
`, projectID)

	record, err := scanProjectInventory(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if includeFiles {
		files, err := d.listInventoryFiles(ctx, record.ID)
		if err != nil {
			return nil, err
		}
		record.Files = files
	}

	return record, nil
}

// LatestProjectInventoryForCommit loads the newest persisted inventory pinned
// to a specific commit. It supports component investigations whose selected
// analysis may no longer describe the project's latest inventory.
func (d *Database) LatestProjectInventoryForCommit(ctx context.Context, projectID int64, commit string, includeFiles bool) (*ProjectInventory, error) {
	if projectID <= 0 || strings.TrimSpace(commit) == "" {
		return nil, fmt.Errorf("project ID and commit are required")
	}
	row := d.db.QueryRowContext(ctx, `
SELECT id, project_id, commit_hash, head_commit, is_dirty, engine_version,
       tracked_files, truncated, summary_json, observed_at
FROM project_inventories
WHERE project_id = ? AND commit_hash = ?
ORDER BY id DESC LIMIT 1
`, projectID, commit)
	record, err := scanProjectInventory(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if includeFiles {
		record.Files, err = d.listInventoryFiles(ctx, record.ID)
		if err != nil {
			return nil, err
		}
	}
	return record, nil
}

type inventoryScanner interface {
	Scan(dest ...any) error
}

func scanProjectInventory(row inventoryScanner) (*ProjectInventory, error) {
	var (
		record     ProjectInventory
		isDirty    int
		truncated  int
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
		&record.TrackedFiles,
		&truncated,
		&encoded,
		&observedAt,
	); err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(encoded), &record.Summary); err != nil {
		return nil, fmt.Errorf("decode inventory %d summary: %w", record.ID, err)
	}
	if record.Summary.TrackedFiles != record.TrackedFiles {
		return nil, fmt.Errorf("inventory %d tracked file count does not match summary", record.ID)
	}
	if record.Summary.Truncated != (truncated != 0) {
		return nil, fmt.Errorf("inventory %d truncation flag does not match summary", record.ID)
	}
	record.IsDirty = isDirty != 0
	record.Truncated = truncated != 0

	parsed, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return nil, fmt.Errorf("parse inventory %d observed_at: %w", record.ID, err)
	}
	record.ObservedAt = parsed

	return &record, nil
}

func (d *Database) listInventoryFiles(
	ctx context.Context,
	inventoryID int64,
) ([]InventoryFile, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
	path,
	blob_hash,
	mode,
	size,
	language,
	category
FROM inventory_files
WHERE inventory_id = ?
ORDER BY path
`, inventoryID)
	if err != nil {
		return nil, fmt.Errorf("list inventory %d files: %w", inventoryID, err)
	}
	defer rows.Close()

	var files []InventoryFile
	for rows.Next() {
		var file InventoryFile
		if err := rows.Scan(
			&file.Path,
			&file.BlobHash,
			&file.Mode,
			&file.Size,
			&file.Language,
			&file.Category,
		); err != nil {
			return nil, fmt.Errorf("scan inventory file: %w", err)
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list inventory files: %w", err)
	}

	return files, nil
}
