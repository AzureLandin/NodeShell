package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nodeshell/internal/agent"
	"nodeshell/internal/credentials"
	"nodeshell/internal/settings"
)

// The agent bindings are the App's half of the assistant contract: the API key
// goes to the OS keyring and never to settings.json, the renderer only ever
// learns whether a key exists, and the endpoint fields are normalised by the
// settings store. The agent loop itself is covered in internal/agent.

// newAgentApp returns an App with an in-memory keyring in place of the OS one.
func newAgentApp(t *testing.T) (*App, *fakeKeyring, string) {
	t.Helper()
	keys := &fakeKeyring{}
	a, dir := testAppWith(t, &fakeKeyring{}, nil)
	a.agentKeys = keys
	return a, keys, dir
}

func TestAgentStatusReportsDefaultsAndNoKey(t *testing.T) {
	a, _, _ := newAgentApp(t)

	got, err := a.AgentStatus()
	if err != nil {
		t.Fatalf("AgentStatus: %v", err)
	}
	if got.Configured {
		t.Fatal("a fresh install must not report a configured agent")
	}
	if got.BaseURL != settings.Defaults.AgentBaseURL || got.Model != settings.Defaults.AgentModel {
		t.Fatalf("status = %+v, want the settings defaults", got)
	}
}

// The key lands in the keyring under the fixed agent account, and the settings
// file keeps only the endpoint fields.
func TestAgentSetConfigStoresKeyInKeyringNotSettings(t *testing.T) {
	a, keys, dir := newAgentApp(t)
	url, model, key := "https://api.deepseek.com/v1/", "deepseek-chat", "sk-secret-value"

	got, err := a.AgentSetConfig(AgentConfigPatch{BaseURL: &url, Model: &model, APIKey: &key})
	if err != nil {
		t.Fatalf("AgentSetConfig: %v", err)
	}
	if !got.Configured {
		t.Fatal("status must report configured after a key is stored")
	}
	if got.BaseURL != "https://api.deepseek.com/v1" || got.Model != model {
		t.Fatalf("status = %+v, want the normalised endpoint", got)
	}

	stored := keys.snapshot()[credentials.ServiceName+":"+agentKeyAccount]
	if stored != key {
		t.Fatalf("keyring entry = %q, want the API key under the agent account", stored)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), key) {
		t.Fatalf("the API key was persisted to settings.json: %s", raw)
	}
	if !strings.Contains(string(raw), "deepseek-chat") {
		t.Fatalf("the model was not persisted: %s", raw)
	}
}

// The status payload is the only channel to the renderer, and it must never
// carry the key itself.
func TestAgentStatusNeverReturnsTheKey(t *testing.T) {
	a, keys, _ := newAgentApp(t)
	if err := keys.Set(credentials.ServiceName, agentKeyAccount, "sk-secret-value"); err != nil {
		t.Fatal(err)
	}

	got, err := a.AgentStatus()
	if err != nil {
		t.Fatalf("AgentStatus: %v", err)
	}
	if !got.Configured {
		t.Fatal("a stored key must report configured")
	}
	if strings.Contains(got.BaseURL+got.Model, "sk-secret") {
		t.Fatalf("status leaked the key: %+v", got)
	}
}

// An empty key clears the entry; a patch that omits the key leaves it alone,
// so saving the endpoint does not silently log the user out.
func TestAgentSetConfigKeyClearAndPreserve(t *testing.T) {
	a, keys, _ := newAgentApp(t)
	key := "sk-secret-value"
	if _, err := a.AgentSetConfig(AgentConfigPatch{APIKey: &key}); err != nil {
		t.Fatalf("store key: %v", err)
	}

	model := "gpt-4o"
	got, err := a.AgentSetConfig(AgentConfigPatch{Model: &model})
	if err != nil {
		t.Fatalf("patch without key: %v", err)
	}
	if !got.Configured {
		t.Fatal("a patch without an API key must keep the stored key")
	}

	empty := ""
	got, err = a.AgentSetConfig(AgentConfigPatch{APIKey: &empty})
	if err != nil {
		t.Fatalf("clear key: %v", err)
	}
	if got.Configured {
		t.Fatal("an empty key must clear the stored entry")
	}
	if _, ok := keys.snapshot()[credentials.ServiceName+":"+agentKeyAccount]; ok {
		t.Fatal("the keyring entry survived the clear")
	}
	// Clearing an absent key is idempotent, not an error.
	if _, err := a.AgentSetConfig(AgentConfigPatch{APIKey: &empty}); err != nil {
		t.Fatalf("clearing an absent key must not fail: %v", err)
	}
}

