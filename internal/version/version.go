// Package version holds the application version and IPC protocol version.
package version

// Version is the application version.  It is stamped at build time:
//
//	go build -ldflags "-X github.com/daika7ana/dns-client/internal/version.Version=v1.2.3"
var Version = "dev"

// ProtocolVersion is the IPC protocol version reported by the `info` method and
// required in every request.
const ProtocolVersion = 1
