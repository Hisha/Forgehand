using Forgehand.Client.Core.Api;

namespace Forgehand.Client.Core.Session;

/// <summary>
/// The in-memory authentication state. It is never persisted. Discarding the
/// instance discards the only copy of the bearer token held by the client.
/// </summary>
public sealed record SignedInSession(string Token, AccountResponse Account, DateTimeOffset ExpiresAt)
{
    public override string ToString() => $"SignedInSession(account {Account.Id})";
}
