// Package settings persists app settings in settings.json, replicating the
// Electron settings-store.ts defaults, validation, clamping and merge
// semantics so the Wails build reads the same file and produces the same
// values the frontend expects.
package settings

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"nodeshell/internal/apperror"
	"nodeshell/internal/atomicfile"
)

// AgentProvider is one named OpenAI-compatible endpoint. The API key lives in
// the OS keyring under this provider's id and never in this file.
type AgentProvider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BaseURL string   `json:"baseUrl"`
	Models  []string `json:"models"`
}

// AppSettings mirrors src/shared/types.ts AppSettings. The agent fields hold
// the non-secret half of the sidebar assistant's configuration; its API key
// lives in the OS keyring and never in this file.
type AppSettings struct {
	Language              string `json:"language"`
	TerminalFontFamily    string `json:"terminalFontFamily"`
	TerminalFontSize      int    `json:"terminalFontSize"`
	McpIdleTimeoutMinutes int    `json:"mcpIdleTimeoutMinutes"`
	McpMaxSessions        int    `json:"mcpMaxSessions"`
	ThemePreference       string `json:"themePreference"`
	// AgentBaseURL and AgentModel are the pre-multi-provider endpoint. They
	// are still read so an old settings.json can be migrated into
	// AgentProviders; they are not a write source for new configuration.
	AgentBaseURL string `json:"agentBaseUrl"`
	AgentModel   string `json:"agentModel"`
	// AgentProviders is the named-provider list. An empty slice is a valid
	// configured-empty state (the user deleted every provider).
	AgentProviders []AgentProvider `json:"agentProviders"`
	// AgentDefaultProviderID and AgentDefaultModel are the conversation
	// picker's starting selection for a new SSH tab.
	AgentDefaultProviderID string `json:"agentDefaultProviderId"`
	AgentDefaultModel      string `json:"agentDefaultModel"`
	// AgentProvidersPresent is true when agentProviders was in the file
	// (including an empty array). False means a pre-multi-provider file or a
	// missing file; Get then synthesises a legacy provider from the old
	// endpoint fields when the file was an object. Not persisted.
	AgentProvidersPresent bool `json:"-"`
	// PermissionPolicy is ask (default), allow, or deny. It gates sensitive
	// agent and MCP tools (commands, writes, uploads, downloads).
	PermissionPolicy string `json:"permissionPolicy"`
}

// Defaults matches DEFAULT_SETTINGS in settings-store.ts, plus the agent
// endpoint defaults (an OpenAI-compatible base URL including the provider's
// version prefix, following the OPENAI_BASE_URL convention).
var Defaults = AppSettings{
	Language:              "zh",
	TerminalFontFamily:    "Hack",
	TerminalFontSize:      14,
	McpIdleTimeoutMinutes: 10,
	McpMaxSessions:        8,
	ThemePreference:       "system",
	AgentBaseURL:          "https://api.openai.com/v1",
	AgentModel:            "gpt-4o-mini",
	PermissionPolicy:      "ask",
}

// Bounds from settings-store.ts.
const (
	FontSizeMin       = 10
	FontSizeMax       = 24
	McpIdleTimeoutMin = 1
	McpIdleTimeoutMax = 120
	McpMaxSessionsMin = 1
	McpMaxSessionsMax = 32
	// AgentFieldMaxLen bounds the agent endpoint strings. A value longer than
	// this is treated as garbage and falls back to the default rather than
	// being persisted and sent to an endpoint.
	AgentFieldMaxLen = 512
	// AgentNameMaxLen bounds a provider display name.
	AgentNameMaxLen = 64
	// AgentProviderMax is the number of named providers kept after normalize.
	AgentProviderMax = 16
	// AgentModelsMax is the number of model ids kept on one provider.
	AgentModelsMax = 32
	// LegacyProviderID is the stable id assigned when an old single-endpoint
	// settings file is synthesised into a provider list.
	LegacyProviderID = "legacy"
	// LegacyProviderName is the display name of that synthesised provider.
	LegacyProviderName = "Default"
)

