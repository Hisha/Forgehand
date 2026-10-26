using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Session;

namespace Forgehand.Client.Core.ViewModels;

/// <summary>Read-only, manually refreshed view of daemon-owned operational state.</summary>
public sealed partial class DaemonStatusViewModel : ObservableObject, IDisposable
{
    private readonly ForgehandService _service;
    private CancellationTokenSource? _refresh;
    private long _generation;

    public DaemonStatusViewModel(ForgehandService service) => _service = service;

    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(RefreshCommand))]
    private bool _isLoading;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasStatus))]
    [NotifyPropertyChangedFor(nameof(HasRestartWarning))]
    [NotifyPropertyChangedFor(nameof(VersionText))]
    [NotifyPropertyChangedFor(nameof(BuildText))]
    [NotifyPropertyChangedFor(nameof(BuildTimestampText))]
    [NotifyPropertyChangedFor(nameof(ApiVersionText))]
    [NotifyPropertyChangedFor(nameof(StartedText))]
    [NotifyPropertyChangedFor(nameof(UptimeText))]
    [NotifyPropertyChangedFor(nameof(WorkerCapacityText))]
    [NotifyPropertyChangedFor(nameof(ActiveWorkersText))]
    [NotifyPropertyChangedFor(nameof(RunningSessionsText))]
    [NotifyPropertyChangedFor(nameof(QueuedExecutionsText))]
    [NotifyPropertyChangedFor(nameof(PendingSessionsText))]
    [NotifyPropertyChangedFor(nameof(CompletedSessionsText))]
    [NotifyPropertyChangedFor(nameof(FailedSessionsText))]
    [NotifyPropertyChangedFor(nameof(ExecutableComparisonText))]
    [NotifyPropertyChangedFor(nameof(RunningIdentityText))]
    [NotifyPropertyChangedFor(nameof(InstalledIdentityText))]
    [NotifyPropertyChangedFor(nameof(DatabaseHealthText))]
    [NotifyPropertyChangedFor(nameof(DispatcherHealthText))]
    [NotifyPropertyChangedFor(nameof(WorkerHealthText))]
    private DaemonStatusResponse? _status;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasError))]
    private string? _error;

    [ObservableProperty]
    private string _lastRefreshText = "Not refreshed yet";

    public bool HasStatus => Status is not null;
    public bool HasError => Error is not null;
    public bool HasRestartWarning => Status?.Executable.RestartRecommended == true && Status.Executable.Comparison == "mismatch";

    public string VersionText => TextOrUnknown(Status?.Identity.Version);
    public string BuildText => TextOrUnknown(Status?.Identity.BuildIdentity);
    public string BuildTimestampText => TextOrUnknown(Status?.Identity.BuildTimestamp);
    public string ApiVersionText => TextOrUnknown(Status?.Identity.ApiVersion);
    public string StartedText => FormatTimestamp(Status?.Identity.StartedAt);
    public string UptimeText => Status is null ? "Unknown" : FormatDuration(Status.Identity.UptimeSeconds);
    public string WorkerCapacityText => Count(Status?.Activity.WorkerCapacity);
    public string ActiveWorkersText => Count(Status?.Activity.ActiveWorkers);
    public string RunningSessionsText => Count(Status?.Activity.RunningSessions);
    public string QueuedExecutionsText => Count(Status?.Activity.QueuedExecutions);
    public string PendingSessionsText => Count(Status?.Activity.PendingSessions);
    public string CompletedSessionsText => Count(Status?.Activity.CompletedSessions);
    public string FailedSessionsText => Count(Status?.Activity.FailedSessions);
    public string ExecutableComparisonText => Status?.Executable.Comparison switch
    {
        "match" => "Running and installed executable match",
        "mismatch" => "Installed executable differs from the running process",
        _ => "Executable comparison unavailable",
    };
    public string RunningIdentityText => TextOrUnknown(Status?.Executable.RunningFileIdentity);
    public string InstalledIdentityText => TextOrUnknown(Status?.Executable.InstalledFileIdentity);
    public string DatabaseHealthText => Health(Status?.Health.Database);
    public string DispatcherHealthText => Health(Status?.Health.Dispatcher);
    public string WorkerHealthText => Health(Status?.Health.Workers);

    [RelayCommand(CanExecute = nameof(CanRefresh))]
    private async Task RefreshAsync()
    {
        _refresh?.Cancel();
        _refresh?.Dispose();
        _refresh = new CancellationTokenSource();
        var cancellation = _refresh.Token;
        var generation = ++_generation;
        IsLoading = true;
        Error = null;
        try
        {
            var status = await _service.GetDaemonStatusAsync(cancellation);
            if (generation != _generation || cancellation.IsCancellationRequested)
                return;
            Status = status;
            LastRefreshText = $"Last refreshed {DateTimeOffset.Now:yyyy-MM-dd HH:mm:ss zzz}";
        }
        catch (OperationCanceledException) when (cancellation.IsCancellationRequested)
        {
        }
        catch (ForgehandApiException exception)
        {
            if (generation == _generation)
                Error = exception.Message;
        }
        finally
        {
            if (generation == _generation)
                IsLoading = false;
        }
    }

    private bool CanRefresh() => !IsLoading && _service.IsSignedIn;

    public void Clear()
    {
        _generation++;
        _refresh?.Cancel();
        _refresh?.Dispose();
        _refresh = null;
        IsLoading = false;
        Status = null;
        Error = null;
        LastRefreshText = "Not refreshed yet";
    }

    public void NotifySessionChanged() => RefreshCommand.NotifyCanExecuteChanged();

    public void Dispose() => Clear();

    private static string TextOrUnknown(string? value) => string.IsNullOrWhiteSpace(value) ? "Unknown" : value;
    private static string Count(int? value) => value?.ToString() ?? "Unavailable";
    private static string Health(string? value) => value switch
    {
        "ok" => "Available",
        "unavailable" => "Unavailable",
        _ => "Unknown",
    };
    private static string FormatTimestamp(string? value) =>
        DateTimeOffset.TryParse(value, out var timestamp) ? timestamp.ToLocalTime().ToString("yyyy-MM-dd HH:mm:ss zzz") : "Unknown";
    private static string FormatDuration(long seconds)
    {
        var duration = TimeSpan.FromSeconds(Math.Max(0, seconds));
        return duration.Days > 0
            ? $"{duration.Days}d {duration.Hours}h {duration.Minutes}m"
            : $"{(int)duration.TotalHours}h {duration.Minutes}m {duration.Seconds}s";
    }
}
