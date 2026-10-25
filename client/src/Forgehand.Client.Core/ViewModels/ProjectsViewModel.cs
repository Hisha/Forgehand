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

public sealed class ExecutionItem
{
    public ExecutionItem(ExecutionDetailResponse execution)
    {
        Response = execution;
        Id = execution.Id;
        SessionId = execution.SessionId;
        Status = execution.Status ?? string.Empty;
        StartedAt = execution.StartedAt ?? string.Empty;
        CompletedAt = EmptyToNull(execution.CompletedAt);
        CanceledAt = EmptyToNull(execution.CanceledAt);
        CancelReason = EmptyToNull(execution.CancelReason);
        FailureKind = EmptyToNull(execution.FailureKind);
        FailureReason = EmptyToNull(execution.FailureReason);
        Kind = EmptyToNull(execution.Kind);
        Provider = EmptyToNull(execution.Provider);
        Model = EmptyToNull(execution.Model);
        ResultCreatedAt = EmptyToNull(execution.ResultCreatedAt);
    }

    public ExecutionDetailResponse Response { get; }
    public long Id { get; }
    public long SessionId { get; }
    public string Status { get; }
    public string StartedAt { get; }
    public string? CompletedAt { get; }
    public string? CanceledAt { get; }
    public string? CancelReason { get; }
    public string? FailureKind { get; }
    public string? FailureReason { get; }
    public string? Kind { get; }
    public string? Provider { get; }
    public string? Model { get; }
    public string? ResultCreatedAt { get; }
    public bool ResultAvailable => Response.ResultAvailable;
    public bool HasFailure => FailureKind is not null || FailureReason is not null;
    public bool HasCancellation => CanceledAt is not null || CancelReason is not null;
    public bool HasProvider => Provider is not null;
    public bool HasModel => Model is not null;
    public bool HasTokens => Response.TotalTokens > 0;
    public bool HasLatency => Response.LatencyMs > 0;
    public string IdText => $"Execution {Id}";
    public string ProgressText => Response.TotalSteps > 0
        ? $"Step {Response.CurrentStep} of {Response.TotalSteps}"
        : string.Empty;
    public string TokenText => $"{Response.TotalTokens:N0} total ({Response.PromptTokens:N0} prompt, {Response.CompletionTokens:N0} completion)";
    public string LatencyText => $"{Response.LatencyMs:N0} ms";
    public string DurationText
    {
        get
        {
            if (!DateTimeOffset.TryParse(StartedAt, out var start)) return string.Empty;
            var terminal = CompletedAt ?? CanceledAt ?? ResultCreatedAt;
            if (!DateTimeOffset.TryParse(terminal, out var end) || end < start) return string.Empty;
            return (end - start).TotalSeconds < 1
                ? $"{(end - start).TotalMilliseconds:N0} ms"
                : $"{(end - start).TotalSeconds:N1} s";
        }
    }
    public bool HasDuration => DurationText.Length > 0;

    private static string? EmptyToNull(string? value) => string.IsNullOrWhiteSpace(value) ? null : value;
}

public sealed class ExecutionResultItem
{
    public ExecutionResultItem(ExecutionRecordResponse record)
    {
        Execution = new ExecutionItem(record.Execution);
        Result = record.Result;
    }

    public ExecutionItem Execution { get; }
    public ModelResultResponse? Result { get; }
    public bool HasResult => Result is not null;
    public bool HasNoResult => Result is null;
    public string Content => Result?.Content ?? string.Empty;
}

public sealed partial class ProjectsViewModel : ObservableObject
{
    private readonly ForgehandService _service;
    private CancellationTokenSource? _projectsCancellation;
    private CancellationTokenSource? _sessionsCancellation;
    private CancellationTokenSource? _detailCancellation;
    private CancellationTokenSource? _executionsCancellation;
    private CancellationTokenSource? _executionResultCancellation;
    private CancellationTokenSource? _writeCancellation;
    private ProjectItem? _selectedProject;
    private SessionItem? _selectedSession;
    private ExecutionItem? _selectedExecution;

