// Package localpathguard resolves local paths and requires them to stay
// inside the user home directory. Every app entry that touches the local
// filesystem on the user's behalf — private keys, SFTP upload sources,
// download targets — goes through this guard, so the boundary check is
// implemented once and symlink escapes (including a symlinked home
// directory) cannot defeat it. Windows paths compare case-insensitively and
// volume-qualified: a path on another drive is never "inside home".
package localpathguard

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"nodeshell/internal/apperror"
)

// Sentinel failures; callers map them onto their own wording so no message
// here leaks a raw path.
var (
	// ErrPathRequired is returned for empty paths.
	ErrPathRequired = errors.New("local path is required")
	// ErrHomeUnavailable is returned when home is empty, relative or
	// unresolvable — the boundary is never left open.
	ErrHomeUnavailable = errors.New("user home directory is unavailable")
	// ErrOutsideHome is returned when the resolved path escapes home.
	ErrOutsideHome = errors.New("local path must stay inside the user home directory")
	// ErrNotReadable is returned when a path (or a download parent) cannot
	// be resolved on disk.
	ErrNotReadable = errors.New("local path is not readable")
)

// Error carries the stable UNKNOWN code for guard failures across IPC.
type Error struct {
	Code    string
	Message string
	cause   error
}

func (e *Error) Error() string { return e.Message }

func (e *Error) ErrorCode() string { return e.Code }

func (e *Error) Unwrap() error { return e.cause }

func guardErr(sentinel error) error {
	return &Error{Code: apperror.Unknown, Message: sentinel.Error(), cause: sentinel}
}

// withinHome reports whether path equals home or lives under it. Windows
// paths are compared case-insensitively (filepath.Clean normalises
// separators but not case); the volume is part of the prefix, so a path on a
// different drive is never considered inside home. Elsewhere comparison is
// case-sensitive, matching the filesystem semantics: a sibling like ".../USER"
// is a distinct directory from ".../user" and never inside it.
func withinHome(path, home string) bool {
	if home == "" {
		return false
	}
	cleanHome := filepath.Clean(home)
	cleanPath := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		cleanHome = strings.ToLower(cleanHome)
		cleanPath = strings.ToLower(cleanPath)
	}
	prefix := strings.TrimSuffix(cleanHome, string(os.PathSeparator)) + string(os.PathSeparator)
	return cleanPath == cleanHome || strings.HasPrefix(cleanPath, prefix)
}

// resolveHome validates and symlink-resolves the home directory. An empty or
// relative home rejects every path (never an open boundary).
func resolveHome(home string) (string, error) {
	if home == "" || !filepath.IsAbs(home) {
		return "", guardErr(ErrHomeUnavailable)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(home))
	if err != nil {
		return "", guardErr(ErrHomeUnavailable)
	}
	return resolved, nil
}

// ResolveExisting resolves path — symlinks included — and requires the
// result to exist and stay inside the symlink-resolved home. It returns the
// resolved absolute path. Use it for paths that must already exist (private
// keys, upload sources).
func ResolveExisting(path, home string) (string, error) {
	if path == "" {
		return "", guardErr(ErrPathRequired)
	}
	homeResolved, err := resolveHome(home)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", guardErr(ErrNotReadable)
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", guardErr(ErrNotReadable)
	}
	if !withinHome(resolved, homeResolved) {
		return "", guardErr(ErrOutsideHome)
	}
	return resolved, nil
}

// ResolveTarget resolves a path that may not exist yet (a download target):
// the parent directory must exist and resolve inside home, and the final
// leaf is preserved verbatim. When the target already exists — including as
// a symlink — it is resolved fully and must stay inside home, so a download
// can never write through a symlink that escapes the boundary.
func ResolveTarget(path, home string) (string, error) {
	if path == "" {
		return "", guardErr(ErrPathRequired)
	}
	homeResolved, err := resolveHome(home)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", guardErr(ErrNotReadable)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		if !withinHome(resolved, homeResolved) {
			return "", guardErr(ErrOutsideHome)
		}
		return resolved, nil
	}
	parent := filepath.Dir(abs)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", guardErr(ErrNotReadable)
	}
	if !withinHome(resolvedParent, homeResolved) {
		return "", guardErr(ErrOutsideHome)
	}
	info, err := os.Stat(resolvedParent)
	if err != nil {
		return "", guardErr(ErrNotReadable)
	}
	if !info.IsDir() {
		return "", guardErr(ErrNotReadable)
	}
	return abs, nil
}
