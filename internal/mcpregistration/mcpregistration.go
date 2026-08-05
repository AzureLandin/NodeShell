// Package mcpregistration registers the native NodeShell MCP server (this
// executable with --mcp) into the four MCP client configs, replacing the
// Electron-era "node .../nodeshell-mcp.mjs" relay entries. It mirrors the
// semantics of src/main/mcp-registration.ts with the launch spec switched
// from "node <script>" to "<exe> --mcp": configs are detected as
// registered/stale/missing, merged without dropping unrelated content, and
// written atomically.
package mcpregistration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"nodeshell/internal/atomicfile"
)

// Target identifies one MCP client config (src/shared/types.ts
// McpRegistrationTarget).
type Target string

const (
	TargetCursor     Target = "cursor"
	TargetClaudeCode Target = "claudeCode"
	TargetCodex      Target = "codex"
	TargetOpenCode   Target = "opencode"
)

// LaunchSpec is the native MCP launch contract: the NodeShell executable with
// the --mcp argument. The frontend-facing shapes are built from it
// (mcpServers {command, args}, opencode command array [exe, --mcp], TOML
// command/args).
type LaunchSpec struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// TargetStatus mirrors McpRegistrationTargetStatus in src/shared/types.ts.
// Detail is optional; an empty detail is omitted from the JSON the frontend
// receives.
type TargetStatus struct {
	ID         Target `json:"id"`
	Label      string `json:"label"`
	ConfigPath string `json:"configPath"`
	Registered bool   `json:"registered"`
	Stale      bool   `json:"stale"`
	Detail     string `json:"detail,omitempty"`
}

