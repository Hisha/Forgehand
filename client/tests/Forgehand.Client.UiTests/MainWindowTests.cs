using Avalonia.Headless.XUnit;
using Avalonia.Controls;
using Avalonia.VisualTree;
using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Configuration;
using Forgehand.Client.Core.Session;
using Forgehand.Client.Core.ViewModels;
using Forgehand.Client.Views;
using Xunit;

namespace Forgehand.Client.UiTests;

/// <summary>
/// Runtime checks of the real XAML: resources and styles load, bindings resolve,
/// and the login and project views switch with the session state.
/// </summary>
public sealed class MainWindowTests
{
    private static (MainWindow Window, ShellViewModel Shell, ForgehandService Service) CreateWindow(IForgehandApi api)
    {
        var service = new ForgehandService(ServerEndpoint.Default, _ => api);
        var settingsPath = Path.Combine(Path.GetTempPath(), "forgehand-ui-tests-" + Guid.NewGuid().ToString("N"), "settings.json");
        var shell = new ShellViewModel(service, new ClientSettingsStore(settingsPath), notice: null);
        var window = new MainWindow { DataContext = shell };
        window.Show();
        return (window, shell, service);
    }

    [AvaloniaFact]
    public void SignedOutWindowShowsLoginAndDefaultServer()
    {
        var (window, shell, _) = CreateWindow(new StubApi());

        var login = window.GetVisualDescendants().OfType<LoginView>().Single();
        var projects = window.GetVisualDescendants().OfType<ProjectsView>().Single();

        Assert.True(login.IsEffectivelyVisible);
        Assert.False(projects.IsEffectivelyVisible);
        Assert.Equal("http://127.0.0.1:8787", shell.EndpointText);
        Assert.False(shell.IsEndpointLocked);

        window.Close();
    }

    [AvaloniaFact]
    public async Task SignInShowsAuthorizedProjectsAndSignOutReturnsToLogin()
    {
        var api = new StubApi
        {
            Projects = [new ProjectResponse(1, "Alpha", "/srv/alpha")],
        };
        var (window, shell, service) = CreateWindow(api);

        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        await shell.Projects.LoadAsync();

        var login = window.GetVisualDescendants().OfType<LoginView>().Single();
        var projects = window.GetVisualDescendants().OfType<ProjectsView>().Single();
        Assert.False(login.IsEffectivelyVisible);
        Assert.True(projects.IsEffectivelyVisible);
        Assert.Single(shell.Projects.Projects);
        Assert.True(shell.IsEndpointLocked);
        Assert.StartsWith("Signed in as Alice", shell.SessionText);

        await shell.SignOutCommand.ExecuteAsync(null);

        Assert.True(login.IsEffectivelyVisible);
        Assert.False(projects.IsEffectivelyVisible);
        Assert.Empty(shell.Projects.Projects);

        window.Close();
    }

    [AvaloniaFact]
    public async Task WorkspaceSelectionDisplaysSessionDetails()
    {
        var api = new StubApi
        {
            Projects = [new ProjectResponse(1, "Alpha", "/srv/alpha")],
            Sessions =
            [
                new SessionSummaryResponse(
                    11, "Investigate failure", "RUNNING", 1, "Find the cause", "task", "openai", "gpt-test",
                    "2026-10-22T12:00:00Z", "2026-10-22T12:05:00Z"),
            ],
        };
        var (window, shell, service) = CreateWindow(api);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        await shell.Projects.LoadAsync();

        await shell.Projects.SelectProjectAsync(shell.Projects.Projects[0]);
        await shell.Projects.SelectSessionAsync(shell.Projects.Sessions[0]);
        window.UpdateLayout();

        Assert.Equal("Investigate failure", shell.Projects.SessionDetails!.Title);
        var projects = window.GetVisualDescendants().OfType<ProjectsView>().Single();
        Assert.Equal("Investigate failure", projects.FindControl<TextBlock>("SessionDetailTitle")!.Text);
        Assert.Equal("/srv/alpha", projects.FindControl<TextBlock>("ProjectRootPath")!.Text);

        window.Close();
    }

    private sealed class StubApi : IForgehandApi
    {
        public IReadOnlyList<ProjectResponse> Projects { get; set; } = [];

        public IReadOnlyList<SessionSummaryResponse> Sessions { get; set; } = [];

        public Task<HealthResponse> GetHealthAsync(CancellationToken cancellationToken) =>
            Task.FromResult(new HealthResponse("ok", "test", "test", "v1", "2026-10-22T12:00:00Z"));

        public Task<SessionResponse> LoginAsync(LoginRequest request, CancellationToken cancellationToken) =>
            Task.FromResult(new SessionResponse(
                "token-for-ui-test",
                "2026-10-22T20:00:00Z",
                new AccountResponse(7, "alice", "Alice", "admin", "active", true)));

        public Task LogoutAsync(string token, CancellationToken cancellationToken) => Task.CompletedTask;

        public Task<IReadOnlyList<ProjectResponse>> GetProjectsAsync(string token, CancellationToken cancellationToken) =>
            Task.FromResult(Projects);

        public Task<IReadOnlyList<SessionSummaryResponse>> GetProjectSessionsAsync(
            string token, long projectId, CancellationToken cancellationToken) =>
            Task.FromResult(Sessions);

        public Task<SessionSummaryResponse> GetSessionAsync(
            string token, long sessionId, CancellationToken cancellationToken) =>
            Task.FromResult(Sessions.Single(session => session.Id == sessionId));

        public void Dispose()
        {
        }
    }
}
