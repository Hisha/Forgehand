using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Configuration;
using Forgehand.Client.Core.Session;

namespace Forgehand.Client.Tests;

public sealed class ForgehandServiceTests
{
    private static (ForgehandService Service, FakeForgehandApi Api) CreateService(FakeForgehandApi? api = null)
    {
        var fake = api ?? new FakeForgehandApi();
        var service = new ForgehandService(ServerEndpoint.Default, _ => fake);
        return (service, fake);
    }

    private static ForgehandApiException Failure(ApiFailure failure) =>
        new(failure, "test failure");

    [Fact]
    public async Task SignInStartsSessionAndRaisesSignedIn()
    {
        var (service, _) = CreateService();
        var changes = new List<SessionChange>();
        service.SessionChanged += changes.Add;

        await service.SignInAsync(" alice ", "pw", "123456", CancellationToken.None);

        Assert.True(service.IsSignedIn);
        Assert.Equal("alice", service.Account!.Username);
        Assert.NotNull(service.SessionExpiresAt);
        Assert.Equal(SessionChange.SignedIn, Assert.Single(changes));
    }

    [Fact]
    public async Task SignInSendsTrimmedCredentialsToApi()
    {
        LoginRequest? received = null;
        var api = new FakeForgehandApi
        {
            Login = request =>
            {
                received = request;
                return Task.FromResult(TestData.Session());
            },
        };
        var (service, _) = CreateService(api);

        await service.SignInAsync("alice", "pw", " 123456 ", CancellationToken.None);

        Assert.Equal(new LoginRequest("alice", "pw", "123456"), received);
    }

    [Theory]
    [InlineData("", "pw", "123456")]
    [InlineData("alice", "", "123456")]
    [InlineData("alice", "pw", "12345")]
    [InlineData("alice", "pw", "1234567")]
    [InlineData("alice", "pw", "12345a")]
    public async Task InvalidInputIsRejectedBeforeAnyRequest(string username, string password, string totp)
    {
        var (service, api) = CreateService();

        var exception = await Assert.ThrowsAsync<ForgehandApiException>(() =>
            service.SignInAsync(username, password, totp, CancellationToken.None));

        Assert.Equal(ApiFailure.InvalidRequest, exception.Failure);
        Assert.Equal(0, api.LoginCalls);
        Assert.False(service.IsSignedIn);
    }

    [Fact]
    public async Task FailedSignInLeavesServiceSignedOutWithoutEvent()
    {
        var api = new FakeForgehandApi
        {
            Login = _ => Task.FromException<SessionResponse>(Failure(ApiFailure.InvalidCredentials)),
        };
        var (service, _) = CreateService(api);
        var changes = new List<SessionChange>();
        service.SessionChanged += changes.Add;

        await Assert.ThrowsAsync<ForgehandApiException>(() =>
            service.SignInAsync("alice", "wrong", "123456", CancellationToken.None));

        Assert.False(service.IsSignedIn);
        Assert.Null(service.Account);
        Assert.Empty(changes);
    }

    [Fact]
    public async Task ProjectsRequireASession()
    {
        var (service, _) = CreateService();

        await Assert.ThrowsAsync<InvalidOperationException>(() =>
            service.GetProjectsAsync(CancellationToken.None));
    }

    [Fact]
    public async Task EmptyAuthorizedProjectListIsReturnedAsIs()
    {
        var (service, _) = CreateService();
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);

        var projects = await service.GetProjectsAsync(CancellationToken.None);

