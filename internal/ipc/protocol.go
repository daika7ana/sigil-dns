// Package ipc implements the versioned request/response protocol spoken over
// the sigildns named pipe (SPEC §2, §4.7, §15).
//
// The transport is abstracted so the protocol is testable off Windows: Windows
// uses a named pipe with a locking SDDL; other platforms use a Unix socket.
package ipc

import (
	"encoding/json"
	"errors"

	"github.com/daika7ana/sigil-dns/internal/version"
)

// ProtocolVersion is the current IPC protocol version.  Peers must send it in
// every request; unknown versions are rejected.
const ProtocolVersion = version.ProtocolVersion

// Capability bits reported by the info method.
const (
	// CapInfo marks the info method.
	CapInfo uint64 = 1 << iota

	// CapConfigSummary marks the config_summary method.
	CapConfigSummary

	// CapReload marks the admin reload method.
	CapReload

	// CapRestore marks the admin restore method.
	CapRestore

	// CapRefreshLists marks the admin refresh_lists method.
	CapRefreshLists

	// CapClearCache marks the admin clear_cache method.
	CapClearCache

	// CapPauseFiltering marks the admin pause_filtering method.
	CapPauseFiltering

	// CapResumeFiltering marks the admin resume_filtering method.
	CapResumeFiltering
)

// Capabilities is the set of capability bits this build supports.
const Capabilities = CapInfo | CapConfigSummary | CapReload | CapRestore | CapRefreshLists | CapClearCache | CapPauseFiltering | CapResumeFiltering

// Method is an IPC request method.
type Method string

// Methods.
const (
	// MethodStatus is read-only.
	MethodStatus Method = "status"

	// MethodInfo is read-only: protocol/app version and capabilities.
	MethodInfo Method = "info"

	// MethodConfigSummary is read-only: the sanitized config summary.
	MethodConfigSummary Method = "config_summary"

	// MethodEnable is state-changing and requires administrator privileges.
	MethodEnable Method = "enable"

	// MethodDisable is state-changing and requires administrator privileges.
	MethodDisable Method = "disable"

	// MethodReload is state-changing and requires administrator privileges.
	MethodReload Method = "reload"

	// MethodRestore is state-changing and requires administrator privileges.
	// It is config-independent (crash/dirty recovery).
	MethodRestore Method = "restore"

	// MethodRefreshLists is state-changing and requires administrator
	// privileges.  It re-downloads the active profile's filter lists and
	// rebuilds the filter engine without tearing down interception.
	MethodRefreshLists Method = "refresh_lists"

	// MethodClearCache is state-changing and requires administrator
	// privileges.  It clears the running resolver's cache and the Windows
	// DNS cache, so a newly added rule (e.g. a provider-side bypass) takes
	// effect for names already cached.
	MethodClearCache Method = "clear_cache"

	// MethodPauseFiltering is state-changing and requires administrator
	// privileges.  It skips the filter stage for a fixed interval without
	// touching interception, so a site broken by a filter can be reached
	// while DNS stays encrypted.
	MethodPauseFiltering Method = "pause_filtering"

	// MethodResumeFiltering is state-changing and requires administrator
	// privileges.  It turns the filter stage back on before the pause
	// expires; interception is untouched.
	MethodResumeFiltering Method = "resume_filtering"
)

// Privilege classifies a method's required caller identity (SPEC §15.3.2).
type Privilege int

// Privileges.
const (
	// ReadOnly methods may be called by any caller (status pipe).
	ReadOnly Privilege = iota

	// Admin methods require an administrator/SYSTEM caller (control pipe).
	Admin
)

// methodPrivileges is the single source of truth for method authorization.  It
// is a table so a test can assert every method's classification and that every
// non-read-only method is rejected for a non-admin caller.
var methodPrivileges = map[Method]Privilege{
	MethodStatus:          ReadOnly,
	MethodInfo:            ReadOnly,
	MethodConfigSummary:   ReadOnly,
	MethodEnable:          Admin,
	MethodDisable:         Admin,
	MethodReload:          Admin,
	MethodRestore:         Admin,
	MethodRefreshLists:    Admin,
	MethodClearCache:      Admin,
	MethodPauseFiltering:  Admin,
	MethodResumeFiltering: Admin,
}

// valid reports whether m is a known method.
func (m Method) valid() (ok bool) {
	_, ok = methodPrivileges[m]

	return ok
}

// privilege returns the required privilege for m.  It is only meaningful for a
// valid method.
func (m Method) privilege() (p Privilege) { return methodPrivileges[m] }

// Request is the JSON envelope of a request.
type Request struct {
	// Version is the protocol version; must equal ProtocolVersion.
	Version int `json:"version"`

	// Method is the requested operation.
	Method Method `json:"method"`
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

	// ErrInternal is the generic, secret-free response for a recovered panic.
	// It never carries the panic value or any request payload.
	ErrInternal = errors.New("internal error")

	// ErrNoAuthorizer is returned by Serve when no Authorizer is supplied.
	ErrNoAuthorizer = errors.New("no authorizer supplied")

	// ErrUnsupported is returned when a method is not available on this server.
	ErrUnsupported = errors.New("method not supported by this server")
)
