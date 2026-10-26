using Avalonia;
using Avalonia.Headless.XUnit;
using Avalonia.Headless;
using Avalonia.Controls;
using Avalonia.Controls.Primitives;
using Avalonia.Input;
using Avalonia.Input.Platform;
using Avalonia.Interactivity;
using Avalonia.Media;
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
    public async Task AdministratorCanOpenAndRefreshDaemonStatusDashboard()
    {
        var api = new StubApi();
        var (window, shell, service) = CreateWindow(api);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);

        var navigation = window.FindControl<Button>("DaemonStatusNavigationButton")!;
        Assert.True(navigation.IsEffectivelyVisible);
        Assert.True(navigation.IsEnabled);
        await shell.ShowDaemonStatusCommand.ExecuteAsync(null);

        var view = window.GetVisualDescendants().OfType<DaemonStatusView>().Single();
        Assert.True(view.IsEffectivelyVisible);
        Assert.True(shell.DaemonStatus.HasStatus);
        Assert.Equal("1", shell.DaemonStatus.ActiveWorkersText);
        Assert.Equal("Running and installed executable match", shell.DaemonStatus.ExecutableComparisonText);
        Assert.False(shell.DaemonStatus.HasRestartWarning);

        await shell.SignOutCommand.ExecuteAsync(null);
        Assert.False(shell.DaemonStatus.HasStatus);
        Assert.False(view.IsEffectivelyVisible);
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
    public async Task CompletedExecutionWrapsSelectsAndCopiesOriginalResultAcrossResize()
    {
        var paragraph = string.Join(' ', Enumerable.Repeat(
            "Forgehand preserves the persisted response while the viewer wraps it to the available width.", 12));
        var unbroken = new string('X', 320);
        var responseText = $"Execution result\n\n    indented detail\n{paragraph}\n{unbroken}\n\nDone.";
        var execution = new ExecutionDetailResponse(21, 11, "COMPLETED", 1, 1,
            "2026-10-22T12:00:00Z", "2026-10-22T12:00:01Z", null, null, null, null,
            "coding", true, "provider", "model", "2026-10-22T12:00:01Z", 4, 5, 9, 1000);
        var api = new StubApi
        {
            Projects = [new ProjectResponse(1, "Alpha", "/srv/alpha")],
            Sessions =
            [
                new SessionSummaryResponse(
                    11, "Completed work", "COMPLETED", 1, "Do it", "coding", "provider", "model",
                    "2026-10-22T12:00:00Z", "2026-10-22T12:00:01Z"),
            ],
            ExecutionHistory = new ExecutionHistoryResponse([execution], false),
            ExecutionRecord = new ExecutionRecordResponse(execution,
                new ModelResultResponse(1, 21, "provider", "model", responseText,
                    "2026-10-22T12:00:01Z", 4, 5, 9, 1000, 0, 0, responseText.Length, true)),
        };
        var (window, shell, service) = CreateWindow(api, width: 1180);
        await service.SignInAsync("alice", "pw", "123456", CancellationToken.None);
        await shell.Projects.LoadAsync();
        await shell.Projects.SelectProjectAsync(shell.Projects.Projects[0]);
        await shell.Projects.SelectSessionAsync(shell.Projects.Sessions[0]);
        window.UpdateLayout();

        var view = window.GetVisualDescendants().OfType<ProjectsView>().Single();
        var result = view.FindControl<TextBox>("ExecutionResultText")!;
        Assert.True(result.IsEffectivelyVisible);
        Assert.True(result.IsReadOnly);
        Assert.Equal(TextWrapping.Wrap, result.TextWrapping);
        Assert.Equal(
            ScrollBarVisibility.Disabled,
            result.GetValue(ScrollViewer.HorizontalScrollBarVisibilityProperty));
        Assert.Equal(
            ScrollBarVisibility.Auto,
            result.GetValue(ScrollViewer.VerticalScrollBarVisibilityProperty));
        Assert.Equal(responseText, result.Text);
        Assert.True(view.FindControl<Button>("CopyResultButton")!.IsEnabled);
        Assert.Equal(21, shell.Projects.SelectedExecution!.Id);

        var resultScroller = result.GetVisualDescendants().OfType<ScrollViewer>().Single();
        var wideWidth = result.Bounds.Width;
        var wideExtentHeight = resultScroller.Extent.Height;
        Assert.True(resultScroller.Extent.Width <= resultScroller.Viewport.Width + 1);

        window.Hide();
        window.Width = 820;
        window.Show();
        window.UpdateLayout();

        Assert.True(
            result.Bounds.Width < wideWidth,
            $"Expected result width to shrink from {wideWidth}, but it was {result.Bounds.Width}; client={window.ClientSize}.");
        Assert.True(resultScroller.Extent.Height > wideExtentHeight);
        Assert.True(resultScroller.Extent.Width <= resultScroller.Viewport.Width + 1);
        Assert.Equal(responseText, result.Text);

        var selectionStart = responseText.IndexOf("indented detail", StringComparison.Ordinal);
        result.SelectionStart = selectionStart;
        result.SelectionEnd = selectionStart + "indented detail".Length;
        Assert.Equal("indented detail", result.SelectedText);

        view.FindControl<Button>("CopyResultButton")!.RaiseEvent(new RoutedEventArgs(Button.ClickEvent));
        await Task.Yield();
        var clipboardData = await window.Clipboard!.TryGetDataAsync();
        Assert.Equal(responseText, await clipboardData!.TryGetTextAsync());

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

        public ExecutionHistoryResponse ExecutionHistory { get; set; } =
            new(Array.Empty<ExecutionDetailResponse>(), false);

        public ExecutionRecordResponse? ExecutionRecord { get; set; }

        public DaemonStatusResponse DaemonStatus { get; set; } = new(
            "ok", "2026-10-22T12:00:00Z",
            new DaemonIdentityResponse("1.2.3", "build", null, "v1", "2026-10-22T11:00:00Z", 3600),
            new DaemonActivityResponse(4, 1, 1, 2, 0, 8, 1),
            new ExecutableStatusResponse("1:10", "1:10", "match", "same_file_identity", "2026-10-22T12:00:00Z", false),
            new DaemonHealthResponse("ok", "ok", "ok"));

        public Task<HealthResponse> GetHealthAsync(CancellationToken cancellationToken) =>
            Task.FromResult(Health);

        public Task<DaemonStatusResponse> GetDaemonStatusAsync(string token, CancellationToken cancellationToken) =>
            Task.FromResult(DaemonStatus);

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

        public Task<ExecutionHistoryResponse> GetSessionExecutionsAsync(
            string token, long sessionId, CancellationToken cancellationToken) =>
            Task.FromResult(ExecutionHistory);

        public Task<ExecutionRecordResponse> GetSessionExecutionAsync(
            string token, long sessionId, long executionId, CancellationToken cancellationToken) =>
            Task.FromResult(ExecutionRecord ?? new ExecutionRecordResponse(
                new ExecutionDetailResponse(executionId, sessionId, "COMPLETED", 1, 1,
                    "2026-10-22T12:00:00Z", "2026-10-22T12:00:01Z", null, null, null, null,
                    "coding", true, "test", "test", "2026-10-22T12:00:01Z", 1, 2, 3, 1000),
                new ModelResultResponse(1, executionId, "test", "test", "done",
                    "2026-10-22T12:00:01Z", 1, 2, 3, 1000, 0, 0, 4, true)));

        public void Dispose()
        {
        }
    }
}
