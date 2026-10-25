using System.Collections.ObjectModel;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Session;

namespace Forgehand.Client.Core.ViewModels;

public sealed class ProjectItem
{
    public ProjectItem(ProjectResponse project)
    {
        Name = project.Name ?? string.Empty;
        Id = project.Id;
        RootPath = string.IsNullOrWhiteSpace(project.RootPath) ? null : project.RootPath;
    }

    public string Name { get; }
    public long Id { get; }
    public string? RootPath { get; }
    public bool HasRootPath => RootPath is not null;
    public string IdText => $"ID {Id}";
}

public sealed class SessionItem
{
    public SessionItem(SessionSummaryResponse session)
    {
        Id = session.Id;
        ProjectId = session.ProjectId;
        Title = session.Title ?? string.Empty;
        State = session.State ?? string.Empty;
        Task = EmptyToNull(session.Task);
        Kind = EmptyToNull(session.Kind);
        Provider = EmptyToNull(session.Provider);
        Model = EmptyToNull(session.Model);
        CreatedAt = session.CreatedAt ?? string.Empty;
        UpdatedAt = session.UpdatedAt ?? string.Empty;
    }

    public long Id { get; }
    public long ProjectId { get; }
    public string Title { get; }
    public string State { get; }
    public string? Task { get; }
    public string? Kind { get; }
    public string? Provider { get; }
    public string? Model { get; }
    public string CreatedAt { get; }
    public string UpdatedAt { get; }
    public string IdText => $"Session {Id}";
    public bool HasTask => Task is not null;
    public bool HasKind => Kind is not null;
    public bool HasProvider => Provider is not null;
    public bool HasModel => Model is not null;
    public bool CanRun => string.Equals(State, "READY", StringComparison.Ordinal);

    private static string? EmptyToNull(string? value) => string.IsNullOrWhiteSpace(value) ? null : value;
}

public sealed partial class ProjectsViewModel : ObservableObject
{
    private readonly ForgehandService _service;
    private CancellationTokenSource? _projectsCancellation;
    private CancellationTokenSource? _sessionsCancellation;
    private CancellationTokenSource? _detailCancellation;
    private CancellationTokenSource? _writeCancellation;
    private ProjectItem? _selectedProject;
    private SessionItem? _selectedSession;

    public ProjectsViewModel(ForgehandService service)
    {
        _service = service;
        _service.CompatibilityChanged += OnCompatibilityChanged;
        Projects.CollectionChanged += (_, _) => RaiseProjectState();
        Sessions.CollectionChanged += (_, _) => RaiseSessionState();
    }

    public ObservableCollection<ProjectItem> Projects { get; } = [];
    public ObservableCollection<SessionItem> Sessions { get; } = [];

    public ProjectItem? SelectedProject
    {
        get => _selectedProject;
        set => _ = SelectProjectAsync(value);
    }

