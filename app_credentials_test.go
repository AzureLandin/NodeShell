package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"nodeshell/internal/credentials"
	"nodeshell/internal/hosts"
	"nodeshell/internal/knownhosts"
	"nodeshell/internal/settings"
)

// fakeKeyring is an in-memory credentials.Backend for App facade tests.
// getErr fails every Get; maxValue (when >0) enforces the OS keyring
// per-entry size cap so chunked saves can be exercised end-to-end.
type fakeKeyring struct {
	mu       sync.Mutex
	values   map[string]string
	getErr   error
	maxValue int
}

func (f *fakeKeyring) key(service, account string) string { return service + ":" + account }

// snapshot returns a copy of the stored entries for assertions.
func (f *fakeKeyring) snapshot() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.values))
	for k, v := range f.values {
		out[k] = v
	}
	return out
}

func (f *fakeKeyring) Set(service, account, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.maxValue > 0 && len(value) > f.maxValue {
		return credentials.ErrTooLarge
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[f.key(service, account)] = value
	return nil
}

func (f *fakeKeyring) Get(service, account string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.values[f.key(service, account)]
	if !ok {
		return "", credentials.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyring) Delete(service, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.values[f.key(service, account)]; !ok {
		return credentials.ErrNotFound
	}
	delete(f.values, f.key(service, account))
	return nil
}

func (f *fakeKeyring) Available() bool { return true }

// failingDeleteKeyring fails every Delete so removal semantics can be tested.
type failingDeleteKeyring struct {
	fakeKeyring
}

func (f *failingDeleteKeyring) Delete(service, account string) error {
	return errors.New("keyring delete failed")
}

func testAppWith(t *testing.T, backend credentials.Backend, readKey credentials.PrivateKeyReader) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	a := NewAppWithServices(dir, hosts.New(dir), settings.New(dir), knownhosts.New(dir), credentials.New(backend), readKey, dir)
	return a, dir
}

func createHost(t *testing.T, a *App, name string) hosts.HostConfig {
	t.Helper()
	h, err := a.HostsCreate(hosts.HostInput{Name: name, Host: "h", Port: 22, Username: "u", AuthMethod: "password"})
	if err != nil {
		t.Fatalf("HostsCreate: %v", err)
	}
	return h
}

func TestCredentialsSaveRoundtripAndFlags(t *testing.T) {
	a, _ := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("CredentialsSave: %v", err)
	}
	// Save must persist both flags, exactly like the Electron main did.
	got, ok, err := a.hosts.GetByID(h.Id)
	if err != nil || !ok {
		t.Fatalf("GetByID: ok=%v err=%v", ok, err)
	}
	if !got.CredentialsSaved || !got.CredentialsPrompted {
		t.Fatalf("after save flags = saved=%v prompted=%v, want both true", got.CredentialsSaved, got.CredentialsPrompted)
	}
	// The stored payload is the versioned envelope with the password.
	b := a.creds
	secrets, found, err := b.Get(h.Id)
	if err != nil {
		t.Fatalf("keyring Get: %v", err)
	}
	if !found || secrets.Password != "hunter2" {
		t.Fatalf("keyring secrets = %+v (found=%v)", secrets, found)
	}
}

func TestCredentialsClearResetsSavedFlag(t *testing.T) {
	a, _ := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("CredentialsSave: %v", err)
	}
	if err := a.CredentialsClear(h.Id); err != nil {
		t.Fatalf("CredentialsClear: %v", err)
	}
	got, _, _ := a.hosts.GetByID(h.Id)
	if got.CredentialsSaved {
		t.Fatal("clear must persist credentialsSaved=false")
	}
	if _, found, _ := a.creds.Get(h.Id); found {
		t.Fatal("keyring entry must be gone after clear")
	}
}

func TestCredentialsClearMissingEntryIsSuccess(t *testing.T) {
	a, _ := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	if err := a.CredentialsClear(h.Id); err != nil {
		t.Fatalf("Clear of never-saved host must succeed, got %v", err)
	}
	got, _, _ := a.hosts.GetByID(h.Id)
	if got.CredentialsSaved {
		t.Fatal("clear must persist credentialsSaved=false even when nothing was stored")
	}
}

