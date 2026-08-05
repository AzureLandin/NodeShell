package atomicfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The failure-protection contract: a write that cannot be renamed must leave
// the previous valid file intact and must not leave a temp file behind.

func TestWriteJSONCreatesParentDirAndFile(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "sub", "data.json")
	if err := WriteJSON(path, map[string]any{"hosts": []any{}}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("written JSON invalid: %v (%q)", err, raw)
	}
	if !strings.HasPrefix(string(raw), "{\n") {
		t.Fatalf("expected 2-space-indented JSON, got %q", raw)
	}
}

func TestWriteJSONReplacesExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	if err := os.WriteFile(path, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(path, map[string]any{"v": 1}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	var got map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("written JSON invalid: %v", err)
	}
	if got["v"].(float64) != 1 {
		t.Fatalf("content = %v, want v=1", got)
	}
}

func TestWriteJSONRenameFailurePreservesTargetAndCleansTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}

	orig := rename
	rename = func(oldpath, newpath string) error { return os.ErrPermission }
	t.Cleanup(func() { rename = orig })

	if err := WriteJSON(path, map[string]any{"v": 2}); err == nil {
		t.Fatal("expected an error when rename fails")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "OLD" {
		t.Fatalf("target modified by failed write: %q", raw)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temp file leaked: %v", entries)
	}
}

// A transient rename failure (Windows antivirus/search indexer hold) must be
// retried rather than surfaced to the caller as a write error.
func TestWriteJSONRetriesTransientRenameFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	failures := 0
	orig := rename
	rename = func(oldpath, newpath string) error {
		failures++
		if failures <= 2 {
			return os.ErrPermission
		}
		return orig(oldpath, newpath)
	}
	t.Cleanup(func() { rename = orig })

	if err := WriteJSON(path, map[string]any{"v": 1}); err != nil {
		t.Fatalf("WriteJSON after transient rename failures: %v", err)
	}
	if failures != 3 {
		t.Fatalf("rename called %d times, want 3 (2 failures + success)", failures)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"v": 1`) {
		t.Fatalf("content = %q, want v:1", raw)
	}
}
