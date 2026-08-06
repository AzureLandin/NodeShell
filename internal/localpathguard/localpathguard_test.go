package localpathguard

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// sameResolvedPath reports whether got and want name the same filesystem
// location. On Windows, t.TempDir() may yield an 8.3 short path (RUNNER~1)
// while filepath.EvalSymlinks expands it to the long form (runneradmin); the
// guard always returns the EvalSymlinks form, so byte-equality against the
// input path is not portable.
func sameResolvedPath(got, want string) bool {
	if got == want {
		return true
	}
	g, errG := filepath.EvalSymlinks(filepath.Clean(got))
	w, errW := filepath.EvalSymlinks(filepath.Clean(want))
	if errG == nil && errW == nil {
		if runtime.GOOS == "windows" {
			return strings.EqualFold(g, w)
		}
		return g == w
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(got), filepath.Clean(want))
	}
	return false
}

func assertSameResolvedPath(t *testing.T, got, want string) {
	t.Helper()
	if !sameResolvedPath(got, want) {
		t.Fatalf("resolved = %q, want %q", got, want)
	}
}

func makeSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable (no privilege): %v", err)
		}
		t.Fatalf("symlink: %v", err)
	}
}

func isOutsideHomeErr(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want ErrOutsideHome")
	}
	if !errors.Is(err, ErrOutsideHome) {
		t.Fatalf("error = %v, want ErrOutsideHome", err)
	}
}

func TestResolveExistingInsideHome(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "docs", "file.txt")
	writeFile(t, p, "x")
	got, err := ResolveExisting(p, home)
	if err != nil {
		t.Fatalf("ResolveExisting: %v", err)
	}
	assertSameResolvedPath(t, got, p)
}

func TestResolveExistingOutsideHomeRejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	p := filepath.Join(outside, "file.txt")
	writeFile(t, p, "x")
	isOutsideHomeErr(t, mustResolve(t, p, home))
}

func TestResolveExistingSymlinkEscapeRejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret")
	writeFile(t, target, "x")
	link := filepath.Join(home, "escape")
	makeSymlink(t, target, link)
	isOutsideHomeErr(t, mustResolve(t, link, home))
}

func TestResolveExistingSymlinkInsideHomeAllowed(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "real")
	writeFile(t, target, "x")
	link := filepath.Join(home, "alias")
	makeSymlink(t, target, link)
	got, err := ResolveExisting(link, home)
	if err != nil {
		t.Fatalf("ResolveExisting: %v", err)
	}
	assertSameResolvedPath(t, got, target)
}

func TestResolveExistingMissingFileErrors(t *testing.T) {
	home := t.TempDir()
	if _, err := ResolveExisting(filepath.Join(home, "nope"), home); err == nil {
		t.Fatal("missing file must error")
	}
}

func TestResolveExistingEmptyPathErrors(t *testing.T) {
	home := t.TempDir()
	if _, err := ResolveExisting("", home); err == nil {
		t.Fatal("empty path must error")
	}
}

func TestResolveExistingEmptyHomeRejects(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "f")
	writeFile(t, p, "x")
	if _, err := ResolveExisting(p, ""); err == nil {
		t.Fatal("empty home must reject every path")
	}
}

func TestResolveTargetExistingFile(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, "existing")
	writeFile(t, p, "keep me")
	got, err := ResolveTarget(p, home)
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	assertSameResolvedPath(t, got, p)
}

func TestResolveTargetMissingLeafPreserved(t *testing.T) {
	home := t.TempDir()
	sub := filepath.Join(home, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(sub, "new-file.txt")
	got, err := ResolveTarget(target, home)
	if err != nil {
		t.Fatalf("ResolveTarget: %v", err)
	}
	// The leaf is preserved verbatim, the parent resolved.
	assertSameResolvedPath(t, got, target)
}

func TestResolveTargetMissingParentRejected(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "no-such-dir", "f")
	if _, err := ResolveTarget(target, home); err == nil {
		t.Fatal("target under a missing parent must error")
	}
}

func TestResolveTargetSymlinkEscapeRejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "existing-file")
	writeFile(t, target, "x")
	link := filepath.Join(home, "victim")
	makeSymlink(t, target, link)
	// A download target that already exists as a symlink escaping home must
	// be rejected (never write through it).
	isOutsideHomeErr(t, mustResolveTarget(t, link, home))
}

