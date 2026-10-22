using System.Diagnostics.CodeAnalysis;
using System.Net;

namespace Forgehand.Client.Core.Configuration;

/// <summary>
/// A validated base address of a Forgehand daemon API.
///
/// Plain HTTP is accepted only for loopback hosts. The daemon serves no TLS on
/// its listener, so a remote plain-HTTP connection would expose credentials on
/// the network. Remote servers must use https://.
/// </summary>
public sealed class ServerEndpoint
{
    public const string DefaultAddress = "http://127.0.0.1:8787";

    private ServerEndpoint(Uri baseAddress)
    {
        BaseAddress = baseAddress;
    }

    public static ServerEndpoint Default { get; } = new(new Uri(DefaultAddress + "/", UriKind.Absolute));

    /// <summary>The address the HTTP client resolves API paths against. Always ends with "/".</summary>
    public Uri BaseAddress { get; }

    /// <summary>Scheme, host, and port, for example "http://127.0.0.1:8787".</summary>
    public string DisplayText => BaseAddress.GetLeftPart(UriPartial.Authority);

    public static bool TryParse(
        string? input,
        [NotNullWhen(true)] out ServerEndpoint? endpoint,
        [NotNullWhen(false)] out string? error)
    {
        endpoint = null;
        var text = input?.Trim();
        if (string.IsNullOrEmpty(text))
        {
            error = "Enter the server address, for example http://127.0.0.1:8787.";
            return false;
        }

        if (!Uri.TryCreate(text, UriKind.Absolute, out var uri)
            || (uri.Scheme != Uri.UriSchemeHttp && uri.Scheme != Uri.UriSchemeHttps))
        {
            error = "The address must start with http:// (loopback only) or https://.";
            return false;
        }

        if (uri.UserInfo.Length > 0)
        {
            error = "The address must not contain a user name or password.";
            return false;
        }

        if (uri.Query.Length > 0 || uri.Fragment.Length > 0)
        {
            error = "The address must not contain a query string or fragment.";
            return false;
        }

        if (uri.AbsolutePath != "/")
        {
            error = "Enter only the scheme, host, and port. The API path is added automatically.";
            return false;
        }

        var loopback = IsLoopbackHost(uri.Host);
        if (uri.Scheme == Uri.UriSchemeHttp)
        {
            if (!loopback)
            {
                error = "Plain HTTP is accepted only for 127.0.0.1, ::1, or localhost. Use https:// for other servers.";
                return false;
            }

            if (uri.IsDefaultPort)
            {
                error = "Specify the daemon port explicitly, for example http://127.0.0.1:8787.";
                return false;
            }
        }

        var baseAddress = new Uri(uri.GetLeftPart(UriPartial.Authority) + "/", UriKind.Absolute);
        endpoint = new ServerEndpoint(baseAddress);
        error = null;
        return true;
    }

    private static bool IsLoopbackHost(string host)
    {
        var bare = host.Trim('[', ']');
        if (string.Equals(bare, "localhost", StringComparison.OrdinalIgnoreCase))
        {
            return true;
        }

        return IPAddress.TryParse(bare, out var address) && IPAddress.IsLoopback(address);
    }
}