// Patch mirrors the Partial<AppSettings> passed to settings.set; nil fields
// are left unchanged. Numeric fields are floats so fractional input is
// rounded exactly like the TS normalize functions.
type Patch struct {
	Language               *string          `json:"language"`
	TerminalFontFamily     *string          `json:"terminalFontFamily"`
	TerminalFontSize       *float64         `json:"terminalFontSize"`
	McpIdleTimeoutMinutes  *float64         `json:"mcpIdleTimeoutMinutes"`
	McpMaxSessions         *float64         `json:"mcpMaxSessions"`
	ThemePreference        *string          `json:"themePreference"`
	AgentBaseURL           *string          `json:"agentBaseUrl"`
	AgentModel             *string          `json:"agentModel"`
	AgentProviders         *[]AgentProvider `json:"agentProviders"`
	AgentDefaultProviderID *string          `json:"agentDefaultProviderId"`
	AgentDefaultModel      *string          `json:"agentDefaultModel"`
	PermissionPolicy       *string          `json:"permissionPolicy"`
}

// Error carries the stable config error code the frontend maps onto
// AppError.code (CONFIG_READ_FAILED, CONFIG_WRITE_FAILED).
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrorCode lets apperror.Format carry the stable code across IPC.
func (e *Error) ErrorCode() string { return e.Code }

// fileSettings is the on-disk shape. All value fields are raw JSON so values
// that would fail strict decoding (e.g. wrong-typed or non-numeric) still
// reach the Number()/typeof-style coercion the TS normalize* functions apply.
type fileSettings struct {
	Language               json.RawMessage `json:"language"`
	TerminalFontFamily     json.RawMessage `json:"terminalFontFamily"`
	TerminalFontSize       json.RawMessage `json:"terminalFontSize"`
	McpIdleTimeoutMinutes  json.RawMessage `json:"mcpIdleTimeoutMinutes"`
	McpMaxSessions         json.RawMessage `json:"mcpMaxSessions"`
	ThemePreference        json.RawMessage `json:"themePreference"`
	AgentBaseURL           json.RawMessage `json:"agentBaseUrl"`
	AgentModel             json.RawMessage `json:"agentModel"`
	AgentProviders         json.RawMessage `json:"agentProviders"`
	AgentDefaultProviderID json.RawMessage `json:"agentDefaultProviderId"`
	AgentDefaultModel      json.RawMessage `json:"agentDefaultModel"`
	PermissionPolicy       json.RawMessage `json:"permissionPolicy"`
	// rawWasObject is true when the file parsed as a JSON object. An array
	// top-level yields defaults and must not synthesise a legacy provider.
	rawWasObject bool
}

// Store reads and writes settings.json. Like the TS store it has no cache:
// every Get re-reads disk. Set is a read-modify-write of the file, so a mutex
// serialises Get/Set to prevent concurrent writers from overwriting each other
// or colliding on the atomic rename.
type Store struct {
	dir  string
	path string
	mu   sync.Mutex
}

// New returns a Store backed by <dir>/settings.json.
func New(dir string) *Store {
	return &Store{dir: dir, path: filepath.Join(dir, "settings.json")}
}

// Get returns normalized settings, or the defaults when the file is missing.
func (s *Store) Get() (AppSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get()
}

// get is Get without locking; Set calls it while already holding the mutex.
func (s *Store) get() (AppSettings, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Defaults, nil
		}
		return AppSettings{}, &Error{Code: apperror.ConfigReadFailed, Message: err.Error()}
	}
	file, err := parseFile(raw)
	if err != nil {
		return AppSettings{}, err
	}
	return normalizeSettings(file), nil
}