func TestAgentSetConfigRejectsOversizedKey(t *testing.T) {
	a, keys, _ := newAgentApp(t)
	huge := strings.Repeat("k", agentKeyMaxLen+1)

	if _, err := a.AgentSetConfig(AgentConfigPatch{APIKey: &huge}); err == nil {
		t.Fatal("an oversized key must be rejected")
	}
	if len(keys.snapshot()) != 0 {
		t.Fatal("a rejected key must not reach the keyring")
	}
}

// The config loader is what the agent service calls per prompt: without a key
// it must reject with the not-configured sentinel rather than build a request.
func TestAgentConfigLoaderRequiresKey(t *testing.T) {
	a, keys, _ := newAgentApp(t)

	if _, err := a.agentConfig(); !errors.Is(err, agent.ErrNotConfigured) {
		t.Fatalf("agentConfig error = %v, want ErrNotConfigured", err)
	}
	if err := keys.Set(credentials.ServiceName, agentKeyAccount, "sk-secret-value"); err != nil {
		t.Fatal(err)
	}
	cfg, err := a.agentConfig()
	if err != nil {
		t.Fatalf("agentConfig: %v", err)
	}
	if cfg.APIKey != "sk-secret-value" || cfg.Model != settings.Defaults.AgentModel {
		t.Fatalf("config = %+v", cfg)
	}
}

// A keyring read failure is a coded, secret-free error, not a panic and not a
// silent "not configured".
func TestAgentStatusSurfacesKeyringFailure(t *testing.T) {
	a, keys, _ := newAgentApp(t)
	keys.getErr = errors.New("keyring unavailable")

	if _, err := a.AgentStatus(); err == nil {
		t.Fatal("a keyring read failure must surface")
	} else if !strings.Contains(err.Error(), "agent API key") {
		t.Fatalf("error = %v, want the agent key read message", err)
	}
}

// A prompt for a session that does not exist is rejected by the service, and
// the bindings work only after startup wired the agent.
func TestAgentBindingsRequireWiring(t *testing.T) {
	bare := &App{}
	if err := bare.AgentPrompt("s1", "host", "hello"); !errors.Is(err, errBackendNotInitialised) {
		t.Fatalf("AgentPrompt before startup = %v, want errBackendNotInitialised", err)
	}
	if err := bare.AgentAbort("s1"); !errors.Is(err, errBackendNotInitialised) {
		t.Fatalf("AgentAbort before startup = %v, want errBackendNotInitialised", err)
	}
	if err := bare.AgentClear("s1"); !errors.Is(err, errBackendNotInitialised) {
		t.Fatalf("AgentClear before startup = %v, want errBackendNotInitialised", err)
	}
	if err := bare.PermissionDecide("x", "allow"); !errors.Is(err, errBackendNotInitialised) {
		t.Fatalf("PermissionDecide before startup = %v, want errBackendNotInitialised", err)
	}
	if _, err := bare.AgentStatus(); !errors.Is(err, errBackendNotInitialised) {
		t.Fatalf("AgentStatus before startup = %v, want errBackendNotInitialised", err)
	}

	a, _, _ := newAgentApp(t)
	if a.agent == nil {
		t.Fatal("NewAppWithServices must wire the agent service")
	}
	// Unconfigured, so the prompt is rejected before any request is attempted.
	if err := a.AgentPrompt("s1", "host", "hello"); err == nil {
		t.Fatal("an unconfigured agent must reject the prompt")
	}
}