        Assert.Empty(projects);
        Assert.True(service.IsSignedIn);
    }

    [Fact]
    public async Task ProjectsUseTheSessionTokenIssuedAtLogin()
    {
        string? usedToken = null;
        var api = new FakeForgehandApi
        {
            Login = _ => Task.FromResult(TestData.Session("issued-token")),
            Projects = token =>
            {
                usedToken = token;
                return Task.FromResult<IReadOnlyList<ProjectResponse>>(
                    [new ProjectResponse(1, "Alpha", "/srv/alpha")]);
            },
        };
        var (service, _) = CreateService(api);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);

        var projects = await service.GetProjectsAsync(CancellationToken.None);

        Assert.Equal("issued-token", usedToken);
        Assert.Equal("Alpha", Assert.Single(projects).Name);
    }

    [Fact]
    public async Task ExpiredSessionEndsSessionAndRaisesExpired()
    {
        var api = new FakeForgehandApi
        {
            Projects = _ => Task.FromException<IReadOnlyList<ProjectResponse>>(Failure(ApiFailure.SessionExpired)),
        };
        var (service, _) = CreateService(api);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        var changes = new List<SessionChange>();
        service.SessionChanged += changes.Add;

        await Assert.ThrowsAsync<ForgehandApiException>(() => service.GetProjectsAsync(CancellationToken.None));

        Assert.False(service.IsSignedIn);
        Assert.Equal(SessionChange.Expired, Assert.Single(changes));
    }

    [Fact]
    public async Task SignOutClearsSessionBeforeContactingServer()
    {
        var (service, api) = CreateService();
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        var stateDuringLogout = true;
        string? tokenSent = null;
        api.Logout = token =>
        {
            stateDuringLogout = service.IsSignedIn;
            tokenSent = token;
            return Task.CompletedTask;
        };
        var changes = new List<SessionChange>();
        service.SessionChanged += changes.Add;

        var confirmed = await service.SignOutAsync();

        Assert.True(confirmed);
        Assert.False(stateDuringLogout);
        Assert.Equal("tok-123", tokenSent);
        Assert.False(service.IsSignedIn);
        Assert.Null(service.Account);
        Assert.Equal(SessionChange.SignedOut, Assert.Single(changes));
    }

    [Fact]
    public async Task SignOutFailureStillDropsTokenLocally()
    {
        var (service, api) = CreateService();
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        api.Logout = _ => Task.FromException(Failure(ApiFailure.Unreachable));

        var confirmed = await service.SignOutAsync();

        Assert.False(confirmed);
        Assert.False(service.IsSignedIn);
        await Assert.ThrowsAsync<InvalidOperationException>(() => service.GetProjectsAsync(CancellationToken.None));
    }

    [Fact]
    public async Task SignOutWhenAlreadyExpiredReportsSuccess()
    {
        var (service, api) = CreateService();
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        api.Logout = _ => Task.FromException(Failure(ApiFailure.SessionExpired));

        Assert.True(await service.SignOutAsync());
        Assert.False(service.IsSignedIn);
    }

    [Fact]
    public async Task ProjectsArrivingAfterSignOutAreDiscarded()
    {
        var pending = new TaskCompletionSource<IReadOnlyList<ProjectResponse>>();
        var api = new FakeForgehandApi { Projects = _ => pending.Task };
        var (service, _) = CreateService(api);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);

        var load = service.GetProjectsAsync(CancellationToken.None);
        await service.SignOutAsync();
        pending.SetResult([new ProjectResponse(1, "Alpha", null)]);

        await Assert.ThrowsAsync<OperationCanceledException>(() => load);
    }

    [Fact]
    public async Task EndpointCannotChangeWhileSignedIn()
    {
        var (service, _) = CreateService();
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);

        var other = ServerEndpoint.TryParse("http://localhost:9999", out var endpoint, out _) ? endpoint! : throw new InvalidOperationException();
        Assert.Throws<InvalidOperationException>(() => service.ChangeEndpoint(other));
        Assert.Equal(ServerEndpoint.DefaultAddress, service.Endpoint.DisplayText);
    }

    [Fact]
    public void EndpointChangeCreatesClientForNewAddress()
    {
        var created = new List<string>();
        var service = new ForgehandService(ServerEndpoint.Default, endpoint =>
        {
            created.Add(endpoint.DisplayText);
            return new FakeForgehandApi();
        });
        ServerEndpoint.TryParse("http://localhost:9999", out var other, out _);

        service.ChangeEndpoint(other!);

        Assert.Equal(new[] { "http://127.0.0.1:8787", "http://localhost:9999" }, created);
        Assert.Equal("http://localhost:9999", service.Endpoint.DisplayText);
    }

    [Fact]
    public async Task CheckConnectionRejectsUnsupportedApiVersion()
    {
        var api = new FakeForgehandApi
        {
            Health = _ => Task.FromResult(new HealthResponse("ok", "9.9.9", "b", "v2", "t")),
        };
        var (service, _) = CreateService(api);

        var exception = await Assert.ThrowsAsync<ForgehandApiException>(() =>
            service.CheckConnectionAsync(CancellationToken.None));

        Assert.Equal(ApiFailure.IncompatibleApi, exception.Failure);
    }
}
