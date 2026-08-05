package apperror_test

import (
	"errors"
	"fmt"
	"testing"

	"nodeshell/internal/apperror"
	"nodeshell/internal/hosts"
	"nodeshell/internal/settings"
)

// fakeCoded emulates a future error type (e.g. SSH) that carries a stable
// code, exercising the whitelist path without a concrete domain error.
type fakeCoded struct {
	code    string
	message string
}

func (f fakeCoded) Error() string     { return f.message }
func (f fakeCoded) ErrorCode() string { return f.code }

func TestFormatHostsError(t *testing.T) {
	err := &hosts.Error{Code: apperror.ConfigWriteFailed, Message: "disk full"}
	if got, want := apperror.Format(err), "NODESHELL_ERR:CONFIG_WRITE_FAILED:disk full"; got != want {
		t.Fatalf("Format(%v) = %q, want %q", err, got, want)
	}
}

func TestFormatSettingsError(t *testing.T) {
	err := &settings.Error{Code: apperror.ConfigReadFailed, Message: "settings file is corrupt"}
	if got, want := apperror.Format(err), "NODESHELL_ERR:CONFIG_READ_FAILED:settings file is corrupt"; got != want {
		t.Fatalf("Format(%v) = %q, want %q", err, got, want)
	}
}

func TestFormatPlainError(t *testing.T) {
	if got, want := apperror.Format(errors.New("boom")), "NODESHELL_ERR:UNKNOWN:boom"; got != want {
		t.Fatalf("Format = %q, want %q", got, want)
	}
}

// TestFormatNil mirrors the API contract: the Wails dispatcher never calls
// the formatter with nil, but it must stay safe if it ever does.
func TestFormatNil(t *testing.T) {
	if got, want := apperror.Format(nil), "NODESHELL_ERR:UNKNOWN:Unknown error"; got != want {
		t.Fatalf("Format(nil) = %q, want %q", got, want)
	}
}

// TestFormatMessageWithColon ensures the message part survives untouched even
// when it contains colons: the frontend parser splits on the first colon only.
func TestFormatMessageWithColon(t *testing.T) {
	err := &hosts.Error{Code: apperror.ConfigWriteFailed, Message: "disk: full: now"}
	if got, want := apperror.Format(err), "NODESHELL_ERR:CONFIG_WRITE_FAILED:disk: full: now"; got != want {
		t.Fatalf("Format(%v) = %q, want %q", err, got, want)
	}
}

// TestFormatRejectsUnknownCode guarantees arbitrary text can never be
// smuggled in as a code: anything outside the whitelist collapses to UNKNOWN.
func TestFormatRejectsUnknownCode(t *testing.T) {
	err := fakeCoded{code: "EVIL:INJECTED", message: "boom"}
	if got, want := apperror.Format(err), "NODESHELL_ERR:UNKNOWN:boom"; got != want {
		t.Fatalf("Format(%v) = %q, want %q", err, got, want)
	}
}

// TestFormatAllowsReservedCode keeps the whitelist future-proof: codes already
// defined for later tasks (SSH) pass through untouched once emitted.
func TestFormatAllowsReservedCode(t *testing.T) {
	err := fakeCoded{code: apperror.AuthFailed, message: "bad credentials"}
	if got, want := apperror.Format(err), "NODESHELL_ERR:AUTH_FAILED:bad credentials"; got != want {
		t.Fatalf("Format(%v) = %q, want %q", err, got, want)
	}
}

// TestFormatUnwrapsCodedError ensures wrapping with %w keeps the code: the
// formatter must look through fmt.Errorf chains via errors.As.
func TestFormatUnwrapsCodedError(t *testing.T) {
	inner := &hosts.Error{Code: apperror.ConfigReadFailed, Message: "corrupt"}
	err := fmt.Errorf("load hosts: %w", inner)
	if got, want := apperror.Format(err), "NODESHELL_ERR:CONFIG_READ_FAILED:load hosts: corrupt"; got != want {
		t.Fatalf("Format(%v) = %q, want %q", err, got, want)
	}
}
