package credentials

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"nodeshell/internal/apperror"
)

// fakeBackend is an in-memory Backend for tests. Keys are "service:account".
// A failing backend can be configured to inject errors for any method.
// maxValue (when >0) enforces the OS keyring per-entry size cap so chunked
// saves can be exercised against a real Windows-style limit; failSetAccount
// and failDeleteAccount fail just that one account, simulating a mid-write
// or mid-delete backend fault.
type fakeBackend struct {
	mu                sync.Mutex
	values            map[string]string
	available         bool
	setErr            error
	getErr            error
	delErr            error
	maxValue          int
	failSetAccount    string
	failDeleteAccount string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{values: map[string]string{}, available: true}
}

func key(service, account string) string { return service + ":" + account }

// snapshot returns a copy of the stored entries for assertions.
func (f *fakeBackend) snapshot() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.values))
	for k, v := range f.values {
		out[k] = v
	}
	return out
}

func (f *fakeBackend) Set(service, account, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSetAccount != "" && account == f.failSetAccount {
		return errors.New("simulated set failure")
	}
	if f.setErr != nil {
		return f.setErr
	}
	if f.maxValue > 0 && len(value) > f.maxValue {
		return ErrTooLarge
	}
	f.values[key(service, account)] = value
	return nil
}

func (f *fakeBackend) Get(service, account string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.values[key(service, account)]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (f *fakeBackend) Delete(service, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDeleteAccount != "" && account == f.failDeleteAccount {
		return errors.New("simulated delete failure")
	}
	if f.delErr != nil {
		return f.delErr
	}
	if _, ok := f.values[key(service, account)]; !ok {
		return ErrNotFound
	}
	delete(f.values, key(service, account))
	return nil
}

func (f *fakeBackend) Available() bool { return f.available }

func strPtr(s string) *string { return &s }

func TestSaveGetRoundtrip(t *testing.T) {
	s := New(newFakeBackend())
	if err := s.Save("host-1", SavePatch{Password: strPtr("hunter2")}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, found, err := s.Get("host-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("Get found = false, want true")
	}
	if got.Password != "hunter2" || got.PrivateKey != "" {
		t.Fatalf("Get = %+v", got)
	}
}

func TestSaveStoresVersionedPayload(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	if err := s.Save("host-1", SavePatch{PrivateKey: strPtr("-----BEGIN KEY-----\n...")}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := b.Get(ServiceName, "host-1")
	if err != nil {
		t.Fatalf("backend Get: %v", err)
	}
	// The on-disk envelope is the versioned JSON payload the domain decodes.
	var p payload
	if err := json.Unmarshal([]byte(raw), &p); err != nil || p.Version != 1 || p.PrivateKey == "" {
		t.Fatalf("backend value is not a valid version-1 payload: %q (err=%v)", raw, err)
	}
}

func TestSavePartialPatchKeepsExistingField(t *testing.T) {
	s := New(newFakeBackend())
	if err := s.Save("host-1", SavePatch{Password: strPtr("pw")}); err != nil {
		t.Fatalf("Save password: %v", err)
	}
	// Saving only the private key must not drop the stored password.
	if err := s.Save("host-1", SavePatch{PrivateKey: strPtr("key")}); err != nil {
		t.Fatalf("Save private key: %v", err)
	}
	got, found, err := s.Get("host-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found || got.Password != "pw" || got.PrivateKey != "key" {
		t.Fatalf("Get after partial patch = %+v (found=%v), want both fields", got, found)
	}
}

func TestSaveEmptyPatchErrors(t *testing.T) {
	s := New(newFakeBackend())
	err := s.Save("host-1", SavePatch{})
	if err == nil {
		t.Fatal("Save with empty patch must error")
	}
	// An explicit empty password is not a no-op field either: it must be
	// treated as absent by the facade, so the domain sees an empty patch.
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	_, found, err := s.Get("host-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("empty patch must not create an entry")
	}
}

func TestGetNotFoundReturnsEmpty(t *testing.T) {
	s := New(newFakeBackend())
	got, found, err := s.Get("missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found || got != (Secrets{}) {
		t.Fatalf("Get = %+v, found=%v; want zero secrets, found=false", got, found)
	}
}

func TestGetCorruptPayloadIsObservable(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	if err := b.Set(ServiceName, "host-1", "not-json"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, _, err := s.Get("host-1")
	if err == nil {
		t.Fatal("corrupt payload must error, not silently vanish")
	}
	var e *Error
	if !errors.As(err, &e) || e.Code != apperror.Unknown {
		t.Fatalf("error = %v, want credentials.Error UNKNOWN", err)
	}
	if strings.Contains(err.Error(), "not-json") {
		t.Fatalf("error must not leak the payload: %v", err)
	}
}

func TestGetWrongVersionIsObservable(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	if err := b.Set(ServiceName, "host-1", `{"version":2,"password":"x"}`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := s.Get("host-1"); err == nil {
		t.Fatal("unsupported payload version must error")
	}
}

// TestGetCorruptErrorWrapsErrCorrupt: the corrupt error must be classifiable
// with errors.Is so the facade can tell a broken entry from a backend outage
// without parsing the message.
func TestGetCorruptErrorWrapsErrCorrupt(t *testing.T) {
	for _, raw := range []string{"not-json", `{"version":2,"generation":"g","chunks":1}`} {
		b := newFakeBackend()
		s := New(b)
		if err := b.Set(ServiceName, "host-1", raw); err != nil {
			t.Fatalf("seed %q: %v", raw, err)
		}
		_, _, err := s.Get("host-1")
		if err == nil {
			t.Fatalf("corrupt payload %q must error", raw)
		}
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("corrupt payload %q error must wrap ErrCorrupt, got %v", raw, err)
		}
		var e *Error
		if !errors.As(err, &e) || e.Code != apperror.Unknown {
			t.Fatalf("corrupt payload %q error = %v, want credentials.Error UNKNOWN", raw, err)
		}
	}
}

// TestBackendErrorIsDistinctFromCorrupt: a raw backend failure must never
// classify as corrupt (nor as not-found), so the facade's errors.Is branch
// only fires for genuinely broken entries.
func TestBackendErrorIsDistinctFromCorrupt(t *testing.T) {
	b := newFakeBackend()
	b.getErr = errors.New("keyring down")
	s := New(b)
	_, _, err := s.Get("host-1")
	if err == nil {
		t.Fatal("Get must surface the backend error")
	}
	if errors.Is(err, ErrCorrupt) || errors.Is(err, ErrNotFound) {
		t.Fatalf("backend error must be distinct from corrupt/not-found, got %v", err)
	}
}

func TestSaveOverCorruptEntryErrors(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	if err := b.Set(ServiceName, "host-1", `{"version":9`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Save("host-1", SavePatch{Password: strPtr("pw")}); err == nil {
		t.Fatal("Save over a corrupt entry must error (Clear is the recovery path)")
	}
}

func TestClearRemovesEntry(t *testing.T) {
	s := New(newFakeBackend())
	if err := s.Save("host-1", SavePatch{Password: strPtr("pw")}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Clear("host-1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, found, err := s.Get("host-1"); err != nil || found {
		t.Fatalf("Get after Clear: found=%v err=%v, want gone", found, err)
	}
}

func TestClearMissingEntryIsSuccess(t *testing.T) {
	s := New(newFakeBackend())
	if err := s.Clear("never-saved"); err != nil {
		t.Fatalf("Clear of missing entry must succeed, got %v", err)
	}
}

func TestSaveWhenBackendUnavailable(t *testing.T) {
	b := newFakeBackend()
	b.available = false
	s := New(b)
	if err := s.Save("host-1", SavePatch{Password: strPtr("pw")}); err == nil {
		t.Fatal("Save must error when the backend reports unavailable")
	}
	_, found, _ := s.Get("host-1")
	if found {
		t.Fatal("unavailable backend must not have stored anything")
	}
}

func TestBackendErrorIsObservableAndSecretFree(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	if err := b.Set(ServiceName, "host-1", "secret-value"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	b.setErr = errors.New("keyring exploded")
	err := s.Save("host-1", SavePatch{Password: strPtr("pw")})
	if err == nil {
		t.Fatal("Save must surface backend errors")
	}
	if strings.Contains(err.Error(), "exploded") || strings.Contains(err.Error(), "pw") {
		t.Fatalf("backend error leaked into message: %v", err)
	}
}

func TestTooLargeErrorGetsSpecificMessage(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	b.setErr = ErrTooLarge
	err := s.Save("host-1", SavePatch{Password: strPtr("pw")})
	if err == nil {
		t.Fatal("Save must surface the too-large sentinel")
	}
	if !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("message = %q, want a size-limit hint", err.Error())
	}
}

func TestConcurrentSaveDoesNotLoseFields(t *testing.T) {
	s := New(newFakeBackend())
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = s.Save("host-1", SavePatch{Password: strPtr("pw")})
			} else {
				_ = s.Save("host-1", SavePatch{PrivateKey: strPtr("key")})
			}
		}(i)
	}
	wg.Wait()
	got, found, err := s.Get("host-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found || got.Password != "pw" || got.PrivateKey != "key" {
		t.Fatalf("concurrent Saves lost a field: %+v (found=%v)", got, found)
	}
}

func TestConcurrentSaveAndClearDoNotRace(t *testing.T) {
	s := New(newFakeBackend())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.Save("host-1", SavePatch{Password: strPtr("pw")})
		}()
		go func() {
			defer wg.Done()
			_ = s.Clear("host-1")
		}()
	}
	wg.Wait()
	// Whatever final state results, it must be one of the two valid outcomes.
	_, _, err := s.Get("host-1")
	if err != nil {
		t.Fatalf("Get after concurrent Save/Clear: %v", err)
	}
}

func TestGetErrorIsObservable(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	b.getErr = errors.New("keyring down")
	if _, _, err := s.Get("host-1"); err == nil {
		t.Fatal("Get must surface backend errors")
	}
}

func TestDeleteErrorIsObservable(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	if err := s.Save("host-1", SavePatch{Password: strPtr("pw")}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b.delErr = errors.New("keyring locked")
	if err := s.Clear("host-1"); err == nil {
		t.Fatal("Clear must surface real backend errors")
	}
}

// bigKey builds a private-key-shaped string of roughly n bytes, larger than
// any platform's single-entry keyring limit.
func bigKey(n int) string {
	return "-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("A", n) + "\n-----END OPENSSH PRIVATE KEY-----\n"
}

// seedManifest writes a v2 manifest as the primary entry.
func seedManifest(t *testing.T, b *fakeBackend, hostID, generation string, chunks int) {
	t.Helper()
	raw := fmt.Sprintf(`{"version":2,"generation":%q,"chunks":%d}`, generation, chunks)
	if err := b.Set(ServiceName, hostID, raw); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}
}

// TestSaveGetLargePrivateKeyChunkedRoundtrip is the core size gate: the OS
// keyring caps one entry at 2560 bytes (Windows) / ~4096 (macOS), so a large
// private key must be split into chunks whose stored values stay under the
// backend cap, and Get must reassemble the exact original. Against the
// single-payload implementation this Save fails with ErrTooLarge (RED); the
// chunked protocol makes it roundtrip (GREEN).
func TestSaveGetLargePrivateKeyChunkedRoundtrip(t *testing.T) {
	b := newFakeBackend()
	b.maxValue = 2560
	s := New(b)
	key := bigKey(5500)
	if err := s.Save("host-1", SavePatch{PrivateKey: &key}); err != nil {
		t.Fatalf("Save of a %d-byte private key must chunk, got: %v", len(key), err)
	}
	got, found, err := s.Get("host-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found || got.PrivateKey != key {
		t.Fatalf("roundtrip mismatch: got %d-byte key, found=%v", len(got.PrivateKey), found)
	}
	// Every stored entry must fit the backend's per-entry cap, the primary
	// must be the v2 manifest, and each chunk must carry at most
	// chunkMaxBytes of decoded content.
	entries := b.snapshot()
	if len(entries) < 2 {
		t.Fatalf("large save must produce a manifest + chunks, got %d entries", len(entries))
	}
	for account, value := range entries {
		if len(value) > 2560 {
			t.Fatalf("entry %q is %d bytes, exceeds the backend cap", account, len(value))
		}
		if account != "NodeShell:host-1" {
			var c chunkValue
			if err := json.Unmarshal([]byte(value), &c); err != nil {
				t.Fatalf("chunk %q is not a chunkValue: %v", account, err)
			}
			data, err := base64.StdEncoding.DecodeString(c.Data)
			if err != nil {
				t.Fatalf("chunk %q has invalid base64: %v", account, err)
			}
			if len(data) > chunkMaxBytes {
				t.Fatalf("chunk %q carries %d bytes of content, limit is %d", account, len(data), chunkMaxBytes)
			}
		}
	}
	if raw := entries["NodeShell:host-1"]; !strings.Contains(raw, `"version":2`) {
		t.Fatalf("primary must be a v2 manifest, got %q", raw)
	}
}

// TestSaveChunkWriteFailureKeepsOldValueAndCleansUp: a chunk write failure
// mid-save must leave the previous credential readable and delete the chunks
// written before the failure (no half-written generation is ever committed).
func TestSaveChunkWriteFailureKeepsOldValueAndCleansUp(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	oldPw := "old-password"
	if err := s.Save("host-1", SavePatch{Password: &oldPw}); err != nil {
		t.Fatalf("seed old value: %v", err)
	}
	s.gen = func() string { return "gen-1" }
	// The second chunk write of the new generation fails.
	b.failSetAccount = chunkAccount("host-1", "gen-1", 1)
	big := bigKey(5500)
	if err := s.Save("host-1", SavePatch{PrivateKey: &big}); err == nil {
		t.Fatal("Save must surface the chunk write failure")
	}
	got, found, err := s.Get("host-1")
	if err != nil {
		t.Fatalf("Get old value: %v", err)
	}
	if !found || got.Password != oldPw || got.PrivateKey != "" {
		t.Fatalf("old value must survive the failed save, got %+v (found=%v)", got, found)
	}
	for account := range b.snapshot() {
		if account == key(ServiceName, chunkAccount("host-1", "gen-1", 0)) {
			t.Fatalf("chunk %q written before the failure was not cleaned up", account)
		}
	}
}

// TestSaveManifestCommitFailureCleansNewChunks: a failure writing the v2
// manifest (the commit point) must delete every new chunk and leave the
// previous value untouched.
func TestSaveManifestCommitFailureCleansNewChunks(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	oldPw := "old-password"
	if err := s.Save("host-1", SavePatch{Password: &oldPw}); err != nil {
		t.Fatalf("seed old value: %v", err)
	}
	s.gen = func() string { return "gen-1" }
	// The manifest targets the primary account; failing it simulates a failed
	// commit after every chunk already succeeded.
	b.failSetAccount = "host-1"
	key := bigKey(5500)
	if err := s.Save("host-1", SavePatch{PrivateKey: &key}); err == nil {
		t.Fatal("Save must surface the manifest commit failure")
	}
	got, found, err := s.Get("host-1")
	if err != nil {
		t.Fatalf("Get old value: %v", err)
	}
	if !found || got.Password != oldPw || got.PrivateKey != "" {
		t.Fatalf("old value must survive the failed commit, got %+v (found=%v)", got, found)
	}
	for account := range b.snapshot() {
		if strings.Contains(account, "#gen-1#") {
			t.Fatalf("chunk %q leaked after a failed manifest commit", account)
		}
	}
}

// TestGetMissingChunkIsCorruptSecretFree: a v2 manifest pointing at a missing
// chunk must fail as a corrupt (secret-free) error, never a partial decode.
func TestGetMissingChunkIsCorruptSecretFree(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	seedManifest(t, b, "host-1", "gen-1", 2)
	if err := b.Set(ServiceName, chunkAccount("host-1", "gen-1", 0), `{"index":0,"data":"eA=="}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get("host-1"); err == nil {
		t.Fatal("missing chunk must error, not decode a partial secret")
	}
}

// TestGetCorruptChunkPayloadIsCorrupt: garbage inside a chunk value, a
// reordered index, or invalid base64 must all surface as secret-free corrupt
// errors.
func TestGetCorruptChunkPayloadIsCorrupt(t *testing.T) {
	cases := []string{
		`not-json`,                            // not a chunkValue
		`{"index":1,"data":"eA=="}`,           // wrong index
		`{"index":0,"data":"!!not-base64!!"}`, // invalid base64
	}
	for _, value := range cases {
		b := newFakeBackend()
		s := New(b)
		seedManifest(t, b, "host-1", "gen-1", 1)
		if err := b.Set(ServiceName, chunkAccount("host-1", "gen-1", 0), value); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Get("host-1"); err == nil {
			t.Fatalf("corrupt chunk %q must error", value)
		}
	}
}

// TestGetExcessiveManifestIsRejected: a malicious manifest must not cause an
// unbounded read — chunk count carries a hard limit and any violation
// surfaces as a secret-free corrupt error.
func TestGetExcessiveManifestIsRejected(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	seedManifest(t, b, "host-1", "gen-1", 1000)
	if _, _, err := s.Get("host-1"); err == nil {
		t.Fatal("a manifest claiming 1000 chunks must be rejected")
	}
}

// TestClearChunkedEntryDeletesChunksAndPrimary: clearing a chunked entry must
// delete every chunk first, then the primary manifest.
func TestClearChunkedEntryDeletesChunksAndPrimary(t *testing.T) {
	b := newFakeBackend()
	b.maxValue = 2560
	s := New(b)
	key := bigKey(5500)
	if err := s.Save("host-1", SavePatch{PrivateKey: &key}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Clear("host-1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(b.snapshot()) != 0 {
		t.Fatalf("Clear left %d entries behind", len(b.snapshot()))
	}
}

// TestClearChunkDeleteFailureKeepsPrimaryObservable: a real chunk delete
// error must abort the clear with the primary retained and the error
// observable, so HostsRemove stops instead of leaving a dangling secret.
func TestClearChunkDeleteFailureKeepsPrimaryObservable(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	s.gen = func() string { return "gen-1" }
	key := bigKey(5500)
	if err := s.Save("host-1", SavePatch{PrivateKey: &key}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b.failDeleteAccount = chunkAccount("host-1", "gen-1", 0)
	if err := s.Clear("host-1"); err == nil {
		t.Fatal("Clear must surface the chunk delete failure")
	}
	if _, found, err := s.Get("host-1"); err != nil || !found {
		t.Fatalf("primary must survive a failed chunk delete: found=%v err=%v", found, err)
	}
}

// TestClearCorruptManifestStillDeletesPrimary: a primary that is not a valid
// manifest (e.g. truncated) must still be deleted; orphaned chunks of an
// unparseable manifest are intentionally not discoverable, and generation
// randomness means accounts were never enumerable.
func TestClearCorruptManifestStillDeletesPrimary(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	seedManifest(t, b, "host-1", "gen-1", 0)
	if err := s.Clear("host-1"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, found, err := s.Get("host-1"); err != nil || found {
		t.Fatalf("primary must be gone: found=%v err=%v", found, err)
	}
}

// TestSmallPayloadStaysInlineV1: a small payload must keep the legacy
// version-1 inline format — no manifest, no chunks — so existing entries stay
// byte-compatible.
func TestSmallPayloadStaysInlineV1(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	if err := s.Save("host-1", SavePatch{Password: strPtr("pw")}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries := b.snapshot()
	if len(entries) != 1 {
		t.Fatalf("small save must write exactly one entry, got %d", len(entries))
	}
	if raw := entries["NodeShell:host-1"]; !strings.Contains(raw, `"version":1`) {
		t.Fatalf("primary must stay a version-1 inline payload, got %q", raw)
	}
	if _, found, err := s.Get("host-1"); err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
}

// TestSavePartialPatchChunkedKeepsOtherField: patching only the private key
// onto an existing password must re-chunk the full merged payload and keep
// the password (partial patch stays transparent to chunking).
func TestSavePartialPatchChunkedKeepsOtherField(t *testing.T) {
	b := newFakeBackend()
	b.maxValue = 2560
	s := New(b)
	if err := s.Save("host-1", SavePatch{Password: strPtr("hunter2")}); err != nil {
		t.Fatalf("Save password: %v", err)
	}
	key := bigKey(5500)
	if err := s.Save("host-1", SavePatch{PrivateKey: &key}); err != nil {
		t.Fatalf("Save private key: %v", err)
	}
	got, found, err := s.Get("host-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found || got.Password != "hunter2" || got.PrivateKey != key {
		t.Fatalf("partial patch lost a field: %+v (found=%v)", got, found)
	}
}

// TestSaveBeyondChunkableLimitErrors: a payload that would need more chunks
// than the protocol allows must be rejected up front — Save must never write
// something Get cannot read back.
func TestSaveBeyondChunkableLimitErrors(t *testing.T) {
	b := newFakeBackend()
	b.maxValue = 2560
	s := New(b)
	huge := bigKey(200_000)
	if err := s.Save("host-1", SavePatch{PrivateKey: &huge}); err == nil {
		t.Fatal("an oversized payload must be rejected, not half-written")
	}
	if len(b.snapshot()) != 0 {
		t.Fatal("rejected save must not write anything")
	}
}

// payloadLenFor mirrors Save's envelope exactly: the marshaled payload length
// for a private key whose content body is n bytes. Deriving test keys from the
// real payload struct (not a hardcoded constant) keeps the boundary tests
// tracking the on-disk format.
func payloadLenFor(n int) int {
	data, _ := json.Marshal(payload{Version: 1, PrivateKey: bigKey(n)})
	return len(data)
}

// minContentForPayload returns the smallest key content length whose marshaled
// payload is at least minPayload bytes. Payload length is monotonic in content
// length, so a binary search pins the exact crossing point.
func minContentForPayload(minPayload int) int {
	lo, hi := 0, minPayload
	for payloadLenFor(hi) < minPayload {
		hi *= 2
	}
	for lo < hi {
		mid := (lo + hi) / 2
		if payloadLenFor(mid) >= minPayload {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

// TestSaveRejectsPayloadOverAssembledLimit is the critical save/read gate:
// decodeChunks refuses to assemble more than maxAssembledBytes, so Save must
// reject such a payload up front — before any chunk write — instead of
// committing a value Get will report corrupt. The oversized key sits inside
// the dangerous window (payload > maxAssembledBytes but within the chunk-count
// capacity), so this is not the already-tested beyond-chunkable rejection.
func TestSaveRejectsPayloadOverAssembledLimit(t *testing.T) {
	b := newFakeBackend()
	b.maxValue = 2560
	s := New(b)
	oldPw := "old-password"
	if err := s.Save("host-1", SavePatch{Password: &oldPw}); err != nil {
		t.Fatalf("seed old credential: %v", err)
	}
	before := b.snapshot()

	content := minContentForPayload(maxAssembledBytes + 1)
	key := bigKey(content)
	if pl := payloadLenFor(content); pl <= maxAssembledBytes || pl > maxChunks*chunkMaxBytes {
		t.Fatalf("test key payload %d bytes, want in (%d, %d]", pl, maxAssembledBytes, maxChunks*chunkMaxBytes)
	}

	err := s.Save("host-1", SavePatch{PrivateKey: &key})
	if err == nil {
		t.Fatalf("Save of a %d-byte payload (over the %d-byte assemble limit) must error", payloadLenFor(content), maxAssembledBytes)
	}
	var e *Error
	if !errors.As(err, &e) || e.Code != apperror.Unknown {
		t.Fatalf("error = %v, want credentials.Error UNKNOWN", err)
	}
	if e.Message != "Stored credentials exceed the supported size limit" {
		t.Fatalf("message = %q, want the supported-size-limit message", e.Message)
	}
	if strings.Contains(e.Error(), "-----BEGIN") || strings.Contains(e.Error(), key) {
		t.Fatalf("error must not leak the key content: %v", err)
	}

	// The rejected save must not add anything to the backend — neither a new
	// primary nor any chunk — and the old credential must still read back.
	if after := b.snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected save must not touch the backend: before=%d entries after=%d", len(before), len(after))
	}
	got, found, err := s.Get("host-1")
	if err != nil {
		t.Fatalf("Get old credential: %v", err)
	}
	if !found || got.Password != oldPw || got.PrivateKey != "" {
		t.Fatalf("old credential lost: %+v (found=%v)", got, found)
	}
}

// TestSavePayloadAroundAssembledLimit pins the exact boundary: a marshaled
// payload of maxAssembledBytes saves and reads back, one byte more is rejected
// with the size-limit error and writes nothing. Key lengths come from the
// search helper, so the test survives envelope changes.
func TestSavePayloadAroundAssembledLimit(t *testing.T) {
	over := minContentForPayload(maxAssembledBytes + 1)

	// At (or just below) the limit: must roundtrip.
	b := newFakeBackend()
	b.maxValue = 2560
	s := New(b)
	atKey := bigKey(over - 1)
	if pl := payloadLenFor(over - 1); pl > maxAssembledBytes {
		t.Fatalf("helper produced an over-limit payload (%d > %d) for the at-limit case", pl, maxAssembledBytes)
	}
	if err := s.Save("host-1", SavePatch{PrivateKey: &atKey}); err != nil {
		t.Fatalf("Save at the %d-byte limit must succeed, got %v", payloadLenFor(over-1), err)
	}
	got, found, err := s.Get("host-1")
	if err != nil || !found || got.PrivateKey != atKey {
		t.Fatalf("roundtrip at the limit failed: found=%v err=%v", found, err)
	}

	// One byte over: must be rejected without writing anything.
	b2 := newFakeBackend()
	b2.maxValue = 2560
	s2 := New(b2)
	overKey := bigKey(over)
	err = s2.Save("host-2", SavePatch{PrivateKey: &overKey})
	if err == nil {
		t.Fatal("a payload one byte over the assemble limit must be rejected")
	}
	var e *Error
	if !errors.As(err, &e) || e.Code != apperror.Unknown {
		t.Fatalf("error = %v, want credentials.Error UNKNOWN", err)
	}
	if !strings.Contains(e.Message, "size limit") {
		t.Fatalf("message = %q, want a size-limit message", e.Message)
	}
	if len(b2.snapshot()) != 0 {
		t.Fatal("rejected boundary save must not write any entry")
	}
}

// genSequence yields each generation in order, so multi-save tests can assert
// exactly which generation each save committed.
func genSequence(gens ...string) func() string {
	i := 0
	return func() string {
		g := gens[i%len(gens)]
		i++
		return g
	}
}

// chunkPayloadValue builds a valid chunkValue whose data decodes to a
// version-1 payload, for seeding chunked entries that Get can actually read.
func chunkPayloadValue(t *testing.T, index int, payload string) string {
	t.Helper()
	cv, err := json.Marshal(chunkValue{Index: index, Data: base64.StdEncoding.EncodeToString([]byte(payload))})
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	return string(cv)
}

// TestSaveCleanupFailureKeepsSupersededChunksAddressable is the core orphan
// gate: a chunk delete failing during a new save's post-commit cleanup must
// not orphan the superseded generation. The committed manifest carries the
// failed ref, Get keeps reading the new value, and a later Clear (once the
// backend recovers) removes every chunk of both generations.
func TestSaveCleanupFailureKeepsSupersededChunksAddressable(t *testing.T) {
	b := newFakeBackend()
	b.maxValue = 2560
	s := New(b)
	s.gen = genSequence("gen-1", "gen-2")
	k1 := bigKey(5500)
	if err := s.Save("host-1", SavePatch{PrivateKey: &k1}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// The post-commit cleanup of gen-1 chunk 0 now fails, exactly the
	// "cleanup failed mid-save" condition that used to orphan gen-1.
	b.failDeleteAccount = chunkAccount("host-1", "gen-1", 0)
	k2 := bigKey(5600)
	if err := s.Save("host-1", SavePatch{PrivateKey: &k2}); err != nil {
		t.Fatalf("second Save must succeed despite the cleanup failure, got: %v", err)
	}
	// The new value reads back normally.
	got, found, err := s.Get("host-1")
	if err != nil || !found || got.PrivateKey != k2 {
		t.Fatalf("Get after cleanup failure: found=%v err=%v", found, err)
	}
	// The primary manifest must still reference the failed gen-1 chunks so
	// they are never lost.
	raw, err := b.Get(ServiceName, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("primary not a manifest: %v", err)
	}
	foundRef := false
	for _, ref := range m.Previous {
		if ref.Generation == "gen-1" && ref.Chunks == 4 {
			foundRef = true
		}
	}
	if !foundRef {
		t.Fatalf("primary manifest must keep the failed gen-1 ref, got %+v", m.Previous)
	}

	// The backend recovers: Clear must now remove every chunk of both
	// generations plus the primary.
	b.failDeleteAccount = ""
	if err := s.Clear("host-1"); err != nil {
		t.Fatalf("Clear after recovery: %v", err)
	}
	if len(b.snapshot()) != 0 {
		t.Fatalf("Clear left %d entries behind", len(b.snapshot()))
	}
}

// TestSaveRetriesCleanupUntilNoPendingChunks: a later save after the backend
// recovers must drain the previously failed ref so the old chunks actually
// disappear (no orphan survives once deletes start succeeding).
func TestSaveRetriesCleanupUntilNoPendingChunks(t *testing.T) {
	b := newFakeBackend()
	b.maxValue = 2560
	s := New(b)
	s.gen = genSequence("gen-1", "gen-2", "gen-3")
	k1 := bigKey(5500)
	if err := s.Save("host-1", SavePatch{PrivateKey: &k1}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	b.failDeleteAccount = chunkAccount("host-1", "gen-1", 0)
	k2 := bigKey(5600)
	if err := s.Save("host-1", SavePatch{PrivateKey: &k2}); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	b.failDeleteAccount = ""
	// The third save inherits the pending gen-1 ref and drains it on the way
	// out — the orphaned chunk must be gone afterwards.
	k3 := bigKey(5700)
	if err := s.Save("host-1", SavePatch{PrivateKey: &k3}); err != nil {
		t.Fatalf("third Save: %v", err)
	}
	if _, err := b.Get(ServiceName, chunkAccount("host-1", "gen-1", 0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("recovered backend must have drained the pending gen-1 chunk, got err=%v", err)
	}
}

// TestSaveRefusesWhenPendingRefsExceedLimit: if persistent cleanup failures
// have accumulated more pending generations than the protocol allows, Save
// must refuse without committing — the old primary keeps every ref and the
// backend is left untouched.
func TestSaveRefusesWhenPendingRefsExceedLimit(t *testing.T) {
	b := newFakeBackend()
	b.maxValue = 2560
	s := New(b)
	// Seed a manifest already carrying the maximum pending refs; the next
	// save would add its own superseded generation and blow past the cap.
	var sb strings.Builder
	for i := 0; i < 16; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"generation":"old-gen-%d","chunks":1}`, i)
	}
	raw := fmt.Sprintf(`{"version":2,"generation":"gen-0","chunks":1,"previous":[%s]}`, sb.String())
	if err := b.Set(ServiceName, "host-1", raw); err != nil {
		t.Fatal(err)
	}
	if err := b.Set(ServiceName, chunkAccount("host-1", "gen-0", 0), chunkPayloadValue(t, 0, `{"version":1,"password":"pw"}`)); err != nil {
		t.Fatal(err)
	}
	// Every delete keeps failing, so the pre-commit drain cannot shrink the
	// list below the cap.
	b.delErr = errors.New("keyring locked")
	before := b.snapshot()
	k := bigKey(5500)
	err := s.Save("host-1", SavePatch{PrivateKey: &k})
	if err == nil {
		t.Fatal("Save must refuse when pending refs exceed the protocol limit")
	}
	if strings.Contains(err.Error(), "-----BEGIN") {
		t.Fatalf("refusal error must be secret-free: %v", err)
	}
	if after := b.snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("refused save must leave the backend untouched: before=%d entries after=%d", len(before), len(after))
	}
	// The old primary keeps every ref: nothing was dropped by the refusal.
	raw, err = b.Get(ServiceName, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("old primary corrupted by the refused save: %v", err)
	}
	if len(m.Previous) != maxPrevious {
		t.Fatalf("old primary must keep all %d refs, got %d", maxPrevious, len(m.Previous))
	}
}