func TestResolveTargetParentSymlinkEscapeRejected(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	parent := filepath.Join(outside, "dir")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(home, "alias-dir")
	makeSymlink(t, parent, link)
	isOutsideHomeErr(t, mustResolveTarget(t, filepath.Join(link, "new-file"), home))
}

// TestResolveCaseVariantBoundary pins the case-sensitivity contract: on a
// case-sensitive filesystem a sibling directory that differs only in case
// (".../user" vs ".../USER") is a distinct directory and every path inside
// it must be rejected; on Windows the filesystem is case-insensitive, so the
// same directory spelled with a different case must still be accepted.
func TestResolveCaseVariantBoundary(t *testing.T) {
	if runtime.GOOS == "windows" {
		home := t.TempDir()
		p := filepath.Join(home, "Docs", "file.txt")
		writeFile(t, p, "x")
		got, err := ResolveExisting(p, strings.ToUpper(home))
		if err != nil {
			t.Fatalf("ResolveExisting with a case-variant home must be accepted on Windows: %v", err)
		}
		if !sameResolvedPath(got, p) {
			t.Fatalf("resolved = %q, want a case-insensitive match of %q", got, p)
		}
		target := filepath.Join(home, "Docs", "new.txt")
		gotTarget, err := ResolveTarget(target, strings.ToUpper(home))
		if err != nil {
			t.Fatalf("ResolveTarget with a case-variant home must be accepted on Windows: %v", err)
		}
		if !sameResolvedPath(gotTarget, target) {
			t.Fatalf("target resolved = %q, want a case-insensitive match of %q", gotTarget, target)
		}
		return
	}
	base := t.TempDir()
	if !caseSensitiveFS(t, base) {
		t.Skip("temp filesystem is case-insensitive; case variants name the same directory")
	}
	home := filepath.Join(base, "user")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	sibling := filepath.Join(base, "USER")
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatalf("mkdir sibling: %v", err)
	}
	// A path inside the sibling differs from home only by case and must be
	// rejected on a case-sensitive filesystem.
	p := filepath.Join(sibling, "file.txt")
	writeFile(t, p, "x")
	isOutsideHomeErr(t, mustResolve(t, p, home))
	isOutsideHomeErr(t, mustResolveTarget(t, filepath.Join(sibling, "new.txt"), home))
}

// TestWithinHomeCaseContract exercises the case-folding decision directly:
// the resolve entry points symlink-resolve both sides, which canonicalises
// case on Windows, so they cannot observe the fold themselves.
func TestWithinHomeCaseContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		if !withinHome(`C:\Users\Alice\Documents\file.txt`, `c:\users\alice`) {
			t.Fatal("withinHome must fold case on Windows")
		}
		if !withinHome(`c:\users\alice`, `C:\Users\Alice`) {
			t.Fatal("withinHome must fold case on Windows")
		}
		return
	}
	if withinHome("/home/user/file.txt", "/home/user") {
		t.Fatal("withinHome must be case-sensitive outside Windows")
	}
	if withinHome("/home/USER/file.txt", "/home/user") {
		t.Fatal("withinHome must reject a sibling differing only in case")
	}
}

// caseSensitiveFS probes whether the filesystem holding base distinguishes
// names by case, by creating two names that differ only in case and checking
// they are distinct directories.
func caseSensitiveFS(t *testing.T, base string) bool {
	t.Helper()
	a := filepath.Join(base, "caseprobe-a")
	b := filepath.Join(base, "caseprobe-A")
	if err := os.MkdirAll(a, 0o700); err != nil {
		t.Fatalf("mkdir probe: %v", err)
	}
	if err := os.MkdirAll(b, 0o700); err != nil {
		t.Fatalf("mkdir probe: %v", err)
	}
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat probe: %v", err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat probe: %v", err)
	}
	return !os.SameFile(fa, fb)
}

func mustResolve(t *testing.T, p, home string) error {
	t.Helper()
	_, err := ResolveExisting(p, home)
	return err
}

func mustResolveTarget(t *testing.T, p, home string) error {
	t.Helper()
	_, err := ResolveTarget(p, home)
	return err
}