// Set merges patch over the current settings, normalizes, persists and
// returns the resulting settings.
func (s *Store) Set(patch Patch) (AppSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.get()
	if err != nil {
		return AppSettings{}, err
	}
	lang, fam, size, idle, max, theme :=
		current.Language, current.TerminalFontFamily,
		float64(current.TerminalFontSize), float64(current.McpIdleTimeoutMinutes),
		float64(current.McpMaxSessions), current.ThemePreference
	agentURL, agentModel, permPolicy := current.AgentBaseURL, current.AgentModel, current.PermissionPolicy
	providers := current.AgentProviders
	defaultID, defaultModel := current.AgentDefaultProviderID, current.AgentDefaultModel
	if patch.Language != nil {
		lang = *patch.Language
	}
	if patch.TerminalFontFamily != nil {
		fam = *patch.TerminalFontFamily
	}
	if patch.TerminalFontSize != nil {
		size = *patch.TerminalFontSize
	}
	if patch.McpIdleTimeoutMinutes != nil {
		idle = *patch.McpIdleTimeoutMinutes
	}
	if patch.McpMaxSessions != nil {
		max = *patch.McpMaxSessions
	}
	if patch.ThemePreference != nil {
		theme = *patch.ThemePreference
	}
	if patch.AgentBaseURL != nil {
		agentURL = *patch.AgentBaseURL
	}
	if patch.AgentModel != nil {
		agentModel = *patch.AgentModel
	}
	if patch.AgentProviders != nil {
		providers = normalizeProviderList(*patch.AgentProviders)
	}
	if patch.AgentDefaultProviderID != nil {
		defaultID = *patch.AgentDefaultProviderID
	}
	if patch.AgentDefaultModel != nil {
		defaultModel = *patch.AgentDefaultModel
	}
	if patch.PermissionPolicy != nil {
		permPolicy = *patch.PermissionPolicy
	}
	defaultID, defaultModel = normalizeAgentDefault(defaultID, defaultModel, providers)
	next := AppSettings{
		Language:               normalizeLanguage(lang),
		TerminalFontFamily:     normalizeTerminalFontFamily(fam),
		TerminalFontSize:       normalizeTerminalFontSize(size),
		McpIdleTimeoutMinutes:  normalizeMcpIdleTimeoutMinutes(idle),
		McpMaxSessions:         normalizeMcpMaxSessions(max),
		ThemePreference:        normalizeThemePreference(theme),
		AgentBaseURL:           normalizeAgentBaseURL(agentURL),
		AgentModel:             normalizeAgentModel(agentModel),
		AgentProviders:         providers,
		AgentDefaultProviderID: defaultID,
		AgentDefaultModel:      defaultModel,
		AgentProvidersPresent:  true,
		PermissionPolicy:       normalizePermissionPolicy(permPolicy),
	}
	if next.AgentProviders == nil {
		next.AgentProviders = []AgentProvider{}
	}
	if err := s.write(next); err != nil {
		return AppSettings{}, err
	}
	return next, nil
}

func (s *Store) write(data AppSettings) error {
	if err := atomicfile.WriteJSON(s.path, data); err != nil {
		return &Error{Code: apperror.ConfigWriteFailed, Message: err.Error()}
	}
	return nil
}

// parseFile mirrors the TS read: invalid JSON or a non-object top level is
// CONFIG_READ_FAILED; an array passes the TS typeof check and yields defaults.
func parseFile(raw []byte) (fileSettings, error) {
	tok := firstToken(raw)
	if tok == '[' {
		return fileSettings{}, nil
	}
	var file fileSettings
	if tok != '{' || json.Unmarshal(raw, &file) != nil {
		return fileSettings{}, &Error{Code: apperror.ConfigReadFailed, Message: "Settings file is corrupt"}
	}
	file.rawWasObject = true
	return file, nil
}

func firstToken(raw []byte) byte {
	for _, b := range raw {
		if !unicode.IsSpace(rune(b)) {
			return b
		}
	}
	return 0
}

