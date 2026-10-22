using Forgehand.Client.Core.Configuration;

namespace Forgehand.Client.Tests;

public sealed class ServerEndpointTests
{
    [Theory]
    [InlineData("http://127.0.0.1:8787", "http://127.0.0.1:8787")]
    [InlineData("  http://127.0.0.1:8787/  ", "http://127.0.0.1:8787")]
    [InlineData("http://localhost:9000", "http://localhost:9000")]
    [InlineData("http://[::1]:8787", "http://[::1]:8787")]
    [InlineData("https://forgehand.example.com:8443", "https://forgehand.example.com:8443")]
    public void AcceptsValidAddresses(string input, string expectedDisplay)
    {
        Assert.True(ServerEndpoint.TryParse(input, out var endpoint, out var error));
        Assert.Null(error);
        Assert.Equal(expectedDisplay, endpoint.DisplayText);
        Assert.EndsWith("/", endpoint.BaseAddress.AbsoluteUri);
    }

    [Fact]
    public void DefaultIsLoopbackDaemonAddress()
    {
        Assert.Equal("http://127.0.0.1:8787", ServerEndpoint.Default.DisplayText);
        Assert.Equal("http://127.0.0.1:8787/", ServerEndpoint.Default.BaseAddress.AbsoluteUri);
    }

    [Theory]
    [InlineData("http://192.168.1.10:8787")]
    [InlineData("http://0.0.0.0:8787")]
    [InlineData("http://forgehand.example.com:8787")]
    [InlineData("http://10.0.0.5:8787")]
    public void RejectsPlainHttpForNonLoopbackHosts(string input)
    {
        Assert.False(ServerEndpoint.TryParse(input, out var endpoint, out var error));
        Assert.Null(endpoint);
        Assert.Contains("Plain HTTP", error);
    }

    [Theory]
    [InlineData("")]
    [InlineData("   ")]
    [InlineData(null)]
    public void RejectsEmptyAddress(string? input)
    {
        Assert.False(ServerEndpoint.TryParse(input, out _, out var error));
        Assert.NotNull(error);
    }

    [Theory]
    [InlineData("127.0.0.1:8787")]
    [InlineData("ftp://127.0.0.1:8787")]
    [InlineData("localhost:8787")]
    [InlineData("not a url")]
    public void RejectsMissingSchemeOrUnsupportedScheme(string input)
    {
        Assert.False(ServerEndpoint.TryParse(input, out _, out var error));
        Assert.Contains("http://", error);
    }

    [Fact]
    public void RejectsPlainHttpWithoutExplicitPort()
    {
        Assert.False(ServerEndpoint.TryParse("http://127.0.0.1", out _, out var error));
        Assert.Contains("port", error);
    }

    [Theory]
    [InlineData("http://user:secret@127.0.0.1:8787")]
    [InlineData("http://127.0.0.1:8787/api/v1")]
    [InlineData("http://127.0.0.1:8787/?x=1")]
    [InlineData("http://127.0.0.1:8787/#fragment")]
    public void RejectsCredentialsPathsQueriesAndFragments(string input)
    {
        Assert.False(ServerEndpoint.TryParse(input, out _, out var error));
        Assert.NotNull(error);
    }

    [Fact]
    public void ErrorMessagesDoNotEchoUserCredentials()
    {
        Assert.False(ServerEndpoint.TryParse("http://alice:hunter2@127.0.0.1:8787", out _, out var error));
        Assert.DoesNotContain("hunter2", error);
    }
}
