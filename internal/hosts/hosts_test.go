package hosts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// legacyFixture is byte-for-byte what the Electron build writes to hosts.json.
const legacyFixture = `{
  "hosts": [
    {
      "id": "host-1",
      "name": "lab",
      "host": "192.168.1.10",
      "port": 22,
      "username": "root",
      "authMethod": "password",
      "credentialsPrompted": true,
      "credentialsSaved": true
    }
  ]
}`

func newStore(t *testing.T) *Store {
	t.Helper()
	return New(t.TempDir())
}

func TestListReadsLegacyFixture(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hosts.json"), []byte(legacyFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	h := got[0]
	if h.Id != "host-1" || h.Name != "lab" || h.Host != "192.168.1.10" || h.Port != 22 ||
		h.Username != "root" || h.AuthMethod != "password" || !h.CredentialsPrompted || !h.CredentialsSaved {
		t.Fatalf("parsed host mismatch: %+v", h)
	}
}

func TestListEmptyWhenFileMissing(t *testing.T) {
	got, err := newStore(t).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestCreateGeneratesUUIDAndPersists(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	h, err := store.Create(HostInput{
		Name: "lab", Host: "192.168.1.10", Port: 22,
		Username: "root", AuthMethod: "password",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h.Id == "" || !strings.Contains(h.Id, "-") {
		t.Fatalf("expected a UUID id, got %q", h.Id)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Hosts []map[string]any `json:"hosts"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("persisted JSON invalid: %v (%q)", err, raw)
	}
	if len(file.Hosts) != 1 {
		t.Fatalf("persisted %d hosts, want 1", len(file.Hosts))
	}
	if id := file.Hosts[0]["id"]; id != h.Id {
		t.Fatalf("persisted id %v, want %q", id, h.Id)
	}
	// Field names must match the TS HostConfig exactly (camelCase).
	if _, ok := file.Hosts[0]["authMethod"]; !ok {
		t.Fatalf("persisted keys %v missing authMethod", file.Hosts[0])
	}
	if _, ok := file.Hosts[0]["privateKeyPath"]; ok {
		t.Fatalf("unset privateKeyPath must not be persisted")
	}
}

func TestUpdatePatchesAndKeepsID(t *testing.T) {
	store := newStore(t)
	h, err := store.Create(HostInput{Name: "lab", Host: "h", Port: 22, Username: "u", AuthMethod: "password"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(h.Id, Patch{Name: strp("prod"), Port: intp(2222)})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Id != h.Id || updated.Name != "prod" || updated.Port != 2222 || updated.Host != "h" {
		t.Fatalf("updated mismatch: %+v", updated)
	}
	listed, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Id != h.Id || listed[0].Name != "prod" {
		t.Fatalf("persisted update mismatch: %+v", listed)
	}
}

func TestUpdateUnknownIDReturnsError(t *testing.T) {
	_, err := newStore(t).Update("nope", Patch{Name: strp("x")})
	if err == nil {
		t.Fatal("expected error for unknown id")
	}
	var e *Error
	if !asError(err, &e) || e.Code != "UNKNOWN" || !strings.Contains(e.Message, "nope") {
		t.Fatalf("error = %v, want UNKNOWN mentioning id", err)
	}
}

func TestRemoveDeletesHost(t *testing.T) {
	store := newStore(t)
	h, err := store.Create(HostInput{Name: "a", Host: "h", Port: 22, Username: "u", AuthMethod: "password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(h.Id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestGetByID(t *testing.T) {
	store := newStore(t)
	h, err := store.Create(HostInput{Name: "a", Host: "h", Port: 22, Username: "u", AuthMethod: "password"})
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetByID(h.Id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !ok || got.Id != h.Id {
		t.Fatalf("GetByID = %+v, %v", got, ok)
	}
	if _, ok, err := store.GetByID("nope"); err != nil || ok {
		t.Fatalf("GetByID('nope') = ok=%v err=%v, want not found without error", ok, err)
	}
}

func TestCorruptFileReadErrorWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(dir).List()
	var e *Error
	if !asError(err, &e) || e.Code != "CONFIG_READ_FAILED" {
		t.Fatalf("error = %v, want CONFIG_READ_FAILED", err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "{not-json" {
		t.Fatalf("corrupt file was overwritten: %q", raw)
	}
}

func TestUnknownFieldsIgnored(t *testing.T) {
	dir := t.TempDir()
	raw := `{
  "note": "future field",
  "hosts": [
    {
      "id": "h1",
      "name": "n",
      "host": "10.0.0.1",
      "port": 2200,
      "username": "u",
      "authMethod": "privateKey",
      "privateKeyPath": "/home/u/.ssh/id_ed25519",
      "futureField": {"nested": true}
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(dir, "hosts.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(dir).List()
	if err != nil {
		t.Fatalf("List with unknown fields: %v", err)
	}
	if len(got) != 1 || got[0].PrivateKeyPath != "/home/u/.ssh/id_ed25519" || got[0].AuthMethod != "privateKey" {
		t.Fatalf("parsed mismatch: %+v", got)
	}
}

func TestListReturnsCopyNotInternalSlice(t *testing.T) {
	store := newStore(t)
	_, err := store.Create(HostInput{Name: "a", Host: "h", Port: 22, Username: "u", AuthMethod: "password"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	first[0].Name = "tampered"
	first = append(first, HostConfig{Id: "x"})
	again, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].Name != "a" {
		t.Fatalf("internal state leaked through List: %+v", again)
	}
}

func TestGetByIDReturnsCopy(t *testing.T) {
	store := newStore(t)
	h, err := store.Create(HostInput{Name: "a", Host: "h", Port: 22, Username: "u", AuthMethod: "password"})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := store.GetByID(h.Id)
	if err != nil {
		t.Fatal(err)
	}
	got.Name = "tampered"
	again, _, err := store.GetByID(h.Id)
	if err != nil {
		t.Fatal(err)
	}
	if again.Name != "a" {
		t.Fatalf("internal state leaked through GetByID: %+v", again)
	}
}

// GetByID must surface a read error instead of swallowing it as "not found",
// matching the TS getById which rejects on CONFIG_READ_FAILED.
func TestGetByIDCorruptFileReturnsReadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hosts.json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := New(dir).GetByID("x")
	var e *Error
	if !asError(err, &e) || e.Code != "CONFIG_READ_FAILED" {
		t.Fatalf("GetByID error = %v, want CONFIG_READ_FAILED", err)
	}
}

// TestConcurrentCreateListNoRaceOrLostUpdates is the stable repo race test:
// 8 writers creating concurrently with 4 readers listing must run race-free,
// produce no CONFIG_WRITE_FAILED, and every create that reported success must
// survive to the final List and to the on-disk file.
func TestConcurrentCreateListNoRaceOrLostUpdates(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	const writers, perWriter, readers, listIter = 8, 25, 4, 50
	var wg sync.WaitGroup
	var created atomic.Int64
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				if _, err := store.Create(HostInput{Name: "h", Host: "h", Port: 22, Username: "u", AuthMethod: "password"}); err != nil {
					t.Errorf("Create: %v", err)
					return
				}
				created.Add(1)
			}
		}()
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < listIter; j++ {
				if _, err := store.List(); err != nil {
					t.Errorf("List: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	want := int(created.Load())
	got, err := store.List()
	if err != nil {
		t.Fatalf("List after writers: %v", err)
	}
	if len(got) != want {
		t.Fatalf("List has %d hosts, want %d: successful creates were lost", len(got), want)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file hostsFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("persisted file corrupt after concurrent writes: %v", err)
	}
	if len(file.Hosts) != want {
		t.Fatalf("on-disk file has %d hosts, want %d", len(file.Hosts), want)
	}
}

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }
func asError(err error, out **Error) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*Error)
	if ok {
		*out = e
	}
	return ok
}