// TestSavePrecommitDrainNeverDeletesLiveGeneration is the live-generation
// gate: when the backlog has hit maxPrevious, the pre-commit drain must only
// touch old.Previous refs — never the old current generation the primary
// still points at. If the superseding save then fails (chunk write or
// manifest commit), the old credential must still read back exactly and its
// current chunks must still exist.
func TestSavePrecommitDrainNeverDeletesLiveGeneration(t *testing.T) {
	cases := []struct {
		name      string
		failSetOn string // the account whose Set fails mid-save
	}{
		{"new chunk write fails", chunkAccount("host-1", "gen-1", 0)},
		{"manifest commit fails", "host-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newFakeBackend()
			b.maxValue = 2560
			s := New(b)
			s.gen = genSequence("gen-1")

			// Seed a v2 primary at maxPrevious: live generation gen-0 with a
			// real chunk plus 16 pending backlog refs whose chunks are already
			// gone (ErrNotFound counts as cleaned).
			var sb strings.Builder
			for i := 0; i < maxPrevious; i++ {
				if i > 0 {
					sb.WriteString(",")
				}
				fmt.Fprintf(&sb, `{"generation":"old-gen-%d","chunks":1}`, i)
			}
			raw := fmt.Sprintf(`{"version":2,"generation":"gen-0","chunks":1,"previous":[%s]}`, sb.String())
			if err := b.Set(ServiceName, "host-1", raw); err != nil {
				t.Fatal(err)
			}
			if err := b.Set(ServiceName, chunkAccount("host-1", "gen-0", 0), chunkPayloadValue(t, 0, `{"version":1,"password":"original-pw"}`)); err != nil {
				t.Fatal(err)
			}

			// Every backlog ref drains cleanly pre-commit; without the fix the
			// drain would delete the live gen-0 chunk too, so the failed save
			// would corrupt the old Get.
			b.failSetAccount = tc.failSetOn
			big := bigKey(5500)
			if err := s.Save("host-1", SavePatch{PrivateKey: &big}); err == nil {
				t.Fatal("Save must surface the injected failure")
			}

			got, found, err := s.Get("host-1")
			if err != nil {
				t.Fatalf("old credential corrupt after failed save: %v", err)
			}
			if !found || got.Password != "original-pw" || got.PrivateKey != "" {
				t.Fatalf("old credential lost after failed save: %+v (found=%v)", got, found)
			}
			if _, err := b.Get(ServiceName, chunkAccount("host-1", "gen-0", 0)); err != nil {
				t.Fatalf("live gen-0 chunk deleted by the failed save: %v", err)
			}
		})
	}
}

