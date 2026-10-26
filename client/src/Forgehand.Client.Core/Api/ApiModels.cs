using System.Text.Json.Serialization;

namespace Forgehand.Client.Core.Api;

// These records mirror the wire format of internal/remoteapi/types.go. Unknown
// response fields (for example recovery_codes) are ignored on purpose.

public sealed record HealthResponse(
    [property: JsonPropertyName("status")] string Status,
    [property: JsonPropertyName("version")] string Version,
    [property: JsonPropertyName("build_identity")] string BuildIdentity,
    [property: JsonPropertyName("api_version")] string ApiVersion,
    [property: JsonPropertyName("time")] string Time);

public sealed record DaemonStatusResponse(
    [property: JsonPropertyName("status")] string Status,
    [property: JsonPropertyName("observed_at")] string ObservedAt,
    [property: JsonPropertyName("identity")] DaemonIdentityResponse Identity,
    [property: JsonPropertyName("activity")] DaemonActivityResponse Activity,
    [property: JsonPropertyName("executable")] ExecutableStatusResponse Executable,
    [property: JsonPropertyName("health")] DaemonHealthResponse Health);

public sealed record DaemonIdentityResponse(
    [property: JsonPropertyName("version")] string Version,
    [property: JsonPropertyName("build_identity")] string BuildIdentity,
    [property: JsonPropertyName("build_timestamp")] string? BuildTimestamp,
    [property: JsonPropertyName("api_version")] string ApiVersion,
    [property: JsonPropertyName("started_at")] string StartedAt,
    [property: JsonPropertyName("uptime_seconds")] long UptimeSeconds);

public sealed record DaemonActivityResponse(
    [property: JsonPropertyName("worker_capacity")] int? WorkerCapacity,
    [property: JsonPropertyName("active_workers")] int? ActiveWorkers,
    [property: JsonPropertyName("running_sessions")] int? RunningSessions,
    [property: JsonPropertyName("queued_executions")] int? QueuedExecutions,
    [property: JsonPropertyName("pending_sessions")] int? PendingSessions,
    [property: JsonPropertyName("completed_sessions")] int? CompletedSessions,
    [property: JsonPropertyName("failed_sessions")] int? FailedSessions);

public sealed record ExecutableStatusResponse(
    [property: JsonPropertyName("running_file_identity")] string? RunningFileIdentity,
    [property: JsonPropertyName("installed_file_identity")] string? InstalledFileIdentity,
    [property: JsonPropertyName("comparison")] string Comparison,
    [property: JsonPropertyName("reason")] string Reason,
    [property: JsonPropertyName("checked_at")] string CheckedAt,
    [property: JsonPropertyName("restart_recommended")] bool RestartRecommended);

public sealed record DaemonHealthResponse(
    [property: JsonPropertyName("database")] string Database,
    [property: JsonPropertyName("dispatcher")] string Dispatcher,
    [property: JsonPropertyName("workers")] string Workers);

public sealed record LoginRequest(
    [property: JsonPropertyName("username")] string Username,
    [property: JsonPropertyName("password")] string Password,
    [property: JsonPropertyName("totp_code")] string TotpCode)
{
    // Keeps the password and one-time code out of logs and debugger output.
    public override string ToString() => "LoginRequest(redacted)";
}

public sealed record AccountResponse(
    [property: JsonPropertyName("id")] long Id,
    [property: JsonPropertyName("username")] string Username,
    [property: JsonPropertyName("display_name")] string? DisplayName,
    [property: JsonPropertyName("role")] string Role,
    [property: JsonPropertyName("status")] string Status,
    [property: JsonPropertyName("totp_enrolled")] bool TotpEnrolled);

public sealed record SessionResponse(
    [property: JsonPropertyName("token")] string Token,
    [property: JsonPropertyName("expires_at")] string ExpiresAt,
    [property: JsonPropertyName("account")] AccountResponse Account)
{
    // The bearer token must never reach logs or diagnostics.
    public override string ToString() => "SessionResponse(redacted)";
}

public sealed record ProjectResponse(
    [property: JsonPropertyName("id")] long Id,
    [property: JsonPropertyName("name")] string Name,
    [property: JsonPropertyName("root_path")] string? RootPath);

