// Package configdir resolves the OS application-data directory for NodeShell.
// The app keeps user data in a directory named "nodeshell" under the OS
// application-data root, matching the Electron layout
// (app.getPath('appData')/nodeshell) so the Wails build reads the same files
// as the 2.0.0 Electron build.
package configdir

import (
	"os"
	"path/filepath"
)

// userConfigDir is a seam for tests. os.UserConfigDir returns the Electron
// appData root on every target platform: %AppData% on Windows, ~/Library/
// Application Support on macOS, $XDG_CONFIG_HOME or ~/.config on Linux.
var userConfigDir = os.UserConfigDir

// DataDir returns the NodeShell application-data directory.
func DataDir() (string, error) {
	base, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "nodeshell"), nil
}
