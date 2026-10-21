package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/Hisha/Forgehand/internal/planner"
	"github.com/Hisha/Forgehand/internal/state"
)

const qualificationJobTimeout = 30 * time.Minute

func (r *modelRuntime) runQualificationWorker(ctx context.Context, db *state.Database, execution state.SessionExecution) {
	job, err := db.GetQualificationJobByExecution(ctx, execution.ID)
	if err != nil {
		failExecutionWithReason(db, execution.ID, state.FailureKindWorker, "load qualification job: "+err.Error())
		return
	}
	if err := db.MarkQualificationRunning(context.Background(), job.ID); err != nil {
		failExecutionWithReason(db, execution.ID, state.FailureKindWorker, "start qualification job: "+err.Error())
		return
	}
	timeout := r.qualificationTimeout
	if timeout <= 0 {
		timeout = qualificationJobTimeout
	}
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cases := planner.RepresentativeQualificationCases()
	decomposer := &planner.ModelDecomposer{Provider: r.provider, Model: job.Model, Limits: planner.ModelLimits{MaxTokens: r.maxTokens}}
	base := planner.QualificationRequest(job.Model, job.ProviderFingerprint, job.Capability)

	for index := job.CompletedEvaluations; index < job.TotalEvaluations; index++ {
		caseIndex := index / job.Repetitions
		attempt := index%job.Repetitions + 1
		if caseIndex >= len(cases) {
			r.failQualification(db, job.ID, "worker", "qualification evaluation index exceeds representative cases")
			return
		}
		report := planner.Qualify(jobCtx, decomposer, base, job.Model, job.ProviderFingerprint, job.Capability, cases[caseIndex:caseIndex+1], 1)
		if ctx.Err() != nil {
			if latest, getErr := db.GetQualificationJob(context.Background(), job.ID); getErr == nil && latest.Status == state.QualificationStatusRunning {
				_ = db.InterruptQualificationJob(context.Background(), job.ID)
			}
			return
		}
		if len(report.Runs) != 1 {
			r.failQualification(db, job.ID, "worker", "qualification engine returned no individual result")
			return
		}
		run := report.Runs[0]
		run.Attempt = attempt
		redactQualificationRun(&run, r.redactionSecrets)
		encoded, marshalErr := json.Marshal(run)
		if marshalErr != nil {
			r.failQualification(db, job.ID, "persistence", marshalErr.Error())
			return
		}
		if err := db.AddQualificationResult(context.Background(), job.ID, index, string(encoded)); err != nil {
			// A user cancellation wins over a worker finishing its request.
			if latest, getErr := db.GetQualificationJob(context.Background(), job.ID); getErr == nil && latest.Status == state.QualificationStatusCancelled {
				return
			}
			r.failQualification(db, job.ID, "persistence", err.Error())
			return
		}
		if jobCtx.Err() != nil {
			if ctx.Err() != nil {
				_ = db.InterruptQualificationJob(context.Background(), job.ID)
			} else {
				r.failQualification(db, job.ID, "timeout", jobCtx.Err().Error())
			}
			return
		}
		if run.InfrastructureFailure {
			reason := "qualification infrastructure failure"
			if len(run.ValidationErrors) > 0 {
				reason = strings.Join(run.ValidationErrors, "; ")
			}
			r.failQualification(db, job.ID, "provider", reason)
			return
		}
	}

	job, err = db.GetQualificationJob(context.Background(), job.ID)
	if err != nil {
		r.failQualification(db, job.ID, "persistence", err.Error())
		return
	}
	runs := make([]planner.QualificationRun, 0, len(job.Results))
	for _, stored := range job.Results {
		var run planner.QualificationRun
		if err := json.Unmarshal([]byte(stored.ResultJSON), &run); err != nil {
			r.failQualification(db, job.ID, "persistence", err.Error())
			return
		}
		runs = append(runs, run)
	}
	evaluatedAt := job.CreatedAt
	if job.StartedAt != nil {
		evaluatedAt = *job.StartedAt
	}
	report := planner.FinalizeQualificationReport(planner.QualificationReport{Model: job.Model, ProviderFingerprint: job.ProviderFingerprint, Capability: job.Capability, EvaluatedAt: evaluatedAt, CaseCount: len(cases), Repetitions: job.Repetitions, Runs: runs}, cases)
	aggregate, err := json.Marshal(struct {
		Aggregate   planner.QualificationAggregate     `json:"aggregate"`
		Consistency []planner.QualificationConsistency `json:"consistency"`
	}{report.Aggregate, report.Consistency})
	if err != nil {
		r.failQualification(db, job.ID, "persistence", err.Error())
		return
	}
	if err := db.CompleteQualificationJob(context.Background(), job.ID, string(aggregate), string(report.Recommendation)); err != nil {
		r.failQualification(db, job.ID, "persistence", err.Error())
	}
}

func (r *modelRuntime) failQualification(db *state.Database, jobID int64, kind, reason string) {
	for _, secret := range r.redactionSecrets {
		if secret != "" {
			reason = strings.ReplaceAll(reason, secret, "[REDACTED]")
		}
	}
	_ = db.FailQualificationJob(context.Background(), jobID, kind, reason)
}

func redactQualificationRun(run *planner.QualificationRun, secrets []string) {
	for index, message := range run.ValidationErrors {
		for _, secret := range secrets {
			if secret != "" {
				message = strings.ReplaceAll(message, secret, "[REDACTED]")
			}
		}
		run.ValidationErrors[index] = message
	}
}
