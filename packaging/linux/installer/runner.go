package main

import (
	"bytes"
	"context"
	"os/exec"
)

// commandRunner abstracts command execution so tests can inject a fake.
type commandRunner interface {
	Run(ctx context.Context, env []string, name string, args ...string) (stdout, stderr string, err error)
}

type realRunner struct{}

func (realRunner) Run(ctx context.Context, env []string, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}
