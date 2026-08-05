package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReplaceMissingTempPreservesTarget: when the temp file is gone, Replace
// must fail and leave the existing target untouched (the download path never
// publishes a broken target).
func TestReplaceMissingTempPreservesTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	missing := filepath.Join(dir, "no-such-temp")
	if err := Replace(missing, target); err == nil {
		t.Fatal("Replace of a missing temp must error")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("target lost: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("target content = %q, want %q (preserved)", got, "old")
	}
}
