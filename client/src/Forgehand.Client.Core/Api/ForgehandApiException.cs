using Forgehand.Client.Core.Configuration;

namespace Forgehand.Client.Core.Api;

public enum ApiFailure
{
    Unreachable,
    Timeout,
    InvalidCredentials,
    SessionExpired,
    AccountLocked,
    Throttled,
    EnrollmentRequired,
    AccountDisabled,
    Forbidden,
    NotFound,
    InvalidRequest,
    ServerError,
    IncompatibleApi,
    UnexpectedResponse,
}

/// <summary>
/// A failed API operation. <see cref="Exception.Message"/> is a fixed,
/// user-facing sentence. It never contains text returned by the server.
/// </summary>
public sealed class ForgehandApiException : Exception
{
    public ForgehandApiException(ApiFailure failure, string message)
        : base(message)
    {
        Failure = failure;
    }

    public ApiFailure Failure { get; }

    public static ForgehandApiException For(ApiFailure failure, ServerEndpoint? endpoint = null) =>
        new(failure, Describe(failure, endpoint));

    private static string Describe(ApiFailure failure, ServerEndpoint? endpoint) => failure switch
    {
        ApiFailure.Unreachable =>
            $"Could not reach the Forgehand daemon at {endpoint?.DisplayText ?? "the configured address"}. " +
            "Confirm that the daemon is running and that its API listener is enabled.",
        ApiFailure.Timeout => "The Forgehand daemon did not respond in time. Check the server and try again.",
        ApiFailure.InvalidCredentials => "The username, password, or authenticator code was not accepted.",
        ApiFailure.SessionExpired => "Your session has expired. Sign in again to continue.",
        ApiFailure.AccountLocked =>
            "This account is temporarily locked after repeated failed sign-in attempts. Try again later.",
        ApiFailure.Throttled => "Too many sign-in attempts from this device. Wait a few minutes and try again.",
        ApiFailure.EnrollmentRequired =>
            "Authenticator enrollment for this account is not complete. Finish enrollment with the local Forgehand tools.",
        ApiFailure.AccountDisabled => "This account is disabled. Contact the Forgehand administrator.",
        ApiFailure.Forbidden => "This account is not authorized for that action.",
        ApiFailure.NotFound => "The requested project or session is no longer available.",
        ApiFailure.InvalidRequest => "The daemon rejected the request as invalid.",
        ApiFailure.ServerError => "The Forgehand daemon could not complete the request.",
        ApiFailure.IncompatibleApi => "The daemon uses an API version this client does not support.",
        _ => "The daemon returned a response this client does not understand.",
    };
}
