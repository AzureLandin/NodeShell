package credentials

import (
	"errors"
	"os"

	"nodeshell/internal/apperror"
	"nodeshell/internal/localpathguard"
)

// ErrPathOutsideHome is returned when a private key path does not resolve
// inside the user home directory (or home could not be established). The
// message never includes the raw path so nothing about the user's layout is
// leaked into the error channel.
var ErrPathOutsideHome = errors.New("private key path must stay inside the user home directory")

// NewHomeReader returns a PrivateKeyReader that only reads files whose
// symlink-resolved real path stays inside homeDir. homeDir itself is
// resolved through symlinks so a symlinked home (e.g. /var vs /private/var)
// does not defeat the boundary check. An empty or relative homeDir rejects
// every path. The boundary logic lives in localpathguard so uploads,
// downloads and key reads share one implementation.
func NewHomeReader(homeDir string) PrivateKeyReader {
	return func(path string) (string, error) {
		return ReadPrivateKeyFile(path, homeDir)
	}
}

// ReadPrivateKeyFile resolves path to an absolute, symlink-resolved file and
// requires it to remain inside homeDir. Relative paths resolve against the
// process working directory (like Electron's path.resolve), never against
// home; the boundary check rejects any resolution that escapes home. It
// reads the resolved path, so a symlink swapped after resolution would have
// to be re-introduced on the resolved path itself. Returns the file content.
func ReadPrivateKeyFile(path, homeDir string) (string, error) {
	resolved, err := localpathguard.ResolveExisting(path, homeDir)
	if err != nil {
		if errors.Is(err, localpathguard.ErrOutsideHome) {
			return "", &Error{Code: apperror.Unknown, Message: ErrPathOutsideHome.Error()}
		}
		return "", &Error{Code: apperror.Unknown, Message: "Private key file is not readable"}
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", &Error{Code: apperror.Unknown, Message: "Private key file is not readable"}
	}
	return string(data), nil
}
