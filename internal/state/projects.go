package state

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Project struct {
	ID        int64
	Name      string
	RootPath  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (d *Database) CreateProject(
	ctx context.Context,
	name string,
	rootPath string,
) (Project, error) {
	name = strings.TrimSpace(name)
	rootPath = strings.TrimSpace(rootPath)

	if name == "" {
		return Project{}, fmt.Errorf("project name must not be empty")
	}

	if rootPath == "" {
		return Project{}, fmt.Errorf("project root path must not be empty")
	}

	now := time.Now().UTC()

	result, err := d.db.ExecContext(ctx, `
INSERT INTO projects (
	name,
	root_path,
	created_at,
	updated_at
)
VALUES (?, ?, ?, ?)
`,
		name,
		rootPath,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return Project{}, fmt.Errorf("read project id: %w", err)
	}

	return Project{
		ID:        id,
		Name:      name,
		RootPath:  rootPath,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (d *Database) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT
	id,
	name,
	root_path,
	created_at,
	updated_at
FROM projects
ORDER BY id
`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var projects []Project

	for rows.Next() {
		var (
			project   Project
			createdAt string
			updatedAt string
		)

		if err := rows.Scan(
			&project.ID,
			&project.Name,
			&project.RootPath,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}

		project.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf(
				"parse project %d created_at: %w",
				project.ID,
				err,
			)
		}

		project.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, fmt.Errorf(
				"parse project %d updated_at: %w",
				project.ID,
				err,
			)
		}

		projects = append(projects, project)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	return projects, nil
}

func (d *Database) GetProject(
	ctx context.Context,
	id int64,
) (Project, error) {
	if id <= 0 {
		return Project{}, fmt.Errorf("project ID must be positive")
	}

	var (
		project   Project
		createdAt string
		updatedAt string
	)

	err := d.db.QueryRowContext(ctx, `
SELECT
	id,
	name,
	root_path,
	created_at,
	updated_at
FROM projects
WHERE id = ?
`,
		id,
	).Scan(
		&project.ID,
		&project.Name,
		&project.RootPath,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return Project{}, fmt.Errorf("get project %d: %w", id, err)
	}

	project.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Project{}, fmt.Errorf(
			"parse project %d created_at: %w",
			project.ID,
			err,
		)
	}

	project.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Project{}, fmt.Errorf(
			"parse project %d updated_at: %w",
			project.ID,
			err,
		)
	}

	return project, nil
}
