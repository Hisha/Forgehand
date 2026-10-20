package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/Hisha/Forgehand/internal/planner"
	"github.com/Hisha/Forgehand/internal/query"
	"github.com/Hisha/Forgehand/internal/repository"
	"github.com/Hisha/Forgehand/internal/state"
)

const (
	maxPlanInventoryResults = 2000
	maxPlanSearchResults    = 200
	maxPlanSearchFileBytes  = 1024 * 1024
	excerptContextLines     = 2
)

func runWorkPlanWorker(ctx context.Context, db *state.Database, execution state.SessionExecution) {
	session, err := db.GetSession(ctx, execution.SessionID)
	if err != nil {
		failExecutionWithReason(db, execution.ID, state.FailureKindWorker, err.Error())
		return
	}
	plan, err := db.GetWorkPlanBySession(ctx, session.ID)
	if err != nil || plan == nil {
		if err == nil {
			err = errors.New("work plan is missing")
		}
		failExecutionWithReason(db, execution.ID, state.FailureKindWorker, err.Error())
		return
	}
	if err := db.StartWorkPlan(ctx, plan.ID); err != nil {
		failExecutionWithReason(db, execution.ID, state.FailureKindWorker, err.Error())
		return
	}

	for position := 1; position <= len(plan.Tasks); position++ {
		if ctx.Err() != nil {
			handleWorkerStop(db, execution, ctx)
			return
		}
		if err := db.RefreshWorkPlanReadiness(ctx, plan.ID); err != nil {
			failExecutionWithReason(db, execution.ID, state.FailureKindWorker, err.Error())
			return
		}
		plan, err = db.GetWorkPlanBySession(ctx, session.ID)
		if err != nil {
			failExecutionWithReason(db, execution.ID, state.FailureKindWorker, err.Error())
			return
		}
		task := plan.Tasks[position-1]
		if task.Status == state.WorkTaskStatusReady {
			if err := db.StartWorkPlanTask(ctx, task.ID); err != nil {
				failExecutionWithReason(db, execution.ID, state.FailureKindWorker, err.Error())
				return
			}
			result, evidence, blocked, execErr := executeWorkPlanTask(ctx, db, *plan, task)
			status, reason := state.WorkTaskStatusCompleted, ""
			if blocked != "" {
				status, reason = state.WorkTaskStatusBlocked, blocked
			} else if execErr != nil {
				status, reason = state.WorkTaskStatusFailed, execErr.Error()
			}
			if err := db.FinishWorkPlanTask(ctx, task.ID, status, result, reason, evidence); err != nil {
				failExecutionWithReason(db, execution.ID, state.FailureKindWorker, err.Error())
				return
			}
		}
		if err := db.AdvanceSessionExecution(ctx, execution.ID, position); err != nil {
			handleWorkerStopWithFailure(db, execution, ctx, state.FailureKindWorker, err.Error())
			return
		}
	}
	if err := db.RefreshWorkPlanReadiness(ctx, plan.ID); err != nil {
		failExecutionWithReason(db, execution.ID, state.FailureKindWorker, err.Error())
		return
	}
	if err := db.FinishWorkPlan(ctx, plan.ID); err != nil {
		failExecutionWithReason(db, execution.ID, state.FailureKindWorker, err.Error())
		return
	}
	if err := db.CompleteSessionExecution(ctx, execution.ID); err != nil {
		handleWorkerStopWithFailure(db, execution, ctx, state.FailureKindWorker, err.Error())
	}
}

func executeWorkPlanTask(ctx context.Context, db *state.Database, plan state.WorkPlan, task state.WorkPlanTask) (string, []state.WorkEvidence, string, error) {
	switch task.ExecutionMethod {
	case planner.MethodInventoryLookup:
		return executeInventoryLookup(ctx, db, plan, task)
	case planner.MethodCommittedSearch:
		return executeCommittedSearch(ctx, db, plan, task)
	case planner.MethodSourceExcerpt:
		return executeSourceExcerpts(ctx, db, plan, task)
	case planner.MethodComponentLookup:
		return executeComponentLookup(ctx, db, plan, task)
	case planner.MethodDependencyLookup:
		return executeDependencyLookup(ctx, db, plan, task)
	case planner.MethodSemanticPlanning:
		return "", nil, task.BlockedReason, nil
	default:
		return "", nil, "", fmt.Errorf("unsupported execution method %q", task.ExecutionMethod)
	}
}