    public ProjectsViewModel(ForgehandService service)
    {
        _service = service;
        _service.CompatibilityChanged += OnCompatibilityChanged;
        Projects.CollectionChanged += (_, _) => RaiseProjectState();
        Sessions.CollectionChanged += (_, _) => RaiseSessionState();
        Executions.CollectionChanged += (_, _) => RaiseExecutionState();
    }

    public ObservableCollection<ProjectItem> Projects { get; } = [];
    public ObservableCollection<SessionItem> Sessions { get; } = [];
    public ObservableCollection<ExecutionItem> Executions { get; } = [];

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

    public ExecutionItem? SelectedExecution
    {
        get => _selectedExecution;
        set => _ = SelectExecutionAsync(value);
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
    [NotifyCanExecuteChangedFor(nameof(RefreshExecutionsCommand))]
    [NotifyPropertyChangedFor(nameof(ShowExecutionEmptyState))]
    private bool _isLoadingExecutions;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(ShowExecutionEmptyState))]
    private bool _hasLoadedExecutions;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasExecutionError))]
    [NotifyPropertyChangedFor(nameof(ShowExecutionEmptyState))]
    private string? _executionErrorMessage;

    [ObservableProperty]
    private bool _isExecutionHistoryTruncated;

    [ObservableProperty]
    private bool _isLoadingExecutionResult;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasExecutionResultError))]
    private string? _executionResultErrorMessage;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasSelectedExecutionResult))]
    private ExecutionResultItem? _selectedExecutionResult;

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
    public bool HasExecutionError => ExecutionErrorMessage is not null;
    public bool HasExecutionResultError => ExecutionResultErrorMessage is not null;
    public bool ShowExecutions => Executions.Count > 0;
    public bool ShowExecutionEmptyState => HasLoadedExecutions && !IsLoadingExecutions && !HasExecutionError && Executions.Count == 0;
    public bool HasSelectedExecutionResult => SelectedExecutionResult is not null;
    public bool HasSelectedExecution => SelectedExecution is not null;
    public bool CanCopyResult => SelectedExecutionResult?.HasResult == true;
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

    [RelayCommand(CanExecute = nameof(CanRefreshExecutions))]
    private Task RefreshExecutionsAsync() => LoadExecutionsAsync(preserveSelection: true);

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
    private bool CanRefreshExecutions() => SelectedSession is not null && !IsLoadingExecutions;
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
    partial void OnSelectedExecutionResultChanged(ExecutionResultItem? value) =>
        OnPropertyChanged(nameof(CanCopyResult));

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
        ClearExecutionState();
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
        ClearExecutionState();
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
        ClearExecutionState();
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
            await LoadExecutionsAsync(preserveSelection: false);
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

    public async Task LoadExecutionsAsync(bool preserveSelection = true)
    {
        var session = SelectedSession;
        if (session is null) return;

        CancelAndDispose(ref _executionsCancellation);
        CancelAndDispose(ref _executionResultCancellation);
        var cancellation = new CancellationTokenSource();
        _executionsCancellation = cancellation;
        var selectedId = preserveSelection ? SelectedExecution?.Id : null;
        IsLoadingExecutions = true;
        ExecutionErrorMessage = null;
        ExecutionResultErrorMessage = null;
        SelectedExecutionResult = null;
        try
        {
            var history = await _service.GetSessionExecutionsAsync(session.Id, cancellation.Token);
            if (!ReferenceEquals(cancellation, _executionsCancellation) || !ReferenceEquals(session, SelectedSession)) return;
            if (history.Executions.Any(item => item.SessionId != session.Id))
                throw ForgehandApiException.For(ApiFailure.UnexpectedResponse);

            Executions.Clear();
            foreach (var execution in history.Executions) Executions.Add(new ExecutionItem(execution));
            HasLoadedExecutions = true;
            IsExecutionHistoryTruncated = history.Truncated;
            var selection = selectedId is null
                ? Executions.FirstOrDefault()
                : Executions.FirstOrDefault(item => item.Id == selectedId) ?? Executions.FirstOrDefault();
            await SelectExecutionAsync(selection);
        }
        catch (ForgehandApiException exception)
        {
            if (!ReferenceEquals(cancellation, _executionsCancellation) || !ReferenceEquals(session, SelectedSession)) return;
            Executions.Clear();
            SetSelectedExecution(null);
            HasLoadedExecutions = false;
            ExecutionErrorMessage = exception.Failure is ApiFailure.Forbidden or ApiFailure.NotFound
                ? "Execution history is no longer available for this session."
                : exception.Message;
        }
        catch (OperationCanceledException)
        {
        }
        finally
        {
            if (ReferenceEquals(cancellation, _executionsCancellation)) IsLoadingExecutions = false;
        }
    }

    public async Task SelectExecutionAsync(ExecutionItem? execution)
    {
        if (ReferenceEquals(execution, _selectedExecution)) return;
        CancelAndDispose(ref _executionResultCancellation);
        SetSelectedExecution(execution);
        SelectedExecutionResult = null;
        ExecutionResultErrorMessage = null;
        if (execution is null)
        {
            IsLoadingExecutionResult = false;
            return;
        }

        var session = SelectedSession;
        var cancellation = new CancellationTokenSource();
        _executionResultCancellation = cancellation;
        IsLoadingExecutionResult = true;
        try
        {
            var record = await _service.GetSessionExecutionAsync(execution.SessionId, execution.Id, cancellation.Token);
            if (!ReferenceEquals(cancellation, _executionResultCancellation) ||
                !ReferenceEquals(execution, SelectedExecution) || !ReferenceEquals(session, SelectedSession)) return;
            if (record.Execution.Id != execution.Id || record.Execution.SessionId != execution.SessionId)
                throw ForgehandApiException.For(ApiFailure.UnexpectedResponse);
            SelectedExecutionResult = new ExecutionResultItem(record);
        }
        catch (ForgehandApiException exception)
        {
            if (!ReferenceEquals(cancellation, _executionResultCancellation) || !ReferenceEquals(execution, SelectedExecution)) return;
            ExecutionResultErrorMessage = exception.Message;
        }
        catch (OperationCanceledException)
        {
        }
        finally
        {
            if (ReferenceEquals(cancellation, _executionResultCancellation)) IsLoadingExecutionResult = false;
        }
    }

    public void Clear()
    {
        CancelAndDispose(ref _projectsCancellation);
        CancelAndDispose(ref _sessionsCancellation);
        CancelAndDispose(ref _detailCancellation);
        CancelAndDispose(ref _executionsCancellation);
        CancelAndDispose(ref _executionResultCancellation);
        CancelAndDispose(ref _writeCancellation);
        Projects.Clear();
        Sessions.Clear();
        SetProperty(ref _selectedProject, null, nameof(SelectedProject));
        SetSelectedSession(null);
        SessionDetails = null;
        Executions.Clear();
        SetSelectedExecution(null);
        SelectedExecutionResult = null;
        IsLoading = false;
        IsLoadingSessions = false;
        IsLoadingSessionDetails = false;
        HasLoaded = false;
        HasLoadedSessions = false;
        ErrorMessage = null;
        SessionErrorMessage = null;
        SessionDetailErrorMessage = null;
        ExecutionErrorMessage = null;
        ExecutionResultErrorMessage = null;
        HasLoadedExecutions = false;
        IsLoadingExecutions = false;
        IsLoadingExecutionResult = false;
        IsExecutionHistoryTruncated = false;
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

    private void SetSelectedExecution(ExecutionItem? execution)
    {
        SetProperty(ref _selectedExecution, execution, nameof(SelectedExecution));
        OnPropertyChanged(nameof(HasSelectedExecution));
        OnPropertyChanged(nameof(CanCopyResult));
    }

    private void ClearExecutionState()
    {
        CancelAndDispose(ref _executionsCancellation);
        CancelAndDispose(ref _executionResultCancellation);
        Executions.Clear();
        SetSelectedExecution(null);
        SelectedExecutionResult = null;
        ExecutionErrorMessage = null;
        ExecutionResultErrorMessage = null;
        HasLoadedExecutions = false;
        IsLoadingExecutions = false;
        IsLoadingExecutionResult = false;
        IsExecutionHistoryTruncated = false;
        RefreshExecutionsCommand.NotifyCanExecuteChanged();
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

    private void RaiseExecutionState()
    {
        OnPropertyChanged(nameof(ShowExecutions));
        OnPropertyChanged(nameof(ShowExecutionEmptyState));
    }
}