// Result mirrors McpRegistrationResult in src/shared/types.ts.
type Result struct {
	ID      Target `json:"id"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Sentinel errors the App layer can errors.Is against. Their messages are
// generic: absolute home/config paths never cross the IPC boundary.
var (
	ErrResolveExecutable = errors.New("nodeshell: cannot resolve the NodeShell executable")
	ErrResolveHome       = errors.New("nodeshell: cannot resolve the user home directory")
	ErrInvalidConfig     = errors.New("nodeshell: MCP config is not valid JSON")
)

// targetMeta pairs a target with its display label.
type targetMeta struct {
	id    Target
	label string
}

// uiOrder mirrors TARGET_META in mcp-registration.ts: the Settings modal
// renders statuses and register('all') writes in this order.
var uiOrder = []targetMeta{
	{id: TargetOpenCode, label: "OpenCode"},
	{id: TargetClaudeCode, label: "Claude Code"},
	{id: TargetCodex, label: "Codex"},
	{id: TargetCursor, label: "Cursor"},
}

// configPathFor returns the config path for target under home (mirrors
// configPathForTarget in mcp-registration.ts). An unknown target yields "".
func configPathFor(home string, t Target) string {
	switch t {
	case TargetCursor:
		return filepath.Join(home, ".cursor", "mcp.json")
	case TargetClaudeCode:
		return filepath.Join(home, ".claude.json")
	case TargetCodex:
		return filepath.Join(home, ".codex", "config.toml")
	case TargetOpenCode:
		return filepath.Join(home, ".config", "opencode", "opencode.json")
	}
	return ""
}

// normalizePathForCompare mirrors normalizePathForCompare in
// mcp-registration.ts: resolve, clean, forward-slash and lowercase, so
// config-written paths compare equal regardless of separator style or case.
func normalizePathForCompare(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	return strings.ToLower(filepath.ToSlash(filepath.Clean(abs)))
}

func pathsEqual(a, b string) bool {
	return normalizePathForCompare(a) == normalizePathForCompare(b)
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// stringArgs converts a JSON-decoded []any of strings into []string.
func stringArgs(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// detectMcpServers inspects the mcpServers.nodeshell entry (cursor/claude).
// Registered requires the command path to equal the native executable AND
// args to carry --mcp; any other nodeshell entry is stale (including the old
// node/node.exe/bun relay and command-array forms).
func detectMcpServers(entry map[string]any, spec LaunchSpec) (registered, stale bool) {
	cmd, _ := entry["command"].(string)
	args, _ := stringArgs(entry["args"])
	if pathsEqual(cmd, spec.Command) && containsArg(args, "--mcp") {
		return true, false
	}
	return false, true
}

// detectOpenCode inspects the mcp.nodeshell entry. Registered requires the
// first command element to equal the native executable and the command array
// to carry --mcp.
func detectOpenCode(entry map[string]any, spec LaunchSpec) (registered, stale bool) {
	cmd, _ := entry["command"].([]any)
	args := make([]string, 0, len(cmd))
	for _, c := range cmd {
		if s, ok := c.(string); ok {
			args = append(args, s)
		}
	}
	first := ""
	if len(cmd) > 0 {
		first, _ = cmd[0].(string)
	}
	if pathsEqual(first, spec.Command) && containsArg(args, "--mcp") {
		return true, false
	}
	return false, true
}

// detectCodex inspects the [mcp_servers.nodeshell] TOML block. A block that
// parses with the native command and --mcp arg is registered; a block that
// exists but does not match (old relay, wrong executable, unparseable) is
// stale; no block is missing.
func detectCodex(text string, spec LaunchSpec) (registered, stale bool) {
	start, end, ok := codexBlock(text)
	if !ok {
		return false, false
	}
	command, args, ok := parseCodexBlock(text[start:end])
	if !ok {
		return false, true
	}
	if pathsEqual(command, spec.Command) && containsArg(args, "--mcp") {
		return true, false
	}
	return false, true
}

// codexBlock locates the [mcp_servers.nodeshell] header and returns the byte
// span of the whole block: from the header line through the next table-header
// line (a line whose trimmed form starts with '[') or EOF. Bounding the block
// at header lines — not at the next '[' character, which the TS regex stops
// at — keeps "args = ["--mcp"]" inside the block so a replace consumes the
// old block completely instead of leaving a trailing fragment.
func codexBlock(text string) (start, end int, ok bool) {
	start = findCodexHeader(text)
	if start < 0 {
		return 0, 0, false
	}
	offset := start
	for {
		lineEnd := len(text)
		if i := strings.IndexByte(text[offset:], '\n'); i >= 0 {
			lineEnd = offset + i
		}
		next := lineEnd + 1
		if next > len(text) {
			return start, len(text), true
		}
		nextEnd := len(text)
		if i := strings.IndexByte(text[next:], '\n'); i >= 0 {
			nextEnd = next + i
		}
		if strings.HasPrefix(strings.TrimLeft(text[next:nextEnd], " \t"), "[") {
			return start, next, true
		}
		offset = next
	}
}

// findCodexHeader returns the offset of the [mcp_servers.nodeshell] header
// line, or -1. The header must be its own line with only whitespace or a
// comment after it, so neither [mcp_servers.nodeshell-extra] nor a comment
// like "# [mcp_servers.nodeshell]" is mistaken for the block.
func findCodexHeader(text string) int {
	offset := 0
	for offset <= len(text) {
		lineEnd := len(text)
		if i := strings.IndexByte(text[offset:], '\n'); i >= 0 {
			lineEnd = offset + i
		}
		line := text[offset:lineEnd]
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "[mcp_servers.nodeshell]") {
			rest := strings.TrimLeft(trimmed[len("[mcp_servers.nodeshell]"):], " \t\r")
			if rest == "" || strings.HasPrefix(rest, "#") {
				return offset
			}
		}
		if lineEnd == len(text) {
			break
		}
		offset = lineEnd + 1
	}
	return -1
}

// parseCodexBlock extracts command and args from a [mcp_servers.nodeshell]
// block. Other keys and comments are ignored; missing/malformed command or
// args yields ok=false.
func parseCodexBlock(block string) (command string, args []string, ok bool) {
	offset := 0
	for offset < len(block) {
		lineEnd := len(block)
		if i := strings.IndexByte(block[offset:], '\n'); i >= 0 {
			lineEnd = offset + i
		}
		line := strings.TrimSpace(block[offset:lineEnd])
		if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "[") {
			if eq := strings.IndexByte(line, '='); eq >= 0 {
				key := strings.TrimSpace(line[:eq])
				rest := strings.TrimSpace(block[offset+eq+1:])
				switch key {
				case "command":
					if len(rest) == 0 || (rest[0] != '"' && rest[0] != '\'') {
						return "", nil, false
					}
					val, _, okv := parseTOMLString(rest, 0)
					if !okv {
						return "", nil, false
					}
					command = val
				case "args":
					if len(rest) == 0 || rest[0] != '[' {
						return "", nil, false
					}
					vals, okv := parseTOMLArray(rest, 0)
					if !okv {
						return "", nil, false
					}
					args = vals
				}
			}
		}
		if lineEnd == len(block) {
			break
		}
		offset = lineEnd + 1
	}
	if command == "" {
		return "", nil, false
	}
	return command, args, true
}

// parseTOMLString parses a TOML basic ("...") or literal ('...') string at
// s[i:] and returns the value and the index just past the closing quote.
func parseTOMLString(s string, i int) (value string, next int, ok bool) {
	if i >= len(s) {
		return "", 0, false
	}
	switch s[i] {
	case '\'':
		end := strings.IndexByte(s[i+1:], '\'')
		if end < 0 {
			return "", 0, false
		}
		return s[i+1 : i+1+end], i + end + 2, true
	case '"':
		var b strings.Builder
		j := i + 1
		for j < len(s) {
			c := s[j]
			if c == '"' {
				return b.String(), j + 1, true
			}
			if c != '\\' {
				b.WriteByte(c)
				j++
				continue
			}
			j++
			if j >= len(s) {
				return "", 0, false
			}
			switch s[j] {
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case 'b':
				b.WriteByte('\b')
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'f':
				b.WriteByte('\f')
			case 'r':
				b.WriteByte('\r')
			case 'u', 'U':
				n := 4
				if s[j] == 'U' {
					n = 8
				}
				if j+n >= len(s) {
					return "", 0, false
				}
				var r rune
				if _, err := fmt.Sscanf(s[j+1:j+1+n], "%x", &r); err != nil {
					return "", 0, false
				}
				b.WriteRune(r)
				j += n
			default:
				return "", 0, false
			}
			j++
		}
		return "", 0, false
	}
	return "", 0, false
}

// parseTOMLArray parses a TOML array of strings starting at s[i:] (the '['),
// consuming whitespace, newlines, commas and comments until the closing ']'.
func parseTOMLArray(s string, i int) ([]string, bool) {
	if i >= len(s) || s[i] != '[' {
		return nil, false
	}
	var out []string
	i++
	for i < len(s) {
		switch c := s[i]; {
		case c == ']':
			return out, true
		case c == ',' || c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '#':
			if nl := strings.IndexByte(s[i:], '\n'); nl >= 0 {
				i += nl + 1
			} else {
				return nil, false
			}
		default:
			val, next, ok := parseTOMLString(s, i)
			if !ok {
				return nil, false
			}
			out = append(out, val)
			i = next
		}
	}
	return nil, false
}

// tomlQuote renders s as a TOML basic string, escaping backslashes, quotes
// and control characters so Windows drive paths round-trip.
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// mergeJSON parses existing (empty means a fresh document) and lets apply
// mutate the root, then re-marshals with 2-space indentation. Invalid JSON,
// null and non-object input yield ErrInvalidConfig so the caller never
// overwrites a corrupt config.
func mergeJSON(existing string, apply func(root map[string]any)) (string, error) {
	root := map[string]any{}
	if strings.TrimSpace(existing) != "" {
		if err := json.Unmarshal([]byte(existing), &root); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		if root == nil {
			return "", ErrInvalidConfig
		}
	}
	apply(root)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

// mergeMcpServersJSON merges the native spec into the cursor/claude
// mcpServers object, preserving every other server and root field.
func mergeMcpServersJSON(existing string, spec LaunchSpec) (string, error) {
	return mergeJSON(existing, func(root map[string]any) {
		servers, _ := root["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		servers["nodeshell"] = map[string]any{"command": spec.Command, "args": spec.Args}
		root["mcpServers"] = servers
	})
}

// mergeOpenCodeJSON merges the native spec into the opencode mcp object,
// preserving root fields ($schema included unless absent) and other servers.
func mergeOpenCodeJSON(existing string, spec LaunchSpec) (string, error) {
	return mergeJSON(existing, func(root map[string]any) {
		mcp, _ := root["mcp"].(map[string]any)
		if mcp == nil {
			mcp = map[string]any{}
		}
		command := append([]string{spec.Command}, spec.Args...)
		mcp["nodeshell"] = map[string]any{"type": "local", "command": command, "enabled": true}
		root["mcp"] = mcp
		if _, has := root["$schema"]; !has {
			root["$schema"] = "https://opencode.ai/config.json"
		}
	})
}

// renderCodexBlock renders the [mcp_servers.nodeshell] block with the file's
// line ending.
func renderCodexBlock(spec LaunchSpec, eol string) string {
	args := make([]string, len(spec.Args))
	for i, a := range spec.Args {
		args[i] = tomlQuote(a)
	}
	return "[mcp_servers.nodeshell]" + eol +
		"command = " + tomlQuote(spec.Command) + eol +
		"args = [" + strings.Join(args, ", ") + "]" + eol
}

// detectEOL picks the file's dominant line ending so appended/replaced blocks
// match the rest of the document.
func detectEOL(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// mergeCodexTOML replaces or appends the [mcp_servers.nodeshell] block,
// preserving comments, other blocks and the file's line endings. The result
// always ends with exactly one line ending.
func mergeCodexTOML(text string, spec LaunchSpec) string {
	eol := detectEOL(text)
	block := renderCodexBlock(spec, eol)
	if start, end, ok := codexBlock(text); ok {
		return strings.TrimRight(text[:start]+block+text[end:], " \t\r\n") + eol
	}
	trimmed := strings.TrimRight(text, " \t\r\n")
	if trimmed == "" {
		return block
	}
	return trimmed + eol + eol + block
}

// clipboardSnippet mirrors buildClipboardSnippet: a copy-paste mcpServers
// JSON for manual configuration, without a trailing newline (TS parity).
func clipboardSnippet(spec LaunchSpec) string {
	doc := map[string]any{
		"mcpServers": map[string]any{
			"nodeshell": map[string]any{"command": spec.Command, "args": spec.Args},
		},
	}
	out, _ := json.MarshalIndent(doc, "", "  ")
	return string(out)
}

// Service registers the native MCP launcher into client configs. It is
// stateless; the executable and home seams default to os.Executable /
// os.UserHomeDir and are injectable for tests.
type Service struct {
	execPath func() (string, error)
	homeDir  func() (string, error)
}

// New returns a Service resolving the executable and home from the OS.
func New() *Service {
	return &Service{execPath: os.Executable, homeDir: os.UserHomeDir}
}

// NewWithSeams returns a Service pinned to the given executable and home
// resolvers (test injection).
func NewWithSeams(execPath, homeDir func() (string, error)) *Service {
	return &Service{execPath: execPath, homeDir: homeDir}
}

func (s *Service) launchSpec() (LaunchSpec, error) {
	exe, err := s.execPath()
	if err != nil {
		return LaunchSpec{}, ErrResolveExecutable
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return LaunchSpec{}, ErrResolveExecutable
	}
	return LaunchSpec{Command: abs, Args: []string{"--mcp"}}, nil
}

func (s *Service) home() (string, error) {
	home, err := s.homeDir()
	if err != nil {
		return "", ErrResolveHome
	}
	return home, nil
}

// Status returns the registration state of every target in UI order. Read and
// parse failures surface as per-target details (TS parity) — the call itself
// only fails to resolve the executable/home, which also becomes a detail.
func (s *Service) Status() []TargetStatus {
	spec, specErr := s.launchSpec()
	home, homeErr := s.home()
	out := make([]TargetStatus, 0, len(uiOrder))
	for _, meta := range uiOrder {
		st := TargetStatus{ID: meta.id, Label: meta.label}
		switch {
		case homeErr != nil:
			st.Detail = ErrResolveHome.Error()
		case specErr != nil:
			st.Detail = specErr.Error()
		default:
			st.ConfigPath = configPathFor(home, meta.id)
			st.Registered, st.Stale, st.Detail = s.detect(meta.id, st.ConfigPath, spec)
		}
		out = append(out, st)
	}
	return out
}

// detect reads one target's config and classifies it against the native spec.
func (s *Service) detect(t Target, path string, spec LaunchSpec) (registered, stale bool, detail string) {
	text, err := readTextIfExists(path)
	if err != nil {
		return false, false, "nodeshell: cannot read the MCP config file"
	}
	if text == "" {
		return false, false, ""
	}
	switch t {
	case TargetCodex:
		registered, stale = detectCodex(text, spec)
		return registered, stale, ""
	case TargetOpenCode:
		root, err := parseJSONObject(text)
		if err != nil {
			return false, false, ErrInvalidConfig.Error()
		}
		mcp, _ := root["mcp"].(map[string]any)
		entry, _ := mcp["nodeshell"].(map[string]any)
		if entry == nil {
			return false, false, ""
		}
		registered, stale = detectOpenCode(entry, spec)
		return registered, stale, ""
	default:
		root, err := parseJSONObject(text)
		if err != nil {
			return false, false, ErrInvalidConfig.Error()
		}
		servers, _ := root["mcpServers"].(map[string]any)
		entry, _ := servers["nodeshell"].(map[string]any)
		if entry == nil {
			return false, false, ""
		}
		registered, stale = detectMcpServers(entry, spec)
		return registered, stale, ""
	}
}

// Register writes the native spec into target ("all" for every client in UI
// order) and returns one Result per target. Targets are merged and written
// independently: a failure surfaces in its Result without rolling back the
// targets already written (TS parity). The call errors only when the
// executable or home cannot be resolved.
func (s *Service) Register(target string) ([]Result, error) {
	spec, err := s.launchSpec()
	if err != nil {
		return nil, err
	}
	home, err := s.home()
	if err != nil {
		return nil, err
	}
	if target == "all" {
		results := make([]Result, 0, len(uiOrder))
		for _, meta := range uiOrder {
			results = append(results, s.registerOne(meta.id, home, spec))
		}
		return results, nil
	}
	t := Target(target)
	if configPathFor(home, t) == "" {
		return []Result{{ID: t, OK: false, Message: "nodeshell: unknown MCP registration target"}}, nil
	}
	return []Result{s.registerOne(t, home, spec)}, nil
}

// registerOne merges and atomically writes one target's config. Messages are
// generic: the only path shown is the success message (TS parity, never
// surfaced by the UI), and failure messages never contain home/config paths.
func (s *Service) registerOne(t Target, home string, spec LaunchSpec) Result {
	path := configPathFor(home, t)
	existing, err := readTextIfExists(path)
	if err != nil {
		return Result{ID: t, OK: false, Message: "nodeshell: cannot read the MCP config file"}
	}
	var next string
	switch t {
	case TargetCodex:
		next = mergeCodexTOML(existing, spec)
	case TargetOpenCode:
		next, err = mergeOpenCodeJSON(existing, spec)
	default:
		next, err = mergeMcpServersJSON(existing, spec)
	}
	if err != nil {
		return Result{ID: t, OK: false, Message: ErrInvalidConfig.Error()}
	}
	if err := atomicWrite(path, []byte(next)); err != nil {
		return Result{ID: t, OK: false, Message: "nodeshell: cannot write the MCP config file"}
	}
	return Result{ID: t, OK: true, Message: "Registered in " + path}
}

// ClipboardSnippet returns a JSON mcpServers block for manual configuration,
// built from the native spec (exe + --mcp).
func (s *Service) ClipboardSnippet() (string, error) {
	spec, err := s.launchSpec()
	if err != nil {
		return "", err
	}
	return clipboardSnippet(spec), nil
}

// readTextIfExists reads a file, mapping a missing file to an empty string.
func readTextIfExists(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// parseJSONObject parses a JSON object, rejecting invalid JSON and null.
func parseJSONObject(text string) (map[string]any, error) {
	var root map[string]any
	if err := json.Unmarshal([]byte(text), &root); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if root == nil {
		return nil, ErrInvalidConfig
	}
	return root, nil
}

// atomicWrite is a seam so tests can simulate a failed write.
var atomicWrite = func(path string, data []byte) error {
	return atomicfile.Write(path, data)
}