func TestCredentialsSaveRejectsEmptyPayload(t *testing.T) {
	a, _ := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	err := a.CredentialsSave(h.Id, credentials.SavePayload{})
	if err == nil {
		t.Fatal("empty payload must error")
	}
	got, _, _ := a.hosts.GetByID(h.Id)
	if got.CredentialsSaved || got.CredentialsPrompted {
		t.Fatalf("failed save must not mark host saved/prompted: %+v", got)
	}
	if _, found, _ := a.creds.Get(h.Id); found {
		t.Fatal("failed save must not write a keyring entry")
	}
}

func TestCredentialsSaveReadsPrivateKeyContent(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, ".ssh", "id_rsa")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	const keyContent = "-----BEGIN OPENSSH PRIVATE KEY-----\nSECRET\n-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(keyPath, []byte(keyContent), 0o600); err != nil {
		t.Fatal(err)
	}
	a, _ := testAppWith(t, &fakeKeyring{}, credentials.NewHomeReader(dir))
	h := createHost(t, a, "lab")
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{PrivateKeyPath: &keyPath}); err != nil {
		t.Fatalf("CredentialsSave with privateKeyPath: %v", err)
	}
	secrets, found, err := a.creds.Get(h.Id)
	if err != nil || !found {
		t.Fatalf("keyring Get: found=%v err=%v", found, err)
	}
	if secrets.PrivateKey != keyContent {
		t.Fatalf("stored private key = %q, want the file content", secrets.PrivateKey)
	}
	got, _, _ := a.hosts.GetByID(h.Id)
	if !got.CredentialsSaved {
		t.Fatal("save with private key must mark host saved")
	}
}

func TestCredentialsSaveRejectsPrivateKeyOutsideHome(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	keyPath := filepath.Join(outside, "id_rsa")
	if err := os.WriteFile(keyPath, []byte("SECRET KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, _ := testAppWith(t, &fakeKeyring{}, credentials.NewHomeReader(home))
	h := createHost(t, a, "lab")
	err := a.CredentialsSave(h.Id, credentials.SavePayload{PrivateKeyPath: &keyPath})
	if err == nil {
		t.Fatal("private key outside home must be rejected")
	}
	if !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("error = %q, want a home-boundary message", err.Error())
	}
	// Nothing may have been stored — not even a password field that would
	// otherwise have been saved.
	if _, found, _ := a.creds.Get(h.Id); found {
		t.Fatal("rejected save must not leave a keyring entry")
	}
	got, _, _ := a.hosts.GetByID(h.Id)
	if got.CredentialsSaved {
		t.Fatal("rejected save must not mark host saved")
	}
}

func TestCredentialsSaveRejectsSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "id_rsa")
	if err := os.WriteFile(target, []byte("SECRET KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "id_rsa")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable (no privilege): %v", err)
		}
		t.Fatal(err)
	}
	a, _ := testAppWith(t, &fakeKeyring{}, credentials.NewHomeReader(home))
	h := createHost(t, a, "lab")
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{PrivateKeyPath: &link}); err == nil {
		t.Fatal("symlink escaping home must be rejected")
	}
}

func TestCredentialsSaveIgnoresEmptyPasswordAndEmptyPath(t *testing.T) {
	a, _ := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	empty := ""
	err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &empty, PrivateKeyPath: &empty})
	if err == nil {
		t.Fatal("payload reduced to nothing must error, not mark the host saved")
	}
	got, _, _ := a.hosts.GetByID(h.Id)
	if got.CredentialsSaved {
		t.Fatal("empty secrets must not mark host saved")
	}
}

func TestCredentialsMarkPromptedDeclinePersistsFlags(t *testing.T) {
	a, _ := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	if err := a.CredentialsMarkPrompted(h.Id, false); err != nil {
		t.Fatalf("CredentialsMarkPrompted: %v", err)
	}
	got, _, _ := a.hosts.GetByID(h.Id)
	if !got.CredentialsPrompted || got.CredentialsSaved {
		t.Fatalf("after decline = prompted=%v saved=%v, want prompted only", got.CredentialsPrompted, got.CredentialsSaved)
	}
}

