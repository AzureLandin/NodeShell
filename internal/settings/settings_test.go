package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const fullFixture = `{
  "language": "en",
  "terminalFontFamily": "Cascadia Code",
  "terminalFontSize": 16,
  "mcpIdleTimeoutMinutes": 30,
  "mcpMaxSessions": 4,
  "themePreference": "light"
}`

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(t.TempDir())
}

func TestDefaultsWhenFileMissing(t *testing.T) {
	got, err := newStore(t).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != Defaults {
		t.Fatalf("got %+v, want defaults %+v", got, Defaults)
	}
}

func TestGetFromFullFixture(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(fullFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := AppSettings{Language: "en", TerminalFontFamily: "Cascadia Code", TerminalFontSize: 16,
		McpIdleTimeoutMinutes: 30, McpMaxSessions: 4, ThemePreference: "light"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPartialFixtureFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"language": "en"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Language != "en" || got.TerminalFontFamily != "Hack" || got.TerminalFontSize != 14 ||
		got.McpIdleTimeoutMinutes != 10 || got.McpMaxSessions != 8 || got.ThemePreference != "system" {
		t.Fatalf("partial merge mismatch: %+v", got)
	}
}

func TestInvalidLanguageFallsBackToZh(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"language": "fr"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).Get()
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "zh" {
		t.Fatalf("language = %q, want zh", got.Language)
	}
}

func TestInvalidThemeFallsBackToSystem(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"themePreference": "neon"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).Get()
	if err != nil {
		t.Fatal(err)
	}
	if got.ThemePreference != "system" {
		t.Fatalf("theme = %q, want system", got.ThemePreference)
	}
}

func TestClampsValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"language": "zh", "terminalFontFamily": "  ", "terminalFontSize": 8, "mcpIdleTimeoutMinutes": 0, "mcpMaxSessions": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(dir)
	got, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	want := AppSettings{Language: "zh", TerminalFontFamily: "Hack", TerminalFontSize: 10,
		McpIdleTimeoutMinutes: 1, McpMaxSessions: 32, ThemePreference: "system"}
	if got != want {
		t.Fatalf("clamp mismatch: got %+v want %+v", got, want)
	}
	after, err := store.Set(Patch{TerminalFontSize: f64p(99), McpIdleTimeoutMinutes: f64p(200)})
	if err != nil {
		t.Fatal(err)
	}
	if after.TerminalFontSize != 24 || after.McpIdleTimeoutMinutes != 120 {
		t.Fatalf("set clamp mismatch: %+v", after)
	}
}

func TestSetMergesAndPersists(t *testing.T) {
	store := newStore(t)
	got, err := store.Set(Patch{Language: strp("en"), TerminalFontSize: f64p(16)})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got.Language != "en" || got.TerminalFontSize != 16 || got.ThemePreference != "system" {
		t.Fatalf("set result mismatch: %+v", got)
	}
	raw, err := os.ReadFile(filepath.Join(store.dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("persisted JSON invalid: %v (%q)", err, raw)
	}
	if file["language"] != "en" || file["terminalFontFamily"] != "Hack" || file["mcpMaxSessions"] != float64(8) {
		t.Fatalf("persisted mismatch: %v", file)
	}
}

func TestCorruptFileReadErrorWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(dir).Get()
	var e *Error
	if !isError(err, &e) || e.Code != "CONFIG_READ_FAILED" {
		t.Fatalf("error = %v, want CONFIG_READ_FAILED", err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "{not-json" {
		t.Fatalf("corrupt file was overwritten: %q", raw)
	}
}

func TestUnknownFieldsIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"language": "en", "futureSetting": {"a": 1}, "mcpIdleTimeoutMinutes": 12}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).Get()
	if err != nil {
		t.Fatalf("Get with unknown fields: %v", err)
	}
	if got.Language != "en" || got.McpIdleTimeoutMinutes != 12 {
		t.Fatalf("mismatch: %+v", got)
	}
}

func TestNullShapeIsCorruptArrayYieldsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir).Get(); err == nil {
		t.Fatal("null file must be CONFIG_READ_FAILED (TS: !parsed)")
	}
	if err := os.WriteFile(path, []byte(`[{"language": "en"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).Get()
	if err != nil {
		t.Fatalf("array file: %v", err)
	}
	if got != Defaults {
		t.Fatalf("array file should normalize to defaults (TS typeof check), got %+v", got)
	}
}

func TestNumericStringsCoercedLikeNumber(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"terminalFontSize": "16", "mcpMaxSessions": "abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.TerminalFontSize != 16 {
		t.Fatalf("fontSize = %d, want 16 (Number(\"16\"))", got.TerminalFontSize)
	}
	if got.McpMaxSessions != 8 {
		t.Fatalf("mcpMaxSessions = %d, want 8 (Number(\"abc\") = NaN)", got.McpMaxSessions)
	}
}

func TestWrongTypedStringFieldsNormalizeLikeTS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"language": 5, "terminalFontFamily": 123, "themePreference": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).Get()
	if err != nil {
		t.Fatalf("Get with wrong-typed string fields: %v", err)
	}
	want := AppSettings{Language: "zh", TerminalFontFamily: "Hack", TerminalFontSize: 14,
		McpIdleTimeoutMinutes: 10, McpMaxSessions: 8, ThemePreference: "system"}
	if got != want {
		t.Fatalf("got %+v, want %+v (TS normalize* falls back to defaults)", got, want)
	}
}

// Set is a file read-modify-write; concurrent Set/Get must not race (verified
// with -race) and must never surface a spurious CONFIG_WRITE_FAILED from
// colliding renames, and the final file must be a complete valid document.
func TestConcurrentSetGetNoRaceOrWriteFailures(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	const writers, readers, iters = 4, 4, 20
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				lang := "zh"
				if (n+j)%2 == 0 {
					lang = "en"
				}
				if _, err := store.Set(Patch{Language: &lang}); err != nil {
					t.Errorf("Set: %v", err)
					return
				}
			}
		}(i)
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if _, err := store.Get(); err != nil {
					t.Errorf("Get: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("final settings file corrupt after concurrent Set/Get: %v (%q)", err, raw)
	}
	if file["language"] != "zh" && file["language"] != "en" {
		t.Fatalf("final language = %v, want zh or en", file["language"])
	}
}

func TestNumericCoercionMatchesJSNumber(t *testing.T) {
	cases := []struct {
		name string
		raw  string // JSON value written into terminalFontSize
		want int
	}{
		// JS Number() semantics on the persisted value, then round + clamp.
		{"empty string", `""`, 10},            // Number("") = 0 -> clamp min
		{"whitespace string", `"  "`, 10},     // Number("  ") = 0 -> clamp min
		{"true", `true`, 10},                  // Number(true) = 1 -> clamp min
		{"false", `false`, 10},                // Number(false) = 0 -> clamp min
		{"empty array", `[]`, 10},             // Number([]) = 0 -> clamp min
		{"single element array", `[5]`, 10},   // Number([5]) = 5 -> clamp min
		{"hex string", `"0x10"`, 16},          // Number("0x10") = 16
		{"huge number", `1e300`, 24},          // clamp max, must not overflow int
		{"object", `{}`, 14},                  // Number({}) = NaN -> default
		{"multi element array", `[1, 2]`, 14}, // Number([1,2]) = NaN -> default
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			raw := `{"terminalFontSize": ` + tc.raw + `}`
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := New(dir).Get()
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.TerminalFontSize != tc.want {
				t.Fatalf("terminalFontSize = %d, want %d (fixture %s)", got.TerminalFontSize, tc.want, tc.raw)
			}
		})
	}
}

func strp(s string) *string   { return &s }
func f64p(f float64) *float64 { return &f }
func isError(err error, out **Error) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*Error)
	if ok {
		*out = e
	}
	return ok
}
