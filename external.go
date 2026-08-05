package main

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// errInvalidExternalURL is returned for URLs that fail the external-link
// policy; the message never echoes the raw URL.
var errInvalidExternalURL = errors.New("nodeshell: invalid external URL")

// openBrowser is the seam for AppOpenExternal: production opens rawURL in the
// system browser through the Wails runtime; tests inject a fake.
// runtime.BrowserOpenURL fatal-exits when its context does not carry the
// Wails frontend, so the value check below mirrors the one runtime itself
// performs — a nil or plain (non-Wails) context is an observable error, never
// a process fatal.
var openBrowser = func(ctx context.Context, rawURL string) error {
	if ctx == nil || ctx.Value("frontend") == nil {
		return errBackendNotInitialised
	}
	runtime.BrowserOpenURL(ctx, rawURL)
	return nil
}

// urlShellMetacharacters is the character class Wails v2.13's
// ValidateAndSanitizeURL rejects on the parsed URL string
// (internal/frontend/utils/urlValidator.go). Scanning the raw URL keeps the
// verdict at least as strict as the runtime's: any URL accepted here is one
// the Wails browser layer will actually open, never one it silently drops.
const urlShellMetacharacters = ";|`$\\<>*{}[]()~! \t\n\r"

// isDangerousUnicode reports whether r falls in the Unicode whitespace /
// format ranges the Wails validator rejects (\u00A0\u1680\u2000-\u200F
// \u2028-\u202F\u205F\u2060\u3000\uFEFF); the C0/C1 control ranges are
// handled by validateExternalURL's control-character check.
func isDangerousUnicode(r rune) bool {
	return r == '\u00A0' || r == '\u1680' ||
		(r >= '\u2000' && r <= '\u200F') ||
		(r >= '\u2028' && r <= '\u202F') ||
		r == '\u205F' || r == '\u2060' || r == '\u3000' || r == '\uFEFF'
}

// validateExternalURL enforces the external-link policy: only http/https URLs
// with a non-empty host, no control characters, and none of the characters
// Wails v2.13 rejects (shell metacharacters and Unicode whitespace). The value
// is parsed structurally (never interpolated into a shell command).
func validateExternalURL(rawURL string) error {
	if strings.IndexFunc(rawURL, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return errInvalidExternalURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return errInvalidExternalURL
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" {
		return errInvalidExternalURL
	}
	if strings.ContainsAny(rawURL, urlShellMetacharacters) || strings.IndexFunc(rawURL, isDangerousUnicode) >= 0 {
		return errInvalidExternalURL
	}
	return nil
}

// AppOpenExternal opens rawURL in the system default browser after strict
// validation. The URL is never used as a shell argument — only http/https
// with a non-empty host, no control characters, and none of the characters
// the Wails runtime validator rejects (shell metacharacters, Unicode
// whitespace) are allowed. The runtime call runs through the openBrowser
// seam, so a missing or plain runtime context is an observable error
// (errBackendNotInitialised), never a process fatal.
func (a *App) AppOpenExternal(rawURL string) error {
	if err := validateExternalURL(rawURL); err != nil {
		return err
	}
	a.mu.RLock()
	ctx := a.ctx
	a.mu.RUnlock()
	return openBrowser(ctx, rawURL)
}
