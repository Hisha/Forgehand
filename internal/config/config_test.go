package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitConfigurationOverridesDevelopmentDefaults(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	socketPath := filepath.Join(shortConfigTestDir(t), "runtime", "daemon.sock")
	t.Setenv(StateDirEnvironment, stateDir)
	t.Setenv(SocketPathEnvironment, socketPath)
	t.Setenv(SocketModeEnvironment, "0660")
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "xdg-state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(shortConfigTestDir(t), "ignored-runtime"))

	gotState, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	gotSocket, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	gotMode, err := SocketMode()
	if err != nil {
		t.Fatalf("SocketMode: %v", err)
	}
	if gotState != stateDir || gotSocket != socketPath || gotMode != 0o660 {
		t.Fatalf("configuration = %q %q %#o", gotState, gotSocket, gotMode)
	}
}

func TestDevelopmentConfigurationUsesXDGDirectories(t *testing.T) {
	stateHome := t.TempDir()
	runtimeDir := shortConfigTestDir(t)
	t.Setenv(StateDirEnvironment, "")
	t.Setenv(SocketPathEnvironment, "")
	t.Setenv(SocketModeEnvironment, "")
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	stateDir, err := StateDir()
	if err != nil {
		t.Fatalf("StateDir: %v", err)
	}
	socketPath, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	mode, err := SocketMode()
	if err != nil {
		t.Fatalf("SocketMode: %v", err)
	}
	if stateDir != filepath.Join(stateHome, "forgehand") {
		t.Fatalf("state directory = %q", stateDir)
	}
	if socketPath != filepath.Join(runtimeDir, "forgehand", "forgehand.sock") {
		t.Fatalf("socket path = %q", socketPath)
	}
	if mode != 0o600 {
		t.Fatalf("development socket mode = %#o, want 0600", mode)
	}
}

func shortConfigTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".fh-config-")
	if err != nil {
		t.Fatalf("create short config test directory: %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolve short config test directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(abs) })
	return abs
}

func TestConfigurationRejectsUnsafeValues(t *testing.T) {
	t.Run("relative state", func(t *testing.T) {
		t.Setenv(StateDirEnvironment, "relative/state")
		if _, err := StateDir(); err == nil {
			t.Fatal("relative state directory unexpectedly accepted")
		}
	})
	t.Run("root state", func(t *testing.T) {
		t.Setenv(StateDirEnvironment, "/")
		if _, err := StateDir(); err == nil {
			t.Fatal("root state directory unexpectedly accepted")
		}
	})
	t.Run("relative socket", func(t *testing.T) {
		t.Setenv(SocketPathEnvironment, "relative/socket")
		if _, err := SocketPath(); err == nil {
			t.Fatal("relative socket path unexpectedly accepted")
		}
	})
	t.Run("root socket directory", func(t *testing.T) {
		t.Setenv(SocketPathEnvironment, "/forgehand.sock")
		if _, err := SocketPath(); err == nil {
			t.Fatal("socket in filesystem root unexpectedly accepted")
		}
	})
	t.Run("unsafe socket mode", func(t *testing.T) {
		t.Setenv(SocketModeEnvironment, "0666")
		if _, err := SocketMode(); err == nil {
			t.Fatal("world-accessible socket mode unexpectedly accepted")
		}
	})
	t.Run("socket path too long", func(t *testing.T) {
		t.Setenv(SocketPathEnvironment, "/tmp/"+strings.Repeat("x", 110))
		if _, err := SocketPath(); err == nil {
			t.Fatal("overlong socket path unexpectedly accepted")
		}
	})
}