func TestCredentialsMarkPromptedSavedForcedFalseWithoutEntry(t *testing.T) {
	a, _ := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	// saved=true with no keyring entry must be forced to false.
	if err := a.CredentialsMarkPrompted(h.Id, true); err != nil {
		t.Fatalf("CredentialsMarkPrompted: %v", err)
	}
	got, _, _ := a.hosts.GetByID(h.Id)
	if got.CredentialsSaved {
		t.Fatal("markPrompted(saved=true) without a keyring entry must force saved=false")
	}
	if !got.CredentialsPrompted {
		t.Fatal("prompted flag must be set regardless")
	}
}

func TestCredentialsMarkPromptedSavedHonouredWithEntry(t *testing.T) {
	a, _ := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("CredentialsSave: %v", err)
	}
	if err := a.CredentialsMarkPrompted(h.Id, true); err != nil {
		t.Fatalf("CredentialsMarkPrompted: %v", err)
	}
	got, _, _ := a.hosts.GetByID(h.Id)
	if !got.CredentialsSaved || !got.CredentialsPrompted {
		t.Fatalf("after markPrompted(true) with entry = saved=%v prompted=%v, want both true", got.CredentialsSaved, got.CredentialsPrompted)
	}
}

// TestHostsListNormalisesStaleLegacyFlag is the core legacy-invalidation
// behaviour: a host whose persisted credentialsSaved=true but with no keyring
// entry (an old Electron save that was never migrated) must be returned with
// the flag normalised away.
func TestHostsListNormalisesStaleLegacyFlag(t *testing.T) {
	a, dir := testAppWith(t, &fakeKeyring{}, nil)
	// Seed the hosts file exactly as the Electron build would have left it.
	store := hosts.New(dir)
	if _, err := store.Create(hosts.HostInput{
		Name: "legacy", Host: "h", Port: 22, Username: "u", AuthMethod: "password",
		CredentialsPrompted: true, CredentialsSaved: true,
	}); err != nil {
		t.Fatalf("seed legacy host: %v", err)
	}
	list, err := a.HostsList()
	if err != nil {
		t.Fatalf("HostsList: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("HostsList = %+v", list)
	}
	if list[0].CredentialsSaved || list[0].CredentialsPrompted {
		t.Fatalf("legacy host must be returned with saved/prompted false, got %+v", list[0])
	}
	// The normalisation must be view-only: the persisted file still carries
	// the legacy flags, untouched for rollback.
	persisted, err := hosts.New(dir).List()
	if err != nil {
		t.Fatal(err)
	}
	if !persisted[0].CredentialsSaved || !persisted[0].CredentialsPrompted {
		t.Fatalf("persisted flags must stay true (view-only normalisation), got %+v", persisted[0])
	}
}

func TestHostsListKeepsSavedFlagWhenKeyringEntryExists(t *testing.T) {
	a, _ := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("CredentialsSave: %v", err)
	}
	list, err := a.HostsList()
	if err != nil {
		t.Fatalf("HostsList: %v", err)
	}
	if len(list) != 1 || !list[0].CredentialsSaved {
		t.Fatalf("host with a real keyring entry must keep saved=true, got %+v", list)
	}
}

