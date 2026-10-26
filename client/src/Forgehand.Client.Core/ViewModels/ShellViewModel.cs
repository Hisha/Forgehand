using System.ComponentModel;
using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Configuration;
using Forgehand.Client.Core.Session;

namespace Forgehand.Client.Core.ViewModels;

public enum ConnectionState
{
    Unknown,
    Checking,
    Disconnected,
    ConnectedCompatible,
    ConnectedIncompatible,
    AuthenticationExpired,
    ServerError,
}

/// <summary>
/// Top-level state: server configuration, connection status, and which of the
/// login or project views is active.
/// </summary>
public sealed partial class ShellViewModel : ObservableObject, IDisposable
{
    private readonly ForgehandService _service;
    private readonly ClientSettingsStore _settingsStore;
    private readonly CancellationTokenSource _lifetime = new();

    public ShellViewModel(ForgehandService service, ClientSettingsStore settingsStore, string? notice)
    {
        _service = service;
        _settingsStore = settingsStore;
        Login = new LoginViewModel(service, _lifetime.Token);
        Projects = new ProjectsViewModel(service);
        DaemonStatus = new DaemonStatusViewModel(service);
        Login.PropertyChanged += OnLoginPropertyChanged;
        _service.SessionChanged += OnSessionChanged;
        _service.CompatibilityChanged += OnCompatibilityChanged;

        _endpointText = service.Endpoint.DisplayText;
        _notice = notice;
        _isSignedIn = service.IsSignedIn;
    }

    public LoginViewModel Login { get; }

    public ProjectsViewModel Projects { get; }

