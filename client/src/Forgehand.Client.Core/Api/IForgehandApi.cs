namespace Forgehand.Client.Core.Api;

/// <summary>The subset of the daemon's remote API used by the desktop client.</summary>
public interface IForgehandApi : IDisposable
{
    Task<HealthResponse> GetHealthAsync(CancellationToken cancellationToken);

    Task<SessionResponse> LoginAsync(LoginRequest request, CancellationToken cancellationToken);

    Task LogoutAsync(string token, CancellationToken cancellationToken);

    Task<IReadOnlyList<ProjectResponse>> GetProjectsAsync(string token, CancellationToken cancellationToken);
}
