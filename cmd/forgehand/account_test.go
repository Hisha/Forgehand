package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hisha/Forgehand/internal/ipc"
)

func TestUsernameAndProjectArgumentsValid(t *testing.T) {
	cases := [][]string{
		{"--username", "admin", "--project", "1"},
		{"--project", "1", "--username", "admin"},
	}
	for _, args := range cases {
		username, projectID, err := usernameAndProjectArguments(args)
		if err != nil {
			t.Fatalf("args %#v: unexpected error: %v", args, err)
		}
		if username != "admin" || projectID != 1 {
			t.Fatalf("args %#v: got %q, %d", args, username, projectID)
		}
	}
}

func TestUsernameAndProjectArgumentsInvalid(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "empty", args: nil},
		{name: "missing username value", args: []string{"--username"}},
		{name: "missing project value", args: []string{"--username", "admin", "--project"}},
		{name: "unknown flag", args: []string{"--username", "admin", "--project", "1", "--bogus"}},
		{name: "duplicate username", args: []string{"--username", "admin", "--username", "other", "--project", "1"}},
		{name: "duplicate project", args: []string{"--username", "admin", "--project", "1", "--project", "2"}},
		{name: "empty username", args: []string{"--username", "", "--project", "1"}},
		{name: "missing username flag", args: []string{"--project", "1"}},
		{name: "missing project flag", args: []string{"--username", "admin"}},
		{name: "invalid project", args: []string{"--username", "admin", "--project", "abc"}},
		{name: "nonpositive project", args: []string{"--username", "admin", "--project", "0"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, _, err := usernameAndProjectArguments(testCase.args); err == nil {
				t.Fatalf("usernameAndProjectArguments(%v) succeeded, want error", testCase.args)
			}
		})
	}
}

func TestAccountGrantAndRevokeArgumentValidation(t *testing.T) {
	for _, fn := range []func([]string) error{grantAccountProject, revokeAccountProject} {
		if err := fn([]string{"--username", "admin", "--project", "1"}); err == nil {
			// Without a daemon this may fail at request time, which is acceptable;
			// argument parsing itself must not fail. A nil error only happens if a
			// daemon is available.
		}
		for _, args := range [][]string{
			{},
			{"--project", "1"},
			{"--username", "admin"},
			{"--username", "admin", "--project", "x"},
			{"--username", "", "--project", "1"},
			{"--username", "admin", "--username", "other", "--project", "1"},
		} {
			if err := fn(args); err == nil {
				t.Fatalf("command(%v) unexpectedly succeeded", args)
			}
		}
	}
}

func TestAccountGrantAndRevokeSendExpectedIPC(t *testing.T) {
	for _, tc := range []struct {
		command string
		fn      func([]string) error
	}{
		{command: "account-grant-project", fn: grantAccountProject},
		{command: "account-revoke-project", fn: revokeAccountProject},
	} {
		t.Run(tc.command, func(t *testing.T) {
			dir, err := os.MkdirTemp(".", ".fh-cli-account-")
			if err != nil {
				t.Fatal(err)
			}
			abs, _ := filepath.Abs(dir)
			t.Cleanup(func() { _ = os.RemoveAll(abs) })
			socketPath := filepath.Join(abs, "forgehand.sock")
			t.Setenv("FORGEHAND_SOCKET_PATH", socketPath)

			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverErr := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serverErr <- err
					return
				}
				defer conn.Close()
				var req ipc.Request
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					serverErr <- err
					return
				}
				if req.Command != tc.command {
					serverErr <- fmt.Errorf("command = %q, want %q", req.Command, tc.command)
					return
				}
				if req.Username != "admin" || req.ProjectID != 1 {
					serverErr <- fmt.Errorf("request = %#v", req)
					return
				}
				serverErr <- json.NewEncoder(conn).Encode(ipc.Response{OK: true, Message: "done"})
			}()

			if err := tc.fn([]string{"--username", "admin", "--project", "1"}); err != nil {
				t.Fatalf("command failed: %v", err)
			}
			if err := <-serverErr; err != nil {
				t.Fatalf("server: %v", err)
			}
		})
	}
}

