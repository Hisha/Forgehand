using System.Net;
using System.Text.Json;
using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Configuration;

namespace Forgehand.Client.Tests;

public sealed class ForgehandApiClientTests
{
    private static ForgehandApiClient CreateClient(ScriptedHttpHandler handler) =>
        new(ServerEndpoint.Default, handler);

    private static async Task<ForgehandApiException> ExpectFailureAsync(Func<Task> action)
    {
        var exception = await Assert.ThrowsAsync<ForgehandApiException>(action);
        return exception;
    }

    [Fact]
    public async Task LoginSendsCredentialsAndParsesSession()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(HttpStatusCode.OK, TestData.SessionJson));
        using var client = CreateClient(handler);

        var session = await client.LoginAsync(new LoginRequest("alice", "correct horse", "123456"), CancellationToken.None);

        var request = Assert.Single(handler.Requests);
        Assert.Equal(HttpMethod.Post, request.Method);
        Assert.Equal("/api/v1/auth/login", request.Path);
        Assert.Null(request.Authorization);
        Assert.StartsWith("application/json", request.ContentType);

        using var body = JsonDocument.Parse(request.Body!);
        Assert.Equal("alice", body.RootElement.GetProperty("username").GetString());
        Assert.Equal("correct horse", body.RootElement.GetProperty("password").GetString());
        Assert.Equal("123456", body.RootElement.GetProperty("totp_code").GetString());
        Assert.False(body.RootElement.TryGetProperty("recovery_code", out _));

        Assert.Equal("tok-123", session.Token);
        Assert.Equal("2026-10-22T20:00:00Z", session.ExpiresAt);
        Assert.Equal(7, session.Account.Id);
        Assert.Equal("alice", session.Account.Username);
        Assert.Equal("admin", session.Account.Role);
        Assert.True(session.Account.TotpEnrolled);
    }

    [Fact]
    public async Task LoginFailureDoesNotExposeServerMessage()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(
            HttpStatusCode.Unauthorized,
            """{"error":{"code":"invalid_credentials","message":"no such account: alice@internal"}}"""));
        using var client = CreateClient(handler);

        var exception = await ExpectFailureAsync(() =>
            client.LoginAsync(new LoginRequest("alice", "wrong", "000000"), CancellationToken.None));

        Assert.Equal(ApiFailure.InvalidCredentials, exception.Failure);
        Assert.DoesNotContain("internal", exception.Message);
        Assert.DoesNotContain("no such account", exception.Message);
    }

    [Theory]
    [InlineData(HttpStatusCode.TooManyRequests, "account_locked", ApiFailure.AccountLocked)]
    [InlineData(HttpStatusCode.TooManyRequests, "throttled", ApiFailure.Throttled)]
    [InlineData(HttpStatusCode.Forbidden, "enrollment_required", ApiFailure.EnrollmentRequired)]
    [InlineData(HttpStatusCode.Forbidden, "account_disabled", ApiFailure.AccountDisabled)]
    [InlineData(HttpStatusCode.BadRequest, "invalid_request", ApiFailure.InvalidRequest)]
    [InlineData(HttpStatusCode.InternalServerError, "internal_error", ApiFailure.ServerError)]
    [InlineData(HttpStatusCode.Redirect, "", ApiFailure.UnexpectedResponse)]
    public async Task LoginMapsErrorCodesToFailures(HttpStatusCode status, string code, ApiFailure expected)
    {
        var body = string.IsNullOrEmpty(code) ? "" : "{\"error\":{\"code\":\"" + code + "\",\"message\":\"x\"}}";
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(status, body));
        using var client = CreateClient(handler);

        var exception = await ExpectFailureAsync(() =>
            client.LoginAsync(new LoginRequest("alice", "pw", "123456"), CancellationToken.None));

        Assert.Equal(expected, exception.Failure);
    }

    [Fact]
    public async Task LoginRejectsSuccessWithoutToken()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(
            HttpStatusCode.OK,
            """{"token":"","expires_at":"2026-10-22T20:00:00Z","account":{"id":1,"username":"a","role":"admin","status":"active","totp_enrolled":true}}"""));
        using var client = CreateClient(handler);

        var exception = await ExpectFailureAsync(() =>
            client.LoginAsync(new LoginRequest("a", "pw", "123456"), CancellationToken.None));

        Assert.Equal(ApiFailure.UnexpectedResponse, exception.Failure);
    }

    [Fact]
    public async Task ProjectsSendsBearerTokenAndParsesOnlyReturnedProjects()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(
            HttpStatusCode.OK,
            """[{"id":1,"name":"Alpha","root_path":"/srv/alpha"},{"id":2,"name":"Beta","root_path":""}]"""));
        using var client = CreateClient(handler);

        var projects = await client.GetProjectsAsync("tok-123", CancellationToken.None);

        var request = Assert.Single(handler.Requests);
        Assert.Equal(HttpMethod.Get, request.Method);
        Assert.Equal("/api/v1/projects", request.Path);
        Assert.Equal("Bearer tok-123", request.Authorization);
        Assert.Equal(2, projects.Count);
        Assert.Equal(1, projects[0].Id);
        Assert.Equal("Alpha", projects[0].Name);
        Assert.Equal("/srv/alpha", projects[0].RootPath);
        Assert.Equal(2, projects[1].Id);
        Assert.Equal("", projects[1].RootPath);
    }

    [Fact]
    public async Task EmptyProjectListIsAValidResponse()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(HttpStatusCode.OK, "[]"));
        using var client = CreateClient(handler);

        var projects = await client.GetProjectsAsync("tok-123", CancellationToken.None);

        Assert.Empty(projects);
    }

    [Fact]
    public async Task ProjectSessionsUseAuthorizedProjectRouteAndParseReturnedFields()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(
            HttpStatusCode.OK,
            """[{"id":41,"title":"Investigate","state":"RUNNING","project_id":7,"task":"Find cause","kind":"task","provider":"openai","model":"gpt-test","created_at":"2026-10-22T12:00:00Z","updated_at":"2026-10-22T12:05:00Z"}]"""));
        using var client = CreateClient(handler);

        var sessions = await client.GetProjectSessionsAsync("tok-123", 7, CancellationToken.None);

        var request = Assert.Single(handler.Requests);
        Assert.Equal("/api/v1/projects/7/sessions", request.Path);
        Assert.Equal("Bearer tok-123", request.Authorization);
        var session = Assert.Single(sessions);
        Assert.Equal(41, session.Id);
        Assert.Equal("Find cause", session.Task);
        Assert.Equal("gpt-test", session.Model);
    }

    [Fact]
    public async Task SessionDetailsUseReadOnlySessionRoute()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(
            HttpStatusCode.OK,
            """{"id":41,"title":"Investigate","state":"COMPLETED","project_id":7,"created_at":"2026-10-22T12:00:00Z","updated_at":"2026-10-22T12:05:00Z"}"""));
        using var client = CreateClient(handler);

        var session = await client.GetSessionAsync("tok-123", 41, CancellationToken.None);

        Assert.Equal("/api/v1/sessions/41", Assert.Single(handler.Requests).Path);
        Assert.Equal("COMPLETED", session.State);
    }

    [Fact]
    public async Task MissingSessionMapsToNotFoundWithoutExposingServerText()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(
            HttpStatusCode.NotFound,
            """{"error":{"code":"not_found","message":"session 41 belongs to private project"}}"""));
        using var client = CreateClient(handler);

        var exception = await ExpectFailureAsync(() => client.GetSessionAsync("tok-123", 41, CancellationToken.None));

        Assert.Equal(ApiFailure.NotFound, exception.Failure);
        Assert.DoesNotContain("private", exception.Message);
    }

    [Fact]
    public async Task ProjectsUnauthorizedMeansSessionExpired()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(
            HttpStatusCode.Unauthorized,
            """{"error":{"code":"unauthorized","message":"a valid session is required"}}"""));
        using var client = CreateClient(handler);

        var exception = await ExpectFailureAsync(() => client.GetProjectsAsync("stale", CancellationToken.None));

        Assert.Equal(ApiFailure.SessionExpired, exception.Failure);
    }

    [Fact]
    public async Task LogoutPostsBearerTokenAndAcceptsConfirmation()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(HttpStatusCode.OK, """{"status":"signed out"}"""));
        using var client = CreateClient(handler);

        await client.LogoutAsync("tok-123", CancellationToken.None);

        var request = Assert.Single(handler.Requests);
        Assert.Equal(HttpMethod.Post, request.Method);
        Assert.Equal("/api/v1/auth/logout", request.Path);
        Assert.Equal("Bearer tok-123", request.Authorization);
    }

    [Fact]
    public async Task HealthParsesDaemonIdentity()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(
            HttpStatusCode.OK,
            """{"status":"ok","version":"0.9.0","build_identity":"abc","api_version":"v1","time":"2026-10-22T12:00:00Z"}"""));
        using var client = CreateClient(handler);

        var health = await client.GetHealthAsync(CancellationToken.None);

        Assert.Equal("/api/v1/health", Assert.Single(handler.Requests).Path);
        Assert.Null(handler.Requests[0].Authorization);
        Assert.Equal("ok", health.Status);
        Assert.Equal("0.9.0", health.Version);
        Assert.Equal("v1", health.ApiVersion);
    }

    [Fact]
    public async Task UnreachableServerProducesUnderstandableFailure()
    {
        var handler = new ScriptedHttpHandler(_ => throw new HttpRequestException("connection refused"));
        using var client = CreateClient(handler);

        var exception = await ExpectFailureAsync(() => client.GetHealthAsync(CancellationToken.None));

        Assert.Equal(ApiFailure.Unreachable, exception.Failure);
        Assert.Contains("http://127.0.0.1:8787", exception.Message);
    }

    [Fact]
    public async Task TransportTimeoutIsReportedAsTimeout()
    {
        var handler = new ScriptedHttpHandler(_ => throw new TaskCanceledException("timed out"));
        using var client = CreateClient(handler);

        var exception = await ExpectFailureAsync(() => client.GetHealthAsync(CancellationToken.None));

        Assert.Equal(ApiFailure.Timeout, exception.Failure);
    }

    [Fact]
    public async Task CallerCancellationIsNotReportedAsApiFailure()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(HttpStatusCode.OK, "{}"));
        using var client = CreateClient(handler);
        using var cancellation = new CancellationTokenSource();
        cancellation.Cancel();

        await Assert.ThrowsAnyAsync<OperationCanceledException>(() => client.GetProjectsAsync("tok", cancellation.Token));
    }

    [Fact]
    public async Task MalformedSuccessBodyIsUnexpectedResponse()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(HttpStatusCode.OK, "<html>proxy page</html>"));
        using var client = CreateClient(handler);

        var exception = await ExpectFailureAsync(() => client.GetProjectsAsync("tok", CancellationToken.None));

        Assert.Equal(ApiFailure.UnexpectedResponse, exception.Failure);
    }

    [Fact]
    public async Task OversizedResponseIsRefused()
    {
        var oversized = new string('x', ForgehandApiClient.MaxResponseBytes + 1);
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(HttpStatusCode.OK, oversized));
        using var client = CreateClient(handler);

        var exception = await ExpectFailureAsync(() => client.GetProjectsAsync("tok", CancellationToken.None));

        Assert.Equal(ApiFailure.UnexpectedResponse, exception.Failure);
    }

    [Fact]
    public async Task ServerErrorDoesNotExposeBody()
    {
        var handler = new ScriptedHttpHandler(_ => ScriptedHttpHandler.Json(
            HttpStatusCode.InternalServerError,
            """{"error":{"code":"internal_error","message":"sql: no rows in table accounts"}}"""));
        using var client = CreateClient(handler);

        var exception = await ExpectFailureAsync(() => client.GetProjectsAsync("tok", CancellationToken.None));

        Assert.Equal(ApiFailure.ServerError, exception.Failure);
        Assert.DoesNotContain("sql", exception.Message);
    }
}
