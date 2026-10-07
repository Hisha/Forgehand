package state

import (
	"context"
	"testing"
)

func TestCreateProject(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	project, err := db.CreateProject(
		ctx,
		"Forgehand",
		"/tmp/forgehand",
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	if project.ID <= 0 {
		t.Fatalf("project ID = %d, want positive", project.ID)
	}

	if project.Name != "Forgehand" {
		t.Fatalf("project name = %q, want %q", project.Name, "Forgehand")
	}

	if project.RootPath != "/tmp/forgehand" {
		t.Fatalf(
			"project root = %q, want %q",
			project.RootPath,
			"/tmp/forgehand",
		)
	}

	if project.CreatedAt.IsZero() {
		t.Fatal("project CreatedAt is zero")
	}

	if project.UpdatedAt.IsZero() {
		t.Fatal("project UpdatedAt is zero")
	}
}

func TestCreateProjectRejectsEmptyValues(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	if _, err := db.CreateProject(ctx, "", "/tmp/forgehand"); err == nil {
		t.Fatal("empty project name unexpectedly succeeded")
	}

	if _, err := db.CreateProject(ctx, "Forgehand", ""); err == nil {
		t.Fatal("empty project root unexpectedly succeeded")
	}
}

func TestCreateProjectRejectsDuplicateRoot(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	if _, err := db.CreateProject(
		ctx,
		"Forgehand",
		"/tmp/forgehand",
	); err != nil {
		t.Fatalf("create first project: %v", err)
	}

	if _, err := db.CreateProject(
		ctx,
		"Another Name",
		"/tmp/forgehand",
	); err == nil {
		t.Fatal("duplicate project root unexpectedly succeeded")
	}
}

func TestListProjects(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	first, err := db.CreateProject(
		ctx,
		"Forgehand",
		"/tmp/forgehand",
	)
	if err != nil {
		t.Fatalf("create first project: %v", err)
	}

	second, err := db.CreateProject(
		ctx,
		"Portalkeeper",
		"/tmp/portalkeeper",
	)
	if err != nil {
		t.Fatalf("create second project: %v", err)
	}

	projects, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}

	if len(projects) != 2 {
		t.Fatalf("project count = %d, want 2", len(projects))
	}

	if projects[0].ID != first.ID {
		t.Fatalf(
			"first project ID = %d, want %d",
			projects[0].ID,
			first.ID,
		)
	}

	if projects[1].ID != second.ID {
		t.Fatalf(
			"second project ID = %d, want %d",
			projects[1].ID,
			second.ID,
		)
	}

	if projects[0].Name != "Forgehand" {
		t.Fatalf(
			"first project name = %q, want %q",
			projects[0].Name,
			"Forgehand",
		)
	}

	if projects[1].RootPath != "/tmp/portalkeeper" {
		t.Fatalf(
			"second project root = %q, want %q",
			projects[1].RootPath,
			"/tmp/portalkeeper",
		)
	}
}

func TestGetProject(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	created, err := db.CreateProject(
		ctx,
		"Forgehand",
		"/tmp/forgehand",
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	project, err := db.GetProject(ctx, created.ID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}

	if project.ID != created.ID {
		t.Fatalf("project ID = %d, want %d", project.ID, created.ID)
	}

	if project.Name != "Forgehand" {
		t.Fatalf("project name = %q, want Forgehand", project.Name)
	}

	if project.RootPath != "/tmp/forgehand" {
		t.Fatalf(
			"project root = %q, want /tmp/forgehand",
			project.RootPath,
		)
	}
}

func TestGetProjectRejectsMissingProject(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	if _, err := db.GetProject(ctx, 999999); err == nil {
		t.Fatal("missing project unexpectedly found")
	}
}
