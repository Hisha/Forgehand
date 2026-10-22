using System.Text.Json;
using System.Text.Json.Serialization;

namespace Forgehand.Client.Core.Configuration;

/// <summary>
/// Connection settings persisted between launches. Credentials are never stored here.
/// </summary>
public sealed record ClientSettings([property: JsonPropertyName("apiEndpoint")] string ApiEndpoint);

/// <summary>The endpoint to use at startup and an optional notice about a discarded setting.</summary>
public sealed record SettingsLoadResult(ServerEndpoint Endpoint, string? Notice);

public sealed class ClientSettingsStore
{
    private static readonly JsonSerializerOptions JsonOptions = new(JsonSerializerDefaults.Web) { WriteIndented = true };

    public ClientSettingsStore(string path)
    {
        Path = path;
    }

    public static string DefaultPath { get; } = System.IO.Path.Combine(
        Environment.GetFolderPath(Environment.SpecialFolder.ApplicationData),
        "Forgehand",
        "client-settings.json");

    public string Path { get; }

    /// <summary>
    /// Loads the saved endpoint. A missing file yields the default. An invalid
    /// saved value is not used silently: the default is returned with a notice.
    /// </summary>
    public SettingsLoadResult Load()
    {
        if (!File.Exists(Path))
        {
            return new SettingsLoadResult(ServerEndpoint.Default, null);
        }

        try
        {
            var settings = JsonSerializer.Deserialize<ClientSettings>(File.ReadAllText(Path), JsonOptions);
            if (settings is not null && ServerEndpoint.TryParse(settings.ApiEndpoint, out var endpoint, out _))
            {
                return new SettingsLoadResult(endpoint, null);
            }
        }
        catch (Exception exception) when (exception is IOException or UnauthorizedAccessException or JsonException)
        {
        }

        return new SettingsLoadResult(
            ServerEndpoint.Default,
            $"The saved server setting in {Path} could not be read, so the default address is used.");
    }

    public void Save(ServerEndpoint endpoint)
    {
        var directory = System.IO.Path.GetDirectoryName(Path);
        if (!string.IsNullOrEmpty(directory))
        {
            Directory.CreateDirectory(directory);
        }

        File.WriteAllText(Path, JsonSerializer.Serialize(new ClientSettings(endpoint.DisplayText), JsonOptions));
    }
}
