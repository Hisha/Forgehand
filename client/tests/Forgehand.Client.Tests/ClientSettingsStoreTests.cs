using Forgehand.Client.Core.Configuration;

namespace Forgehand.Client.Tests;

public sealed class ClientSettingsStoreTests : IDisposable
{
    private readonly string _directory = Path.Combine(Path.GetTempPath(), "forgehand-client-tests-" + Guid.NewGuid().ToString("N"));

    public void Dispose()
    {
        if (Directory.Exists(_directory))
        {
            Directory.Delete(_directory, recursive: true);
        }
    }

    private string SettingsPath => Path.Combine(_directory, "client-settings.json");

    [Fact]
    public void MissingFileYieldsDefaultWithoutNotice()
    {
        var result = new ClientSettingsStore(SettingsPath).Load();

        Assert.Equal(ServerEndpoint.DefaultAddress, result.Endpoint.DisplayText);
        Assert.Null(result.Notice);
    }

    [Fact]
    public void SavedEndpointRoundTripsWithoutCredentials()
    {
        var store = new ClientSettingsStore(SettingsPath);
        ServerEndpoint.TryParse("http://localhost:9000", out var endpoint, out _);

        store.Save(endpoint!);
        var result = store.Load();

        Assert.Equal("http://localhost:9000", result.Endpoint.DisplayText);
        Assert.Null(result.Notice);
        var contents = File.ReadAllText(SettingsPath);
        Assert.DoesNotContain("password", contents, StringComparison.OrdinalIgnoreCase);
        Assert.DoesNotContain("token", contents, StringComparison.OrdinalIgnoreCase);
    }

    [Fact]
    public void InvalidRemotePlainHttpSettingIsNotUsedSilently()
    {
        Directory.CreateDirectory(_directory);
        File.WriteAllText(SettingsPath, """{"apiEndpoint":"http://192.168.1.10:8787"}""");

        var result = new ClientSettingsStore(SettingsPath).Load();

        Assert.Equal(ServerEndpoint.DefaultAddress, result.Endpoint.DisplayText);
        Assert.NotNull(result.Notice);
    }

    [Fact]
    public void MalformedFileYieldsDefaultWithNotice()
    {
        Directory.CreateDirectory(_directory);
        File.WriteAllText(SettingsPath, "{not json");

        var result = new ClientSettingsStore(SettingsPath).Load();

        Assert.Equal(ServerEndpoint.DefaultAddress, result.Endpoint.DisplayText);
        Assert.NotNull(result.Notice);
    }
}
