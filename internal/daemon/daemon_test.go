package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSocketPathUsesXDGRuntimeDir(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", temp)

	got, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath() returned error: %v", err)
	}

	want := filepath.Join(temp, "forgehand", "forgehand.sock")
	if got != want {
		t.Fatalf("SocketPath() = %q, want %q", got, want)
	}

	if _, err := os.Stat(filepath.Join(temp, "forgehand")); !os.IsNotExist(err) {
		t.Fatalf("SocketPath() unexpectedly created runtime directory")
	}
}
