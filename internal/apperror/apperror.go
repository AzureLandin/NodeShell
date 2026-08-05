// Package apperror defines the machine-readable error codes that cross the
// Wails IPC boundary and the formatter that encodes them, so the frontend can
// map a rejected call back onto AppError.code.
package apperror

import "errors"

// Codes are the stable identifiers the frontend recognises (SSH_CODES in
// src/shared/ipc-error.ts is the authoritative list). Domain errors carry one
// of these; anything else collapses to Unknown before crossing IPC.
const (
	ConfigReadFailed  = "CONFIG_READ_FAILED"
	ConfigWriteFailed = "CONFIG_WRITE_FAILED"

	// Reserved for the SSH task (T-series); already defined frontend-side.
	ConnectionRefused = "CONNECTION_REFUSED"
	Timeout           = "TIMEOUT"
	AuthFailed        = "AUTH_FAILED"
	HostUnreachable   = "HOST_UNREACHABLE"
	HostKeyChanged    = "HOST_KEY_CHANGED"
	HostKeyUnknown    = "HOST_KEY_UNKNOWN"
	SessionNotFound   = "SESSION_NOT_FOUND"
	McpSessionLimit   = "MCP_SESSION_LIMIT"
	Cancelled         = "CANCELLED"

	// HostNotFound is the DNS-resolution failure code (HOST_NOT_FOUND).
	HostNotFound = "HOST_NOT_FOUND"

	Unknown = "UNKNOWN"
)

// Coded is implemented by error types that carry a stable machine-readable
// code across the IPC boundary (hosts.Error, settings.Error, future SSH
// errors). ErrorCode is a method, not the Code field, so a field of the same
// name does not collide.
type Coded interface {
	ErrorCode() string
}

// Format encodes err for the Wails frontend as
// "NODESHELL_ERR:<CODE>:<message>", which parseIpcThrownError in
// src/shared/ipc-error.ts splits back into code and message. The message part
// is passed through verbatim (colons included); the code is taken from a
// whitelist so arbitrary text can never be injected as a code.
func Format(err error) any {
	if err == nil {
		return "NODESHELL_ERR:" + Unknown + ":Unknown error"
	}
	var coded Coded
	code := Unknown
	if errors.As(err, &coded) && knownCode(coded.ErrorCode()) {
		code = coded.ErrorCode()
	}
	return "NODESHELL_ERR:" + code + ":" + err.Error()
}

// knownCode reports whether code is in the frontend-recognised whitelist.
func knownCode(code string) bool {
	switch code {
	case ConfigReadFailed, ConfigWriteFailed,
		ConnectionRefused, Timeout, AuthFailed, HostUnreachable,
		HostKeyChanged, HostKeyUnknown, HostNotFound, SessionNotFound, McpSessionLimit,
		Cancelled, Unknown:
		return true
	}
	return false
}
