package knownhosts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(t.TempDir())
}

// writeError is a seam-injected writer failure that mimics a real
// atomicfile/WriteJSON failure: an *os.PathError carrying the absolute store
// path.
func failingWriter(dir string) func(string, any) error {
	path := filepath.Join(dir, "known_hosts.json")
	return func(string, any) error {
		return &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	}
}

func asKnownError(err error, out **Error) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*Error)
	if ok {
		*out = e
	}
	return ok
}

func TestUnknownForNewHost(t *testing.T) {
	store := newStore(t)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	got, err := store.Check("h", 22, "fp1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unknown" || got.Previous != "" {
		t.Fatalf("got %+v, want unknown", got)
	}
}

func TestCheckSyncMatchesAsyncCheckAfterLoad(t *testing.T) {
	store := newStore(t)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.CheckSync("h", 22, "fp1"); got.Status != "unknown" {
		t.Fatalf("expected unknown, got %+v", got)
	}
	if err := store.Remember("h", 22, "fp1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.CheckSync("h", 22, "fp1"); got.Status != "ok" {
		t.Fatalf("expected ok, got %+v", got)
	}
	got, _ := store.CheckSync("h", 22, "fp2")
	if got.Status != "changed" || got.Previous != "fp1" {
		t.Fatalf("expected changed with previous fp1, got %+v", got)
	}
}

func TestCheckSyncRequiresLoad(t *testing.T) {
	store := newStore(t)
	if _, err := store.CheckSync("h", 22, "fp1"); err == nil {
		t.Fatal("CheckSync before Load must fail (TS: cache not loaded)")
	}
}

func TestOkWhenFingerprintMatches(t *testing.T) {
	store := newStore(t)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("h", 22, "fp1"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Check("h", 22, "fp1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" {
		t.Fatalf("got %+v, want ok", got)
	}
}

func TestChangedWhenFingerprintDiffers(t *testing.T) {
	store := newStore(t)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("h", 22, "fp1"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Check("h", 22, "fp2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "changed" || got.Previous != "fp1" {
		t.Fatalf("got %+v, want changed with previous fp1", got)
	}
}

func TestCheckAutoLoadsWhenNotLoaded(t *testing.T) {
	store := newStore(t)
	got, err := store.Check("h", 22, "fp1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unknown" {
		t.Fatalf("auto-load should yield unknown, got %+v", got)
	}
}

func TestPersistsHostPortKey(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("h", 22, "fp1"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "known_hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]string
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("persisted JSON invalid: %v (%q)", err, raw)
	}
	if file["h:22"] != "fp1" {
		t.Fatalf("persisted %v, want h:22 -> fp1", file)
	}
}

func TestReloadSeesRememberedKeys(t *testing.T) {
	dir := t.TempDir()
	first := New(dir)
	if err := first.Load(); err != nil {
		t.Fatal(err)
	}
	if err := first.Remember("h", 22, "fp1"); err != nil {
		t.Fatal(err)
	}
	second := New(dir)
	if err := second.Load(); err != nil {
		t.Fatal(err)
	}
	got, err := second.Check("h", 22, "fp1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" {
		t.Fatalf("reloaded store check = %+v, want ok", got)
	}
}

func TestCorruptFileLoadFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "known_hosts.json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := New(dir).Load()
	var e *Error
	if !asKnownError(err, &e) || e.Code != "CONFIG_READ_FAILED" {
		t.Fatalf("Load of corrupt file: %v, want CONFIG_READ_FAILED (TS rethrows)", err)
	}
	if strings.Contains(e.Message, dir) {
		t.Fatalf("error message %q leaks the store path %q", e.Message, dir)
	}
}

// RED/GREEN: a failed Remember must not mutate the in-memory cache — the
// cache is replaced only after the write succeeds (copy-on-write), so a
// host key accepted in memory but not persisted is never reported trusted.
func TestRememberWriteFailureLeavesCacheUnknown(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	store.writer = failingWriter(dir)
	err := store.Remember("h", 22, "fp1")
	var e *Error
	if !asKnownError(err, &e) || e.Code != "CONFIG_WRITE_FAILED" {
		t.Fatalf("Remember error = %v, want CONFIG_WRITE_FAILED", err)
	}
	if strings.Contains(e.Message, dir) {
		t.Fatalf("error message %q leaks the store path %q", e.Message, dir)
	}
	got, err := store.CheckSync("h", 22, "fp1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "unknown" {
		t.Fatalf("cache after failed Remember = %+v, want unknown (failed write must not touch memory)", got)
	}
}

// RED/GREEN: the same copy-on-write guarantee with a pre-existing entry: a
// failed overwrite keeps the old fingerprint in memory.
func TestRememberWriteFailureKeepsOldFingerprint(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("h", 22, "old"); err != nil {
		t.Fatal(err)
	}
	store.writer = failingWriter(dir)
	err := store.Remember("h", 22, "new")
	var e *Error
	if !asKnownError(err, &e) || e.Code != "CONFIG_WRITE_FAILED" {
		t.Fatalf("Remember error = %v, want CONFIG_WRITE_FAILED", err)
	}
	got, err := store.CheckSync("h", 22, "new")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "changed" || got.Previous != "old" {
		t.Fatalf("cache after failed overwrite = %+v, want changed with previous old", got)
	}
	if ok, err := store.CheckSync("h", 22, "old"); err != nil || ok.Status != "ok" {
		t.Fatalf("old fingerprint after failed overwrite = %+v, %v, want ok", ok, err)
	}
}

// RED/GREEN: the auto-loading Check path wraps a read failure in the same
// path-free coded error, so the host-key callback never surfaces a filesystem
// path to the frontend.
func TestCheckReadFailureIsCodedAndPathFree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "known_hosts.json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(dir).Check("h", 22, "fp")
	var e *Error
	if !asKnownError(err, &e) || e.Code != "CONFIG_READ_FAILED" {
		t.Fatalf("Check error = %v, want CONFIG_READ_FAILED", err)
	}
	if strings.Contains(e.Message, dir) {
		t.Fatalf("error message %q leaks the store path %q", e.Message, dir)
	}
}
