using Avalonia;
using Avalonia.Controls.ApplicationLifetimes;
using Avalonia.Markup.Xaml;
using Forgehand.Client.Core.Api;
using Forgehand.Client.Core.Configuration;
using Forgehand.Client.Core.Session;
using Forgehand.Client.Core.ViewModels;
using Forgehand.Client.Views;

namespace Forgehand.Client;

public partial class App : Application
{
    public override void Initialize()
    {
        AvaloniaXamlLoader.Load(this);
    }

    public override void OnFrameworkInitializationCompleted()
    {
        if (ApplicationLifetime is IClassicDesktopStyleApplicationLifetime desktop)
        {
            var settingsStore = new ClientSettingsStore(ClientSettingsStore.DefaultPath);
            var settings = settingsStore.Load();
            var service = new ForgehandService(settings.Endpoint, endpoint => new ForgehandApiClient(endpoint));
            var shell = new ShellViewModel(service, settingsStore, settings.Notice);

            desktop.MainWindow = new MainWindow
            {
                DataContext = shell,
            };
        }

        base.OnFrameworkInitializationCompleted();
    }
}