// TestHostsListNormalisesCorruptEntry: a corrupt keyring payload must not
// take down the whole host list — that host's secret state is unknowable, so
// its saved/prompted flags are normalised away view-only, the list is still
// returned, and the persisted file stays byte-identical.
func TestHostsListNormalisesCorruptEntry(t *testing.T) {
	b := &fakeKeyring{}
	a, dir := testAppWith(t, b, nil)
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	// Corrupt the entry directly in the keyring.
	if err := b.Set(credentials.ServiceName, h.Id, "not-json"); err != nil {
		t.Fatal(err)
	}
	list, err := a.HostsList()
	if err != nil {
		t.Fatalf("HostsList must normalise a corrupt entry, not reject the list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("HostsList = %+v", list)
	}
	if list[0].CredentialsSaved || list[0].CredentialsPrompted {
		t.Fatalf("corrupt host must be returned with saved/prompted=false, got %+v", list[0])
	}
	// The persisted file must stay byte-identical: the flag is not normalised
	// away and no write happened.
	persisted, err := hosts.New(dir).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || !persisted[0].CredentialsSaved || !persisted[0].CredentialsPrompted {
		t.Fatalf("corrupt entry must not mutate the persisted flags, got %+v", persisted)
	}
}

// TestHostsListNormalisesCorruptEntryKeepsOtherHosts: with one corrupt saved
// host and one healthy saved host, HostsList must return BOTH — the corrupt
// host's flags normalised to false, the healthy host untouched.
func TestHostsListNormalisesCorruptEntryKeepsOtherHosts(t *testing.T) {
	b := &fakeKeyring{}
	a, dir := testAppWith(t, b, nil)
	good := createHost(t, a, "good")
	pw := "hunter2"
	if err := a.CredentialsSave(good.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("seed good credentials: %v", err)
	}
	bad := createHost(t, a, "bad")
	if err := a.CredentialsSave(bad.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("seed bad credentials: %v", err)
	}
	if err := b.Set(credentials.ServiceName, bad.Id, "not-json"); err != nil {
		t.Fatal(err)
	}
	list, err := a.HostsList()
	if err != nil {
		t.Fatalf("HostsList must not reject the list for one corrupt entry, got: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("HostsList = %d hosts, want both (corrupt + healthy)", len(list))
	}
	var goodGot, badGot *hosts.HostConfig
	for i := range list {
		switch list[i].Id {
		case good.Id:
			goodGot = &list[i]
		case bad.Id:
			badGot = &list[i]
		}
	}
	if goodGot == nil || badGot == nil {
		t.Fatalf("both hosts must be present: %+v", list)
	}
	if !goodGot.CredentialsSaved {
		t.Fatalf("healthy host must keep saved=true, got %+v", goodGot)
	}
	if badGot.CredentialsSaved || badGot.CredentialsPrompted {
		t.Fatalf("corrupt host must be returned with saved/prompted=false, got %+v", badGot)
	}
	// View-only: the persisted file keeps both saved flags.
	persisted, err := hosts.New(dir).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 2 {
		t.Fatalf("persisted hosts = %+v", persisted)
	}
	for _, h := range persisted {
		if !h.CredentialsSaved {
			t.Fatalf("persisted flags must stay true (view-only), got %+v", h)
		}
	}
}

// TestHostsListRejectsBackendError: a real backend Get failure must propagate
// as an error — only a missing entry (not-found) normalises the stale flag.
func TestHostsListRejectsBackendError(t *testing.T) {
	b := &fakeKeyring{}
	a, _ := testAppWith(t, b, nil)
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("CredentialsSave: %v", err)
	}
	b.getErr = errors.New("keyring down")
	if _, err := a.HostsList(); err == nil {
		t.Fatal("HostsList must reject a backend Get error, not normalise the flag")
	}
}

// TestCredentialsSaveOverCorruptEntryStaysObservableClearRecovers: saving
// over a corrupt keyring entry stays an observable error (never a silent
// overwrite of an unrecoverable entry), and Clear is the recovery path —
// afterwards the host is cleanly unsaved and a fresh save works.
func TestCredentialsSaveOverCorruptEntryStaysObservableClearRecovers(t *testing.T) {
	b := &fakeKeyring{}
	a, _ := testAppWith(t, b, nil)
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	if err := b.Set(credentials.ServiceName, h.Id, "not-json"); err != nil {
		t.Fatal(err)
	}
	next := "new-password"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &next}); err == nil {
		t.Fatal("CredentialsSave over a corrupt entry must stay observable")
	}
	if err := a.CredentialsClear(h.Id); err != nil {
		t.Fatalf("CredentialsClear must recover a corrupt entry, got %v", err)
	}
	if _, found, _ := a.creds.Get(h.Id); found {
		t.Fatal("clear must remove the corrupt entry")
	}
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &next}); err != nil {
		t.Fatalf("fresh save after Clear must work: %v", err)
	}
	secrets, found, err := a.creds.Get(h.Id)
	if err != nil || !found || secrets.Password != next {
		t.Fatalf("secret after recovery = %+v (found=%v) err=%v", secrets, found, err)
	}
}