// TestGetManifestWithExcessivePreviousIsCorrupt: a manifest whose previous
// ref list exceeds the protocol cap must surface as a corrupt error, never an
// unbounded read.
func TestGetManifestWithExcessivePreviousIsCorrupt(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	var sb strings.Builder
	for i := 0; i < 17; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"generation":"gen-%d","chunks":1}`, i)
	}
	raw := fmt.Sprintf(`{"version":2,"generation":"gen-cur","chunks":1,"previous":[%s]}`, sb.String())
	if err := b.Set(ServiceName, "host-1", raw); err != nil {
		t.Fatal(err)
	}
	if err := b.Set(ServiceName, chunkAccount("host-1", "gen-cur", 0), chunkPayloadValue(t, 0, `{"version":1,"password":"pw"}`)); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Get("host-1")
	if err == nil {
		t.Fatal("an over-limit previous list must be corrupt, not read back")
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("over-limit previous list must surface as ErrCorrupt, got %v", err)
	}
}

// TestLegacyV2ManifestWithoutPreviousIsCompatible: a version-2 primary
// written by an older build (no previous field) must still read back, and a
// save superseding it must drain its generation without losing the value.
func TestLegacyV2ManifestWithoutPreviousIsCompatible(t *testing.T) {
	b := newFakeBackend()
	s := New(b)
	// Seed a legacy v2 primary with its chunks, exactly as a pre-Previous
	// build would have stored them.
	seedManifest(t, b, "host-1", "gen-1", 2)
	if err := b.Set(ServiceName, chunkAccount("host-1", "gen-1", 0), chunkPayloadValue(t, 0, `{"version":1,"password":"legacy"}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.Set(ServiceName, chunkAccount("host-1", "gen-1", 1), chunkPayloadValue(t, 1, ``)); err != nil {
		t.Fatal(err)
	}
	got, found, err := s.Get("host-1")
	if err != nil || !found || got.Password != "legacy" {
		t.Fatalf("legacy manifest must read back: found=%v err=%v got=%+v", found, err, got)
	}
	s.gen = genSequence("gen-2")
	if err := s.Save("host-1", SavePatch{Password: strPtr("new-pw")}); err != nil {
		t.Fatalf("Save over a legacy manifest: %v", err)
	}
	got, found, err = s.Get("host-1")
	if err != nil || !found || got.Password != "new-pw" {
		t.Fatalf("Get after superseding legacy manifest: found=%v err=%v got=%+v", found, err, got)
	}
	// The legacy generation was drained on the way out.
	if _, err := b.Get(ServiceName, chunkAccount("host-1", "gen-1", 0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy generation must be drained after the save, got err=%v", err)
	}
}
