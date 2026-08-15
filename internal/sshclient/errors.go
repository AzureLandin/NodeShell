package sshclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"

	"nodeshell/internal/apperror"
)

// Error carries the stable machine-readable code the frontend maps onto
// AppError.code, plus fingerprint context for host-key failures. Messages
// never embed passwords or private-key content.
type Error struct {
	Code        string
	Message     string
	Fingerprint string // set for host-key errors
	Previous    string // set for HOST_KEY_CHANGED
}

func (e *Error) Error() string { return e.Message }

// ErrorCode lets apperror.Format carry the stable code across IPC.
func (e *Error) ErrorCode() string { return e.Code }

// Fingerprint returns the SHA256 base64 fingerprint of the raw SSH wire
// encoding of key — byte-compatible with the Electron build's known_hosts.json
// values (the old TS hashed key.getPublicSSH()).
func Fingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return base64.StdEncoding.EncodeToString(sum[:])
}

// Windows WSA error codes. The syscall package maps ECONNREFUSED etc. to
// invented values that never equal a real errno on Windows, so the raw
// values are compared directly; on other platforms these numbers are unused.
const (
	wsaConnRefused = 10061 // WSAECONNREFUSED
	wsaHostUnreach = 10065 // WSAEHOSTUNREACH
	wsaNetUnreach  = 10051 // WSAENETUNREACH
	wsaConnReset   = 10054 // WSAECONNRESET
)

// errnoIs reports whether err unwraps to one of the given syscall errnos.
// On Unix these are the real ECONNREFUSED/EHOSTUNREACH/ENETUNREACH values;
// on Windows the syscall package invents fake constants that never match a
// real errno, so the raw WSA codes are listed explicitly.
func errnoIs(err error, codes ...syscall.Errno) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		for _, c := range codes {
			if errno == c {
				return true
			}
		}
	}
	return false
}

// mapDialError classifies a failed TCP dial.
func mapDialError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return &Error{Code: apperror.Cancelled, Message: "Connection cancelled"}
		}
		return &Error{Code: apperror.Timeout, Message: "Connection timed out"}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return &Error{Code: apperror.HostNotFound, Message: fmt.Sprintf("Host not found: %s", dnsErr.Name)}
	}
	if errnoIs(err, syscall.ECONNREFUSED, wsaConnRefused) {
		return &Error{Code: apperror.ConnectionRefused, Message: "Connection refused"}
	}
	if errnoIs(err, syscall.EHOSTUNREACH, syscall.ENETUNREACH, wsaHostUnreach, wsaNetUnreach) {
		return &Error{Code: apperror.HostUnreachable, Message: "Host unreachable"}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{Code: apperror.Timeout, Message: "Connection timed out"}
	}
	return &Error{Code: apperror.Unknown, Message: "Connection failed"}
}

// mapForwardError classifies a failed direct-tcpip open. Servers that disable
// AllowTcpForwarding typically reject with "administratively prohibited".
func mapForwardError(err error) error {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "administratively prohibited") ||
		strings.Contains(msg, "tcpip-forward") ||
		strings.Contains(msg, "port forwarding") {
		return &Error{Code: apperror.PermissionDenied, Message: "The server does not allow TCP forwarding"}
	}
	return &Error{Code: apperror.Unknown, Message: "Failed to open the remote connection"}
}

// mapCheckError classifies a host-key store error from the HostKeys.Check
// seam. A coded config error (CONFIG_READ_FAILED / CONFIG_WRITE_FAILED) keeps
// its stable code so the frontend can surface a broken known-hosts store;
// anything else collapses to UNKNOWN. The message stays generic — a raw
// err.Error() may embed a filesystem path. Only the config codes are allowed
// through, so no foreign code can inject itself into the IPC whitelist.
func mapCheckError(err error) error {
	code := apperror.Unknown
	var coded apperror.Coded
	if errors.As(err, &coded) {
		switch coded.ErrorCode() {
		case apperror.ConfigReadFailed, apperror.ConfigWriteFailed, apperror.Unknown:
			code = coded.ErrorCode()
		}
	}
	return &Error{Code: code, Message: "Failed to verify host key"}
}

// mapHandshakeError classifies a failed SSH handshake (key exchange,
// host-key verification, authentication).
func mapHandshakeError(ctx context.Context, err error) error {
	// The host-key callback error is returned through NewClientConn
	// unwrapped (wrapped only by "ssh: handshake failed: %w"), so errors.As
	// recovers the typed fingerprint error.
	var hkErr *Error
	if errors.As(err, &hkErr) {
		return hkErr
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return &Error{Code: apperror.Cancelled, Message: "Connection cancelled"}
		}
		return &Error{Code: apperror.Timeout, Message: "Connection timed out"}
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain") ||
		strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "permission denied") {
		return &Error{Code: apperror.AuthFailed, Message: "Authentication failed"}
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		// A session channel ended cleanly — not a connection error.
		return nil
	}
	if errors.Is(err, io.EOF) {
		return &Error{Code: apperror.Unknown, Message: "Connection closed by the server"}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{Code: apperror.Timeout, Message: "Connection timed out"}
	}
	return &Error{Code: apperror.Unknown, Message: "Connection failed"}
}