    public DaemonStatusViewModel DaemonStatus { get; }

    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(SignOutCommand))]
    [NotifyPropertyChangedFor(nameof(IsSignedOut))]
    [NotifyPropertyChangedFor(nameof(CanViewDaemonStatus))]
    private bool _isSignedIn;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(IsDaemonStatusSelected))]
    private bool _isProjectsSelected = true;

    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(ApplyEndpointCommand))]
    private bool _isEndpointLocked;

    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(CheckConnectionCommand))]
    private bool _isChecking;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasEndpointError))]
    private string? _endpointError;

    [ObservableProperty]
    private string _endpointText = string.Empty;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasNotice))]
    private string? _notice;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(IsConnected))]
    [NotifyPropertyChangedFor(nameof(IsIncompatible))]
    private ConnectionState _connection = ConnectionState.Unknown;

    [ObservableProperty]
    private string _connectionText = "Not checked yet";

    [ObservableProperty]
    private string _sessionText = string.Empty;

    public bool IsSignedOut => !IsSignedIn;

    public bool IsDaemonStatusSelected => !IsProjectsSelected;

    public bool CanViewDaemonStatus => IsSignedIn &&
        (_service.Account?.Role is "administrator" or "admin");

    public bool HasEndpointError => EndpointError is not null;

    public bool HasNotice => Notice is not null;

    public bool IsConnected => Connection == ConnectionState.ConnectedCompatible;

    public bool IsIncompatible => Connection == ConnectionState.ConnectedIncompatible;

    public Task InitializeAsync() => CheckConnectionAsync();

    partial void OnIsSignedInChanged(bool value) => UpdateEndpointLock();

    partial void OnIsCheckingChanged(bool value) => UpdateEndpointLock();

    [RelayCommand(CanExecute = nameof(CanChangeEndpoint))]
    private async Task ApplyEndpointAsync()
    {
        if (!ServerEndpoint.TryParse(EndpointText, out var endpoint, out var error))
        {
            EndpointError = error;
            return;
        }

        EndpointError = null;
        _service.ChangeEndpoint(endpoint);
        EndpointText = endpoint.DisplayText;

        try
        {
            _settingsStore.Save(endpoint);
        }
        catch (Exception exception) when (exception is IOException or UnauthorizedAccessException)
        {
            Notice = "The server address is in use now but could not be saved for the next launch.";
        }

        await CheckConnectionAsync();
    }

    [RelayCommand(CanExecute = nameof(CanCheckConnection))]
    private async Task CheckConnectionAsync()
    {
        IsChecking = true;
        Connection = ConnectionState.Checking;
        ConnectionText = "Checking connection…";
        try
        {
            var health = await _service.CheckConnectionAsync(_lifetime.Token);
            ShowCompatibleStatus(health);
        }
        catch (ForgehandApiException exception)
        {
            Connection = exception.Failure switch
            {
                ApiFailure.IncompatibleApi => ConnectionState.ConnectedIncompatible,
                ApiFailure.Unreachable or ApiFailure.Timeout => ConnectionState.Disconnected,
                ApiFailure.SessionExpired => ConnectionState.AuthenticationExpired,
                _ => ConnectionState.ServerError,
            };
            ConnectionText = exception.Message;
            if (exception.Failure == ApiFailure.IncompatibleApi)
            {
                ConnectionText = DescribeIncompatibleConnection();
            }
        }
        catch (OperationCanceledException) when (_lifetime.IsCancellationRequested)
        {
        }
        finally
        {
            IsChecking = false;
        }
    }

    [RelayCommand(CanExecute = nameof(IsSignedIn))]
    private async Task SignOutAsync()
    {
        var confirmed = await _service.SignOutAsync();
        if (!confirmed)
        {
            Notice = "Signed out on this device, but the daemon did not confirm the sign-out. " +
                     "The server session remains valid until it expires.";
        }
    }

    [RelayCommand]
    private void ShowProjects() => IsProjectsSelected = true;

    [RelayCommand(CanExecute = nameof(CanViewDaemonStatus))]
    private async Task ShowDaemonStatusAsync()
    {
        IsProjectsSelected = false;
        await DaemonStatus.RefreshCommand.ExecuteAsync(null);
    }

    private bool CanChangeEndpoint() => !IsEndpointLocked;

    private bool CanCheckConnection() => !IsChecking && !Login.IsBusy;

    private void UpdateEndpointLock()
    {
        IsEndpointLocked = IsSignedIn || IsChecking || Login.IsBusy;
    }

    private void OnLoginPropertyChanged(object? sender, PropertyChangedEventArgs e)
    {
        if (e.PropertyName == nameof(LoginViewModel.IsBusy))
        {
            UpdateEndpointLock();
        }
    }

    private void OnSessionChanged(SessionChange change)
    {
        IsSignedIn = _service.IsSignedIn;
        ShowDaemonStatusCommand.NotifyCanExecuteChanged();
        DaemonStatus.NotifySessionChanged();
        switch (change)
        {
            case SessionChange.SignedIn:
                Notice = null;
                SessionText = DescribeSession();
                if (_service.DaemonHealth is { } health) ShowCompatibleStatus(health);
                IsProjectsSelected = true;
                _ = Projects.LoadAsync();
                break;
            case SessionChange.SignedOut:
                Projects.Clear();
                DaemonStatus.Clear();
                IsProjectsSelected = true;
                SessionText = string.Empty;
                break;
            case SessionChange.Expired:
                Projects.Clear();
                DaemonStatus.Clear();
                IsProjectsSelected = true;
                SessionText = string.Empty;
                Notice = "Your session has expired. Sign in again to continue.";
                Connection = ConnectionState.AuthenticationExpired;
                ConnectionText = "Authentication expired · sign in again to continue";
                break;
        }
    }

    private string DescribeSession()
    {
        var account = _service.Account;
        var name = string.IsNullOrWhiteSpace(account?.DisplayName) ? account?.Username : account.DisplayName;
        var expiry = _service.SessionExpiresAt?.ToLocalTime().ToString("HH:mm");
        return $"Signed in as {name} · session until {expiry}";
    }

    public void Dispose()
    {
        _service.SessionChanged -= OnSessionChanged;
        _service.CompatibilityChanged -= OnCompatibilityChanged;
        Login.PropertyChanged -= OnLoginPropertyChanged;
        _lifetime.Cancel();
        DaemonStatus.Dispose();
        _service.Dispose();
    }

    private void OnCompatibilityChanged()
    {
        if (_service.ApiCompatibility == ApiCompatibilityState.Compatible && _service.DaemonHealth is { } health)
        {
            ShowCompatibleStatus(health);
        }
        else if (_service.ApiCompatibility == ApiCompatibilityState.Incompatible)
        {
            Connection = ConnectionState.ConnectedIncompatible;
            ConnectionText = DescribeIncompatibleConnection();
        }
    }

    private void ShowCompatibleStatus(HealthResponse health)
    {
        Connection = ConnectionState.ConnectedCompatible;
        var version = string.IsNullOrWhiteSpace(health.Version) ? "unknown" : health.Version;
        var build = string.IsNullOrWhiteSpace(health.BuildIdentity) ? "unavailable" : health.BuildIdentity;
        ConnectionText = $"Connected and compatible · Daemon {version} · Build {build} · HTTP API {health.ApiVersion}";
    }

    private string DescribeIncompatibleConnection()
    {
        var health = _service.DaemonHealth;
        var version = string.IsNullOrWhiteSpace(health?.Version) ? "unknown" : health.Version;
        var build = string.IsNullOrWhiteSpace(health?.BuildIdentity) ? "unavailable" : health.BuildIdentity;
        var api = string.IsNullOrWhiteSpace(health?.ApiVersion) ? "missing/unknown" : health.ApiVersion;
        return $"Connected but incompatible · Daemon {version} · Build {build} · HTTP API {api} " +
               $"(client requires {ForgehandApiClient.SupportedApiVersion}). Update or restart the daemon, then check again.";
    }
}
