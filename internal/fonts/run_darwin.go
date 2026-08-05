//go:build darwin

package fonts

import "context"

// platformList enumerates macOS fonts via `system_profiler SPFontsDataType
// -json`, whose real output is the object `{"SPFontsDataType": [...]}` with
// family names at item level or nested in "typefaces". JSON keys are
// locale-independent, unlike the plain-text output, so localized systems
// parse identically; no grep/awk shell pipeline is used.
func platformList(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	out, err := runCommand(ctx, MaxOutputBytes, "system_profiler", "-json", "SPFontsDataType")
	if err != nil {
		return nil, err
	}
	return parseDarwin(string(out))
}
