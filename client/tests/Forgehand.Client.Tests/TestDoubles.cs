using System.Net;
using System.Text;
using Forgehand.Client.Core.Api;

namespace Forgehand.Client.Tests;

internal sealed record CapturedRequest(
    HttpMethod Method,
    string Path,
    string? Authorization,
    string? ApiVersion,
    string? ContentType,
    string? Body);

/// <summary>Records each request and answers it with a scripted response or exception.</summary>
internal sealed class ScriptedHttpHandler : HttpMessageHandler
{
    private readonly Func<CapturedRequest, HttpResponseMessage> _respond;

    public ScriptedHttpHandler(Func<CapturedRequest, HttpResponseMessage> respond)
    {
        _respond = respond;
    }

    public List<CapturedRequest> Requests { get; } = [];

    protected override async Task<HttpResponseMessage> SendAsync(
        HttpRequestMessage request,
        CancellationToken cancellationToken)
    {
        var body = request.Content is null ? null : await request.Content.ReadAsStringAsync(cancellationToken);
        var captured = new CapturedRequest(
            request.Method,
            request.RequestUri!.PathAndQuery,
            request.Headers.Authorization?.ToString(),
            request.Headers.TryGetValues(ForgehandApiClient.CompatibilityHeader, out var versions)
                ? versions.SingleOrDefault()
                : null,
            request.Content?.Headers.ContentType?.ToString(),
            body);
        Requests.Add(captured);
        return _respond(captured);
    }

    public static HttpResponseMessage Json(HttpStatusCode status, string body) =>
        new(status) { Content = new StringContent(body, Encoding.UTF8, "application/json") };
}

internal static class TestData
{
    public const string SessionJson =
        """
        {"token":"tok-123","expires_at":"2026-10-22T20:00:00Z",
         "account":{"id":7,"username":"alice","display_name":"Alice","role":"admin","status":"active","totp_enrolled":true},
         "unexpected_field":"ignored"}
        """;

    public static SessionResponse Session(string token = "tok-123") => new(
        token,
        "2026-10-22T20:00:00Z",
        new AccountResponse(7, "alice", "Alice", "admin", "active", true));

    public static SessionSummaryResponse SessionSummary(long id, long projectId, string title = "Investigate") => new(
        id, title, "RUNNING", projectId, "Inspect the failure", "task", "openai", "gpt-test",
        "2026-10-22T12:00:00Z", "2026-10-22T12:05:00Z");
}

/// <summary>An in-memory API double for service and view model tests.</summary>
internal sealed class FakeForgehandApi : IForgehandApi
{
    public Func<CancellationToken, Task<HealthResponse>> Health { get; set; } =
        _ => Task.FromResult(new HealthResponse("ok", "1.2.3", "build", "v1", "2026-10-22T12:00:00Z"));

    public Func<LoginRequest, Task<SessionResponse>> Login { get; set; } =
        _ => Task.FromResult(TestData.Session());

    public Func<string, Task> Logout { get; set; } = _ => Task.CompletedTask;

    public Func<string, Task<IReadOnlyList<ProjectResponse>>> Projects { get; set; } =
        _ => Task.FromResult<IReadOnlyList<ProjectResponse>>(Array.Empty<ProjectResponse>());

    public Func<string, long, CancellationToken, Task<IReadOnlyList<SessionSummaryResponse>>> ProjectSessions { get; set; } =
        (_, _, _) => Task.FromResult<IReadOnlyList<SessionSummaryResponse>>(Array.Empty<SessionSummaryResponse>());

    public Func<string, long, CancellationToken, Task<SessionSummaryResponse>> SessionDetails { get; set; } =
        (_, id, _) => Task.FromResult(TestData.SessionSummary(id, 1));

    public Func<string, long, CreateSessionRequest, CancellationToken, Task<SessionSummaryResponse>> CreateSession { get; set; } =
        (_, projectId, request, _) => Task.FromResult(TestData.SessionSummary(99, projectId, request.Title) with { Task = request.Task });

    public Func<string, long, CancellationToken, Task<RunSessionResponse>> RunSession { get; set; } =
        (_, id, _) => Task.FromResult(new RunSessionResponse(
            TestData.SessionSummary(id, 1) with { State = "QUEUED" },
            new ExecutionResponse(100, id, "QUEUED", 1)));

    public Func<string, long, CancellationToken, Task<ExecutionHistoryResponse>> SessionExecutions { get; set; } =
        (_, _, _) => Task.FromResult(new ExecutionHistoryResponse(Array.Empty<ExecutionDetailResponse>(), false));

    public Func<string, long, long, CancellationToken, Task<ExecutionRecordResponse>> SessionExecution { get; set; } =
        (_, sessionId, executionId, _) => Task.FromResult(new ExecutionRecordResponse(
            new ExecutionDetailResponse(executionId, sessionId, "COMPLETED", 1, 1,
                "2026-10-22T12:00:00Z", "2026-10-22T12:00:01Z", null, null, null, null,
                "coding", true, "openai", "gpt-test", "2026-10-22T12:00:01Z", 1, 2, 3, 1000),
            new ModelResultResponse(1, executionId, "openai", "gpt-test", "done",
                "2026-10-22T12:00:01Z", 1, 2, 3, 1000, 0, 0, 4, true)));

    public int LoginCalls { get; private set; }

    public int ProjectCalls { get; private set; }

    public int ProjectSessionCalls { get; private set; }

    public int SessionCalls { get; private set; }

    public int CreateSessionCalls { get; private set; }

    public int RunSessionCalls { get; private set; }

    public Task<HealthResponse> GetHealthAsync(CancellationToken cancellationToken) => Health(cancellationToken);

    public Task<SessionResponse> LoginAsync(LoginRequest request, CancellationToken cancellationToken)
    {
        LoginCalls++;
        return Login(request);
    }

    public Task LogoutAsync(string token, CancellationToken cancellationToken) => Logout(token);

    public Task<IReadOnlyList<ProjectResponse>> GetProjectsAsync(string token, CancellationToken cancellationToken)
    {
        ProjectCalls++;
        return Projects(token);
    }

    public Task<IReadOnlyList<SessionSummaryResponse>> GetProjectSessionsAsync(
        string token, long projectId, CancellationToken cancellationToken)
    {
        ProjectSessionCalls++;
        return ProjectSessions(token, projectId, cancellationToken);
    }

    public Task<SessionSummaryResponse> GetSessionAsync(
        string token, long sessionId, CancellationToken cancellationToken)
    {
        SessionCalls++;
        return SessionDetails(token, sessionId, cancellationToken);
    }

    public Task<SessionSummaryResponse> CreateSessionAsync(
        string token, long projectId, CreateSessionRequest request, CancellationToken cancellationToken)
    {
        CreateSessionCalls++;
        return CreateSession(token, projectId, request, cancellationToken);
    }

    public Task<RunSessionResponse> RunSessionAsync(
        string token, long sessionId, CancellationToken cancellationToken)
    {
        RunSessionCalls++;
        return RunSession(token, sessionId, cancellationToken);
    }

    public Task<ExecutionHistoryResponse> GetSessionExecutionsAsync(
        string token, long sessionId, CancellationToken cancellationToken) =>
        SessionExecutions(token, sessionId, cancellationToken);

    public Task<ExecutionRecordResponse> GetSessionExecutionAsync(
        string token, long sessionId, long executionId, CancellationToken cancellationToken) =>
        SessionExecution(token, sessionId, executionId, cancellationToken);

    public void Dispose()
    {
    }
}
