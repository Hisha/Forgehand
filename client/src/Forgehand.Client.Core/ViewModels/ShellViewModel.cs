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
    Connected,
    Unavailable,
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
        Login.PropertyChanged += OnLoginPropertyChanged;
        _service.SessionChanged += OnSessionChanged;

        _endpointText = service.Endpoint.DisplayText;
        _notice = notice;
        _isSignedIn = service.IsSignedIn;
    }

    public LoginViewModel Login { get; }

    public ProjectsViewModel Projects { get; }

    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(SignOutCommand))]
    [NotifyPropertyChangedFor(nameof(IsSignedOut))]
    private bool _isSignedIn;

    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(ApplyEndpointCommand))]
    [NotifyCanExecuteChangedFor(nameof(CheckConnectionCommand))]
    private bool _isEndpointLocked;

    [ObservableProperty]
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
    private ConnectionState _connection = ConnectionState.Unknown;

    [ObservableProperty]
    private string _connectionText = "Not checked yet";

    [ObservableProperty]
    private string _sessionText = string.Empty;

    public bool IsSignedOut => !IsSignedIn;

    public bool HasEndpointError => EndpointError is not null;

    public bool HasNotice => Notice is not null;

    public bool IsConnected => Connection == ConnectionState.Connected;

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

    [RelayCommand(CanExecute = nameof(CanChangeEndpoint))]
    private async Task CheckConnectionAsync()
    {
        IsChecking = true;
        Connection = ConnectionState.Checking;
        ConnectionText = "Checking connection…";
        try
        {
            var health = await _service.CheckConnectionAsync(_lifetime.Token);
            Connection = ConnectionState.Connected;
            ConnectionText = $"Connected to Forgehand {health.Version} · API {health.ApiVersion}";
        }
        catch (ForgehandApiException exception)
        {
            Connection = ConnectionState.Unavailable;
            ConnectionText = exception.Message;
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

    private bool CanChangeEndpoint() => !IsEndpointLocked;

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
        switch (change)
        {
            case SessionChange.SignedIn:
                Notice = null;
                SessionText = DescribeSession();
                _ = Projects.LoadAsync();
                break;
            case SessionChange.SignedOut:
                Projects.Clear();
                SessionText = string.Empty;
                break;
            case SessionChange.Expired:
                Projects.Clear();
                SessionText = string.Empty;
                Notice = "Your session has expired. Sign in again to continue.";
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
        Login.PropertyChanged -= OnLoginPropertyChanged;
        _lifetime.Cancel();
        _service.Dispose();
    }
}
