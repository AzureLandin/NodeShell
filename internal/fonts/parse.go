package fonts

import (
	"encoding/json"
	"regexp"
	"strings"
)

// registrySuffixRe matches the metadata suffixes Windows appends to font
// registry display names (e.g. "Arial (TrueType)").
var registrySuffixRe = regexp.MustCompile(`(?i)\s*\((?:TrueType|OpenType|All res|VGA res)\)$`)

// isFontFilePath reports whether s looks like an absolute filesystem path; a
// stray path (e.g. a .Source echo) must never surface as a family name.
func isFontFilePath(s string) bool {
	return strings.HasPrefix(s, `\\`) ||
		strings.HasPrefix(s, "/") ||
		(len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/'))
}

// parsePresentationCore parses the one-family-per-line output of the Windows
// PresentationCore PowerShell enumeration (FamilyNames values, never Source
// file paths). CRLF is trimmed, blank lines dropped.
func parsePresentationCore(out string) ([]string, error) {
	lines := strings.Split(out, "\n")
	fams := make([]string, 0, len(lines))
	for _, ln := range lines {
		f := strings.TrimSpace(ln)
		if f == "" || isFontFilePath(f) {
			continue
		}
		fams = append(fams, f)
	}
	return fams, nil
}

// parseRegistry parses the display-name-per-line output of the Windows font
// registry enumeration, stripping TrueType/OpenType metadata suffixes.
func parseRegistry(out string) ([]string, error) {
	lines := strings.Split(out, "\n")
	fams := make([]string, 0, len(lines))
	for _, ln := range lines {
		f := strings.TrimSpace(ln)
		if f == "" || isFontFilePath(f) {
			continue
		}
		f = registrySuffixRe.ReplaceAllString(f, "")
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		fams = append(fams, f)
	}
	return fams, nil
}

// darwinFontDoc mirrors the real top-level shape of `system_profiler
// SPFontsDataType -json`: the items live under an "SPFontsDataType" object
// key, never as a bare array. A bare array (or any other top-level shape)
// fails json.Unmarshal and is reported as malformed, matching production
// output on every released macOS.
type darwinFontDoc struct {
	Fonts []darwinFontEntry `json:"SPFontsDataType"`
}

// darwinFontEntry is one font entry. The classic item-level shape (macOS
// 10.x–12.x) carries a capitalized "Family" key; newer entries (macOS 10.15+)
// may instead expose a lowercase "family" at the same level or nest faces in
// a "typefaces" array. "_name" is the family name in the classic shape but a
// file name in the nested shape, so it must never be used as a family.
type darwinFontEntry struct {
	Family    string           `json:"Family"`
	FamilyAlt string           `json:"family"`
	Typefaces []darwinTypeface `json:"typefaces"`
}

// darwinTypeface is one face inside a nested "typefaces" array; only its
// "family" is a family name — "style" and "_name" (a PostScript name) must
// not be promoted to families.
type darwinTypeface struct {
	Family string `json:"family"`
}

// parseDarwin parses the JSON output of `system_profiler SPFontsDataType
// -json`, which is `{"SPFontsDataType": [...]}`. Family names are taken from
// the stable "Family"/"family" keys (item-level first, then nested
// typefaces); file names, paths, PostScript names and styles never become
// families. JSON keys are locale-independent, so localized system_profiler
// output cannot break the parse. Malformed JSON errors; an empty font list is
// legal and yields no error. Duplicates are preserved (dedupe happens in
// normalize).
func parseDarwin(out string) ([]string, error) {
	var doc darwinFontDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return nil, err
	}
	fams := make([]string, 0, len(doc.Fonts))
	for _, entry := range doc.Fonts {
		if f := strings.TrimSpace(entry.Family); f != "" {
			fams = append(fams, f)
			continue
		}
		if f := strings.TrimSpace(entry.FamilyAlt); f != "" {
			fams = append(fams, f)
			continue
		}
		for _, tf := range entry.Typefaces {
			if f := strings.TrimSpace(tf.Family); f != "" {
				fams = append(fams, f)
			}
		}
	}
	return fams, nil
}

// parseLinux parses the one-family-per-line output of `fc-list -f
// '%{family[0]}\n'`.
func parseLinux(out string) ([]string, error) {
	lines := strings.Split(out, "\n")
	fams := make([]string, 0, len(lines))
	for _, ln := range lines {
		f := strings.TrimSpace(ln)
		if f == "" {
			continue
		}
		fams = append(fams, f)
	}
	return fams, nil
}
