using System.Net;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using Forgehand.Client.Core.Configuration;

namespace Forgehand.Client.Core.Api;

/// <summary>
/// HTTP client for the Forgehand daemon's /api/v1 contract.
/// </summary>
public sealed class ForgehandApiClient : IForgehandApi
{
    public const string SupportedApiVersion = "v1";
    public const string CompatibilityHeader = "Forgehand-API-Version";

    /// <summary>Upper bound on a response body. The API returns small JSON documents.</summary>
    public const int MaxResponseBytes = 1024 * 1024;
    public const int ResultPageCharacters = 64 * 1024;
    public const int MaxResultCharacters = 4 * 1024 * 1024;

    public static readonly TimeSpan RequestTimeout = TimeSpan.FromSeconds(15);

    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web);

    private readonly ServerEndpoint _endpoint;
    private readonly HttpClient _httpClient;

    /// <param name="endpoint">The daemon address. Requests never leave this origin.</param>
    /// <param name="handler">Transport override for tests. Null selects the default transport.</param>
    public ForgehandApiClient(ServerEndpoint endpoint, HttpMessageHandler? handler = null)
    {
        _endpoint = endpoint;
        _httpClient = new HttpClient(handler ?? CreateTransport(), disposeHandler: true)
        {
            BaseAddress = endpoint.BaseAddress,
            Timeout = RequestTimeout,
        };
    }

    public Task<HealthResponse> GetHealthAsync(CancellationToken cancellationToken) =>
        SendAsync<HealthResponse>(HttpMethod.Get, "api/v1/health", token: null, body: null, cancellationToken);

    public async Task<DaemonStatusResponse> GetDaemonStatusAsync(string token, CancellationToken cancellationToken)
    {
        var status = await SendAsync<DaemonStatusResponse>(
            HttpMethod.Get, "api/v1/daemon/status", token, body: null, cancellationToken);
        if (status.Identity is null || status.Activity is null || status.Executable is null || status.Health is null ||
            status.Identity.UptimeSeconds < 0 ||
            status.Executable.Comparison is not ("match" or "mismatch" or "unknown"))
        {
            throw ForgehandApiException.For(ApiFailure.UnexpectedResponse, _endpoint);
        }
        return status;
    }

    public async Task<SessionResponse> LoginAsync(LoginRequest request, CancellationToken cancellationToken)
    {
        var session = await SendAsync<SessionResponse>(
            HttpMethod.Post, "api/v1/auth/login", token: null, request, cancellationToken);
        if (string.IsNullOrWhiteSpace(session.Token) || session.Account is null)
        {
            throw ForgehandApiException.For(ApiFailure.UnexpectedResponse, _endpoint);
        }

        return session;
    }

    public async Task LogoutAsync(string token, CancellationToken cancellationToken)
    {
        await ExecuteAsync(HttpMethod.Post, "api/v1/auth/logout", token, body: null, cancellationToken);
    }

    public async Task<IReadOnlyList<ProjectResponse>> GetProjectsAsync(string token, CancellationToken cancellationToken)
    {
        var projects = await SendAsync<List<ProjectResponse>>(
            HttpMethod.Get, "api/v1/projects", token, body: null, cancellationToken);
        if (projects.Any(project => project is null))
        {
            throw ForgehandApiException.For(ApiFailure.UnexpectedResponse, _endpoint);
        }

        return projects;
    }

    public async Task<IReadOnlyList<SessionSummaryResponse>> GetProjectSessionsAsync(
        string token,
        long projectId,
        CancellationToken cancellationToken)
    {
        var sessions = await SendAsync<List<SessionSummaryResponse>>(
            HttpMethod.Get, $"api/v1/projects/{projectId}/sessions", token, body: null, cancellationToken);
        if (sessions.Any(session => session is null || session.ProjectId != projectId))
        {
            throw ForgehandApiException.For(ApiFailure.UnexpectedResponse, _endpoint);
        }

        return sessions;
    }

    public Task<SessionSummaryResponse> GetSessionAsync(
        string token,
        long sessionId,
        CancellationToken cancellationToken) =>
        SendAsync<SessionSummaryResponse>(
            HttpMethod.Get, $"api/v1/sessions/{sessionId}", token, body: null, cancellationToken);

    public Task<SessionSummaryResponse> CreateSessionAsync(
        string token,
        long projectId,
        CreateSessionRequest request,
        CancellationToken cancellationToken) =>
        SendAsync<SessionSummaryResponse>(
            HttpMethod.Post, $"api/v1/projects/{projectId}/sessions", token, request, cancellationToken);

    public Task<RunSessionResponse> RunSessionAsync(
        string token,
        long sessionId,
        CancellationToken cancellationToken) =>
        SendAsync<RunSessionResponse>(
            HttpMethod.Post, $"api/v1/sessions/{sessionId}/run", token, body: null, cancellationToken);

    public async Task<ExecutionHistoryResponse> GetSessionExecutionsAsync(
        string token,
        long sessionId,
        CancellationToken cancellationToken)
    {
        var history = await SendAsync<ExecutionHistoryResponse>(
            HttpMethod.Get, $"api/v1/sessions/{sessionId}/executions", token, body: null, cancellationToken);
        if (history.Executions is null || history.Executions.Any(item => item is null || item.SessionId != sessionId))
            throw ForgehandApiException.For(ApiFailure.UnexpectedResponse, _endpoint);
        return history;
    }

    public async Task<ExecutionRecordResponse> GetSessionExecutionAsync(
        string token,
        long sessionId,
        long executionId,
        CancellationToken cancellationToken)
    {
        var offset = 0;
        ExecutionDetailResponse? execution = null;
        ModelResultResponse? firstResult = null;
        var content = new StringBuilder();
        var receivedCharacters = 0;
        while (true)
        {
            var page = await SendAsync<ExecutionRecordResponse>(
                HttpMethod.Get,
                $"api/v1/sessions/{sessionId}/executions/{executionId}?offset={offset}&limit={ResultPageCharacters}",
                token,
                body: null,
                cancellationToken);
            if (page.Execution is null || page.Execution.Id != executionId || page.Execution.SessionId != sessionId)
                throw ForgehandApiException.For(ApiFailure.UnexpectedResponse, _endpoint);
            execution ??= page.Execution;
            if (page.Result is null)
                return new ExecutionRecordResponse(execution, null);
            if (page.Result.ExecutionId != executionId || page.Result.ContentOffset != offset ||
                page.Result.TotalCharacters < 0 || page.Result.TotalCharacters > MaxResultCharacters)
                throw page.Result.TotalCharacters > MaxResultCharacters
                    ? ForgehandApiException.For(ApiFailure.ResultTooLarge, _endpoint)
                    : ForgehandApiException.For(ApiFailure.UnexpectedResponse, _endpoint);

            firstResult ??= page.Result;
            content.Append(page.Result.Content);
            receivedCharacters += page.Result.Content.EnumerateRunes().Count();
            if (receivedCharacters > MaxResultCharacters)
                throw ForgehandApiException.For(ApiFailure.ResultTooLarge, _endpoint);
            if (page.Result.Complete)
            {
                if (receivedCharacters != page.Result.TotalCharacters)
                    throw ForgehandApiException.For(ApiFailure.UnexpectedResponse, _endpoint);
                return new ExecutionRecordResponse(execution, firstResult with
                {
                    Content = content.ToString(),
                    ContentOffset = 0,
                    NextOffset = 0,
                    Complete = true,
                });
            }
            if (page.Result.NextOffset <= offset || page.Result.NextOffset != receivedCharacters)
                throw ForgehandApiException.For(ApiFailure.UnexpectedResponse, _endpoint);
            offset = page.Result.NextOffset;
        }
    }

    public void Dispose()
    {
        _httpClient.Dispose();
    }

    private static SocketsHttpHandler CreateTransport() => new()
    {
        // A redirect would send the bearer token to a location the user did not configure.
        AllowAutoRedirect = false,
        // A system proxy would receive credentials. The client connects to the configured address directly.
        UseProxy = false,
    };

    private async Task<T> SendAsync<T>(
        HttpMethod method,
        string path,
        string? token,
        object? body,
        CancellationToken cancellationToken)
        where T : class
    {
        var text = await ExecuteAsync(method, path, token, body, cancellationToken);
        try
        {
            return JsonSerializer.Deserialize<T>(text, JsonOptions)
                ?? throw ForgehandApiException.For(ApiFailure.UnexpectedResponse, _endpoint);
        }
        catch (JsonException)
        {
            throw ForgehandApiException.For(ApiFailure.UnexpectedResponse, _endpoint);
        }
    }

    private async Task<string> ExecuteAsync(
        HttpMethod method,
        string path,
        string? token,
        object? body,
        CancellationToken cancellationToken)
    {
        using var request = new HttpRequestMessage(method, path);
        request.Headers.Accept.Add(new MediaTypeWithQualityHeaderValue("application/json"));
        request.Headers.Add(CompatibilityHeader, SupportedApiVersion);
        if (token is not null)
        {
            request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", token);
        }

        if (body is not null)
        {
            request.Content = new StringContent(
                JsonSerializer.Serialize(body, JsonOptions),
                Encoding.UTF8,
                "application/json");
        }

        try
        {
            using var response = await _httpClient.SendAsync(
                request, HttpCompletionOption.ResponseHeadersRead, cancellationToken);
            var text = await ReadBoundedAsync(response.Content, cancellationToken);
            if (response.IsSuccessStatusCode)
            {
                return text;
            }

            throw ErrorFor(response.StatusCode, text);
        }
        catch (HttpRequestException)
        {
            throw ForgehandApiException.For(ApiFailure.Unreachable, _endpoint);
        }
        catch (IOException)
        {
            throw ForgehandApiException.For(ApiFailure.Unreachable, _endpoint);
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            throw ForgehandApiException.For(ApiFailure.Timeout, _endpoint);
        }
    }

    private static async Task<string> ReadBoundedAsync(HttpContent content, CancellationToken cancellationToken)
    {
        await using var stream = await content.ReadAsStreamAsync(cancellationToken);
        using var buffer = new MemoryStream();
        var chunk = new byte[8192];
        int read;
        while ((read = await stream.ReadAsync(chunk.AsMemory(), cancellationToken)) > 0)
        {
            if (buffer.Length + read > MaxResponseBytes)
            {
                throw ForgehandApiException.For(ApiFailure.UnexpectedResponse);
            }

            buffer.Write(chunk, 0, read);
        }

        return Encoding.UTF8.GetString(buffer.GetBuffer(), 0, (int)buffer.Length);
    }

    private ForgehandApiException ErrorFor(HttpStatusCode status, string body)
    {
        var code = ReadErrorCode(body);
        var failure = (status, code) switch
        {
            (HttpStatusCode.Unauthorized, "invalid_credentials") => ApiFailure.InvalidCredentials,
            (HttpStatusCode.Unauthorized, "unauthorized") => ApiFailure.SessionExpired,
            (HttpStatusCode.Forbidden, "enrollment_required") => ApiFailure.EnrollmentRequired,
            (HttpStatusCode.Forbidden, "account_disabled") => ApiFailure.AccountDisabled,
            (HttpStatusCode.Forbidden, "forbidden") => ApiFailure.Forbidden,
            (HttpStatusCode.NotFound, _) => ApiFailure.NotFound,
            (HttpStatusCode.Conflict, "session_conflict") => ApiFailure.SessionConflict,
            (HttpStatusCode.ServiceUnavailable, "execution_unavailable") => ApiFailure.ExecutionUnavailable,
            (HttpStatusCode.UpgradeRequired, "incompatible_api") => ApiFailure.IncompatibleApi,
            (HttpStatusCode.TooManyRequests, "account_locked") => ApiFailure.AccountLocked,
            (HttpStatusCode.TooManyRequests, "throttled") => ApiFailure.Throttled,
            (HttpStatusCode.BadRequest, "invalid_request") => ApiFailure.InvalidRequest,
            _ when (int)status >= 500 => ApiFailure.ServerError,
            _ => ApiFailure.UnexpectedResponse,
        };

        return ForgehandApiException.For(failure, _endpoint);
    }

    private static string? ReadErrorCode(string body)
    {
        try
        {
            return JsonSerializer.Deserialize<ErrorEnvelope>(body, JsonOptions)?.Error?.Code;
        }
        catch (JsonException)
        {
            return null;
        }
    }
}