// TestHostsRemoveDeletesKeyringAndHost: removal must not leave a secret
// behind — keyring delete happens before the host is removed.
func TestHostsRemoveDeletesKeyringAndHost(t *testing.T) {
	a, _ := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("CredentialsSave: %v", err)
	}
	if err := a.HostsRemove(h.Id); err != nil {
		t.Fatalf("HostsRemove: %v", err)
	}
	if _, found, _ := a.creds.Get(h.Id); found {
		t.Fatal("keyring entry must be deleted when the host is removed")
	}
	list, err := a.HostsList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("hosts after remove = %+v", list)
	}
}

func TestHostsRemoveWithoutEntryStillRemovesHost(t *testing.T) {
	a, _ := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	if err := a.HostsRemove(h.Id); err != nil {
		t.Fatalf("HostsRemove of a host without keyring entry: %v", err)
	}
	list, err := a.HostsList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("hosts after remove = %+v", list)
	}
}

func TestHostsRemoveKeepsHostWhenKeyringDeleteFails(t *testing.T) {
	a, _ := testAppWith(t, &failingDeleteKeyring{}, nil)
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	err := a.HostsRemove(h.Id)
	if err == nil {
		t.Fatal("HostsRemove must error when the keyring delete fails")
	}
	// The host must still be there: removal aborts before touching the store.
	_, ok, err := a.hosts.GetByID(h.Id)
	if err != nil || !ok {
		t.Fatalf("host must survive a failed keyring delete: ok=%v err=%v", ok, err)
	}
}

// TestLegacyCredentialsFileUntouchedDuringAppLifecycle seeds an
// undecryptable, read-only credentials.json (the old Electron vault) in the
// data dir and drives startup plus every credentials operation; the file must
// stay byte- and mtime-identical because the Wails service never opens it.
func TestLegacyCredentialsFileUntouchedDuringAppLifecycle(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "credentials.json")
	// Mimic an Electron safeStorage vault: opaque base64 ciphertext.
	const cipher = "eyJ2ZXJzaW9uIjoxLCJlbnRyaWVzIjp7fX0=AAFF00UNDECRYPTABLE"
	if err := os.WriteFile(marker, []byte(cipher), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0o444); err != nil {
		t.Logf("chmod marker read-only: %v", err)
	}
	defer func() {
		_ = os.Chmod(marker, 0o600)
	}()

	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	beforeMtime := info.ModTime()

	a, _ := testAppWith(t, &fakeKeyring{}, credentials.NewHomeReader(dir))
	a.startup(context.Background())
	if _, err := a.CredentialsIsAvailable(); err != nil {
		t.Fatalf("CredentialsIsAvailable: %v", err)
	}
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("CredentialsSave: %v", err)
	}
	if _, _, err := a.creds.Get(h.Id); err != nil {
		t.Fatalf("keyring Get: %v", err)
	}
	if err := a.CredentialsClear(h.Id); err != nil {
		t.Fatalf("CredentialsClear: %v", err)
	}
	if err := a.HostsRemove(h.Id); err != nil {
		t.Fatalf("HostsRemove: %v", err)
	}

	after, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker unreadable after lifecycle: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("credentials.json bytes changed — the service must never touch the legacy vault")
	}
	afterInfo, err := os.Stat(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !afterInfo.ModTime().Equal(beforeMtime) {
		t.Fatal("credentials.json mtime changed — the service must never open the legacy vault")
	}
}

