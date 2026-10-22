using Avalonia;
using Avalonia.Headless;
using Avalonia.Headless.XUnit;

[assembly: AvaloniaTestApplication(typeof(Forgehand.Client.UiTests.HeadlessAppBuilder))]

namespace Forgehand.Client.UiTests;

public static class HeadlessAppBuilder
{
    public static AppBuilder BuildAvaloniaApp() =>
        AppBuilder.Configure<Forgehand.Client.App>()
            .UseHeadless(new AvaloniaHeadlessPlatformOptions());
}