    public SessionItem? SelectedSession
    {
        get => _selectedSession;
        set => _ = SelectSessionAsync(value);
    }

    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(RefreshCommand))]
    [NotifyPropertyChangedFor(nameof(ShowProjectEmptyState))]
    private bool _isLoading;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(ShowProjectEmptyState))]
    private bool _hasLoaded;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasError))]
    [NotifyPropertyChangedFor(nameof(ShowProjectEmptyState))]
    private string? _errorMessage;

    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(RefreshSessionsCommand))]
    [NotifyPropertyChangedFor(nameof(ShowSessionEmptyState))]
    private bool _isLoadingSessions;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasSessionError))]
    [NotifyPropertyChangedFor(nameof(ShowSessionEmptyState))]
    private string? _sessionErrorMessage;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(ShowSessionEmptyState))]
    private bool _hasLoadedSessions;

    [ObservableProperty]
    private bool _isLoadingSessionDetails;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasSessionDetailError))]
    private string? _sessionDetailErrorMessage;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasSessionDetails))]
    private SessionItem? _sessionDetails;

    [ObservableProperty]
    private bool _isNewSessionFormOpen;

    [ObservableProperty]
    private string _newSessionTitle = string.Empty;

    [ObservableProperty]
    private string _newSessionTask = string.Empty;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasNewSessionError))]
    private string? _newSessionErrorMessage;

    [ObservableProperty]
    private bool _isCreatingSession;

    [ObservableProperty]
    private bool _isRunConfirmationOpen;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasRunError))]
    private string? _runErrorMessage;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasRunStatus))]
    private string? _runStatusMessage;

    [ObservableProperty]
    private bool _isSubmittingRun;

    public bool HasError => ErrorMessage is not null;
    public bool HasSessionError => SessionErrorMessage is not null;
    public bool HasSessionDetailError => SessionDetailErrorMessage is not null;
    public bool ShowProjects => Projects.Count > 0;
    public bool ShowProjectEmptyState => HasLoaded && !IsLoading && !HasError && Projects.Count == 0;
    public bool ShowEmptyState => ShowProjectEmptyState;
    public bool ShowWorkspaceEmptyState => SelectedProject is null && Projects.Count > 0;
    public bool ShowWorkspace => SelectedProject is not null;
    public bool ShowSessions => Sessions.Count > 0;
    public bool ShowSessionEmptyState =>
        SelectedProject is not null && HasLoadedSessions && !IsLoadingSessions && !HasSessionError && Sessions.Count == 0;
    public bool ShowSessionSelectionState =>
        SelectedProject is not null && Sessions.Count > 0 && SelectedSession is null && !IsLoadingSessionDetails;
    public bool HasSessionDetails => SessionDetails is not null;
    public bool HasNewSessionError => NewSessionErrorMessage is not null;
    public bool HasRunError => RunErrorMessage is not null;
    public bool HasRunStatus => RunStatusMessage is not null;
    public bool CanModifySessions => _service.IsApiCompatible;
    public bool HasCompatibilityWarning => !CanModifySessions;
    public string CompatibilityMessage => _service.CompatibilityExplanation;
    public bool ShowRunSelectedSession => (SessionDetails ?? SelectedSession) is { CanRun: true };
    public bool CanRunSelectedSession =>
        ShowRunSelectedSession && CanModifySessions && !IsSubmittingRun && !IsLoadingSessionDetails;

    [RelayCommand(CanExecute = nameof(CanRefresh))]
    private Task RefreshAsync() => LoadAsync();

    [RelayCommand(CanExecute = nameof(CanRefreshSessions))]
    private Task RefreshSessionsAsync() => LoadSessionsAsync();

    [RelayCommand(CanExecute = nameof(CanOpenNewSession))]
    private void OpenNewSession()
    {
        NewSessionErrorMessage = null;
        IsNewSessionFormOpen = true;
    }

    [RelayCommand]
    private void CloseNewSession() => IsNewSessionFormOpen = false;

    [RelayCommand(CanExecute = nameof(CanCreateSession))]
    private async Task CreateSessionAsync()
    {
        var project = SelectedProject;
        if (project is null) return;

        var title = NewSessionTitle.Trim();
        var task = NewSessionTask.Trim();
        if (title.Length == 0 || task.Length == 0)
        {
            NewSessionErrorMessage = "Enter both a session title and a task description.";
            return;
        }
        if (title.Length > 200 || task.Length > 32 * 1024)
        {
            NewSessionErrorMessage = "The title or task is longer than Forgehand supports.";
            return;
        }

        CancelAndDispose(ref _writeCancellation);
        var cancellation = new CancellationTokenSource();
        _writeCancellation = cancellation;
        IsCreatingSession = true;
        NewSessionErrorMessage = null;
        try
        {
            var created = await _service.CreateSessionAsync(project.Id, title, task, cancellation.Token);
            if (!ReferenceEquals(cancellation, _writeCancellation) || !ReferenceEquals(project, SelectedProject)) return;

            IsNewSessionFormOpen = false;
            NewSessionTitle = string.Empty;
            NewSessionTask = string.Empty;
            await LoadSessionsAsync();
            if (!ReferenceEquals(project, SelectedProject) || HasSessionError) return;
            var item = Sessions.FirstOrDefault(candidate => candidate.Id == created.Id);
            if (item is null)
            {
                item = new SessionItem(created);
                Sessions.Add(item);
            }
            if (item is not null) await SelectSessionAsync(item);
        }
        catch (ForgehandApiException exception)
        {
            if (ReferenceEquals(cancellation, _writeCancellation)) NewSessionErrorMessage = exception.Message;
        }
        catch (OperationCanceledException)
        {
        }
        finally
        {
            if (ReferenceEquals(cancellation, _writeCancellation)) IsCreatingSession = false;
        }
    }

    [RelayCommand(CanExecute = nameof(CanRequestRun))]
    private void RequestRun()
    {
        RunErrorMessage = null;
        RunStatusMessage = null;
        IsRunConfirmationOpen = true;
    }

    [RelayCommand]
    private void CancelRun() => IsRunConfirmationOpen = false;

    [RelayCommand(CanExecute = nameof(CanConfirmRun))]
    private async Task ConfirmRunAsync()
    {
        var project = SelectedProject;
        var session = SelectedSession;
        if (project is null || session is null) return;

        CancelAndDispose(ref _writeCancellation);
        var cancellation = new CancellationTokenSource();
        _writeCancellation = cancellation;
        IsSubmittingRun = true;
        RunErrorMessage = null;
        RunStatusMessage = null;
        try
        {
            var result = await _service.RunSessionAsync(session.Id, cancellation.Token);
            if (!ReferenceEquals(cancellation, _writeCancellation) ||
                !ReferenceEquals(project, SelectedProject) ||
                !ReferenceEquals(session, SelectedSession)) return;

            IsRunConfirmationOpen = false;
            await LoadSessionsAsync();
            if (!ReferenceEquals(project, SelectedProject) || HasSessionError) return;
            var refreshed = Sessions.FirstOrDefault(candidate => candidate.Id == session.Id);
            if (refreshed is not null) await SelectSessionAsync(refreshed);
            RunStatusMessage = $"Daemon accepted execution {result.Execution.Id}: {result.Execution.Status}.";
        }
        catch (ForgehandApiException exception)
        {
            if (!ReferenceEquals(cancellation, _writeCancellation)) return;
            RunErrorMessage = exception.Message;
            if (exception.Failure is ApiFailure.Forbidden or ApiFailure.NotFound)
                await LoadAsync();
        }
        catch (OperationCanceledException)
        {
        }
        finally
        {
            if (ReferenceEquals(cancellation, _writeCancellation)) IsSubmittingRun = false;
        }
    }

    private bool CanRefresh() => !IsLoading;
    private bool CanRefreshSessions() => SelectedProject is not null && !IsLoadingSessions;
    private bool CanOpenNewSession() =>
        CanModifySessions && SelectedProject is not null && !IsCreatingSession && !IsSubmittingRun;
    private bool CanCreateSession() =>
        CanModifySessions && IsNewSessionFormOpen && !IsCreatingSession && SelectedProject is not null;
    private bool CanRequestRun() => CanRunSelectedSession;
    private bool CanConfirmRun() => IsRunConfirmationOpen && CanRunSelectedSession;

    partial void OnIsCreatingSessionChanged(bool value) => NotifyWriteCommands();
    partial void OnIsSubmittingRunChanged(bool value) => NotifyWriteCommands();
    partial void OnIsNewSessionFormOpenChanged(bool value) => CreateSessionCommand.NotifyCanExecuteChanged();
    partial void OnIsRunConfirmationOpenChanged(bool value) => ConfirmRunCommand.NotifyCanExecuteChanged();
    partial void OnSessionDetailsChanged(SessionItem? value) => NotifyRunCommands();
    partial void OnIsLoadingSessionDetailsChanged(bool value) => NotifyRunCommands();

    public async Task LoadAsync()
    {
        CancelAndDispose(ref _projectsCancellation);
        var cancellation = new CancellationTokenSource();
        _projectsCancellation = cancellation;
        IsLoading = true;
        ErrorMessage = null;
        try
        {
            var projects = await _service.GetProjectsAsync(cancellation.Token);
            if (!ReferenceEquals(cancellation, _projectsCancellation)) return;

            var selectedId = SelectedProject?.Id;
            Projects.Clear();
            foreach (var project in projects) Projects.Add(new ProjectItem(project));

            HasLoaded = true;
            var replacement = selectedId is null ? null : Projects.FirstOrDefault(project => project.Id == selectedId);
            if (replacement is null)
            {
                await SelectProjectAsync(null);
            }
            else if (!ReferenceEquals(replacement, _selectedProject))
            {
                await SelectProjectAsync(replacement);
            }
        }
        catch (ForgehandApiException exception)
        {
            if (!ReferenceEquals(cancellation, _projectsCancellation)) return;
            Projects.Clear();
            HasLoaded = false;
            ErrorMessage = exception.Message;
            await SelectProjectAsync(null);
        }
        catch (OperationCanceledException)
        {
        }
        finally
        {
            if (ReferenceEquals(cancellation, _projectsCancellation)) IsLoading = false;
        }
    }

    public async Task SelectProjectAsync(ProjectItem? project)
    {
        if (ReferenceEquals(project, _selectedProject)) return;

        CancelAndDispose(ref _sessionsCancellation);
        CancelAndDispose(ref _detailCancellation);
        CancelAndDispose(ref _writeCancellation);
        SetProperty(ref _selectedProject, project, nameof(SelectedProject));
        Sessions.Clear();
        HasLoadedSessions = false;
        SessionErrorMessage = null;
        SetSelectedSession(null);
        SessionDetails = null;
        SessionDetailErrorMessage = null;
        IsLoadingSessionDetails = false;
        RaiseWorkspaceState();
        RefreshSessionsCommand.NotifyCanExecuteChanged();
        ResetWriteUI(clearDraft: true);
        NotifyWriteCommands();

        if (project is not null) await LoadSessionsAsync();
    }

    public async Task LoadSessionsAsync()
    {
        var project = SelectedProject;
        if (project is null) return;

        CancelAndDispose(ref _sessionsCancellation);
        CancelAndDispose(ref _detailCancellation);
        CancelAndDispose(ref _writeCancellation);
        var cancellation = new CancellationTokenSource();
        _sessionsCancellation = cancellation;
        Sessions.Clear();
        HasLoadedSessions = false;
        SessionErrorMessage = null;
        SetSelectedSession(null);
        SessionDetails = null;
        SessionDetailErrorMessage = null;
        ResetWriteUI(clearDraft: true);
        RunErrorMessage = null;
        RunStatusMessage = null;
        IsRunConfirmationOpen = false;
        IsLoadingSessionDetails = false;
        IsLoadingSessions = true;
        RaiseWorkspaceState();
        try
        {
            var sessions = await _service.GetProjectSessionsAsync(project.Id, cancellation.Token);
            if (!ReferenceEquals(cancellation, _sessionsCancellation) || !ReferenceEquals(project, SelectedProject)) return;

            foreach (var session in sessions) Sessions.Add(new SessionItem(session));
            HasLoadedSessions = true;
        }
        catch (ForgehandApiException exception)
        {
            if (!ReferenceEquals(cancellation, _sessionsCancellation) || !ReferenceEquals(project, SelectedProject)) return;
            Sessions.Clear();
            SessionDetails = null;
            SessionErrorMessage = exception.Failure is ApiFailure.Forbidden or ApiFailure.NotFound
                ? "Sessions for this project are no longer available. Refresh the project list to update access."
                : exception.Message;
            if (exception.Failure is ApiFailure.Forbidden or ApiFailure.NotFound)
                await LoadAsync();
        }
        catch (OperationCanceledException)
        {
        }
        finally
        {
            if (ReferenceEquals(cancellation, _sessionsCancellation)) IsLoadingSessions = false;
        }
    }

    public async Task SelectSessionAsync(SessionItem? session)
    {
        if (ReferenceEquals(session, _selectedSession)) return;

        CancelAndDispose(ref _detailCancellation);
        CancelAndDispose(ref _writeCancellation);
        SetSelectedSession(session);
        SessionDetails = null;
        SessionDetailErrorMessage = null;
        ResetWriteUI(clearDraft: true);
        if (session is null)
        {
            IsLoadingSessionDetails = false;
            return;
        }

        var project = SelectedProject;
        var cancellation = new CancellationTokenSource();
        _detailCancellation = cancellation;
        IsLoadingSessionDetails = true;
        try
        {
            var details = await _service.GetSessionAsync(session.Id, cancellation.Token);
            if (!ReferenceEquals(cancellation, _detailCancellation) ||
                !ReferenceEquals(session, SelectedSession) ||
                !ReferenceEquals(project, SelectedProject)) return;

            if (details.Id != session.Id || project is null || details.ProjectId != project.Id)
                throw ForgehandApiException.For(ApiFailure.UnexpectedResponse);

            SessionDetails = new SessionItem(details);
        }
        catch (ForgehandApiException exception)
        {
            if (!ReferenceEquals(cancellation, _detailCancellation) || !ReferenceEquals(session, SelectedSession)) return;
            SessionDetails = null;
            SessionDetailErrorMessage = exception.Failure is ApiFailure.Forbidden or ApiFailure.NotFound
                ? "This session is no longer available. Refresh the session list to update it."
                : exception.Message;
        }
        catch (OperationCanceledException)
        {
        }
        finally
        {
            if (ReferenceEquals(cancellation, _detailCancellation)) IsLoadingSessionDetails = false;
        }
    }

    public void Clear()
    {
        CancelAndDispose(ref _projectsCancellation);
        CancelAndDispose(ref _sessionsCancellation);
        CancelAndDispose(ref _detailCancellation);
        CancelAndDispose(ref _writeCancellation);
        Projects.Clear();
        Sessions.Clear();
        SetProperty(ref _selectedProject, null, nameof(SelectedProject));
        SetSelectedSession(null);
        SessionDetails = null;
        IsLoading = false;
        IsLoadingSessions = false;
        IsLoadingSessionDetails = false;
        HasLoaded = false;
        HasLoadedSessions = false;
        ErrorMessage = null;
        SessionErrorMessage = null;
        SessionDetailErrorMessage = null;
        ResetWriteUI(clearDraft: true);
        RaiseWorkspaceState();
        RefreshSessionsCommand.NotifyCanExecuteChanged();
        NotifyWriteCommands();
    }

    private void SetSelectedSession(SessionItem? session)
    {
        SetProperty(ref _selectedSession, session, nameof(SelectedSession));
        OnPropertyChanged(nameof(ShowSessionSelectionState));
        NotifyRunCommands();
    }

    private void ResetWriteUI(bool clearDraft)
    {
        IsNewSessionFormOpen = false;
        IsRunConfirmationOpen = false;
        IsCreatingSession = false;
        IsSubmittingRun = false;
        NewSessionErrorMessage = null;
        RunErrorMessage = null;
        RunStatusMessage = null;
        if (clearDraft)
        {
            NewSessionTitle = string.Empty;
            NewSessionTask = string.Empty;
        }
    }

    private void NotifyWriteCommands()
    {
        OpenNewSessionCommand.NotifyCanExecuteChanged();
        CreateSessionCommand.NotifyCanExecuteChanged();
        NotifyRunCommands();
    }

    private void NotifyRunCommands()
    {
        OnPropertyChanged(nameof(ShowRunSelectedSession));
        OnPropertyChanged(nameof(CanRunSelectedSession));
        RequestRunCommand.NotifyCanExecuteChanged();
        ConfirmRunCommand.NotifyCanExecuteChanged();
    }

    private void OnCompatibilityChanged()
    {
        OnPropertyChanged(nameof(CanModifySessions));
        OnPropertyChanged(nameof(HasCompatibilityWarning));
        OnPropertyChanged(nameof(CompatibilityMessage));
        if (!CanModifySessions)
        {
            IsNewSessionFormOpen = false;
            IsRunConfirmationOpen = false;
        }
        NotifyWriteCommands();
    }

    private static void CancelAndDispose(ref CancellationTokenSource? source)
    {
        source?.Cancel();
        source?.Dispose();
        source = null;
    }

    private void RaiseProjectState()
    {
        OnPropertyChanged(nameof(ShowProjects));
        OnPropertyChanged(nameof(ShowProjectEmptyState));
        OnPropertyChanged(nameof(ShowEmptyState));
        OnPropertyChanged(nameof(ShowWorkspaceEmptyState));
    }

    private void RaiseSessionState()
    {
        OnPropertyChanged(nameof(ShowSessions));
        OnPropertyChanged(nameof(ShowSessionEmptyState));
        OnPropertyChanged(nameof(ShowSessionSelectionState));
    }

    private void RaiseWorkspaceState()
    {
        OnPropertyChanged(nameof(ShowWorkspace));
        OnPropertyChanged(nameof(ShowWorkspaceEmptyState));
        OnPropertyChanged(nameof(ShowSessionEmptyState));
        OnPropertyChanged(nameof(ShowSessionSelectionState));
    }
}
