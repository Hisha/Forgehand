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

    public ServerEndpoint Endpoint { get; private set; }

    public bool IsSignedIn => _session is not null;

    public AccountResponse? Account => _session?.Account;

    public DateTimeOffset? SessionExpiresAt => _session?.ExpiresAt;

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
    }

    public async Task<HealthResponse> CheckConnectionAsync(CancellationToken cancellationToken)
    {
        var health = await _api.GetHealthAsync(cancellationToken);
        if (health.ApiVersion != ForgehandApiClient.SupportedApiVersion)
        {
            throw ForgehandApiException.For(ApiFailure.IncompatibleApi, Endpoint);
        }

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
        var session = _session ?? throw new InvalidOperationException("Sign in before loading projects.");
        try
        {
            var projects = await _api.GetProjectsAsync(session.Token, cancellationToken);
            if (!ReferenceEquals(session, _session))
            {
                // Sign-out happened while the request was in flight. Discard the result.
                throw new OperationCanceledException("The session ended before the project list was received.");
            }

            return projects;
        }
        catch (ForgehandApiException exception) when (exception.Failure == ApiFailure.SessionExpired)
        {
            EndSession(SessionChange.Expired, session);
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
}
