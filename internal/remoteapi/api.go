package remoteapi

// APIVersion is the version of the remote API contract implemented by this
// package. It is independent from the Forgehand release version and from the
// local Unix socket IPC protocol version.
const APIVersion = "v1"

// APIBasePath is the root path of the versioned remote API.
const APIBasePath = "/api/" + APIVersion

// MaxRequestBytes bounds the size of a request body. The remote API only
// accepts small JSON documents, so a tight bound keeps a hostile client from
// making the daemon buffer an unbounded payload.
const MaxRequestBytes = 64 * 1024

// MaxIdentifierLength bounds the length of an identifier accepted in a URL
// path segment.
const MaxIdentifierLength = 32
