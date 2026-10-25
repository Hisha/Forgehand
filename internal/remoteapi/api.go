package remoteapi

// APIVersion is the version of the remote API contract implemented by this
// package. It is independent from the Forgehand release version and from the
// local Unix socket IPC protocol version.
const APIVersion = "v1"

// CompatibilityHeader carries the client's HTTP API compatibility claim.
// Every state-changing request must present the exact API version implemented
// by this server. Build identities are intentionally not part of this contract.
const CompatibilityHeader = "Forgehand-API-Version"

// APIBasePath is the root path of the versioned remote API.
const APIBasePath = "/api/" + APIVersion

// MaxRequestBytes bounds the size of a request body. The remote API only
// accepts small JSON documents, so a tight bound keeps a hostile client from
// making the daemon buffer an unbounded payload.
const MaxRequestBytes = 64 * 1024

// MaxIdentifierLength bounds the length of an identifier accepted in a URL
// path segment.
const MaxIdentifierLength = 32

const (
	MaxExecutionHistory            = 200
	DefaultResultContentCharacters = 64 * 1024
	MaxResultContentCharacters     = 128 * 1024
)

// Creation limits keep user-authored content bounded before it reaches the
// durable session engine. The overall request remains subject to
// MaxRequestBytes as well.
const (
	MaxSessionTitleLength = 200
	MaxSessionTaskLength  = 32 * 1024
)
