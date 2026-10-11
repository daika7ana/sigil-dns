// Package ipc implements the versioned request/response protocol spoken over
// the dns-client named pipe (SPEC §2, §4.7).
//
// The transport is abstracted so the protocol is testable off Windows: Windows
// uses a named pipe with a locking SDDL; other platforms use a Unix socket.
package ipc

import (
	"encoding/json"
	"errors"
)

// ProtocolVersion is the current IPC protocol version.  Peers must send it in
// every request; unknown versions are rejected.
const ProtocolVersion = 1

// Method is an IPC request method.
type Method string

// Methods.
const (
	// MethodStatus is read-only and available to any connected caller.
	MethodStatus Method = "status"

	// MethodEnable is state-changing and requires administrator privileges.
	MethodEnable Method = "enable"

	// MethodDisable is state-changing and requires administrator privileges.
	MethodDisable Method = "disable"
)

// valid reports whether m is a known method.
func (m Method) valid() (ok bool) {
	switch m {
	case MethodStatus, MethodEnable, MethodDisable:
		return true
	default:
		return false
	}
}

// Request is the JSON envelope of a request.
type Request struct {
	// Version is the protocol version; must equal ProtocolVersion.
	Version int `json:"version"`

	// Method is the requested operation.
	Method Method `json:"method"`

	// Params is the method-specific payload (unused so far).
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the JSON envelope of a response.
type Response struct {
	// Data is the method-specific payload, present only on success.
	Data json.RawMessage `json:"data,omitempty"`

	// Error is a human-readable error, present only on failure.
	Error string `json:"error,omitempty"`

	// Version is the protocol version.
	Version int `json:"version"`

	// OK is true on success.
	OK bool `json:"ok"`
}

// Caller is the identity of a connected peer, established by the transport
// (kernel SDDL / impersonation), never by the request itself.
type Caller struct {
	// Admin is true when the caller has administrator privileges.
	Admin bool
}

// Protocol errors.
var (
	// ErrVersion is returned for an unsupported protocol version.
	ErrVersion = errors.New("unsupported protocol version")

	// ErrMethod is returned for an unknown method.
	ErrMethod = errors.New("unknown method")

	// ErrForbidden is returned when a caller lacks the required privilege.
	ErrForbidden = errors.New("method requires administrator privileges")

	// ErrMalformed is returned for unparseable input.
	ErrMalformed = errors.New("malformed request")
)
