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

public sealed class WorkspaceViewModelTests
{
    private static async Task<(ForgehandService Service, ProjectsViewModel Workspace)> CreateAsync(FakeForgehandApi api)
    {
        var service = new ForgehandService(ServerEndpoint.Default, _ => api);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        var workspace = new ProjectsViewModel(service);
        await workspace.LoadAsync();
        return (service, workspace);
    }

    private static FakeForgehandApi ApiWithProjects(params ProjectResponse[] projects) => new()
    {
        Projects = _ => Task.FromResult<IReadOnlyList<ProjectResponse>>(projects),
    };

    [Fact]
    public async Task SelectingProjectLoadsItsSessions()
    {
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", "/srv/alpha"));
        api.ProjectSessions = (_, projectId, _) => Task.FromResult<IReadOnlyList<SessionSummaryResponse>>(
            [TestData.SessionSummary(11, projectId)]);
        var (_, workspace) = await CreateAsync(api);

        await workspace.SelectProjectAsync(Assert.Single(workspace.Projects));

        Assert.Equal(1, workspace.SelectedProject!.Id);
        Assert.Equal(11, Assert.Single(workspace.Sessions).Id);
        Assert.True(workspace.HasLoadedSessions);
        Assert.Equal(1, api.ProjectSessionCalls);
    }

    [Fact]
    public async Task SelectingSessionLoadsReadOnlyDetails()
    {
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", "/srv/alpha"));
        api.ProjectSessions = (_, _, _) => Task.FromResult<IReadOnlyList<SessionSummaryResponse>>(
            [TestData.SessionSummary(11, 1)]);
        api.SessionDetails = (_, id, _) => Task.FromResult(TestData.SessionSummary(id, 1, "Detailed title"));
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);

        await workspace.SelectSessionAsync(workspace.Sessions[0]);

