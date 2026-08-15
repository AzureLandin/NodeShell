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

// The agent bindings are the App's half of the assistant contract: API keys
// go to the OS keyring under agent-api-key:<providerId> and never to
// settings.json, the renderer only ever learns whether a key exists, and the
// provider list is normalised by the settings store.

func newAgentApp(t *testing.T) (*App, *fakeKeyring, string) {
	t.Helper()
	keys := &fakeKeyring{}
	a, dir := testAppWith(t, &fakeKeyring{}, nil)
	a.agentKeys = keys
	return a, keys, dir
}

func TestAgentStatusReportsEmptyOnFreshInstall(t *testing.T) {
	a, _, _ := newAgentApp(t)

	got, err := a.AgentStatus()
	if err != nil {
		t.Fatalf("AgentStatus: %v", err)
	}
	if got.Configured {
		t.Fatal("a fresh install must not report a configured agent")
	}
	if len(got.Providers) != 0 {
		t.Fatalf("providers = %+v, want none on a missing settings file", got.Providers)
	}
}

func TestAgentUpsertStoresKeyInKeyringNotSettings(t *testing.T) {
	a, keys, dir := newAgentApp(t)

	got, err := a.AgentUpsertProvider(AgentProviderInput{
		Name:    "DeepSeek",
		BaseURL: "https://api.deepseek.com/v1/",
		Models:  []string{"deepseek-chat"},
	})
	if err != nil {
		t.Fatalf("AgentUpsertProvider: %v", err)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("providers = %+v, want one", got.Providers)
	}
	p := got.Providers[0]
	if p.BaseURL != "https://api.deepseek.com/v1" || p.Models[0] != "deepseek-chat" {
		t.Fatalf("status = %+v, want the normalised endpoint", p)
	}
	if p.HasKey {
		t.Fatal("upsert must not invent a key")
	}

	key := "sk-secret-value"
	got, err = a.AgentSetProviderKey(p.ID, key)
	if err != nil {
		t.Fatalf("AgentSetProviderKey: %v", err)
	}
	if !got.Configured || !got.Providers[0].HasKey {
		t.Fatal("status must report configured after a key is stored")
	}

	stored := keys.snapshot()[credentials.ServiceName+":"+agentKeyAccountFor(p.ID)]
	if stored != key {
		t.Fatalf("keyring entry = %q, want the API key under the provider account", stored)
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

func TestAgentStatusNeverReturnsTheKey(t *testing.T) {
	a, keys, _ := newAgentApp(t)
	got, err := a.AgentUpsertProvider(AgentProviderInput{
		Name: "P", BaseURL: "https://x.test/v1", Models: []string{"m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := got.Providers[0].ID
	if err := keys.Set(credentials.ServiceName, agentKeyAccountFor(id), "sk-secret-value"); err != nil {
		t.Fatal(err)
	}

	got, err = a.AgentStatus()
	if err != nil {
		t.Fatalf("AgentStatus: %v", err)
	}
	if !got.Configured {
		t.Fatal("a stored key must report configured")
	}
	if strings.Contains(got.Providers[0].Name+got.Providers[0].BaseURL+got.DefaultModel, "sk-secret") {
		t.Fatalf("status leaked the key: %+v", got)
	}
}

func TestAgentSetProviderKeyClearAndPreserve(t *testing.T) {
	a, keys, _ := newAgentApp(t)
	got, err := a.AgentUpsertProvider(AgentProviderInput{
		Name: "P", BaseURL: "https://x.test/v1", Models: []string{"m", "n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := got.Providers[0].ID
	if _, err := a.AgentSetProviderKey(id, "sk-secret-value"); err != nil {
		t.Fatalf("store key: %v", err)
	}

	got, err = a.AgentUpsertProvider(AgentProviderInput{
		ID: id, Name: "P", BaseURL: "https://x.test/v1", Models: []string{"m", "n", "o"},
	})
	if err != nil {
		t.Fatalf("upsert without key: %v", err)
	}
	if !got.Configured || !got.Providers[0].HasKey {
		t.Fatal("updating models must keep the stored key")
	}

	got, err = a.AgentSetProviderKey(id, "")
	if err != nil {
		t.Fatalf("clear key: %v", err)
	}
	if got.Configured {
		t.Fatal("an empty key must clear the stored entry")
	}
	if _, ok := keys.snapshot()[credentials.ServiceName+":"+agentKeyAccountFor(id)]; ok {
		t.Fatal("the keyring entry survived the clear")
	}
	if _, err := a.AgentSetProviderKey(id, ""); err != nil {
		t.Fatalf("clearing an absent key must not fail: %v", err)
	}
}

func TestAgentSetProviderKeyRejectsOversizedKey(t *testing.T) {
	a, keys, _ := newAgentApp(t)
	got, err := a.AgentUpsertProvider(AgentProviderInput{
		Name: "P", BaseURL: "https://x.test/v1", Models: []string{"m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("k", agentKeyMaxLen+1)
	if _, err := a.AgentSetProviderKey(got.Providers[0].ID, huge); err == nil {
		t.Fatal("an oversized key must be rejected")
	}
	if len(keys.snapshot()) != 0 {
		t.Fatal("a rejected key must not reach the keyring")
	}
}

func TestAgentConfigForRequiresKeyAndKnownModel(t *testing.T) {
	a, keys, _ := newAgentApp(t)

	if _, err := a.agentConfigFor("p", "m"); err == nil {
		t.Fatal("missing provider must reject")
	}

	got, err := a.AgentUpsertProvider(AgentProviderInput{
		Name: "P", BaseURL: "https://x.test/v1", Models: []string{"m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := got.Providers[0].ID
	if _, err := a.agentConfigFor(id, "m"); !errors.Is(err, agent.ErrNotConfigured) {
		t.Fatalf("agentConfigFor error = %v, want ErrNotConfigured", err)
	}
	if err := keys.Set(credentials.ServiceName, agentKeyAccountFor(id), "sk-secret-value"); err != nil {
		t.Fatal(err)
	}
	cfg, err := a.agentConfigFor(id, "m")
	if err != nil {
		t.Fatalf("agentConfigFor: %v", err)
	}
	if cfg.APIKey != "sk-secret-value" || cfg.Model != "m" || cfg.BaseURL != "https://x.test/v1" {
		t.Fatalf("config = %+v", cfg)
	}
	if _, err := a.agentConfigFor(id, "other"); err == nil {
		t.Fatal("a model not on the provider must be rejected")
	}
}

func TestAgentLegacyMigrationCopiesKeyAndPersistsProvider(t *testing.T) {
	a, keys, dir := newAgentApp(t)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(
		`{"agentBaseUrl":"https://api.deepseek.com/v1","agentModel":"deepseek-chat"}`,
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := keys.Set(credentials.ServiceName, agentKeyAccount, "sk-legacy"); err != nil {
		t.Fatal(err)
	}

	got, err := a.AgentStatus()
	if err != nil {
		t.Fatalf("AgentStatus: %v", err)
	}
	if !got.Configured || len(got.Providers) != 1 {
		t.Fatalf("status = %+v, want one configured legacy provider", got)
	}
	p := got.Providers[0]
	if p.ID != settings.LegacyProviderID || p.BaseURL != "https://api.deepseek.com/v1" || p.Models[0] != "deepseek-chat" {
		t.Fatalf("legacy provider = %+v", p)
	}
	if keys.snapshot()[credentials.ServiceName+":"+agentKeyAccountFor(settings.LegacyProviderID)] != "sk-legacy" {
		t.Fatal("the legacy key was not copied onto the provider account")
	}
	if _, ok := keys.snapshot()[credentials.ServiceName+":"+agentKeyAccount]; ok {
		t.Fatal("the old unprefixed keyring account must be removed after copy")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"id": "legacy"`) && !strings.Contains(string(raw), `"id":"legacy"`) {
		t.Fatalf("legacy provider was not persisted: %s", raw)
	}
}

func TestAgentDeleteProviderRemovesKey(t *testing.T) {
	a, keys, _ := newAgentApp(t)
	got, err := a.AgentUpsertProvider(AgentProviderInput{
		Name: "P", BaseURL: "https://x.test/v1", Models: []string{"m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := got.Providers[0].ID
	if _, err := a.AgentSetProviderKey(id, "sk"); err != nil {
		t.Fatal(err)
	}
	got, err = a.AgentDeleteProvider(id)
	if err != nil {
		t.Fatalf("AgentDeleteProvider: %v", err)
	}
	if len(got.Providers) != 0 {
		t.Fatalf("providers = %+v, want none", got.Providers)
	}
	if _, ok := keys.snapshot()[credentials.ServiceName+":"+agentKeyAccountFor(id)]; ok {
		t.Fatal("deleting a provider must drop its key")
	}
}

func TestAgentSetDefaultModel(t *testing.T) {
	a, _, _ := newAgentApp(t)
	got, err := a.AgentUpsertProvider(AgentProviderInput{
		Name: "P", BaseURL: "https://x.test/v1", Models: []string{"a", "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := got.Providers[0].ID
	got, err = a.AgentSetDefaultModel(id, "b")
	if err != nil {
		t.Fatalf("AgentSetDefaultModel: %v", err)
	}
	if got.DefaultProviderID != id || got.DefaultModel != "b" {
		t.Fatalf("default = (%q, %q)", got.DefaultProviderID, got.DefaultModel)
	}
	if _, err := a.AgentSetDefaultModel(id, "missing"); err == nil {
		t.Fatal("a model not on the provider must be rejected")
	}
}

func TestAgentStatusSurfacesKeyringFailure(t *testing.T) {
	a, keys, _ := newAgentApp(t)
	keys.getErr = errors.New("keyring unavailable")

	if _, err := a.AgentStatus(); err == nil {
		t.Fatal("a keyring read failure must surface")
	} else if !strings.Contains(err.Error(), "agent API key") {
		t.Fatalf("error = %v, want the agent key read message", err)
	}
}

func TestAgentBindingsRequireWiring(t *testing.T) {
	bare := &App{}
	if err := bare.AgentPrompt("s1", "host", "hello", "p", "m"); !errors.Is(err, errBackendNotInitialised) {
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
	if err := a.AgentPrompt("s1", "host", "hello", "p", "m"); err == nil {
		t.Fatal("an unconfigured agent must reject the prompt")
	}
}