func normalizeLanguage(value string) string {
	if value == "en" || value == "zh" {
		return value
	}
	return Defaults.Language
}

func normalizeTerminalFontFamily(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" {
		return trimmed
	}
	return Defaults.TerminalFontFamily
}

// normalizeTerminalFontSize clamps and rounds in the float domain (matching
// TS Math.min/Math.max/Math.round order) before converting to int, so huge
// values like 1e300 clamp to the max instead of overflowing int.
func normalizeTerminalFontSize(value float64) int {
	if !finite(value) {
		return Defaults.TerminalFontSize
	}
	return clampRound(FontSizeMin, FontSizeMax, value)
}

func normalizeMcpIdleTimeoutMinutes(value float64) int {
	if !finite(value) {
		return Defaults.McpIdleTimeoutMinutes
	}
	return clampRound(McpIdleTimeoutMin, McpIdleTimeoutMax, value)
}

func normalizeMcpMaxSessions(value float64) int {
	if !finite(value) {
		return Defaults.McpMaxSessions
	}
	return clampRound(McpMaxSessionsMin, McpMaxSessionsMax, value)
}

func normalizeThemePreference(value string) string {
	if value == "system" || value == "light" || value == "dark" {
		return value
	}
	return Defaults.ThemePreference
}

// normalizeAgentBaseURL keeps only an http(s) URL of sane length; anything
// else falls back to the default, so a corrupt or hostile settings file can
// never redirect the assistant to a non-HTTP scheme (file:, data:).
func normalizeAgentBaseURL(value string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	if trimmed == "" || len(trimmed) > AgentFieldMaxLen {
		return Defaults.AgentBaseURL
	}
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return Defaults.AgentBaseURL
	}
	return trimmed
}

func normalizeAgentModel(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > AgentFieldMaxLen {
		return Defaults.AgentModel
	}
	return trimmed
}

// parseAgentBaseURL is the provider-list variant of normalizeAgentBaseURL: a
// bad URL is rejected instead of rewritten to the OpenAI default, so one
// corrupt provider cannot silently redirect traffic.
func parseAgentBaseURL(value string) (string, bool) {
	trimmed := strings.TrimRight(strings.TrimSpace(value), "/")
	if trimmed == "" || len(trimmed) > AgentFieldMaxLen {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return "", false
	}
	return trimmed, true
}

func validProviderID(id string) bool {
	if id == "" || len(id) > 64 || strings.Contains(id, "::") {
		return false
	}
	for _, r := range id {
		if r <= 32 || r == 127 {
			return false
		}
	}
	return true
}

func normalizeModelID(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > AgentFieldMaxLen {
		return ""
	}
	return trimmed
}

func normalizeModelList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		id := normalizeModelID(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= AgentModelsMax {
			break
		}
	}
	return out
}