        Assert.Equal("Detailed title", workspace.SessionDetails!.Title);
        Assert.True(workspace.HasSessionDetails);
        Assert.Equal(1, api.SessionCalls);
    }

    [Fact]
    public async Task EmptySessionListShowsEmptyState()
    {
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        var (_, workspace) = await CreateAsync(api);

        await workspace.SelectProjectAsync(workspace.Projects[0]);

        Assert.Empty(workspace.Sessions);
        Assert.True(workspace.ShowSessionEmptyState);
    }

    [Fact]
    public async Task SwitchingProjectsDiscardsLateSessionResponse()
    {
        var pending = new TaskCompletionSource<IReadOnlyList<SessionSummaryResponse>>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var api = ApiWithProjects(
            new ProjectResponse(1, "Alpha", null),
            new ProjectResponse(2, "Beta", null));
        api.ProjectSessions = (_, projectId, _) => projectId == 1
            ? pending.Task
            : Task.FromResult<IReadOnlyList<SessionSummaryResponse>>([TestData.SessionSummary(22, 2, "Beta session")]);
        var (_, workspace) = await CreateAsync(api);

        var firstLoad = workspace.SelectProjectAsync(workspace.Projects[0]);
        await workspace.SelectProjectAsync(workspace.Projects[1]);
        pending.SetResult([TestData.SessionSummary(11, 1, "Stale Alpha session")]);
        await firstLoad;

        Assert.Equal(2, workspace.SelectedProject!.Id);
        Assert.Equal("Beta session", Assert.Single(workspace.Sessions).Title);
    }

    [Fact]
    public async Task RefreshingSessionsReplacesOldData()
    {
        var responses = new Queue<IReadOnlyList<SessionSummaryResponse>>();
        responses.Enqueue([TestData.SessionSummary(11, 1, "Before")]);
        responses.Enqueue([TestData.SessionSummary(12, 1, "After")]);
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        api.ProjectSessions = (_, _, _) => Task.FromResult(responses.Dequeue());
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);

        await workspace.RefreshSessionsCommand.ExecuteAsync(null);

        Assert.Equal("After", Assert.Single(workspace.Sessions).Title);
        Assert.Equal(2, api.ProjectSessionCalls);
    }

    [Fact]
    public async Task RefreshRemovesRevokedSelectedProjectAndAllScopedData()
    {
        IReadOnlyList<ProjectResponse> grants = [new ProjectResponse(1, "Alpha", null)];
        var api = new FakeForgehandApi
        {
            Projects = _ => Task.FromResult(grants),
            ProjectSessions = (_, _, _) => Task.FromResult<IReadOnlyList<SessionSummaryResponse>>(
                [TestData.SessionSummary(11, 1)]),
            SessionDetails = (_, _, _) => Task.FromResult(TestData.SessionSummary(11, 1)),
        };
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);
        await workspace.SelectSessionAsync(workspace.Sessions[0]);

        grants = [];
        await workspace.LoadAsync();

        Assert.Empty(workspace.Projects);
        Assert.Null(workspace.SelectedProject);
        Assert.Empty(workspace.Sessions);
        Assert.Null(workspace.SelectedSession);
        Assert.Null(workspace.SessionDetails);
    }

    [Fact]
    public async Task MissingSessionClearsDetailsAndShowsSpecificMessage()
    {
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        api.ProjectSessions = (_, _, _) => Task.FromResult<IReadOnlyList<SessionSummaryResponse>>(
            [TestData.SessionSummary(11, 1)]);
        api.SessionDetails = (_, _, _) => Task.FromException<SessionSummaryResponse>(
            ForgehandApiException.For(ApiFailure.NotFound));
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);

        await workspace.SelectSessionAsync(workspace.Sessions[0]);

        Assert.Null(workspace.SessionDetails);
        Assert.True(workspace.HasSessionDetailError);
        Assert.Contains("no longer available", workspace.SessionDetailErrorMessage);
    }

    [Fact]
    public async Task LogoutWhileSessionsPendingPreventsLateDataFromReturning()
    {
        var pending = new TaskCompletionSource<IReadOnlyList<SessionSummaryResponse>>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        api.ProjectSessions = (_, _, _) => pending.Task;
        var (service, workspace) = await CreateAsync(api);

        var load = workspace.SelectProjectAsync(workspace.Projects[0]);
        await service.SignOutAsync();
        workspace.Clear();
        pending.SetResult([TestData.SessionSummary(11, 1)]);
        await load;

        Assert.Empty(workspace.Projects);
        Assert.Empty(workspace.Sessions);
        Assert.Null(workspace.SessionDetails);
    }

    [Fact]
    public async Task LateSessionDetailsCannotRepopulateAfterNavigation()
    {
        var pending = new TaskCompletionSource<SessionSummaryResponse>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        api.ProjectSessions = (_, _, _) => Task.FromResult<IReadOnlyList<SessionSummaryResponse>>(
            [TestData.SessionSummary(11, 1), TestData.SessionSummary(12, 1)]);
        api.SessionDetails = (_, id, _) => id == 11
            ? pending.Task
            : Task.FromResult(TestData.SessionSummary(12, 1, "Current"));
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);

        var first = workspace.SelectSessionAsync(workspace.Sessions[0]);
        await workspace.SelectSessionAsync(workspace.Sessions[1]);
        pending.SetResult(TestData.SessionSummary(11, 1, "Stale"));
        await first;

        Assert.Equal(12, workspace.SelectedSession!.Id);
        Assert.Equal("Current", workspace.SessionDetails!.Title);
    }

    [Fact]
    public async Task NewSessionFormValidatesRequiredFieldsWithoutCallingApi()
    {
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);
        workspace.OpenNewSessionCommand.Execute(null);
        workspace.NewSessionTitle = "A title";

        await workspace.CreateSessionCommand.ExecuteAsync(null);

        Assert.True(workspace.IsNewSessionFormOpen);
        Assert.True(workspace.HasNewSessionError);
        Assert.Equal(0, api.CreateSessionCalls);
    }

    [Fact]
    public async Task FailedCreationPreservesDraft()
    {
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        api.CreateSession = (_, _, _, _) => Task.FromException<SessionSummaryResponse>(
            ForgehandApiException.For(ApiFailure.ServerError));
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);
        workspace.OpenNewSessionCommand.Execute(null);
        workspace.NewSessionTitle = "Keep this title";
        workspace.NewSessionTask = "Keep this task";

        await workspace.CreateSessionCommand.ExecuteAsync(null);

        Assert.Equal("Keep this title", workspace.NewSessionTitle);
        Assert.Equal("Keep this task", workspace.NewSessionTask);
        Assert.True(workspace.IsNewSessionFormOpen);
        Assert.True(workspace.HasNewSessionError);
    }

    [Fact]
    public async Task SuccessfulCreationRefreshesAndSelectsNewSession()
    {
        IReadOnlyList<SessionSummaryResponse> sessions = [];
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        api.ProjectSessions = (_, _, _) => Task.FromResult(sessions);
        api.CreateSession = (_, projectId, request, _) =>
        {
            var created = TestData.SessionSummary(99, projectId, request.Title) with
            {
                Task = request.Task,
                State = "READY",
            };
            sessions = [created];
            return Task.FromResult(created);
        };
        api.SessionDetails = (_, _, _) => Task.FromResult(sessions[0]);
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);
        workspace.OpenNewSessionCommand.Execute(null);
        workspace.NewSessionTitle = "New work";
        workspace.NewSessionTask = "Implement it";

        await workspace.CreateSessionCommand.ExecuteAsync(null);

        Assert.False(workspace.IsNewSessionFormOpen);
        Assert.Equal(99, workspace.SelectedSession!.Id);
        Assert.Equal("New work", workspace.SessionDetails!.Title);
        Assert.Equal(string.Empty, workspace.NewSessionTitle);
    }

    [Fact]
    public async Task RunRequiresExplicitConfirmation()
    {
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        api.ProjectSessions = (_, _, _) => Task.FromResult<IReadOnlyList<SessionSummaryResponse>>(
            [TestData.SessionSummary(11, 1) with { State = "READY" }]);
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);
        await workspace.SelectSessionAsync(workspace.Sessions[0]);

        workspace.RequestRunCommand.Execute(null);

        Assert.True(workspace.IsRunConfirmationOpen);
        Assert.Equal(0, api.RunSessionCalls);
        Assert.Equal("Inspect the failure", workspace.SelectedSession!.Task);
    }

    [Fact]
    public async Task ConfirmedRunRefreshesAuthoritativeQueuedState()
    {
        SessionSummaryResponse current = TestData.SessionSummary(11, 1) with { State = "READY" };
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        api.ProjectSessions = (_, _, _) => Task.FromResult<IReadOnlyList<SessionSummaryResponse>>([current]);
        api.SessionDetails = (_, _, _) => Task.FromResult(current);
        api.RunSession = (_, id, _) =>
        {
            current = current with { State = "QUEUED" };
            return Task.FromResult(new RunSessionResponse(
                current, new ExecutionResponse(55, id, "QUEUED", 1)));
        };
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);
        await workspace.SelectSessionAsync(workspace.Sessions[0]);
        workspace.RequestRunCommand.Execute(null);

        await workspace.ConfirmRunCommand.ExecuteAsync(null);

        Assert.Equal(1, api.RunSessionCalls);
        Assert.Equal("QUEUED", workspace.SessionDetails!.State);
        Assert.Contains("Daemon accepted execution 55: QUEUED", workspace.RunStatusMessage);
        Assert.False(workspace.IsRunConfirmationOpen);
        Assert.False(workspace.CanRunSelectedSession);
    }

    [Fact]
    public async Task ConflictingRunKeepsConfirmationAndShowsRefreshMessage()
    {
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        api.ProjectSessions = (_, _, _) => Task.FromResult<IReadOnlyList<SessionSummaryResponse>>(
            [TestData.SessionSummary(11, 1) with { State = "READY" }]);
        api.RunSession = (_, _, _) => Task.FromException<RunSessionResponse>(
            ForgehandApiException.For(ApiFailure.SessionConflict));
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);
        await workspace.SelectSessionAsync(workspace.Sessions[0]);
        workspace.RequestRunCommand.Execute(null);

        await workspace.ConfirmRunCommand.ExecuteAsync(null);

        Assert.True(workspace.IsRunConfirmationOpen);
        Assert.True(workspace.HasRunError);
        Assert.Contains("Refresh", workspace.RunErrorMessage);
    }

    [Fact]
    public async Task RevocationDuringPendingRunCannotRestoreSessionData()
    {
        IReadOnlyList<ProjectResponse> grants = [new ProjectResponse(1, "Alpha", null)];
        var pending = new TaskCompletionSource<RunSessionResponse>(TaskCreationOptions.RunContinuationsAsynchronously);
        var api = new FakeForgehandApi
        {
            Projects = _ => Task.FromResult(grants),
            ProjectSessions = (_, _, _) => Task.FromResult<IReadOnlyList<SessionSummaryResponse>>(
                [TestData.SessionSummary(11, 1) with { State = "READY" }]),
            RunSession = (_, _, _) => pending.Task,
        };
        var (_, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);
        await workspace.SelectSessionAsync(workspace.Sessions[0]);
        workspace.RequestRunCommand.Execute(null);
        var run = workspace.ConfirmRunCommand.ExecuteAsync(null);

        grants = [];
        await workspace.LoadAsync();
        pending.SetResult(new RunSessionResponse(
            TestData.SessionSummary(11, 1) with { State = "QUEUED" },
            new ExecutionResponse(2, 11, "QUEUED", 1)));
        await run;

        Assert.Null(workspace.SelectedProject);
        Assert.Empty(workspace.Sessions);
        Assert.Null(workspace.SessionDetails);
        Assert.Null(workspace.RunStatusMessage);
    }

    [Fact]
    public async Task LogoutDuringPendingCreationClearsDraftAndIgnoresLateResponse()
    {
        var pending = new TaskCompletionSource<SessionSummaryResponse>(TaskCreationOptions.RunContinuationsAsynchronously);
        var api = ApiWithProjects(new ProjectResponse(1, "Alpha", null));
        api.CreateSession = (_, _, _, _) => pending.Task;
        var (service, workspace) = await CreateAsync(api);
        await workspace.SelectProjectAsync(workspace.Projects[0]);
        workspace.OpenNewSessionCommand.Execute(null);
        workspace.NewSessionTitle = "Sensitive draft";
        workspace.NewSessionTask = "Project-scoped task";
        var create = workspace.CreateSessionCommand.ExecuteAsync(null);

        await service.SignOutAsync();
        workspace.Clear();
        pending.SetResult(TestData.SessionSummary(99, 1, "Late"));
        await create;

        Assert.Empty(workspace.Projects);
        Assert.Empty(workspace.Sessions);
        Assert.Equal(string.Empty, workspace.NewSessionTitle);
        Assert.Equal(string.Empty, workspace.NewSessionTask);
        Assert.Null(workspace.SelectedSession);
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

    [Theory]
    [InlineData(ApiFailure.Unreachable, ConnectionState.Disconnected)]
    [InlineData(ApiFailure.Timeout, ConnectionState.Disconnected)]
    [InlineData(ApiFailure.ServerError, ConnectionState.ServerError)]
    public async Task ConnectionCheckDistinguishesTransportAndServerFailures(
        ApiFailure failure,
        ConnectionState expected)
    {
        var api = new FakeForgehandApi
        {
            Health = _ => Task.FromException<HealthResponse>(ForgehandApiException.For(failure)),
        };
        var service = new ForgehandService(ServerEndpoint.Default, _ => api);
        var shell = new ShellViewModel(service, new ClientSettingsStore(_settingsPath), notice: null);

        await shell.CheckConnectionCommand.ExecuteAsync(null);

        Assert.Equal(expected, shell.Connection);
        Assert.False(shell.IsConnected);
    }

    [Fact]
    public async Task ExpiredAuthenticationHasDedicatedConnectionStatus()
    {
        var api = new FakeForgehandApi();
        var service = new ForgehandService(ServerEndpoint.Default, _ => api);
        var shell = new ShellViewModel(service, new ClientSettingsStore(_settingsPath), notice: null);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        api.Projects = _ => Task.FromException<IReadOnlyList<ProjectResponse>>(
            ForgehandApiException.For(ApiFailure.SessionExpired));

        await Assert.ThrowsAsync<ForgehandApiException>(() => service.GetProjectsAsync(CancellationToken.None));

        Assert.Equal(ConnectionState.AuthenticationExpired, shell.Connection);
        Assert.Contains("Authentication expired", shell.ConnectionText);
        Assert.False(shell.IsSignedIn);
    }
}