func currentPlanInventory(ctx context.Context, db *state.Database, plan state.WorkPlan) (*state.ProjectInventory, state.Project, error) {
	project, err := db.GetProject(ctx, plan.ProjectID)
	if err != nil {
		return nil, project, err
	}
	commit := plan.CommitHash
	if commit == "" {
		commit, _, err = repository.HeadCommit(ctx, project.RootPath)
		if err != nil {
			return nil, project, err
		}
	}
	inv, err := db.LatestProjectInventoryForCommit(ctx, project.ID, commit, true)
	if err != nil {
		return nil, project, err
	}
	if inv == nil {
		built, err := inventoryRegisteredProject(ctx, db, project.ID)
		if err != nil {
			return nil, project, err
		}
		if built.Inventory.CommitHash != commit {
			return nil, project, fmt.Errorf("repository HEAD changed while planning (from %s to %s)", commit, built.Inventory.CommitHash)
		}
		inv, err = db.GetProjectInventory(ctx, built.Inventory.ID, true)
		if err != nil {
			return nil, project, err
		}
	}
	if plan.CommitHash == "" {
		if err := db.SetWorkPlanCommit(ctx, plan.ID, commit); err != nil {
			return nil, project, err
		}
	}
	return inv, project, nil
}

func executeInventoryLookup(ctx context.Context, db *state.Database, plan state.WorkPlan, task state.WorkPlanTask) (string, []state.WorkEvidence, string, error) {
	inv, _, err := currentPlanInventory(ctx, db, plan)
	if err != nil {
		return "", nil, "repository inventory unavailable: " + err.Error(), nil
	}
	prefix := cleanPrefix(task.Inputs["path_prefix"])
	var evidence []state.WorkEvidence
	for _, file := range inv.Files {
		if prefix != "" && file.Path != prefix && !strings.HasPrefix(file.Path, prefix+"/") {
			continue
		}
		evidence = append(evidence, state.WorkEvidence{Kind: "inventory-file", Path: file.Path, BlobHash: file.BlobHash, Detail: file.Category})
		if len(evidence) >= maxPlanInventoryResults {
			return fmt.Sprintf("Captured the first %d committed file(s) at commit %s; result limit reached", len(evidence), inv.CommitHash), evidence, "", nil
		}
	}
	return fmt.Sprintf("Found %d committed file(s) at commit %s", len(evidence), inv.CommitHash), evidence, "", nil
}

func executeCommittedSearch(ctx context.Context, db *state.Database, plan state.WorkPlan, task state.WorkPlanTask) (string, []state.WorkEvidence, string, error) {
	inv, project, err := currentPlanInventory(ctx, db, plan)
	if err != nil {
		return "", nil, "repository inventory unavailable: " + err.Error(), nil
	}
	term := task.Inputs["term"]
	if term == "" {
		return "", nil, "", errors.New("search term is empty")
	}
	prefix := cleanPrefix(task.Inputs["path_prefix"])
	var evidence []state.WorkEvidence
	for _, file := range inv.Files {
		if ctx.Err() != nil {
			return "", nil, "", ctx.Err()
		}
		if prefix != "" && file.Path != prefix && !strings.HasPrefix(file.Path, prefix+"/") {
			continue
		}
		if file.Size > maxPlanSearchFileBytes {
			continue
		}
		data, err := repository.ReadBlob(ctx, project.RootPath, file.BlobHash)
		if err != nil {
			return "", nil, "", fmt.Errorf("read committed file %s: %w", file.Path, err)
		}
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, term) {
				evidence = append(evidence, state.WorkEvidence{Kind: "text-match", Path: file.Path, BlobHash: file.BlobHash, Line: i + 1, EndLine: i + 1, Excerpt: line})
				if len(evidence) >= maxPlanSearchResults {
					return fmt.Sprintf("Captured the first %d literal match(es); result limit reached", len(evidence)), evidence, "", nil
				}
			}
		}
	}
	return fmt.Sprintf("Found %d literal match(es) for %q", len(evidence), term), evidence, "", nil
}

