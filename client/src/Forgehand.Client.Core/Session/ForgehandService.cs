using System.Globalization;
using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Configuration;

namespace Forgehand.Client.Core.Session;

public enum SessionChange
{
    SignedIn,
    SignedOut,
    Expired,
}

public enum ApiCompatibilityState
{
    Unknown,
    Compatible,
    Incompatible,
}

/// <summary>
/// Owns the daemon connection and the in-memory session. View models call this
/// service and never handle tokens or HTTP directly.
/// </summary>
public sealed class ForgehandService : IDisposable
{
    private readonly Func<ServerEndpoint, IForgehandApi> _apiFactory;
    private IForgehandApi _api;
    private SignedInSession? _session;

    public ForgehandService(ServerEndpoint endpoint, Func<ServerEndpoint, IForgehandApi> apiFactory)
    {
        _apiFactory = apiFactory;
        Endpoint = endpoint;
        _api = apiFactory(endpoint);
    }

    public event Action<SessionChange>? SessionChanged;

    public event Action? CompatibilityChanged;

    public ServerEndpoint Endpoint { get; private set; }

    public bool IsSignedIn => _session is not null;

    public AccountResponse? Account => _session?.Account;

    public DateTimeOffset? SessionExpiresAt => _session?.ExpiresAt;

    public ApiCompatibilityState ApiCompatibility { get; private set; }

    public HealthResponse? DaemonHealth { get; private set; }

    public bool IsApiCompatible => ApiCompatibility == ApiCompatibilityState.Compatible;

    public string CompatibilityExplanation => ApiCompatibility switch
    {
        ApiCompatibilityState.Incompatible when DaemonHealth is not null && string.IsNullOrWhiteSpace(DaemonHealth.ApiVersion) =>
            $"Session changes are disabled because the daemon did not report HTTP API compatibility metadata. " +
            $"This client requires {ForgehandApiClient.SupportedApiVersion}.",
        ApiCompatibilityState.Incompatible when DaemonHealth is not null =>
            $"Session changes are disabled because daemon HTTP API {DaemonHealth!.ApiVersion} is not compatible " +
            $"with client API {ForgehandApiClient.SupportedApiVersion}.",
        ApiCompatibilityState.Incompatible =>
            $"Session changes are disabled because the daemon rejected client HTTP API " +
            $"{ForgehandApiClient.SupportedApiVersion} as incompatible.",
        _ => "Session changes are disabled until HTTP API compatibility has been confirmed.",
    };

    /// <summary>
    /// Switches to another daemon. It is refused while signed in so that a bearer
    /// token is never sent to a server other than the one that issued it.
    /// </summary>
    public void ChangeEndpoint(ServerEndpoint endpoint)
    {
        if (_session is not null)
        {
            throw new InvalidOperationException("Sign out before changing the server.");
        }

        _api.Dispose();
        Endpoint = endpoint;
        _api = _apiFactory(endpoint);
        SetCompatibility(ApiCompatibilityState.Unknown, health: null);
    }

    public async Task<HealthResponse> CheckConnectionAsync(CancellationToken cancellationToken)
    {
        HealthResponse health;
        try
        {
            health = await _api.GetHealthAsync(cancellationToken);
        }
        catch
        {
            SetCompatibility(ApiCompatibilityState.Unknown, health: null);
            throw;
        }

        if (string.IsNullOrWhiteSpace(health.ApiVersion)
            || !string.Equals(health.ApiVersion, ForgehandApiClient.SupportedApiVersion, StringComparison.Ordinal))
        {
            SetCompatibility(ApiCompatibilityState.Incompatible, health);
            throw ForgehandApiException.For(ApiFailure.IncompatibleApi, Endpoint);
        }

        SetCompatibility(ApiCompatibilityState.Compatible, health);
        return health;
    }

    public async Task SignInAsync(string username, string password, string totpCode, CancellationToken cancellationToken)
    {
        if (_session is not null)
        {
            throw new InvalidOperationException("A session is already active.");
        }

        var validation = LoginValidation.Validate(username, password, totpCode);
        if (validation is not null)
        {
            throw new ForgehandApiException(ApiFailure.InvalidRequest, validation);
        }

        await CheckConnectionAsync(cancellationToken);

        var response = await _api.LoginAsync(
            new LoginRequest(username.Trim(), password, totpCode.Trim()),
            cancellationToken);

        if (!DateTimeOffset.TryParse(
                response.ExpiresAt,
                CultureInfo.InvariantCulture,
                DateTimeStyles.AssumeUniversal,
                out var expiresAt))
        {
            throw ForgehandApiException.For(ApiFailure.UnexpectedResponse, Endpoint);
        }

        _session = new SignedInSession(response.Token, response.Account, expiresAt);
        SessionChanged?.Invoke(SessionChange.SignedIn);
    }