func normalizeOneProvider(id, name, baseURL string, models []string) (AgentProvider, bool) {
	id = strings.TrimSpace(id)
	if !validProviderID(id) {
		return AgentProvider{}, false
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > AgentNameMaxLen {
		return AgentProvider{}, false
	}
	url, ok := parseAgentBaseURL(baseURL)
	if !ok {
		return AgentProvider{}, false
	}
	return AgentProvider{
		ID:      id,
		Name:    name,
		BaseURL: url,
		Models:  normalizeModelList(models),
	}, true
}

func normalizeProviderList(in []AgentProvider) []AgentProvider {
	seen := make(map[string]struct{}, len(in))
	out := make([]AgentProvider, 0, len(in))
	for _, p := range in {
		next, ok := normalizeOneProvider(p.ID, p.Name, p.BaseURL, p.Models)
		if !ok {
			continue
		}
		if _, dup := seen[next.ID]; dup {
			continue
		}
		seen[next.ID] = struct{}{}
		out = append(out, next)
		if len(out) >= AgentProviderMax {
			break
		}
	}
	return out
}

func normalizeAgentProvidersRaw(raw json.RawMessage) (providers []AgentProvider, present bool) {
	if len(raw) == 0 {
		return nil, false
	}
	present = true
	raw = skipSpace(raw)
	if string(raw) == "null" {
		return []AgentProvider{}, true
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return []AgentProvider{}, true
	}
	parsed := make([]AgentProvider, 0, len(items))
	for _, item := range items {
		var row struct {
			ID      json.RawMessage `json:"id"`
			Name    json.RawMessage `json:"name"`
			BaseURL json.RawMessage `json:"baseUrl"`
			Models  json.RawMessage `json:"models"`
		}
		if json.Unmarshal(item, &row) != nil {
			continue
		}
		models := coerceStringSlice(row.Models)
		p, ok := normalizeOneProvider(coerceString(row.ID), coerceString(row.Name), coerceString(row.BaseURL), models)
		if !ok {
			continue
		}
		parsed = append(parsed, p)
	}
	return normalizeProviderList(parsed), true
}

func coerceStringSlice(raw json.RawMessage) []string {
	raw = skipSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var elems []json.RawMessage
	if json.Unmarshal(raw, &elems) != nil {
		return nil
	}
	out := make([]string, 0, len(elems))
	for _, el := range elems {
		if s := coerceString(el); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func normalizeAgentDefault(id, model string, providers []AgentProvider) (string, string) {
	id = strings.TrimSpace(id)
	model = strings.TrimSpace(model)
	for _, p := range providers {
		if p.ID != id {
			continue
		}
		for _, m := range p.Models {
			if m == model {
				return p.ID, m
			}
		}
		if len(p.Models) > 0 {
			return p.ID, p.Models[0]
		}
		return "", ""
	}
	if len(providers) > 0 && len(providers[0].Models) > 0 {
		return providers[0].ID, providers[0].Models[0]
	}
	return "", ""
}

// NormalizeProvider validates one provider the way Get/Set do, so the App
// upsert path cannot persist a name, URL or id that the store would drop.
func NormalizeProvider(p AgentProvider) (AgentProvider, bool) {
	return normalizeOneProvider(p.ID, p.Name, p.BaseURL, p.Models)
}

func synthesiseLegacyProvider(url, model string) AgentProvider {
	return AgentProvider{
		ID:      LegacyProviderID,
		Name:    LegacyProviderName,
		BaseURL: normalizeAgentBaseURL(url),
		Models:  []string{normalizeAgentModel(model)},
	}
}

// normalizePermissionPolicy keeps only ask/allow/deny; anything else falls
// back to ask so a corrupt file never silently auto-allows tool calls.
func normalizePermissionPolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ask", "allow", "deny":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return Defaults.PermissionPolicy
	}
}

// coerceString resolves a string field the way the TS normalize* functions
// do: a JSON string wins; a missing field, null, number or boolean is not a
// string, and each normalizer maps it to its default.
func coerceString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// coerceNumber resolves a numeric field the way TS Number(value) does: JSON
// numbers, parseable numeric strings (including 0x/0b/0o prefixes, Infinity),
// booleans, null, and single-element arrays coerce; anything else — objects,
// multi-element arrays, absent fields — is not-a-number, which the normalizers
// turn into the default. Minimal by design; no general JS engine.
func coerceNumber(raw json.RawMessage) float64 {
	raw = skipSpace(raw)
	if len(raw) == 0 {
		return math.NaN() // absent field: TS Number(undefined)
	}
	switch raw[0] {
	case '{':
		return math.NaN()
	case '[':
		var elems []json.RawMessage
		if json.Unmarshal(raw, &elems) != nil {
			return math.NaN()
		}
		switch len(elems) {
		case 0:
			return 0 // Number([]) = 0
		case 1:
			return coerceNumber(elems[0])
		default:
			return math.NaN() // Number([a, b]) = NaN
		}
	case '"':
		return jsStringToNumber(raw)
	case 't':
		return 1 // Number(true)
	case 'f':
		return 0 // Number(false)
	case 'n':
		return 0 // Number(null)
	default:
		var f float64
		if json.Unmarshal(raw, &f) == nil {
			return f
		}
		return math.NaN()
	}
}

// jsStringToNumber replicates JS ToNumber on a string: trim whitespace (empty
// -> 0), then Infinity, 0x/0b/0o prefixed literals, or a decimal float.
func jsStringToNumber(raw json.RawMessage) float64 {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return math.NaN()
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if s == "Infinity" || s == "+Infinity" {
		return math.Inf(1)
	}
	if s == "-Infinity" {
		return math.Inf(-1)
	}
	if len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X") {
		return bigIntToFloat(s[2:], 16)
	}
	if len(s) > 2 && (s[:2] == "0b" || s[:2] == "0B") {
		return bigIntToFloat(s[2:], 2)
	}
	if len(s) > 2 && (s[:2] == "0o" || s[:2] == "0O") {
		return bigIntToFloat(s[2:], 8)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// bigIntToFloat parses an integer literal in the given base with arbitrary
// precision and converts to float64, matching JS's approximate result for
// very long literals and exact values for short ones.
func bigIntToFloat(digits string, base int) float64 {
	n, ok := new(big.Int).SetString(digits, base)
	if !ok {
		return math.NaN()
	}
	f, _ := new(big.Float).SetInt(n).Float64()
	return f
}

// skipSpace returns raw with leading JSON whitespace removed.
func skipSpace(raw []byte) []byte {
	for len(raw) > 0 && unicode.IsSpace(rune(raw[0])) {
		raw = raw[1:]
	}
	return raw
}

func finite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

// clampRound clamps v into [min, max] and rounds, in float64, then converts
// to int — equivalent to TS Math.min(max, Math.max(min, Math.round(v))) for
// integer bounds, and safe for out-of-range floats.
func clampRound(min, max int, v float64) int {
	return int(math.Round(math.Min(float64(max), math.Max(float64(min), v))))
}

func normalizeSettings(raw fileSettings) AppSettings {
	url := normalizeAgentBaseURL(coerceString(raw.AgentBaseURL))
	model := normalizeAgentModel(coerceString(raw.AgentModel))
	providers, present := normalizeAgentProvidersRaw(raw.AgentProviders)
	if !present && raw.rawWasObject {
		providers = []AgentProvider{synthesiseLegacyProvider(url, model)}
	}
	if present && providers == nil {
		providers = []AgentProvider{}
	}
	defaultID, defaultModel := normalizeAgentDefault(
		coerceString(raw.AgentDefaultProviderID),
		coerceString(raw.AgentDefaultModel),
		providers,
	)
	return AppSettings{
		Language:               normalizeLanguage(coerceString(raw.Language)),
		TerminalFontFamily:     normalizeTerminalFontFamily(coerceString(raw.TerminalFontFamily)),
		TerminalFontSize:       normalizeTerminalFontSize(coerceNumber(raw.TerminalFontSize)),
		McpIdleTimeoutMinutes:  normalizeMcpIdleTimeoutMinutes(coerceNumber(raw.McpIdleTimeoutMinutes)),
		McpMaxSessions:         normalizeMcpMaxSessions(coerceNumber(raw.McpMaxSessions)),
		ThemePreference:        normalizeThemePreference(coerceString(raw.ThemePreference)),
		AgentBaseURL:           url,
		AgentModel:             model,
		AgentProviders:         providers,
		AgentDefaultProviderID: defaultID,
		AgentDefaultModel:      defaultModel,
		AgentProvidersPresent:  present,
		PermissionPolicy:       normalizePermissionPolicy(coerceString(raw.PermissionPolicy)),
	}
}