func executeSourceExcerpts(ctx context.Context, db *state.Database, plan state.WorkPlan, task state.WorkPlanTask) (string, []state.WorkEvidence, string, error) {
	loaded, err := db.GetWorkPlanBySession(ctx, plan.SessionID)
	if err != nil {
		return "", nil, "", err
	}
	var source *state.WorkPlanTask
	for i := range loaded.Tasks {
		if loaded.Tasks[i].Key == task.Inputs["source_task"] {
			source = &loaded.Tasks[i]
			break
		}
	}
	if source == nil {
		return "", nil, "missing source search task", nil
	}
	project, err := db.GetProject(ctx, plan.ProjectID)
	if err != nil {
		return "", nil, "", err
	}
	byBlob := map[string][]string{}
	var evidence []state.WorkEvidence
	for _, match := range source.Evidence {
		lines, ok := byBlob[match.BlobHash]
		if !ok {
			data, err := repository.ReadBlob(ctx, project.RootPath, match.BlobHash)
			if err != nil {
				return "", nil, "", err
			}
			lines = strings.Split(string(data), "\n")
			byBlob[match.BlobHash] = lines
		}
		start := max(1, match.Line-excerptContextLines)
		end := min(len(lines), match.Line+excerptContextLines)
		evidence = append(evidence, state.WorkEvidence{Kind: "source-excerpt", Path: match.Path, BlobHash: match.BlobHash, Line: start, EndLine: end, Excerpt: strings.Join(lines[start-1:end], "\n")})
	}
	return fmt.Sprintf("Retrieved %d exact committed excerpt(s)", len(evidence)), evidence, "", nil
}

func executeComponentLookup(ctx context.Context, db *state.Database, plan state.WorkPlan, task state.WorkPlanTask) (string, []state.WorkEvidence, string, error) {
	result, err := query.New(db).Components(ctx, plan.ProjectID, query.Selection{}, query.ComponentFilter{Identity: task.Inputs["identity"]})
	if err != nil {
		return "", nil, "component analysis unavailable: " + err.Error(), nil
	}
	if len(result.Components) == 0 {
		return "", nil, fmt.Sprintf("component %q is not present in stored analysis", task.Inputs["identity"]), nil
	}
	var evidence []state.WorkEvidence
	for _, c := range result.Components {
		for _, ev := range c.Evidence {
			evidence = append(evidence, state.WorkEvidence{Kind: "component-declaration", Path: ev.Path, BlobHash: ev.BlobHash, Line: ev.Line, EndLine: ev.Line, Excerpt: ev.Declaration})
		}
	}
	return fmt.Sprintf("Resolved %d component record(s) for %q", len(result.Components), task.Inputs["identity"]), evidence, "", nil
}

func executeDependencyLookup(ctx context.Context, db *state.Database, plan state.WorkPlan, task state.WorkPlanTask) (string, []state.WorkEvidence, string, error) {
	engine := query.New(db)
	identity, relation := task.Inputs["identity"], task.Inputs["relation"]
	var deps []query.Dependency
	if relation == "dependencies" {
		result, err := engine.Dependencies(ctx, plan.ProjectID, identity, query.Selection{})
		if err != nil {
			return "", nil, "relationship lookup unavailable: " + err.Error(), nil
		}
		deps = result.Dependencies
	} else {
		result, err := engine.Dependents(ctx, plan.ProjectID, identity, query.Selection{})
		if err != nil {
			return "", nil, "relationship lookup unavailable: " + err.Error(), nil
		}
		deps = result.Dependents
	}
	var evidence []state.WorkEvidence
	for _, dep := range deps {
		for _, ev := range dep.Evidence {
			evidence = append(evidence, state.WorkEvidence{Kind: "dependency-declaration", Path: ev.Path, BlobHash: ev.BlobHash, Line: ev.Line, EndLine: ev.Line, Excerpt: ev.Declaration, Detail: dep.Source + " -> " + dep.Target})
		}
	}
	sort.Slice(evidence, func(i, j int) bool {
		if evidence[i].Path != evidence[j].Path {
			return evidence[i].Path < evidence[j].Path
		}
		return evidence[i].Line < evidence[j].Line
	})
	return fmt.Sprintf("Found %d direct %s for %q", len(deps), relation, identity), evidence, "", nil
}

func cleanPrefix(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(path.Clean("/"+value), "/"))
	if value == "." {
		return ""
	}
	return value
}