    public async Task<IReadOnlyList<ProjectResponse>> GetProjectsAsync(CancellationToken cancellationToken)
    {
        return await WithSessionAsync(
            (api, token, cancellation) => api.GetProjectsAsync(token, cancellation),
            cancellationToken);
    }

    public async Task<IReadOnlyList<SessionSummaryResponse>> GetProjectSessionsAsync(
        long projectId,
        CancellationToken cancellationToken)
    {
        return await WithSessionAsync(
            (api, token, cancellation) => api.GetProjectSessionsAsync(token, projectId, cancellation),
            cancellationToken);
    }

    public async Task<SessionSummaryResponse> GetSessionAsync(long sessionId, CancellationToken cancellationToken)
    {
        return await WithSessionAsync(
            (api, token, cancellation) => api.GetSessionAsync(token, sessionId, cancellation),
            cancellationToken);
    }

    public async Task<SessionSummaryResponse> CreateSessionAsync(
        long projectId,
        string title,
        string task,
        CancellationToken cancellationToken)
    {
        EnsureCompatible();
        return await WithSessionAsync(
            (api, token, cancellation) => api.CreateSessionAsync(
                token, projectId, new CreateSessionRequest(title, task), cancellation),
            cancellationToken);
    }

    public async Task<RunSessionResponse> RunSessionAsync(long sessionId, CancellationToken cancellationToken)
    {
        EnsureCompatible();
        return await WithSessionAsync(
            (api, token, cancellation) => api.RunSessionAsync(token, sessionId, cancellation),
            cancellationToken);
    }

    public async Task<ExecutionHistoryResponse> GetSessionExecutionsAsync(
        long sessionId,
        CancellationToken cancellationToken)
    {
        return await WithSessionAsync(
            (api, token, cancellation) => api.GetSessionExecutionsAsync(token, sessionId, cancellation),
            cancellationToken);
    }

    public async Task<ExecutionRecordResponse> GetSessionExecutionAsync(
        long sessionId,
        long executionId,
        CancellationToken cancellationToken)
    {
        return await WithSessionAsync(
            (api, token, cancellation) => api.GetSessionExecutionAsync(token, sessionId, executionId, cancellation),
            cancellationToken);
    }

    private async Task<T> WithSessionAsync<T>(
        Func<IForgehandApi, string, CancellationToken, Task<T>> request,
        CancellationToken cancellationToken)
    {
        var session = _session ?? throw new InvalidOperationException("Sign in before loading Forgehand data.");
        try
        {
            var result = await request(_api, session.Token, cancellationToken);
            if (!ReferenceEquals(session, _session))
            {
                throw new OperationCanceledException("The session ended before the response was received.");
            }

            return result;
        }
        catch (ForgehandApiException exception) when (exception.Failure == ApiFailure.SessionExpired)
        {
            EndSession(SessionChange.Expired, session);
            throw;
        }
        catch (ForgehandApiException exception) when (exception.Failure == ApiFailure.IncompatibleApi)
        {
            SetCompatibility(ApiCompatibilityState.Incompatible, health: null);
            throw;
        }
    }

    /// <summary>
    /// Clears the local session before contacting the server, so the token is
    /// unusable in the client even if the request fails. Returns false when the
    /// server did not confirm the sign-out.
    /// </summary>
    public async Task<bool> SignOutAsync()
    {
        var session = _session;
        if (session is null)
        {
            return true;
        }

        EndSession(SessionChange.SignedOut, session);
        try
        {
            await _api.LogoutAsync(session.Token, CancellationToken.None);
            return true;
        }
        catch (ForgehandApiException exception) when (exception.Failure == ApiFailure.SessionExpired)
        {
            return true;
        }
        catch (ForgehandApiException)
        {
            return false;
        }
    }

    public void Dispose()
    {
        _api.Dispose();
    }

    private void EndSession(SessionChange change, SignedInSession session)
    {
        if (!ReferenceEquals(session, _session))
        {
            return;
        }

        _session = null;
        SessionChanged?.Invoke(change);
    }

    private void EnsureCompatible()
    {
        if (!IsApiCompatible)
        {
            throw ForgehandApiException.For(ApiFailure.IncompatibleApi, Endpoint);
        }
    }

    private void SetCompatibility(ApiCompatibilityState state, HealthResponse? health)
    {
        if (ApiCompatibility == state && Equals(DaemonHealth, health))
        {
            return;
        }

        ApiCompatibility = state;
        DaemonHealth = health;
        CompatibilityChanged?.Invoke();
    }
}
