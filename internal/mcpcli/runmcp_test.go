package mcpcli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nodeshell/internal/credentials"
)

// nopCredBackend satisfies credentials.Backend without touching the OS
// keyring; the RunMCP smoke test only constructs the store, never reads it.
type nopCredBackend struct{}

func (nopCredBackend) Set(string, string, string) error   { return nil }
func (nopCredBackend) Get(string, string) (string, error) { return "", credentials.ErrNotFound }
func (nopCredBackend) Delete(string, string) error        { return nil }
func (nopCredBackend) Available() bool                    { return true }

// TestRunMCPHandshake drives the production --mcp wiring end to end over
// in-memory streams: data dir, host store, settings policy, session manager,
// SFTP service and reaper, then the stdio handshake (initialize, tools/list,
// tools/call) and a clean EOF shutdown.
func TestRunMCPHandshake(t *testing.T) {
	dir := t.TempDir()
	hostsJSON := `{"hosts":[{"id":"h1","name":"lab","host":"192.0.2.1","port":22,"username":"user","authMethod":"password","credentialsSaved":false}]}`
	if err := os.WriteFile(filepath.Join(dir, "hosts.json"), []byte(hostsJSON), 0o600); err != nil {
		t.Fatalf("write hosts fixture: %v", err)
	}

	oldDir, oldHome, oldCreds := resolveDataDir, userHomeDir, newCredentials
	t.Cleanup(func() {
		resolveDataDir, userHomeDir, newCredentials = oldDir, oldHome, oldCreds
	})
	resolveDataDir = func() (string, error) { return dir, nil }
	userHomeDir = func() (string, error) { return t.TempDir(), nil }
	newCredentials = func() *credentials.Store { return credentials.New(nopCredBackend{}) }

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","clientInfo":{"name":"smoke","version":"1"},"capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_hosts","arguments":{}}}`,
	}, "\n") + "\n"

	// Keep stdin open until all responses arrive. strings.NewReader hits EOF as
	// soon as the last byte is read, which races Serve's EOF→cancel-in-flight
	// path and can drop the final tools/call response.
	var out, errOut bytes.Buffer
	inR, inW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- RunMCP(context.Background(), inR, &out, &errOut) }()
	if _, err := io.WriteString(inW, input); err != nil {
		_ = inW.Close()
		t.Fatalf("write input: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countStdoutJSONLines(out.String()) >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunMCP: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunMCP did not return after EOF")
	}
	if errOut.String() != "" {
		t.Fatalf("stderr = %q, want clean", errOut.String())
	}
	msgs := decodeLines(t, out.String())
	if len(msgs) != 3 {
		t.Fatalf("responses = %d, want 3 (initialize, tools/list, tools/call)", len(msgs))
	}
	if r := resultOf(t, msgs[0]); r["protocolVersion"] != ProtocolVersion {
		t.Fatalf("initialize protocolVersion = %v", r["protocolVersion"])
	}
	tools := resultOf(t, msgs[1])["tools"].([]any)
	if len(tools) != 10 {
		t.Fatalf("tools count = %d, want 10", len(tools))
	}
	text := toolText(t, resultOf(t, msgs[2]))
	var hosts []map[string]any
	if err := json.Unmarshal([]byte(text), &hosts); err != nil {
		t.Fatalf("list_hosts content is not JSON: %v (%q)", err, text)
	}
	if len(hosts) != 1 || hosts[0]["id"] != "h1" {
		t.Fatalf("hosts = %v", hosts)
	}
}
