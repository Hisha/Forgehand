package state

import (
	"context"
	"testing"
)

func TestCreateSessionCreatesReadySession(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	session, err := db.CreateSession(ctx, "Test session")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if session.ID == 0 {
		t.Fatal("session ID was not assigned")
	}

	if session.Title != "Test session" {
		t.Fatalf("Title = %q, want %q", session.Title, "Test session")
	}

	if session.State != SessionStateReady {
		t.Fatalf("State = %q, want %q", session.State, SessionStateReady)
	}

	if session.CreatedAt.IsZero() {
		t.Fatal("CreatedAt is zero")
	}

	if session.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero")
	}
}

func TestCreateSessionRejectsEmptyTitle(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	if _, err := db.CreateSession(ctx, "   "); err == nil {
		t.Fatal("CreateSession accepted an empty title")
	}
}

func TestListSessionsReturnsPersistedSessions(t *testing.T) {
	ctx := context.Background()
	db := openTestDatabase(t)

	first, err := db.CreateSession(ctx, "First session")
	if err != nil {
		t.Fatalf("create first session: %v", err)
	}

	second, err := db.CreateSession(ctx, "Second session")
	if err != nil {
		t.Fatalf("create second session: %v", err)
	}

	sessions, err := db.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}

	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}

	if sessions[0].ID != first.ID || sessions[0].Title != first.Title {
		t.Fatalf("first session = %#v, want %#v", sessions[0], first)
	}

	if sessions[1].ID != second.ID || sessions[1].Title != second.Title {
		t.Fatalf("second session = %#v, want %#v", sessions[1], second)
	}

	if sessions[0].State != SessionStateReady {
		t.Fatalf("first state = %q, want %q", sessions[0].State, SessionStateReady)
	}

	if sessions[1].State != SessionStateReady {
		t.Fatalf("second state = %q, want %q", sessions[1].State, SessionStateReady)
	}
}