public sealed record SessionSummaryResponse(
    [property: JsonPropertyName("id")] long Id,
    [property: JsonPropertyName("title")] string Title,
    [property: JsonPropertyName("state")] string State,
    [property: JsonPropertyName("project_id")] long ProjectId,
    [property: JsonPropertyName("task")] string? Task,
    [property: JsonPropertyName("kind")] string? Kind,
    [property: JsonPropertyName("provider")] string? Provider,
    [property: JsonPropertyName("model")] string? Model,
    [property: JsonPropertyName("created_at")] string CreatedAt,
    [property: JsonPropertyName("updated_at")] string UpdatedAt);

public sealed record CreateSessionRequest(
    [property: JsonPropertyName("title")] string Title,
    [property: JsonPropertyName("task")] string Task);

public sealed record ExecutionResponse(
    [property: JsonPropertyName("id")] long Id,
    [property: JsonPropertyName("session_id")] long SessionId,
    [property: JsonPropertyName("status")] string Status,
    [property: JsonPropertyName("queue_position")] int QueuePosition);

public sealed record RunSessionResponse(
    [property: JsonPropertyName("session")] SessionSummaryResponse Session,
    [property: JsonPropertyName("execution")] ExecutionResponse Execution);

public sealed record ExecutionDetailResponse(
    [property: JsonPropertyName("id")] long Id,
    [property: JsonPropertyName("session_id")] long SessionId,
    [property: JsonPropertyName("status")] string Status,
    [property: JsonPropertyName("current_step")] int CurrentStep,
    [property: JsonPropertyName("total_steps")] int TotalSteps,
    [property: JsonPropertyName("started_at")] string StartedAt,
    [property: JsonPropertyName("completed_at")] string? CompletedAt,
    [property: JsonPropertyName("canceled_at")] string? CanceledAt,
    [property: JsonPropertyName("cancel_reason")] string? CancelReason,
    [property: JsonPropertyName("failure_kind")] string? FailureKind,
    [property: JsonPropertyName("failure_reason")] string? FailureReason,
    [property: JsonPropertyName("kind")] string? Kind,
    [property: JsonPropertyName("result_available")] bool ResultAvailable,
    [property: JsonPropertyName("provider")] string? Provider,
    [property: JsonPropertyName("model")] string? Model,
    [property: JsonPropertyName("result_created_at")] string? ResultCreatedAt,
    [property: JsonPropertyName("prompt_tokens")] int PromptTokens,
    [property: JsonPropertyName("completion_tokens")] int CompletionTokens,
    [property: JsonPropertyName("total_tokens")] int TotalTokens,
    [property: JsonPropertyName("latency_ms")] long LatencyMs);

public sealed record ExecutionHistoryResponse(
    [property: JsonPropertyName("executions")] IReadOnlyList<ExecutionDetailResponse> Executions,
    [property: JsonPropertyName("truncated")] bool Truncated);

public sealed record ModelResultResponse(
    [property: JsonPropertyName("id")] long Id,
    [property: JsonPropertyName("execution_id")] long ExecutionId,
    [property: JsonPropertyName("provider")] string Provider,
    [property: JsonPropertyName("model")] string Model,
    [property: JsonPropertyName("content")] string Content,
    [property: JsonPropertyName("created_at")] string CreatedAt,
    [property: JsonPropertyName("prompt_tokens")] int PromptTokens,
    [property: JsonPropertyName("completion_tokens")] int CompletionTokens,
    [property: JsonPropertyName("total_tokens")] int TotalTokens,
    [property: JsonPropertyName("latency_ms")] long LatencyMs,
    [property: JsonPropertyName("content_offset")] int ContentOffset,
    [property: JsonPropertyName("next_offset")] int NextOffset,
    [property: JsonPropertyName("total_characters")] int TotalCharacters,
    [property: JsonPropertyName("complete")] bool Complete);

public sealed record ExecutionRecordResponse(
    [property: JsonPropertyName("execution")] ExecutionDetailResponse Execution,
    [property: JsonPropertyName("result")] ModelResultResponse? Result);

internal sealed record ErrorEnvelope([property: JsonPropertyName("error")] ErrorDetail? Error);

internal sealed record ErrorDetail([property: JsonPropertyName("code")] string? Code);