// TestStartupDoesNotTouchLegacyCredentialsFile runs a real startup (which
// wires the OS-keyring backend) against a data dir containing the legacy
// vault marker and asserts the marker is untouched without any keyring write.
func TestStartupDoesNotTouchLegacyCredentialsFile(t *testing.T) {
	orig := resolveDataDir
	defer func() { resolveDataDir = orig }()
	dir := t.TempDir()
	marker := filepath.Join(dir, "credentials.json")
	const cipher = "AAAAUNDECRYPTABLE"
	if err := os.WriteFile(marker, []byte(cipher), 0o600); err != nil {
		t.Fatal(err)
	}
	resolveDataDir = func() (string, error) { return dir, nil }

	a := NewApp()
	a.startup(context.Background())

	after, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker unreadable after startup: %v", err)
	}
	if string(after) != cipher {
		t.Fatal("credentials.json bytes changed during startup")
	}
	if _, err := a.CredentialsIsAvailable(); err != nil {
		t.Fatalf("CredentialsIsAvailable after startup: %v", err)
	}
}

func TestCredentialsUninitialisedAppErrors(t *testing.T) {
	a := NewApp()
	if _, err := a.CredentialsIsAvailable(); err == nil {
		t.Fatal("CredentialsIsAvailable on uninitialised App must error")
	}
	pw := "x"
	if err := a.CredentialsSave("id", credentials.SavePayload{Password: &pw}); err == nil {
		t.Fatal("CredentialsSave on uninitialised App must error")
	}
	if err := a.CredentialsClear("id"); err == nil {
		t.Fatal("CredentialsClear on uninitialised App must error")
	}
	if err := a.CredentialsMarkPrompted("id", false); err == nil {
		t.Fatal("CredentialsMarkPrompted on uninitialised App must error")
	}
}

// TestCredentialsSaveUnknownHostRejectsWithoutKeyringAccount: saving for an
// id that does not exist must fail before any keyring write — an orphan
// secret must never be created for an unknown host.
func TestCredentialsSaveUnknownHostRejectsWithoutKeyringAccount(t *testing.T) {
	b := &fakeKeyring{}
	a, _ := testAppWith(t, b, nil)
	pw := "hunter2"
	if err := a.CredentialsSave("no-such-host", credentials.SavePayload{Password: &pw}); err == nil {
		t.Fatal("CredentialsSave for an unknown host must error")
	}
	if len(b.snapshot()) != 0 {
		t.Fatal("an unknown host must never create a keyring account")
	}
}

// TestCredentialsSaveEmptyHostIDRejectsWithoutKeyringAccount: an empty host
// id must be rejected identically, never reaching the keyring.
func TestCredentialsSaveEmptyHostIDRejectsWithoutKeyringAccount(t *testing.T) {
	b := &fakeKeyring{}
	a, _ := testAppWith(t, b, nil)
	pw := "hunter2"
	if err := a.CredentialsSave("", credentials.SavePayload{Password: &pw}); err == nil {
		t.Fatal("CredentialsSave with an empty host id must error")
	}
	if len(b.snapshot()) != 0 {
		t.Fatal("an empty host id must never create a keyring account")
	}
}

// TestCredentialsSaveLargePrivateKeyChunkedRoundtrip drives a >5KB private
// key through the full facade against a Windows-capped backend: the key must
// roundtrip exactly, the host must be marked saved, and every stored entry
// must fit the backend cap.
func TestCredentialsSaveLargePrivateKeyChunkedRoundtrip(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, ".ssh", "id_rsa")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("A", 5500) + "\n-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &fakeKeyring{maxValue: 2560}
	a, _ := testAppWith(t, b, credentials.NewHomeReader(dir))
	h := createHost(t, a, "lab")
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{PrivateKeyPath: &keyPath}); err != nil {
		t.Fatalf("CredentialsSave with a >5KB key: %v", err)
	}
	secrets, found, err := a.creds.Get(h.Id)
	if err != nil || !found {
		t.Fatalf("keyring Get: found=%v err=%v", found, err)
	}
	if secrets.PrivateKey != key {
		t.Fatal("large private key did not roundtrip exactly")
	}
	got, _, _ := a.hosts.GetByID(h.Id)
	if !got.CredentialsSaved {
		t.Fatal("large save must mark the host saved")
	}
	for account, value := range b.snapshot() {
		if len(value) > 2560 {
			t.Fatalf("entry %q is %d bytes, exceeds the backend cap", account, len(value))
		}
	}
}

