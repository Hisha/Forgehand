using Avalonia;
using Avalonia.Headless.XUnit;
using Avalonia.Headless;
using Avalonia.Controls;
using Avalonia.Input;
using Avalonia.Interactivity;
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
    private static (MainWindow Window, ShellViewModel Shell, ForgehandService Service) CreateWindow(
        IForgehandApi api,
        double? width = null)
    {
        var service = new ForgehandService(ServerEndpoint.Default, _ => api);
        var settingsPath = Path.Combine(Path.GetTempPath(), "forgehand-ui-tests-" + Guid.NewGuid().ToString("N"), "settings.json");
        var shell = new ShellViewModel(service, new ClientSettingsStore(settingsPath), notice: null);
        var window = new MainWindow { DataContext = shell };
        if (width is not null)
        {
            window.Width = width.Value;
        }
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

    [AvaloniaFact]
    public async Task SessionWriteActionsShowCreationAndRunConfirmationPanels()
    {
        var api = new StubApi
        {
            Projects = [new ProjectResponse(1, "Alpha", "/srv/alpha")],
            Sessions =
            [
                new SessionSummaryResponse(
                    11, "Ready work", "READY", 1, "Implement the requested change", "coding", "test", "test",
                    "2026-10-22T12:00:00Z", "2026-10-22T12:00:00Z"),
            ],
        };
        var (window, shell, service) = CreateWindow(api);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        await shell.Projects.LoadAsync();
        await shell.Projects.SelectProjectAsync(shell.Projects.Projects[0]);
        var view = window.GetVisualDescendants().OfType<ProjectsView>().Single();

        shell.Projects.OpenNewSessionCommand.Execute(null);
        window.UpdateLayout();
        Assert.True(view.FindControl<Border>("NewSessionPanel")!.IsEffectivelyVisible);

        shell.Projects.CloseNewSessionCommand.Execute(null);
        await shell.Projects.SelectSessionAsync(shell.Projects.Sessions[0]);
        shell.Projects.RequestRunCommand.Execute(null);
        window.UpdateLayout();
        Assert.True(view.FindControl<Border>("RunConfirmationPanel")!.IsEffectivelyVisible);

        window.Close();
    }

    [AvaloniaFact]
    public async Task RunSessionRespondsToLeftMouseClickAndSpaceKey()
    {
        var api = new StubApi
        {
            Projects = [new ProjectResponse(1, "Alpha", "/srv/alpha")],
            Sessions =
            [
                new SessionSummaryResponse(
                    11, "Ready work", "READY", 1, "Implement the requested change", "coding", "test", "test",
                    "2026-10-22T12:00:00Z", "2026-10-22T12:00:00Z"),
            ],
        };
        var (window, shell, service) = CreateWindow(api, width: 1000);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        await shell.Projects.LoadAsync();
        await shell.Projects.SelectProjectAsync(shell.Projects.Projects[0]);
        await shell.Projects.SelectSessionAsync(shell.Projects.Sessions[0]);
        window.UpdateLayout();

        var view = window.GetVisualDescendants().OfType<ProjectsView>().Single();
        var button = view.FindControl<Button>("RunSessionButton")!;
        var buttonCenter = new Point(button.Bounds.Width / 2, button.Bounds.Height / 2);
        var clickPoint = button.TranslatePoint(buttonCenter, window)!.Value;
        object? pointerSource = null;
        window.AddHandler(
            InputElement.PointerPressedEvent,
            (_, args) => pointerSource = args.Source,
            RoutingStrategies.Tunnel,
            handledEventsToo: true);
        window.MouseDown(clickPoint, MouseButton.Left, RawInputModifiers.None);
        window.MouseUp(clickPoint, MouseButton.Left, RawInputModifiers.None);
        window.UpdateLayout();

        Assert.True(
            pointerSource is Visual sourceVisual
            && (ReferenceEquals(sourceVisual, button) || sourceVisual.GetVisualAncestors().Contains(button)),
            $"Expected the Run Session button, but pointer input reached {pointerSource?.GetType().Name ?? "nothing"}.");
        Assert.True(
            shell.Projects.IsRunConfirmationOpen,
            $"Pointer source: {pointerSource?.GetType().Name ?? "none"}; point: {clickPoint}; client: {window.ClientSize}.");
        Assert.True(view.FindControl<Border>("RunConfirmationPanel")!.IsEffectivelyVisible);

        var cancelButton = view.FindControl<Button>("CancelRunButton")!;
        var cancelPoint = cancelButton.TranslatePoint(
            new Point(cancelButton.Bounds.Width / 2, cancelButton.Bounds.Height / 2), window)!.Value;
        window.MouseDown(cancelPoint, MouseButton.Left, RawInputModifiers.None);
        window.MouseUp(cancelPoint, MouseButton.Left, RawInputModifiers.None);
        window.UpdateLayout();

        Assert.False(shell.Projects.IsRunConfirmationOpen);

        Assert.True(button.Focus());
        window.KeyPressQwerty(PhysicalKey.Space, RawInputModifiers.None);
        window.KeyReleaseQwerty(PhysicalKey.Space, RawInputModifiers.None);
        window.UpdateLayout();

        Assert.True(shell.Projects.IsRunConfirmationOpen);

        window.Close();
    }

    [AvaloniaFact]
    public async Task IncompatibleDaemonDisablesWritesAndRecoversAfterUpgrade()
    {
        var api = new StubApi
        {
            Projects = [new ProjectResponse(1, "Alpha", "/srv/alpha")],
            Sessions =
            [
                new SessionSummaryResponse(
                    11, "Ready work", "READY", 1, "Implement the requested change", "coding", "test", "test",
                    "2026-10-22T12:00:00Z", "2026-10-22T12:00:00Z"),
            ],
        };
        var (window, shell, service) = CreateWindow(api);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        await shell.Projects.LoadAsync();
        await shell.Projects.SelectProjectAsync(shell.Projects.Projects[0]);
        await shell.Projects.SelectSessionAsync(shell.Projects.Sessions[0]);
        var view = window.GetVisualDescendants().OfType<ProjectsView>().Single();

        api.Health = api.Health with { Version = "0.9.0", BuildIdentity = "old-daemon", ApiVersion = "v2" };
        await shell.CheckConnectionCommand.ExecuteAsync(null);
        window.UpdateLayout();

        Assert.Equal(ConnectionState.ConnectedIncompatible, shell.Connection);
        Assert.Contains("Connected but incompatible", shell.ConnectionText);
        Assert.Contains("old-daemon", shell.ConnectionText);
        Assert.Contains("HTTP API v2", shell.ConnectionText);
        Assert.False(view.FindControl<Button>("NewSessionButton")!.IsEnabled);
        Assert.False(view.FindControl<Button>("RunSessionButton")!.IsEnabled);
        Assert.True(view.FindControl<Border>("CompatibilityWarning")!.IsEffectivelyVisible);

        api.Health = api.Health with { Version = "1.0.0", BuildIdentity = "upgraded-daemon", ApiVersion = "v1" };
        await shell.CheckConnectionCommand.ExecuteAsync(null);
        window.UpdateLayout();

        Assert.Equal(ConnectionState.ConnectedCompatible, shell.Connection);
        Assert.Contains("Connected and compatible", shell.ConnectionText);
        Assert.Contains("upgraded-daemon", shell.ConnectionText);
        Assert.True(view.FindControl<Button>("NewSessionButton")!.IsEnabled);
        Assert.True(view.FindControl<Button>("RunSessionButton")!.IsEnabled);
        Assert.False(view.FindControl<Border>("CompatibilityWarning")!.IsEffectivelyVisible);

        window.Close();
    }

    private sealed class StubApi : IForgehandApi
    {
        public HealthResponse Health { get; set; } =
            new("ok", "test", "test", "v1", "2026-10-22T12:00:00Z");

        public IReadOnlyList<ProjectResponse> Projects { get; set; } = [];

        public IReadOnlyList<SessionSummaryResponse> Sessions { get; set; } = [];

        public Task<HealthResponse> GetHealthAsync(CancellationToken cancellationToken) =>
            Task.FromResult(Health);

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

        public Task<SessionSummaryResponse> CreateSessionAsync(
            string token, long projectId, CreateSessionRequest request, CancellationToken cancellationToken) =>
            Task.FromResult(new SessionSummaryResponse(
                99, request.Title, "READY", projectId, request.Task, "coding", "test", "test",
                "2026-10-22T12:00:00Z", "2026-10-22T12:00:00Z"));

        public Task<RunSessionResponse> RunSessionAsync(
            string token, long sessionId, CancellationToken cancellationToken) =>
            Task.FromResult(new RunSessionResponse(
                Sessions.Single(session => session.Id == sessionId) with { State = "QUEUED" },
                new ExecutionResponse(1, sessionId, "QUEUED", 1)));

        public void Dispose()
        {
        }
    }
}
