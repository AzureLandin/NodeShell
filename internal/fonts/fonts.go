// Package fonts enumerates the system-installed font families for the
// Settings modal (window.api.fonts.list parity with the Electron build).
package fonts

import (
	"context"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds one enumeration command; a cold PowerShell JIT or
	// a slow system_profiler must never hang the Settings modal.
	DefaultTimeout = 8 * time.Second

	// MaxOutputBytes caps the collected stdout of one enumeration command.
	// Family lists stay small even with CJK fonts; 4 MiB is generous headroom.
	MaxOutputBytes = 4 * 1024 * 1024
)

// List returns the system font families, trimmed, deduplicated and sorted
// case-insensitively. Any enumeration failure (missing tool, timeout,
// oversized output) returns nil with the error so the App can surface an
// empty list without rejecting the binding (Electron parity).
func List(ctx context.Context) ([]string, error) {
	raw, err := platformList(ctx)
	if err != nil {
		return nil, err
	}
	return normalize(raw), nil
}

// normalize trims, drops empties, strips surrounding quotes, deduplicates
// exactly and sorts case-insensitively (localeCompare-ish; the original value
// breaks ties) without mutating the input slice.
func normalize(families []string) []string {
	seen := make(map[string]struct{}, len(families))
	out := make([]string, 0, len(families))
	for _, f := range families {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if len(f) >= 2 && (f[0] == '"' && f[len(f)-1] == '"' || f[0] == '\'' && f[len(f)-1] == '\'') {
			f = f[1 : len(f)-1]
		}
		if f == "" {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i]), strings.ToLower(out[j])
		if li != lj {
			return li < lj
		}
		return out[i] < out[j]
	})
	return out
}
