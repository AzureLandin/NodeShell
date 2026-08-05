//go:build windows

package fonts

import (
	"context"
)

// commandRunner runs one enumeration command. The package seam lets tests
// record and script invocations without spawning PowerShell; tests swapping
// it must restore it and must not run in parallel.
var commandRunner = runCommand

// psPresentationCoreScript enumerates family names (FamilyNames, preferring
// zh-cn like the Electron build), never font file paths, with UTF-8 output so
// Chinese family names survive the console round-trip.
const psPresentationCoreScript = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName PresentationCore
$families = [System.Windows.Media.Fonts]::SystemFontFamilies
foreach ($family in $families) {
  $name = ''
  if (-not $family.FamilyNames.TryGetValue([System.Windows.Markup.XmlLanguage]::GetLanguage('zh-cn'), [ref]$name)) {
    $name = $family.FamilyNames[[System.Windows.Markup.XmlLanguage]::GetLanguage('en-us')]
  }
  [Console]::WriteLine($name)
}`

// psRegistryScript enumerates the display names under the system font
// registry key (fallback when PresentationCore is unavailable); the values'
// file paths never reach the output, only the value names.
const psRegistryScript = `[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$fonts = Get-ItemProperty -Path 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Fonts'
foreach ($prop in $fonts.PSObject.Properties) {
  if ($prop.Name -notlike 'PS*') {
    [Console]::WriteLine($prop.Name)
  }
}`

// platformList enumerates Windows fonts: PresentationCore family names via
// PowerShell (Electron parity), falling back to the font registry when the
// first attempt errors or yields nothing. Both commands share one timeout
// budget; an already-expired budget makes the registry attempt fail naturally
// with the context error.
func platformList(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()
	out, err := runEnumScript(ctx, psPresentationCoreScript)
	if err == nil {
		fams, perr := parsePresentationCore(string(out))
		if perr == nil && len(fams) > 0 {
			return fams, nil
		}
	}
	out, err = runEnumScript(ctx, psRegistryScript)
	if err != nil {
		return nil, err
	}
	return parseRegistry(string(out))
}

// runEnumScript runs one PowerShell enumeration script through the runner
// seam with the fixed invocation arguments.
func runEnumScript(ctx context.Context, script string) ([]byte, error) {
	return commandRunner(ctx, MaxOutputBytes, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
}
