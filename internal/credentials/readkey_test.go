package credentials

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestReadPrivateKeyInsideHome(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".ssh", "id_rsa"), "PRIVATE KEY CONTENT")
	got, err := ReadPrivateKeyFile(filepath.Join(home, ".ssh", "id_rsa"), home)
	if err != nil {
		t.Fatalf("ReadPrivateKeyFile: %v", err)
	}
	if got != "PRIVATE KEY CONTENT" {
		t.Fatalf("content = %q", got)
	}
}

func TestReadPrivateKeyRelativePathRejected(t *testing.T) {
	// Relative paths resolve against the process working directory (same as
	// Electron's path.resolve) — never against home — so a relative path that
	// escapes the boundary is rejected outright.
	home := t.TempDir()
	if _, err := ReadPrivateKeyFile("keys/id_ed25519", home); err == nil {
		t.Fatal("relative path resolving outside home must be rejected")
	}
}

func TestReadPrivateKeyOutsideHomeRejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "key"), "SECRET")
	_, err := ReadPrivateKeyFile(filepath.Join(outside, "key"), home)
	if err == nil {
		t.Fatal("path outside home must be rejected")
	}
	if err.Error() != ErrPathOutsideHome.Error() {
		t.Fatalf("error = %q, want the outside-home message", err.Error())
	}
}

func TestReadPrivateKeySymlinkEscapeRejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "target-key"), "SECRET OUTSIDE")
	link := filepath.Join(home, "escaped-key")
	if err := os.Symlink(filepath.Join(outside, "target-key"), link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable (no privilege): %v", err)
		}
		t.Fatalf("symlink: %v", err)
	}
	_, err := ReadPrivateKeyFile(link, home)
	if err == nil {
		t.Fatal("symlink escaping home must be rejected")
	}
	if err.Error() != ErrPathOutsideHome.Error() {
		t.Fatalf("error = %q, want the outside-home message", err.Error())
	}
}

func TestReadPrivateKeySymlinkInsideHomeAllowed(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "real-key"), "CONTENT")
	link := filepath.Join(home, "alias-key")
	if err := os.Symlink(filepath.Join(home, "real-key"), link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable (no privilege): %v", err)
		}
		t.Fatalf("symlink: %v", err)
	}
	got, err := ReadPrivateKeyFile(link, home)
	if err != nil {
		t.Fatalf("symlink staying inside home must be allowed: %v", err)
	}
	if got != "CONTENT" {
		t.Fatalf("content = %q", got)
	}
}

func TestReadPrivateKeyMissingFileErrors(t *testing.T) {
	home := t.TempDir()
	_, err := ReadPrivateKeyFile(filepath.Join(home, "no-such-key"), home)
	if err == nil {
		t.Fatal("missing file must error")
	}
}

func TestReadPrivateKeyEmptyPathErrors(t *testing.T) {
	home := t.TempDir()
	if _, err := ReadPrivateKeyFile("", home); err == nil {
		t.Fatal("empty path must error")
	}
}

func TestReadPrivateKeyEmptyHomeRejects(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "key"), "CONTENT")
	if _, err := ReadPrivateKeyFile(filepath.Join(home, "key"), ""); err == nil {
		t.Fatal("empty home must reject every path (never an open boundary)")
	}
}

func TestNewHomeReaderSeam(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "key"), "SEAM")
	reader := NewHomeReader(home)
	got, err := reader(filepath.Join(home, "key"))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	if got != "SEAM" {
		t.Fatalf("content = %q", got)
	}
}
