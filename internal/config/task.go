package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// TaskMaxIterationsEnvironment bounds how many provider round-trips a
	// controlled coding task may make before it fails closed.
	TaskMaxIterationsEnvironment = "FORGEHAND_TASK_MAX_ITERATIONS"
	// TaskMaxFileBytesEnvironment bounds a single file read or written by a
	// task.
	TaskMaxFileBytesEnvironment = "FORGEHAND_TASK_MAX_FILE_BYTES"
	// TaskMaxTotalReadBytesEnvironment bounds the total bytes a task may
	// inspect across its run.
	TaskMaxTotalReadBytesEnvironment = "FORGEHAND_TASK_MAX_TOTAL_READ_BYTES"
	// TaskMaxChangesEnvironment bounds how many files a task proposal may
	// change.
	TaskMaxChangesEnvironment = "FORGEHAND_TASK_MAX_CHANGES"
	// TaskMaxSearchResultsEnvironment bounds the number of search hits returned
	// per search operation.
	TaskMaxSearchResultsEnvironment = "FORGEHAND_TASK_MAX_SEARCH_RESULTS"
	// TaskWorkspacesDirEnvironment overrides the directory under which isolated
	// task worktrees are created. When unset, <state dir>/workspaces is used.
	TaskWorkspacesDirEnvironment = "FORGEHAND_TASK_WORKSPACES_DIR"
	// TaskValidateEnvironment enables the post-apply Go format-and-test
	// validation stage. It is disabled by default because running repository
	// tests executes untrusted code; it must be explicitly enabled for
	// repositories the operator trusts.
	TaskValidateEnvironment = "FORGEHAND_TASK_VALIDATE"
	// TaskValidateTimeoutEnvironment bounds how long the validation stage may
	// run before it is canceled.
	TaskValidateTimeoutEnvironment = "FORGEHAND_TASK_VALIDATE_TIMEOUT"
)

// TaskConfig is the resolved configuration for controlled coding tasks. Its
// limits carry the task package defaults when a value is unset, and it fails
// closed when an explicitly configured value is invalid.
type TaskConfig struct {
	MaxIterations     int
	MaxFileBytes      int64
	MaxTotalReadBytes int64
	MaxChanges        int
	MaxSearchResults  int
	// WorkspacesDir is the absolute directory under which task worktrees are
	// created. It is always set (defaulting to <stateDir>/workspaces).
	WorkspacesDir string
	// Validate enables the post-apply Go format-and-test stage. It is disabled
	// by default: running project tests executes repository content, which is
	// untrusted, so it must be turned on explicitly for trusted repositories.
	Validate bool
	// ValidateTimeout bounds the validation stage. Zero means the default.
	ValidateTimeout time.Duration
}

// Task resolves the task configuration from the environment. The stateDir is
// used to derive the default workspaces directory.
func Task(stateDir string) (TaskConfig, error) {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		return TaskConfig{}, fmt.Errorf("state directory is required for task configuration")
	}

	config := TaskConfig{}

	limits := []struct {
		environment string
		target      *int
	}{
		{TaskMaxIterationsEnvironment, &config.MaxIterations},
		{TaskMaxSearchResultsEnvironment, &config.MaxSearchResults},
		{TaskMaxChangesEnvironment, &config.MaxChanges},
	}
	for _, limit := range limits {
		value, err := optionalPositiveInt(limit.environment)
		if err != nil {
			return TaskConfig{}, err
		}
		*limit.target = value
	}

	byteLimits := []struct {
		environment string
		target      *int64
	}{
		{TaskMaxFileBytesEnvironment, &config.MaxFileBytes},
		{TaskMaxTotalReadBytesEnvironment, &config.MaxTotalReadBytes},
	}
	for _, limit := range byteLimits {
		value, err := optionalPositiveInt64(limit.environment)
		if err != nil {
			return TaskConfig{}, err
		}
		*limit.target = value
	}

	if configured := strings.TrimSpace(os.Getenv(TaskWorkspacesDirEnvironment)); configured != "" {
		dir, err := validateDirectoryPath("task workspaces directory", configured)
		if err != nil {
			return TaskConfig{}, err
		}
		config.WorkspacesDir = dir
	} else {
		config.WorkspacesDir = filepath.Join(stateDir, "workspaces")
	}

	if configured := strings.TrimSpace(os.Getenv(TaskValidateEnvironment)); configured != "" {
		enabled, err := strconv.ParseBool(configured)
		if err != nil {
			return TaskConfig{}, fmt.Errorf("parse %s as a boolean: %w", TaskValidateEnvironment, err)
		}
		config.Validate = enabled
	}

	if configured := strings.TrimSpace(os.Getenv(TaskValidateTimeoutEnvironment)); configured != "" {
		timeout, err := time.ParseDuration(configured)
		if err != nil {
			return TaskConfig{}, fmt.Errorf("parse %s as a duration: %w", TaskValidateTimeoutEnvironment, err)
		}
		if timeout <= 0 {
			return TaskConfig{}, fmt.Errorf("%s must be positive", TaskValidateTimeoutEnvironment)
		}
		config.ValidateTimeout = timeout
	}

	return config, nil
}

func optionalPositiveInt(environment string) (int, error) {
	configured := strings.TrimSpace(os.Getenv(environment))
	if configured == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(configured)
	if err != nil {
		return 0, fmt.Errorf("parse %s as an integer: %w", environment, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", environment)
	}
	return value, nil
}

func optionalPositiveInt64(environment string) (int64, error) {
	configured := strings.TrimSpace(os.Getenv(environment))
	if configured == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(configured, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s as an integer: %w", environment, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", environment)
	}
	return value, nil
}
