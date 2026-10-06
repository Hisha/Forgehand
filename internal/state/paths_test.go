package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirUsesXDGStateHome(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", temp)

	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir() returned error: %v", err)
	}

	want := filepath.Join(temp, "forgehand")
	if got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestDatabasePathDoesNotCreateStateDirectory(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", temp)

	got, err := DatabasePath()
	if err != nil {
		t.Fatalf("DatabasePath() returned error: %v", err)
	}

	want := filepath.Join(temp, "forgehand", "forgehand.db")
	if got != want {
		t.Fatalf("DatabasePath() = %q, want %q", got, want)
	}

	if _, err := os.Stat(filepath.Join(temp, "forgehand")); !os.IsNotExist(err) {
		t.Fatalf("DatabasePath() unexpectedly created state directory")
	}
}
