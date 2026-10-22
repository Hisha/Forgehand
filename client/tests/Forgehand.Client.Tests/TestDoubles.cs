using System.Net;
using System.Text;
using Forgehand.Client.Core.Api;

namespace Forgehand.Client.Tests;

internal sealed record CapturedRequest(
    HttpMethod Method,
    string Path,
    string? Authorization,
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

    public int LoginCalls { get; private set; }

    public int ProjectCalls { get; private set; }

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

    public void Dispose()
    {
    }
}
