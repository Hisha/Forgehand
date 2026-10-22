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

internal sealed record ErrorEnvelope([property: JsonPropertyName("error")] ErrorDetail? Error);

internal sealed record ErrorDetail([property: JsonPropertyName("code")] string? Code);
