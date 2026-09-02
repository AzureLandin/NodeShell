package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const fullFixture = `{
  "language": "en",
  "terminalFontFamily": "Cascadia Code",
  "terminalFontSize": 16,
  "mcpIdleTimeoutMinutes": 30,
  "mcpMaxSessions": 4,
  "themePreference": "light",
  "agentBaseUrl": "https://api.deepseek.com/v1",
  "agentModel": "deepseek-chat"
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
	if !reflect.DeepEqual(got, Defaults) {
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
		McpIdleTimeoutMinutes: 30, McpMaxSessions: 4, ThemePreference: "light",
		AgentBaseURL: "https://api.deepseek.com/v1", AgentModel: "deepseek-chat",
		AgentProviders: []AgentProvider{{
			ID: LegacyProviderID, Name: LegacyProviderName,
			BaseURL: "https://api.deepseek.com/v1", Models: []string{"deepseek-chat"},
		}},
		AgentDefaultProviderID: LegacyProviderID, AgentDefaultModel: "deepseek-chat",
		PermissionPolicy: Defaults.PermissionPolicy, McpPermissionMode: Defaults.McpPermissionMode}
	if !reflect.DeepEqual(got, want) {
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
		got.McpIdleTimeoutMinutes != 10 || got.McpMaxSessions != 8 || got.ThemePreference != "system" ||
		got.PermissionPolicy != "ask" || got.McpPermissionMode != "external" {
		t.Fatalf("partial merge mismatch: %+v", got)
	}
	if len(got.AgentProviders) != 1 || got.AgentProviders[0].ID != LegacyProviderID {
		t.Fatalf("old file without agentProviders must synthesise a legacy provider: %+v", got.AgentProviders)
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
		McpIdleTimeoutMinutes: 1, McpMaxSessions: 32, ThemePreference: "system",
		AgentBaseURL: Defaults.AgentBaseURL, AgentModel: Defaults.AgentModel,
		AgentProviders:         []AgentProvider{synthesiseLegacyProvider(Defaults.AgentBaseURL, Defaults.AgentModel)},
		AgentDefaultProviderID: LegacyProviderID, AgentDefaultModel: Defaults.AgentModel,
		PermissionPolicy: Defaults.PermissionPolicy, McpPermissionMode: Defaults.McpPermissionMode}
	if !reflect.DeepEqual(got, want) {
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
	if !reflect.DeepEqual(got, Defaults) {
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
		McpIdleTimeoutMinutes: 10, McpMaxSessions: 8, ThemePreference: "system",
		AgentBaseURL: Defaults.AgentBaseURL, AgentModel: Defaults.AgentModel,
		AgentProviders:         []AgentProvider{synthesiseLegacyProvider(Defaults.AgentBaseURL, Defaults.AgentModel)},
		AgentDefaultProviderID: LegacyProviderID, AgentDefaultModel: Defaults.AgentModel,
		PermissionPolicy: Defaults.PermissionPolicy, McpPermissionMode: Defaults.McpPermissionMode}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v (TS normalize* falls back to defaults)", got, want)
	}
}

// The agent endpoint is only ever read back as an http(s) URL: a non-HTTP
// scheme in the file must not be able to redirect the assistant, and a
// blank/oversized model or URL falls back to the default.
func TestAgentEndpointNormalisation(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantURL   string
		wantModel string
	}{
		{"trailing slash trimmed", `{"agentBaseUrl": "https://x.test/v1/", "agentModel": " m "}`,
			"https://x.test/v1", "m"},
		{"non-http scheme rejected", `{"agentBaseUrl": "file:///etc/passwd"}`,
			Defaults.AgentBaseURL, Defaults.AgentModel},
		{"blank falls back", `{"agentBaseUrl": "  ", "agentModel": ""}`,
			Defaults.AgentBaseURL, Defaults.AgentModel},
		{"wrong type falls back", `{"agentBaseUrl": 7, "agentModel": true}`,
			Defaults.AgentBaseURL, Defaults.AgentModel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := New(dir).Get()
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.AgentBaseURL != tc.wantURL || got.AgentModel != tc.wantModel {
				t.Fatalf("agent config = (%q, %q), want (%q, %q)",
					got.AgentBaseURL, got.AgentModel, tc.wantURL, tc.wantModel)
			}
		})
	}
}

// Long values are garbage rather than configuration: they must not be
// persisted or sent to an endpoint.
func TestAgentFieldsRejectOversizedValues(t *testing.T) {
	long := "https://x.test/" + strings.Repeat("a", AgentFieldMaxLen)
	got, err := newStore(t).Set(Patch{AgentBaseURL: &long, AgentModel: &long})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got.AgentBaseURL != Defaults.AgentBaseURL || got.AgentModel != Defaults.AgentModel {
		t.Fatalf("oversized agent fields were kept: %+v", got)
	}
}

func TestPermissionPolicyNormalisation(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`{"permissionPolicy": "allow"}`, "allow"},
		{`{"permissionPolicy": "DENY"}`, "deny"},
		{`{"permissionPolicy": "ask"}`, "ask"},
		{`{"permissionPolicy": "always"}`, "ask"},
		{`{"permissionPolicy": 7}`, "ask"},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(tc.raw), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := New(dir).Get()
		if err != nil {
			t.Fatalf("Get(%s): %v", tc.raw, err)
		}
		if got.PermissionPolicy != tc.want {
			t.Fatalf("permissionPolicy from %s = %q, want %q", tc.raw, got.PermissionPolicy, tc.want)
		}
	}
	deny := "deny"
	got, err := newStore(t).Set(Patch{PermissionPolicy: &deny})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got.PermissionPolicy != "deny" {
		t.Fatalf("Set permissionPolicy = %q", got.PermissionPolicy)
	}
}

func TestMcpPermissionModeNormalisation(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`{}`, "external"},
		{`{"mcpPermissionMode": "external"}`, "external"},
		{`{"mcpPermissionMode": "LOCAL"}`, "local"},
		{`{"mcpPermissionMode": "local"}`, "local"},
		{`{"mcpPermissionMode": "disabled"}`, "local"},
		{`{"mcpPermissionMode": 7}`, "external"},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(tc.raw), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := New(dir).Get()
		if err != nil {
			t.Fatalf("Get(%s): %v", tc.raw, err)
		}
		if got.McpPermissionMode != tc.want {
			t.Fatalf("mcpPermissionMode from %s = %q, want %q", tc.raw, got.McpPermissionMode, tc.want)
		}
	}
	local := "local"
	got, err := newStore(t).Set(Patch{McpPermissionMode: &local})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got.McpPermissionMode != "local" {
		t.Fatalf("Set mcpPermissionMode = %q", got.McpPermissionMode)
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

func TestAgentProvidersNormalisation(t *testing.T) {
	dir := t.TempDir()
	raw := `{
	  "agentProviders": [
	    {"id": "p1", "name": " DeepSeek ", "baseUrl": "https://api.deepseek.com/v1/", "models": [" deepseek-chat ", "deepseek-chat", ""]},
	    {"id": "p1", "name": "dup", "baseUrl": "https://x.test/v1", "models": ["m"]},
	    {"id": "bad::id", "name": "x", "baseUrl": "https://x.test/v1", "models": ["m"]},
	    {"id": "file", "name": "x", "baseUrl": "file:///etc/passwd", "models": ["m"]},
	    {"id": "p2", "name": "Local", "baseUrl": "http://127.0.0.1:11434/v1", "models": ["llama"]}
	  ],
	  "agentDefaultProviderId": "missing",
	  "agentDefaultModel": "nope"
	}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.AgentProvidersPresent {
		t.Fatal("a present agentProviders field must set AgentProvidersPresent")
	}
	want := []AgentProvider{
		{ID: "p1", Name: "DeepSeek", BaseURL: "https://api.deepseek.com/v1", Models: []string{"deepseek-chat"}},
		{ID: "p2", Name: "Local", BaseURL: "http://127.0.0.1:11434/v1", Models: []string{"llama"}},
	}
	if !reflect.DeepEqual(got.AgentProviders, want) {
		t.Fatalf("providers = %+v, want %+v", got.AgentProviders, want)
	}
	if got.AgentDefaultProviderID != "p1" || got.AgentDefaultModel != "deepseek-chat" {
		t.Fatalf("default = (%q, %q), want the first valid provider's first model",
			got.AgentDefaultProviderID, got.AgentDefaultModel)
	}
}

func TestEmptyAgentProvidersIsNotLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"agentProviders": [], "agentBaseUrl": "https://api.deepseek.com/v1", "agentModel": "deepseek-chat"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.AgentProvidersPresent {
		t.Fatal("empty array is a present field")
	}
	if len(got.AgentProviders) != 0 {
		t.Fatalf("empty array must not synthesise a legacy provider: %+v", got.AgentProviders)
	}
}

func TestAgentProvidersCapAndSetRoundTrip(t *testing.T) {
	in := make([]AgentProvider, AgentProviderMax+2)
	for i := range in {
		in[i] = AgentProvider{
			ID:      "p" + strings.Repeat("x", i) + "id",
			Name:    "N",
			BaseURL: "https://x.test/v1",
			Models:  []string{"m"},
		}
	}
	got, err := newStore(t).Set(Patch{AgentProviders: &in})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(got.AgentProviders) != AgentProviderMax {
		t.Fatalf("kept %d providers, want the %d cap", len(got.AgentProviders), AgentProviderMax)
	}
	if !got.AgentProvidersPresent {
		t.Fatal("Set must persist agentProviders as a present field")
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