// TestCredentialsSaveOversizedKeyRejectedFlagsUntouched: a key whose marshaled
// payload exceeds the assemble limit (maxAssembledBytes, 64 KiB) but stays
// under the chunk-count capacity must be rejected through the facade — no
// keyring entry is created and the saved/prompted flags stay untouched, so a
// failed save never marks the host saved.
func TestCredentialsSaveOversizedKeyRejectedFlagsUntouched(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, ".ssh", "id_rsa")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// 70000 content bytes put the marshaled payload at ~70048, inside the
	// (65536, 115200] window the chunked protocol could store but Get refuses
	// to assemble.
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("A", 70000) + "\n-----END OPENSSH PRIVATE KEY-----\n"
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &fakeKeyring{maxValue: 2560}
	a, _ := testAppWith(t, b, credentials.NewHomeReader(dir))
	h := createHost(t, a, "lab")
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{PrivateKeyPath: &keyPath}); err == nil {
		t.Fatal("CredentialsSave of an over-assemble-limit key must error")
	}
	if _, found, _ := a.creds.Get(h.Id); found {
		t.Fatal("rejected oversized save must not create a keyring entry")
	}
	got, _, _ := a.hosts.GetByID(h.Id)
	if got.CredentialsSaved || got.CredentialsPrompted {
		t.Fatalf("rejected oversized save must not update flags: %+v", got)
	}
}

// sabotageHostsWrite makes hosts.json un-replaceable so the next store write
// fails, simulating a CONFIG_WRITE_FAILED flag update.
func sabotageHostsWrite(t *testing.T, dir string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, "hosts.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "hosts.json"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// TestCredentialsSaveRollsBackWhenFlagUpdateFails: if the keyring write
// succeeds but persisting the saved flag fails, the keyring must be rolled
// back to the previous credential so no secret survives without its flag.
func TestCredentialsSaveRollsBackWhenFlagUpdateFails(t *testing.T) {
	a, dir := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	old := "old-password"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &old}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	sabotageHostsWrite(t, dir)
	next := "new-password"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &next}); err == nil {
		t.Fatal("CredentialsSave must surface the flag-update failure")
	}
	secrets, found, err := a.creds.Get(h.Id)
	if err != nil {
		t.Fatalf("keyring Get: %v", err)
	}
	if !found || secrets.Password != old {
		t.Fatalf("keyring must be rolled back to the old credential, got %+v (found=%v)", secrets, found)
	}
}

// TestCredentialsClearRollsBackWhenFlagUpdateFails: clearing the keyring then
// failing to persist saved=false must restore the cleared secret, so the
// host's real credential state stays consistent.
func TestCredentialsClearRollsBackWhenFlagUpdateFails(t *testing.T) {
	a, dir := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	sabotageHostsWrite(t, dir)
	if err := a.CredentialsClear(h.Id); err == nil {
		t.Fatal("CredentialsClear must surface the flag-update failure")
	}
	secrets, found, err := a.creds.Get(h.Id)
	if err != nil {
		t.Fatalf("keyring Get: %v", err)
	}
	if !found || secrets.Password != pw {
		t.Fatalf("cleared secret must be restored when the flag update fails, got %+v (found=%v)", secrets, found)
	}
}

// TestHostsRemoveRestoresSecretWhenHostRemoveFails: removing the host must
// delete the secret first, and a failed host-store write must restore it so a
// secret never disappears while the host survives.
func TestHostsRemoveRestoresSecretWhenHostRemoveFails(t *testing.T) {
	a, dir := testAppWith(t, &fakeKeyring{}, nil)
	h := createHost(t, a, "lab")
	pw := "hunter2"
	if err := a.CredentialsSave(h.Id, credentials.SavePayload{Password: &pw}); err != nil {
		t.Fatalf("seed credentials: %v", err)
	}
	sabotageHostsWrite(t, dir)
	if err := a.HostsRemove(h.Id); err == nil {
		t.Fatal("HostsRemove must surface the host-store write failure")
	}
	secrets, found, err := a.creds.Get(h.Id)
	if err != nil {
		t.Fatalf("keyring Get: %v", err)
	}
	if !found || secrets.Password != pw {
		t.Fatalf("secret must be restored when host removal fails, got %+v (found=%v)", secrets, found)
	}
}
