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

    /// <summary>Upper bound on a response body. The API returns small JSON documents.</summary>
    public const int MaxResponseBytes = 1024 * 1024;

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
