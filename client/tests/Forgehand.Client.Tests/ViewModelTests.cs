using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Configuration;
using Forgehand.Client.Core.Session;
using Forgehand.Client.Core.ViewModels;

namespace Forgehand.Client.Tests;

public sealed class LoginViewModelTests
{
    private static (LoginViewModel Login, FakeForgehandApi Api) Create(FakeForgehandApi? api = null)
    {
        var fake = api ?? new FakeForgehandApi();
        var service = new ForgehandService(ServerEndpoint.Default, _ => fake);
        return (new LoginViewModel(service), fake);
    }

    [Fact]
    public async Task InvalidCodeShowsValidationErrorWithoutCallingServer()
    {
        var (login, api) = Create();
        login.Username = "alice";
        login.Password = "pw";
        login.TotpCode = "12";

        await login.SignInCommand.ExecuteAsync(null);

        Assert.Equal("Enter the six-digit code from your authenticator app.", login.ErrorMessage);
        Assert.True(login.HasError);
        Assert.Equal(0, api.LoginCalls);
    }

    [Fact]
    public async Task SuccessfulSignInClearsSecretsFromTheForm()
    {
        var (login, _) = Create();
        login.Username = "alice";
        login.Password = "pw";
        login.TotpCode = "123456";

        await login.SignInCommand.ExecuteAsync(null);

        Assert.Equal(string.Empty, login.Password);
        Assert.Equal(string.Empty, login.TotpCode);
        Assert.False(login.HasError);
        Assert.False(login.IsBusy);
    }

    [Fact]
    public async Task FailedSignInShowsGenericMessageAndClearsSecrets()
    {
        var api = new FakeForgehandApi
        {
            Login = _ => Task.FromException<SessionResponse>(ForgehandApiException.For(ApiFailure.InvalidCredentials)),
        };
        var (login, _) = Create(api);
        login.Username = "alice";
        login.Password = "wrong";
        login.TotpCode = "654321";

        await login.SignInCommand.ExecuteAsync(null);

        Assert.Equal("The username, password, or authenticator code was not accepted.", login.ErrorMessage);
        Assert.Equal("alice", login.Username);
        Assert.Equal(string.Empty, login.Password);
        Assert.Equal(string.Empty, login.TotpCode);
        Assert.False(login.IsBusy);
    }
}

public sealed class ProjectsViewModelTests
{
    private static async Task<ForgehandService> SignedInServiceAsync(FakeForgehandApi api)
    {
        var service = new ForgehandService(ServerEndpoint.Default, _ => api);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        return service;
    }

    [Fact]
    public async Task EmptyAuthorizedListShowsEmptyState()
    {
        var service = await SignedInServiceAsync(new FakeForgehandApi());
        var projects = new ProjectsViewModel(service);

        await projects.LoadAsync();

        Assert.Empty(projects.Projects);
        Assert.True(projects.ShowEmptyState);
        Assert.False(projects.ShowProjects);
        Assert.False(projects.HasError);
    }

    [Fact]
    public async Task ProjectsShowIdentityAndOmitMissingRootPath()
    {
        var api = new FakeForgehandApi
        {
            Projects = _ => Task.FromResult<IReadOnlyList<ProjectResponse>>(
            [
                new ProjectResponse(1, "Alpha", "/srv/alpha"),
                new ProjectResponse(2, "Beta", ""),
            ]),
        };
        var service = await SignedInServiceAsync(api);
        var projects = new ProjectsViewModel(service);

        await projects.LoadAsync();

        Assert.Equal(2, projects.Projects.Count);
        Assert.Equal("Alpha", projects.Projects[0].Name);
        Assert.Equal("ID 1", projects.Projects[0].IdText);
        Assert.Equal("/srv/alpha", projects.Projects[0].RootPath);
        Assert.True(projects.Projects[0].HasRootPath);
        Assert.False(projects.Projects[1].HasRootPath);
        Assert.False(projects.ShowEmptyState);
    }

    [Fact]
    public async Task FailedLoadClearsPreviousProjectsAndShowsError()
    {
        var api = new FakeForgehandApi
        {
            Projects = _ => Task.FromException<IReadOnlyList<ProjectResponse>>(
                new ForgehandApiException(ApiFailure.Unreachable, "unreachable")),
        };
        var service = await SignedInServiceAsync(api);
        var projects = new ProjectsViewModel(service);
        projects.Projects.Add(new ProjectItem(new ProjectResponse(9, "Stale", null)));

        await projects.LoadAsync();

        Assert.Empty(projects.Projects);
        Assert.True(projects.HasError);
        Assert.Equal("unreachable", projects.ErrorMessage);
        Assert.False(projects.ShowEmptyState);
    }

    [Fact]
    public async Task ClearRemovesAllProjectData()
    {
        var api = new FakeForgehandApi
        {
            Projects = _ => Task.FromResult<IReadOnlyList<ProjectResponse>>([new ProjectResponse(1, "Alpha", null)]),
        };
        var service = await SignedInServiceAsync(api);
        var projects = new ProjectsViewModel(service);
        await projects.LoadAsync();

        projects.Clear();

        Assert.Empty(projects.Projects);
        Assert.False(projects.HasLoaded);
        Assert.False(projects.ShowEmptyState);
    }
}

public sealed class ShellViewModelTests : IDisposable
{
    private readonly string _settingsPath = Path.Combine(
        Path.GetTempPath(),
        "forgehand-shell-tests-" + Guid.NewGuid().ToString("N"),
        "client-settings.json");

    public void Dispose()
    {
        var directory = Path.GetDirectoryName(_settingsPath);
        if (directory is not null && Directory.Exists(directory))
        {
            Directory.Delete(directory, recursive: true);
        }
    }

    [Fact]
    public async Task SignOutFailureShowsNoticeAndLocksServerSelectionOnlyWhileSignedIn()
    {
        var api = new FakeForgehandApi
        {
            Logout = _ => Task.FromException(new ForgehandApiException(ApiFailure.Unreachable, "unreachable")),
        };
        var service = new ForgehandService(ServerEndpoint.Default, _ => api);
        var shell = new ShellViewModel(service, new ClientSettingsStore(_settingsPath), notice: null);

        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        Assert.True(shell.IsSignedIn);
        Assert.True(shell.IsEndpointLocked);
        Assert.False(shell.ApplyEndpointCommand.CanExecute(null));

        await shell.SignOutCommand.ExecuteAsync(null);

        Assert.False(shell.IsSignedIn);
        Assert.False(shell.IsEndpointLocked);
        Assert.True(shell.HasNotice);
        Assert.Contains("did not confirm", shell.Notice);
        Assert.Empty(shell.Projects.Projects);
    }

    [Fact]
    public void StartupNoticeIsShown()
    {
        var service = new ForgehandService(ServerEndpoint.Default, _ => new FakeForgehandApi());
        var shell = new ShellViewModel(service, new ClientSettingsStore(_settingsPath), "saved setting ignored");

        Assert.Equal("saved setting ignored", shell.Notice);
        Assert.True(shell.HasNotice);
        Assert.False(shell.IsSignedIn);
    }
}
