namespace Forgehand.Client.Core.Session;

public static class LoginValidation
{
    /// <summary>Returns a user-facing message for the first invalid field, or null when the input is acceptable.</summary>
    public static string? Validate(string? username, string? password, string? totpCode)
    {
        if (string.IsNullOrWhiteSpace(username))
        {
            return "Enter your username.";
        }

        if (string.IsNullOrEmpty(password))
        {
            return "Enter your password.";
        }

        var code = totpCode?.Trim() ?? string.Empty;
        if (code.Length != 6 || !code.All(character => character is >= '0' and <= '9'))
        {
            return "Enter the six-digit code from your authenticator app.";
        }

        return null;
    }
}
