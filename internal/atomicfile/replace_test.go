package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReplaceOverExistingTarget: replacing an existing target must succeed
// and publish the new content (the download path relies on this).
func TestReplaceOverExistingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	tmp, err := os.CreateTemp(dir, ".nodeshell-download-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString("new"); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		t.Fatalf("sync tmp: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close tmp: %v", err)
	}
	if err := Replace(tmpName, target); err != nil {
		t.Fatalf("Replace over existing target: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("target content = %q, want %q", got, "new")
	}
}
