package mcpregistration

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// nativeExe is a Windows-style absolute path; on non-Windows it degrades to a
// cwd-relative string the same way TS resolve() would, so comparisons stay
// consistent within a test as long as both sides use the same string.
const nativeExe = `C:\Program Files\NodeShell\nodeshell.exe`

func nativeSpec() LaunchSpec {
	return LaunchSpec{Command: nativeExe, Args: []string{"--mcp"}}
}

func TestConfigPathsFollowHomeLayout(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "u")
	want := map[Target]string{
		TargetCursor:     filepath.Join(home, ".cursor", "mcp.json"),
		TargetClaudeCode: filepath.Join(home, ".claude.json"),
		TargetCodex:      filepath.Join(home, ".codex", "config.toml"),
		TargetOpenCode:   filepath.Join(home, ".config", "opencode", "opencode.json"),
	}
	for target, wantPath := range want {
		if got := configPathFor(home, target); got != wantPath {
			t.Errorf("configPathFor(%q) = %q, want %q", target, got, wantPath)
		}
	}
}

func TestPathsEqualNormalizesSeparatorsAndCase(t *testing.T) {
	base := filepath.Join(t.TempDir(), "NodeShell", "nodeshell.EXE")
	alt := strings.ReplaceAll(base, `\`, "/")
	if !pathsEqual(base, strings.ToLower(alt)) {
		t.Fatalf("pathsEqual(%q, %q) = false, want true", base, strings.ToLower(alt))
	}
	if pathsEqual(base, filepath.Join(t.TempDir(), "other.exe")) {
		t.Fatal("pathsEqual must distinguish different paths")
	}
}

func TestPathsEqualWindowsDrivePaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows drive-path comparison is Windows-specific")
	}
	if !pathsEqual(`C:\A\b.mjs`, `c:/a/b.mjs`) {
		t.Fatal("pathsEqual must ignore drive-letter case and separator style")
	}
}

func TestLaunchSpecUsesExecutableSeamWithMcpFlag(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "nodeshell.exe")
	svc := NewWithSeams(
		func() (string, error) { return exe, nil },
		func() (string, error) { return t.TempDir(), nil },
	)
	spec, err := svc.launchSpec()
	if err != nil {
		t.Fatalf("launchSpec: %v", err)
	}
	want, err := filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command != want {
		t.Fatalf("command = %q, want %q", spec.Command, want)
	}
	if !reflect.DeepEqual(spec.Args, []string{"--mcp"}) {
		t.Fatalf("args = %v, want [--mcp]", spec.Args)
	}
}

func TestLaunchSpecExecutableErrorIsObservable(t *testing.T) {
	svc := NewWithSeams(
		func() (string, error) { return "", errors.New("boom") },
		func() (string, error) { return t.TempDir(), nil },
	)
	_, err := svc.launchSpec()
	if !errors.Is(err, ErrResolveExecutable) {
		t.Fatalf("error = %v, want ErrResolveExecutable", err)
	}
}

func TestDetectMcpServersJSON(t *testing.T) {
	spec := nativeSpec()
	cases := []struct {
		name       string
		entry      map[string]any
		registered bool
		stale      bool
	}{
		{"native spec", map[string]any{"command": nativeExe, "args": []any{"--mcp"}}, true, false},
		{"native spec with extra args", map[string]any{"command": nativeExe, "args": []any{"--mcp", "--verbose"}}, true, false},
		{"old node relay", map[string]any{"command": "node", "args": []any{"/old/nodeshell-mcp.mjs"}}, false, true},
		{"old node.exe relay", map[string]any{"command": "node.exe", "args": []any{"C:/old/nodeshell-mcp.mjs"}}, false, true},
		{"old bun relay", map[string]any{"command": "bun", "args": []any{"C:/old/nodeshell-mcp.mjs"}}, false, true},
		{"command array relay", map[string]any{"command": []any{"node", "C:/old/nodeshell-mcp.mjs"}}, false, true},
		{"different executable", map[string]any{"command": "C:/other/nodeshell.exe", "args": []any{"--mcp"}}, false, true},
		{"no mcp arg", map[string]any{"command": nativeExe, "args": []any{"serve"}}, false, true},
		{"missing args", map[string]any{"command": nativeExe}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registered, stale := detectMcpServers(tc.entry, spec)
			if registered != tc.registered || stale != tc.stale {
				t.Fatalf("detectMcpServers = (%v, %v), want (%v, %v)", registered, stale, tc.registered, tc.stale)
			}
		})
	}
}

func TestDetectOpenCodeJSON(t *testing.T) {
	spec := nativeSpec()
	cases := []struct {
		name       string
		entry      map[string]any
		registered bool
		stale      bool
	}{
		{"native spec", map[string]any{"type": "local", "command": []any{nativeExe, "--mcp"}, "enabled": true}, true, false},
		{"native spec with extra args", map[string]any{"command": []any{nativeExe, "--mcp", "extra"}}, true, false},
		{"old node relay", map[string]any{"command": []any{"node", "C:/old/nodeshell-mcp.mjs"}}, false, true},
		{"missing mcp arg", map[string]any{"command": []any{nativeExe}}, false, true},
		{"command not an array", map[string]any{"command": nativeExe}, false, true},
		{"empty command array", map[string]any{"command": []any{}}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registered, stale := detectOpenCode(tc.entry, spec)
			if registered != tc.registered || stale != tc.stale {
				t.Fatalf("detectOpenCode = (%v, %v), want (%v, %v)", registered, stale, tc.registered, tc.stale)
			}
		})
	}
}

func TestDetectCodexTOML(t *testing.T) {
	spec := nativeSpec()
	cases := []struct {
		name       string
		text       string
		registered bool
		stale      bool
	}{
		{"native spec", `[mcp_servers.nodeshell]
command = "C:\\Program Files\\NodeShell\\nodeshell.exe"
args = ["--mcp"]
`, true, false},
		{"native spec literal string path", `[mcp_servers.nodeshell]
command = 'C:\Program Files\NodeShell\nodeshell.exe'
args = ["--mcp"]
`, true, false},
		{"native spec multiline args", `[mcp_servers.nodeshell]
command = "C:\\Program Files\\NodeShell\\nodeshell.exe"
args = [
  "--mcp",
]
`, true, false},
		{"block with comments", `# header comment
[mcp_servers.nodeshell] # inline
command = "C:\\Program Files\\NodeShell\\nodeshell.exe" # trailing
args = ["--mcp"]
# footer comment
`, true, false},
		{"old node relay", `[mcp_servers.nodeshell]
command = "node"
args = ["C:/old/nodeshell-mcp.mjs"]
`, false, true},
		{"missing mcp arg", `[mcp_servers.nodeshell]
command = "C:\\Program Files\\NodeShell\\nodeshell.exe"
args = ["serve"]
`, false, true},
		{"no block", `model = "gpt"
`, false, false},
		{"other server block", `[mcp_servers.other]
command = "x"
`, false, false},
		{"header prefix must not match", `[mcp_servers.nodeshell-extra]
command = "C:\\Program Files\\NodeShell\\nodeshell.exe"
args = ["--mcp"]
`, false, false},
		{"commented header must not match", `# [mcp_servers.nodeshell]
command = "C:\\Program Files\\NodeShell\\nodeshell.exe"
`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registered, stale := detectCodex(tc.text, spec)
			if registered != tc.registered || stale != tc.stale {
				t.Fatalf("detectCodex = (%v, %v), want (%v, %v)", registered, stale, tc.registered, tc.stale)
			}
		})
	}
}

func TestMergeMcpServersJSONPreservesUnrelatedContent(t *testing.T) {
	next, err := mergeMcpServersJSON(`{"mcpServers": {"other": {"command": "x", "args": []}}, "future": {"a": 1}}`, nativeSpec())
	if err != nil {
		t.Fatalf("mergeMcpServersJSON: %v", err)
	}
	root := parseTestJSON(t, next)
	servers := root["mcpServers"].(map[string]any)
	if got := servers["other"].(map[string]any)["command"]; got != "x" {
		t.Fatalf("other server dropped: %v", servers["other"])
	}
	ns := servers["nodeshell"].(map[string]any)
	if ns["command"] != nativeExe {
		t.Fatalf("nodeshell command = %v, want %q", ns["command"], nativeExe)
	}
	if !reflect.DeepEqual(ns["args"], []any{"--mcp"}) {
		t.Fatalf("nodeshell args = %v, want [--mcp]", ns["args"])
	}
	if root["future"] == nil {
		t.Fatal("unknown root field was dropped")
	}
}

func TestMergeMcpServersJSONFromEmptyInput(t *testing.T) {
	for _, input := range []string{"", "   ", "\n"} {
		next, err := mergeMcpServersJSON(input, nativeSpec())
		if err != nil {
			t.Fatalf("mergeMcpServersJSON(%q): %v", input, err)
		}
		root := parseTestJSON(t, next)
		if root["mcpServers"].(map[string]any)["nodeshell"] == nil {
			t.Fatal("empty input must produce a nodeshell entry")
		}
	}
}

func TestMergeMcpServersJSONInvalidIsError(t *testing.T) {
	for _, input := range []string{"{not-json", "[1, 2]", "null", `"str"`, "5"} {
		if _, err := mergeMcpServersJSON(input, nativeSpec()); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("mergeMcpServersJSON(%q) error = %v, want ErrInvalidConfig", input, err)
		}
	}
}

func TestMergeOpenCodeJSON(t *testing.T) {
	next, err := mergeOpenCodeJSON(`{"model":"x","$schema":"https://opencode.ai/config.json","mcp":{"other":{"type":"remote"}}}`, nativeSpec())
	if err != nil {
		t.Fatalf("mergeOpenCodeJSON: %v", err)
	}
	root := parseTestJSON(t, next)
	if root["model"] != "x" {
		t.Fatalf("model dropped: %v", root["model"])
	}
	if root["$schema"] != "https://opencode.ai/config.json" {
		t.Fatalf("$schema changed: %v", root["$schema"])
	}
	if root["mcp"].(map[string]any)["other"] == nil {
		t.Fatal("other mcp server dropped")
	}
	ns := root["mcp"].(map[string]any)["nodeshell"].(map[string]any)
	if ns["type"] != "local" || ns["enabled"] != true {
		t.Fatalf("nodeshell = %v, want type local enabled true", ns)
	}
	if !reflect.DeepEqual(ns["command"], []any{nativeExe, "--mcp"}) {
		t.Fatalf("nodeshell command = %v, want [%q, --mcp]", ns["command"], nativeExe)
	}
}

func TestMergeOpenCodeJSONAddsSchemaWhenAbsent(t *testing.T) {
	next, err := mergeOpenCodeJSON(`{"model":"x"}`, nativeSpec())
	if err != nil {
		t.Fatal(err)
	}
	root := parseTestJSON(t, next)
	if root["$schema"] != "https://opencode.ai/config.json" {
		t.Fatalf("$schema = %v, want the opencode schema URL", root["$schema"])
	}
}

func TestMergeCodexTOMLAppendsBlockPreservingContent(t *testing.T) {
	next := mergeCodexTOML("model = \"gpt\"\n[other]\nkey = 1\n", nativeSpec())
	for _, want := range []string{`model = "gpt"`, "[other]", "key = 1", "[mcp_servers.nodeshell]", `args = ["--mcp"]`} {
		if !strings.Contains(next, want) {
			t.Fatalf("merged TOML lost %q:\n%s", want, next)
		}
	}
	if strings.Count(next, "[mcp_servers.nodeshell]") != 1 {
		t.Fatalf("nodeshell block count = %d, want 1:\n%s", strings.Count(next, "[mcp_servers.nodeshell]"), next)
	}
}

func TestMergeCodexTOMLUpdatesExistingBlockInPlace(t *testing.T) {
	existing := `model = "gpt"
[mcp_servers.nodeshell]
command = "node"
args = ["C:/old/nodeshell-mcp.mjs"]
[b]
y = 2
`
	next := mergeCodexTOML(existing, nativeSpec())
	if strings.Count(next, "[mcp_servers.nodeshell]") != 1 {
		t.Fatalf("nodeshell block count = %d, want 1:\n%s", strings.Count(next, "[mcp_servers.nodeshell]"), next)
	}
	if strings.Contains(next, "nodeshell-mcp.mjs") {
		t.Fatalf("old relay path still present:\n%s", next)
	}
	if !strings.Contains(next, `command = "C:\\Program Files\\NodeShell\\nodeshell.exe"`) {
		t.Fatalf("native command missing:\n%s", next)
	}
	if !strings.Contains(next, "[b]") || !strings.Contains(next, "y = 2") {
		t.Fatalf("following block was damaged:\n%s", next)
	}
	model, ns, b := strings.Index(next, "model = \"gpt\""), strings.Index(next, "[mcp_servers.nodeshell]"), strings.Index(next, "[b]")
	if !(model < ns && ns < b) {
		t.Fatalf("unexpected order (model=%d ns=%d b=%d):\n%s", model, ns, b, next)
	}
}

func TestMergeCodexTOMLPreservesComments(t *testing.T) {
	existing := `# top comment
model = "gpt"
# before other
[other]
key = 1
# after other
`
	next := mergeCodexTOML(existing, nativeSpec())
	for _, want := range []string{"# top comment", "# before other", "# after other", `model = "gpt"`, "[other]"} {
		if !strings.Contains(next, want) {
			t.Fatalf("merged TOML lost %q:\n%s", want, next)
		}
	}
	if strings.Count(next, "[mcp_servers.nodeshell]") != 1 {
		t.Fatalf("nodeshell block count = %d, want 1:\n%s", strings.Count(next, "[mcp_servers.nodeshell]"), next)
	}
}

func TestMergeCodexTOMLPreservesCRLF(t *testing.T) {
	existing := "model = \"gpt\"\r\n[other]\r\nkey = 1\r\n"
	next := mergeCodexTOML(existing, nativeSpec())
	if !strings.Contains(next, "model = \"gpt\"\r\n") {
		t.Fatalf("CRLF lost before the block:\n%q", next)
	}
	if !strings.Contains(next, "[other]\r\n") {
		t.Fatalf("CRLF lost in other block:\n%q", next)
	}
	if !strings.Contains(next, "command = \"C:\\\\Program Files\\\\NodeShell\\\\nodeshell.exe\"\r\n") {
		t.Fatalf("new block did not follow the file's CRLF ending:\n%q", next)
	}
	if strings.Contains(next, "key = 1\n\n") || !strings.HasSuffix(next, "\r\n") {
		t.Fatalf("trailing newline mismatch:\n%q", next)
	}
}

func TestMergeCodexTOMLWindowsPathEscaping(t *testing.T) {
	next := mergeCodexTOML("", LaunchSpec{Command: `C:\Program Files\NodeShell\nodeshell.exe`, Args: []string{"--mcp"}})
	if !strings.Contains(next, `command = "C:\\Program Files\\NodeShell\\nodeshell.exe"`) {
		t.Fatalf("backslashes not escaped:\n%q", next)
	}
	if !strings.Contains(next, `args = ["--mcp"]`) {
		t.Fatalf("args missing:\n%q", next)
	}
}

func TestMergeCodexTOMLRoundTripDetectsRegistered(t *testing.T) {
	spec := LaunchSpec{Command: `C:\Program Files\NodeShell\nodeshell.exe`, Args: []string{"--mcp"}}
	merged := mergeCodexTOML("", spec)
	registered, stale := detectCodex(merged, spec)
	if !registered || stale {
		t.Fatalf("round-trip detect = (%v, %v), want (true, false):\n%s", registered, stale, merged)
	}
}

func TestMergeCodexTOMLEmptyInput(t *testing.T) {
	next := mergeCodexTOML("", nativeSpec())
	if strings.TrimSpace(next) == "" || strings.Count(next, "[mcp_servers.nodeshell]") != 1 {
		t.Fatalf("empty input produced a broken document:\n%q", next)
	}
	if !strings.HasSuffix(next, "\n") {
		t.Fatalf("block must end with a newline:\n%q", next)
	}
}

func TestServiceRegisterAllThenStatusRegistered(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "nodeshell.exe")
	svc := NewWithSeams(
		func() (string, error) { return exe, nil },
		func() (string, error) { return home, nil },
	)

	results, err := svc.Register("all")
	if err != nil {
		t.Fatalf("Register(all): %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("Register(all) returned %d results, want 4", len(results))
	}
	for _, r := range results {
		if !r.OK {
			t.Fatalf("register %s failed: %s", r.ID, r.Message)
		}
	}

	status := svc.Status()
	if len(status) != 4 {
		t.Fatalf("Status returned %d targets, want 4", len(status))
	}
	wantOrder := []Target{TargetOpenCode, TargetClaudeCode, TargetCodex, TargetCursor}
	for i, want := range wantOrder {
		if status[i].ID != want {
			t.Fatalf("Status[%d].ID = %q, want %q (UI order)", i, status[i].ID, want)
		}
		if status[i].Label == "" {
			t.Fatalf("Status[%d] missing label", i)
		}
		if status[i].ConfigPath == "" {
			t.Fatalf("Status[%d] missing configPath", i)
		}
		if !status[i].Registered || status[i].Stale {
			t.Fatalf("Status[%d] = %+v, want registered and not stale", i, status[i])
		}
	}

	cursor := parseTestJSONFile(t, filepath.Join(home, ".cursor", "mcp.json"))
	ns := cursor["mcpServers"].(map[string]any)["nodeshell"].(map[string]any)
	if ns["command"] != exe {
		t.Fatalf("cursor command = %v, want %q", ns["command"], exe)
	}
	if !reflect.DeepEqual(ns["args"], []any{"--mcp"}) {
		t.Fatalf("cursor args = %v, want [--mcp]", ns["args"])
	}

	opencode := parseTestJSONFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"))
	ocNS := opencode["mcp"].(map[string]any)["nodeshell"].(map[string]any)
	if ocNS["type"] != "local" || ocNS["enabled"] != true {
		t.Fatalf("opencode nodeshell = %v, want type local enabled true", ocNS)
	}
	if !reflect.DeepEqual(ocNS["command"], []any{exe, "--mcp"}) {
		t.Fatalf("opencode command = %v, want [%q, --mcp]", ocNS["command"], exe)
	}
	if opencode["$schema"] != "https://opencode.ai/config.json" {
		t.Fatalf("opencode $schema = %v", opencode["$schema"])
	}

	codex, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("codex config: %v", err)
	}
	if !strings.Contains(string(codex), "[mcp_servers.nodeshell]") {
		t.Fatalf("codex block missing:\n%s", codex)
	}
}

func TestServiceRegisterUpgradesStaleRelaysInPlace(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "nodeshell.exe")
	writeTestFile(t, filepath.Join(home, ".cursor", "mcp.json"),
		`{"mcpServers":{"nodeshell":{"command":"node","args":["C:/old/nodeshell-mcp.mjs"]}}}`)
	writeTestFile(t, filepath.Join(home, ".claude.json"),
		`{"mcpServers":{"nodeshell":{"command":"node.exe","args":["C:/old/nodeshell-mcp.mjs"]}}}`)
	writeTestFile(t, filepath.Join(home, ".codex", "config.toml"),
		"[mcp_servers.nodeshell]\ncommand = \"node\"\nargs = [\"C:/old/nodeshell-mcp.mjs\"]\n")
	writeTestFile(t, filepath.Join(home, ".config", "opencode", "opencode.json"),
		`{"mcp":{"nodeshell":{"type":"local","command":["node","C:/old/nodeshell-mcp.mjs"],"enabled":true}}}`)

	svc := NewWithSeams(
		func() (string, error) { return exe, nil },
		func() (string, error) { return home, nil },
	)

	status := svc.Status()
	for _, s := range status {
		if s.Registered || !s.Stale {
			t.Fatalf("pre-register Status[%s] = %+v, want stale", s.ID, s)
		}
	}

	results, err := svc.Register("all")
	if err != nil {
		t.Fatalf("Register(all): %v", err)
	}
	for _, r := range results {
		if !r.OK {
			t.Fatalf("upgrade %s failed: %s", r.ID, r.Message)
		}
	}

	status = svc.Status()
	for _, s := range status {
		if !s.Registered || s.Stale {
			t.Fatalf("post-register Status[%s] = %+v, want registered", s.ID, s)
		}
	}
}

func TestServiceRegisterInvalidJSONPreservesBytes(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "nodeshell.exe")
	cursorPath := filepath.Join(home, ".cursor", "mcp.json")
	writeTestFile(t, cursorPath, "{not-json")
	svc := NewWithSeams(
		func() (string, error) { return exe, nil },
		func() (string, error) { return home, nil },
	)
	results, err := svc.Register("cursor")
	if err != nil {
		t.Fatalf("Register(cursor): %v", err)
	}
	if results[0].OK {
		t.Fatal("register must fail on an invalid JSON config")
	}
	if !strings.Contains(results[0].Message, ErrInvalidConfig.Error()) {
		t.Fatalf("message = %q, want it to mention %q", results[0].Message, ErrInvalidConfig.Error())
	}
	if strings.Contains(results[0].Message, home) {
		t.Fatalf("message leaks the home path: %q", results[0].Message)
	}
	raw, err := os.ReadFile(cursorPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{not-json" {
		t.Fatalf("invalid config was overwritten: %q", raw)
	}
}

func TestServiceRegisterWriteFailureIsGeneric(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "nodeshell.exe")
	// A FILE at ~/.cursor makes the atomic write's MkdirAll fail.
	writeTestFile(t, filepath.Join(home, ".cursor"), "x")
	svc := NewWithSeams(
		func() (string, error) { return exe, nil },
		func() (string, error) { return home, nil },
	)
	results, err := svc.Register("cursor")
	if err != nil {
		t.Fatalf("Register(cursor): %v", err)
	}
	if results[0].OK {
		t.Fatal("register must fail when the config dir cannot be created")
	}
	if strings.Contains(results[0].Message, home) || strings.Contains(results[0].Message, "mcp.json") {
		t.Fatalf("failure message leaks a path: %q", results[0].Message)
	}
}

func TestServiceRegisterAllPartialFailureKeepsPriorSuccesses(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "nodeshell.exe")
	// opencode is first in UI order; a FILE at ~/.config/opencode fails it.
	writeTestFile(t, filepath.Join(home, ".config", "opencode"), "x")
	svc := NewWithSeams(
		func() (string, error) { return exe, nil },
		func() (string, error) { return home, nil },
	)
	results, err := svc.Register("all")
	if err != nil {
		t.Fatalf("Register(all): %v", err)
	}
	if results[0].ID != TargetOpenCode || results[0].OK {
		t.Fatalf("opencode result = %+v, want ok=false (first in UI order)", results[0])
	}
	for _, r := range results[1:] {
		if !r.OK {
			t.Fatalf("%s should have succeeded: %s", r.ID, r.Message)
		}
	}
	for _, path := range []string{
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".codex", "config.toml"),
		filepath.Join(home, ".cursor", "mcp.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("prior success not persisted for %s: %v", path, err)
		}
	}
}

func TestServiceRegisterUnknownTargetFailsObservably(t *testing.T) {
	svc := NewWithSeams(
		func() (string, error) { return filepath.Join(t.TempDir(), "nodeshell.exe"), nil },
		func() (string, error) { return t.TempDir(), nil },
	)
	results, err := svc.Register("vim")
	if err != nil {
		t.Fatalf("Register(vim): %v", err)
	}
	if len(results) != 1 || results[0].OK {
		t.Fatalf("results = %+v, want one failing result", results)
	}
}

func TestServiceExecutableErrorIsWholeCallError(t *testing.T) {
	svc := NewWithSeams(
		func() (string, error) { return "", errors.New("boom") },
		func() (string, error) { return t.TempDir(), nil },
	)
	if _, err := svc.Register("all"); !errors.Is(err, ErrResolveExecutable) {
		t.Fatalf("Register error = %v, want ErrResolveExecutable", err)
	}
	if _, err := svc.Register("cursor"); !errors.Is(err, ErrResolveExecutable) {
		t.Fatalf("Register(cursor) error = %v, want ErrResolveExecutable", err)
	}
	if _, err := svc.ClipboardSnippet(); !errors.Is(err, ErrResolveExecutable) {
		t.Fatalf("ClipboardSnippet error = %v, want ErrResolveExecutable", err)
	}
	status := svc.Status()
	for _, s := range status {
		if s.Detail != ErrResolveExecutable.Error() {
			t.Fatalf("Status[%s].Detail = %q, want the executable error", s.ID, s.Detail)
		}
		if s.Registered || s.Stale {
			t.Fatalf("Status[%s] must not report registered/stale without an executable", s.ID)
		}
	}
}

func TestServiceHomeFailureIsObservable(t *testing.T) {
	svc := NewWithSeams(
		func() (string, error) { return filepath.Join(t.TempDir(), "nodeshell.exe"), nil },
		func() (string, error) { return "", errors.New("no home") },
	)
	if _, err := svc.Register("all"); !errors.Is(err, ErrResolveHome) {
		t.Fatalf("Register error = %v, want ErrResolveHome", err)
	}
	status := svc.Status()
	for _, s := range status {
		if s.Detail != ErrResolveHome.Error() {
			t.Fatalf("Status[%s].Detail = %q, want the home error", s.ID, s.Detail)
		}
	}
}

func TestServiceStatusDetailOnInvalidConfig(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "nodeshell.exe")
	writeTestFile(t, filepath.Join(home, ".cursor", "mcp.json"), "{broken")
	svc := NewWithSeams(
		func() (string, error) { return exe, nil },
		func() (string, error) { return home, nil },
	)
	status := svc.Status()
	for _, s := range status {
		if s.ID != TargetCursor {
			if s.Detail != "" {
				t.Fatalf("Status[%s].Detail = %q, want empty for a missing config", s.ID, s.Detail)
			}
			continue
		}
		if s.Registered || s.Stale || s.Detail == "" {
			t.Fatalf("Status[cursor] = %+v, want detail for the corrupt config", s)
		}
		if strings.Contains(s.Detail, home) {
			t.Fatalf("detail leaks the home path: %q", s.Detail)
		}
	}
}

func TestServiceClipboardSnippetUsesNativeSpec(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "nodeshell.exe")
	svc := NewWithSeams(
		func() (string, error) { return exe, nil },
		func() (string, error) { return t.TempDir(), nil },
	)
	snippet, err := svc.ClipboardSnippet()
	if err != nil {
		t.Fatalf("ClipboardSnippet: %v", err)
	}
	if strings.HasSuffix(snippet, "\n") {
		t.Fatalf("snippet must not end with a newline (TS parity): %q", snippet)
	}
	doc := parseTestJSON(t, snippet)
	ns := doc["mcpServers"].(map[string]any)["nodeshell"].(map[string]any)
	if ns["command"] != exe {
		t.Fatalf("snippet command = %v, want %q", ns["command"], exe)
	}
	if !reflect.DeepEqual(ns["args"], []any{"--mcp"}) {
		t.Fatalf("snippet args = %v, want [--mcp]", ns["args"])
	}
}

func TestServiceManualConfigEmitsAllFormats(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "nodeshell.exe")
	svc := NewWithSeams(
		func() (string, error) { return exe, nil },
		func() (string, error) { return t.TempDir(), nil },
	)
	got, err := svc.ManualConfig()
	if err != nil {
		t.Fatalf("ManualConfig: %v", err)
	}
	if got.Command != exe {
		t.Fatalf("command = %q, want %q", got.Command, exe)
	}
	if !reflect.DeepEqual(got.Args, []string{"--mcp"}) {
		t.Fatalf("args = %v, want [--mcp]", got.Args)
	}

	standard := parseTestJSON(t, got.Snippets.Standard)
	ns := standard["mcpServers"].(map[string]any)["nodeshell"].(map[string]any)
	if ns["command"] != exe || !reflect.DeepEqual(ns["args"], []any{"--mcp"}) {
		t.Fatalf("standard snippet = %s", got.Snippets.Standard)
	}

	vscode := parseTestJSON(t, got.Snippets.VSCode)
	vs := vscode["servers"].(map[string]any)["nodeshell"].(map[string]any)
	if vs["type"] != "stdio" || vs["command"] != exe || !reflect.DeepEqual(vs["args"], []any{"--mcp"}) {
		t.Fatalf("vscode snippet = %s", got.Snippets.VSCode)
	}

	opencode := parseTestJSON(t, got.Snippets.OpenCode)
	oc := opencode["mcp"].(map[string]any)["nodeshell"].(map[string]any)
	if oc["type"] != "local" || oc["enabled"] != true || !reflect.DeepEqual(oc["command"], []any{exe, "--mcp"}) {
		t.Fatalf("opencode snippet = %s", got.Snippets.OpenCode)
	}

	if !strings.Contains(got.Snippets.Codex, "[mcp_servers.nodeshell]") {
		t.Fatalf("codex snippet missing header: %s", got.Snippets.Codex)
	}
	if !strings.Contains(got.Snippets.Codex, tomlQuote(exe)) || !strings.Contains(got.Snippets.Codex, `--mcp`) {
		t.Fatalf("codex snippet missing launch spec: %s", got.Snippets.Codex)
	}

	for name, text := range map[string]string{
		"standard": got.Snippets.Standard,
		"vscode":   got.Snippets.VSCode,
		"opencode": got.Snippets.OpenCode,
		"codex":    got.Snippets.Codex,
	} {
		if strings.HasSuffix(text, "\n") {
			t.Fatalf("%s snippet must not end with a newline: %q", name, text)
		}
	}

	snippet, err := svc.ClipboardSnippet()
	if err != nil {
		t.Fatalf("ClipboardSnippet: %v", err)
	}
	if got.Snippets.Standard != snippet {
		t.Fatalf("standard snippet must match ClipboardSnippet")
	}
}

func TestServiceManualConfigResolveExecutableError(t *testing.T) {
	svc := NewWithSeams(
		func() (string, error) { return "", errors.New("no exe") },
		func() (string, error) { return t.TempDir(), nil },
	)
	if _, err := svc.ManualConfig(); !errors.Is(err, ErrResolveExecutable) {
		t.Fatalf("ManualConfig error = %v, want ErrResolveExecutable", err)
	}
}

func TestServiceStatusOrderAndLabels(t *testing.T) {
	svc := NewWithSeams(
		func() (string, error) { return filepath.Join(t.TempDir(), "nodeshell.exe"), nil },
		func() (string, error) { return t.TempDir(), nil },
	)
	status := svc.Status()
	want := []struct {
		id    Target
		label string
	}{
		{TargetOpenCode, "OpenCode"},
		{TargetClaudeCode, "Claude Code"},
		{TargetCodex, "Codex"},
		{TargetCursor, "Cursor"},
	}
	if len(status) != len(want) {
		t.Fatalf("Status returned %d targets, want %d", len(status), len(want))
	}
	for i, w := range want {
		if status[i].ID != w.id || status[i].Label != w.label {
			t.Fatalf("Status[%d] = (%q, %q), want (%q, %q)", i, status[i].ID, status[i].Label, w.id, w.label)
		}
	}
}

func TestCodexTOMLParserRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"unterminated string", "[mcp_servers.nodeshell]\ncommand = \"C:\\x\n"},
		{"unterminated array", "[mcp_servers.nodeshell]\ncommand = \"x\"\nargs = [\"--mcp\"\n"},
		{"missing command", "[mcp_servers.nodeshell]\nargs = [\"--mcp\"]\n"},
		{"missing args", "[mcp_servers.nodeshell]\ncommand = \"x\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registered, stale := detectCodex(tc.text, nativeSpec())
			if registered || !stale {
				t.Fatalf("detectCodex = (%v, %v), want stale on malformed block", registered, stale)
			}
		})
	}
}

// --- helpers ---

func parseTestJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	if doc == nil {
		t.Fatalf("JSON parsed to null: %q", raw)
	}
	return doc
}

func parseTestJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return parseTestJSON(t, string(raw))
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
