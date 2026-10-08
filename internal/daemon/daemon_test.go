package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSocketPathUsesXDGRuntimeDir(t *testing.T) {
	temp := shortSocketTestDir(t)
	t.Setenv("FORGEHAND_SOCKET_PATH", "")
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

func TestRootDaemonRefusalOccursBeforeFilesystemChanges(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	socketPath := filepath.Join(root, "runtime", "forgehand.sock")
	t.Setenv("FORGEHAND_STATE_DIR", stateDir)
	t.Setenv("FORGEHAND_SOCKET_PATH", socketPath)

	if err := runWithIdentity(0, 1000, 0); err == nil {
		t.Fatal("real UID 0 unexpectedly accepted")
	}
	if err := runWithIdentity(1000, 0, 0); err == nil {
		t.Fatal("effective UID 0 unexpectedly accepted")
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("root refusal touched state directory: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(socketPath)); !os.IsNotExist(err) {
		t.Fatalf("root refusal touched runtime directory: %v", err)
	}
}

func TestDaemonRejectsUnexpectedEffectiveCapabilities(t *testing.T) {
	if err := validateDaemonIdentity(1000, 1000, 1); err == nil {
		t.Fatal("effective capability unexpectedly accepted")
	}
}

func TestEnsureRuntimeDirModes(t *testing.T) {
	tests := []struct {
		name       string
		socketMode os.FileMode
		wantDir    os.FileMode
	}{
		{name: "development", socketMode: 0o600, wantDir: 0o700},
		{name: "group service", socketMode: 0o660, wantDir: 0o770},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "runtime")
			if err := ensureRuntimeDir(filepath.Join(dir, "forgehand.sock"), test.socketMode); err != nil {
				t.Fatalf("ensureRuntimeDir: %v", err)
			}
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatalf("stat runtime directory: %v", err)
			}
			if info.Mode().Perm() != test.wantDir {
				t.Fatalf("runtime mode = %#o, want %#o", info.Mode().Perm(), test.wantDir)
			}
		})
	}
}
