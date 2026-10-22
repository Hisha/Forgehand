using CommunityToolkit.Mvvm.ComponentModel;
using CommunityToolkit.Mvvm.Input;
using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Session;

namespace Forgehand.Client.Core.ViewModels;

public sealed partial class LoginViewModel : ObservableObject
{
    private readonly ForgehandService _service;
    private readonly CancellationToken _cancellationToken;

    public LoginViewModel(ForgehandService service, CancellationToken cancellationToken = default)
    {
        _service = service;
        _cancellationToken = cancellationToken;
    }

    [ObservableProperty]
    private string _username = string.Empty;

    [ObservableProperty]
    private string _password = string.Empty;

    [ObservableProperty]
    private string _totpCode = string.Empty;

    [ObservableProperty]
    [NotifyPropertyChangedFor(nameof(HasError))]
    private string? _errorMessage;

    [ObservableProperty]
    [NotifyCanExecuteChangedFor(nameof(SignInCommand))]
    private bool _isBusy;

    public bool HasError => ErrorMessage is not null;

    [RelayCommand(CanExecute = nameof(CanSignIn))]
    private async Task SignInAsync()
    {
        ErrorMessage = LoginValidation.Validate(Username, Password, TotpCode);
        if (ErrorMessage is not null)
        {
            return;
        }

        IsBusy = true;
        try
        {
            await _service.SignInAsync(Username, Password, TotpCode, _cancellationToken);
        }
        catch (ForgehandApiException exception)
        {
            ErrorMessage = exception.Message;
        }
        catch (OperationCanceledException)
        {
        }
        finally
        {
            // The password and one-time code are not kept in the UI after an attempt.
            Password = string.Empty;
            TotpCode = string.Empty;
            IsBusy = false;
        }
    }

    private bool CanSignIn() => !IsBusy;
}
