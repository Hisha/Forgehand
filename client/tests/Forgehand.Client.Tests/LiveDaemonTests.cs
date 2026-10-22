using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Configuration;

namespace Forgehand.Client.Tests;

/// <summary>
/// Opt-in checks against a running Forgehand daemon. They are skipped unless
/// FORGEHAND_CLIENT_LIVE_ENDPOINT is set, for example http://127.0.0.1:8787.
/// They use no real credentials: every authenticated call uses a token that the
/// daemon has never issued, and the login uses an account name that does not exist.
/// </summary>
public sealed class LiveDaemonTests
{
    private const string EndpointVariable = "FORGEHAND_CLIENT_LIVE_ENDPOINT";

    private static ForgehandApiClient? CreateLiveClient()
    {
        var configured = Environment.GetEnvironmentVariable(EndpointVariable);
        if (string.IsNullOrWhiteSpace(configured))
        {
            return null;
        }

        Assert.True(ServerEndpoint.TryParse(configured, out var endpoint, out var error), error);
        return new ForgehandApiClient(endpoint);
    }

    [Fact]
    public async Task HealthReportsSupportedApiVersion()
    {
        using var client = CreateLiveClient();
        if (client is null)
        {
            return;
        }

        var health = await client.GetHealthAsync(CancellationToken.None);

        Assert.Equal("ok", health.Status);
        Assert.Equal(ForgehandApiClient.SupportedApiVersion, health.ApiVersion);
    }

    [Fact]
    public async Task ProjectsRejectsUnissuedToken()
    {
        using var client = CreateLiveClient();
        if (client is null)
        {
            return;
        }

        var exception = await Assert.ThrowsAsync<ForgehandApiException>(() =>
            client.GetProjectsAsync("not-an-issued-token", CancellationToken.None));

        Assert.Equal(ApiFailure.SessionExpired, exception.Failure);
    }

    [Fact]
    public async Task LoginRejectsUnknownAccountWithGenericFailure()
    {
        using var client = CreateLiveClient();
        if (client is null)
        {
            return;
        }

        var exception = await Assert.ThrowsAsync<ForgehandApiException>(() =>
            client.LoginAsync(new LoginRequest("no-such-account-for-client-tests", "not-a-password", "000000"), CancellationToken.None));

        Assert.Equal(ApiFailure.InvalidCredentials, exception.Failure);
    }

    [Fact]
    public async Task LogoutWithUnissuedTokenIsReportedAsExpiredSession()
    {
        using var client = CreateLiveClient();
        if (client is null)
        {
            return;
        }

        var exception = await Assert.ThrowsAsync<ForgehandApiException>(() =>
            client.LogoutAsync("not-an-issued-token", CancellationToken.None));

        Assert.Equal(ApiFailure.SessionExpired, exception.Failure);
    }
}
