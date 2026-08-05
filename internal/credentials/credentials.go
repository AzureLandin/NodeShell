// Package credentials stores per-host connection secrets in the OS keyring
// (Windows Credential Manager, macOS Keychain, Linux Secret Service) instead
// of the Electron build's encrypted credentials.json vault.
//
// The old vault is intentionally not migrated or decrypted: this package never
// receives its path, so the file stays byte-identical for rollback. A host
// whose persisted credentialsSaved flag is true but that has no keyring entry
// is treated as having no saved credentials (the App facade normalises the
// flag away in HostsList).
package credentials

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	"github.com/google/uuid"

	"nodeshell/internal/apperror"
)

// ServiceName is the fixed, stable keyring service name shared by all
// platforms. Account names are host IDs, which are UUIDs, so service+account
// is unambiguous within the OS keyring.
const ServiceName = "NodeShell"

// Sentinel errors the production backend (internal/credentials/keyring) maps
// onto so the domain can tell not-found and size failures apart without
// depending on the library.
var (
	// ErrNotFound is returned by Backend.Get/Delete when no entry exists.
	ErrNotFound = errors.New("credentials: entry not found")
	// ErrTooLarge is returned by Backend.Set when the value exceeds the OS
	// keyring's size limit (Windows caps credential blobs at 2560 bytes).
	ErrTooLarge = errors.New("credentials: secret too large for the OS keyring")
	// ErrCorrupt marks an unrecoverable stored entry (inline or chunked). The
	// facade uses it to tell "this host's secret is broken" apart from "the
	// backend is down" without parsing error messages; Clear is the recovery.
	ErrCorrupt = errors.New("credentials: stored entry corrupt")
)

// Secrets is the versioned payload stored in the keyring for one host.
// PrivateKey holds the key file CONTENT, not a path.
type Secrets struct {
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
}

// SavePatch carries the fields to merge into an existing entry; nil means
// "leave unchanged". Only non-nil fields are written, so a partial save never
// drops the other field already stored.
type SavePatch struct {
	Password   *string
	PrivateKey *string
}

// SavePayload is the Wails-bound JSON shape the frontend sends
// (Electron save payload: {password?, privateKeyPath?}). The facade resolves
// privateKeyPath to content before calling Store.Save.
type SavePayload struct {
	Password       *string `json:"password"`
	PrivateKeyPath *string `json:"privateKeyPath"`
}

// PrivateKeyReader safely resolves and reads a private key file. The App
// facade injects a home-boundary-checked implementation; tests inject their
// own reader over a temp directory.
type PrivateKeyReader func(path string) (string, error)

// Backend is the narrow keyring seam the Store depends on. The production
// adapter only wires this to the OS keyring library (Set/Get/Delete); tests
// inject an in-memory fake.
type Backend interface {
	Set(service, account, value string) error
	Get(service, account string) (string, error)
	Delete(service, account string) error
	// Available reports whether the backend is likely usable. Production has
	// no side-effect-free probe and returns true ("attempts are available");
	// real failures surface on Save. A fake can return false to exercise the
	// save gate.
	Available() bool
}

// Error carries the stable code the frontend maps onto AppError.code. All
// keyring errors currently use apperror.Unknown; messages are generic and
// never embed password, private key content, or keyring payload. cause lets
// callers classify the failure with errors.Is (ErrCorrupt vs ErrNotFound vs a
// plain backend failure) without depending on the message text.
type Error struct {
	Code    string
	Message string
	cause   error
}

func (e *Error) Error() string { return e.Message }

// ErrorCode lets apperror.Format carry the stable code across IPC.
func (e *Error) ErrorCode() string { return e.Code }

// Unwrap exposes the sentinel cause so errors.Is can classify corrupt entries.
func (e *Error) Unwrap() error { return e.cause }

// corruptErr builds the observable, secret-free error for an unrecoverable
// stored entry; it always wraps ErrCorrupt.
func corruptErr() error {
	return &Error{Code: apperror.Unknown, Message: "Stored credentials are corrupt", cause: ErrCorrupt}
}

// payload is the logical on-disk envelope shared by inline (v1) and chunked
// (v2) storage: a v2 save stores exactly these bytes split across chunk
// accounts, so decoding is unified.
type payload struct {
	Version    int    `json:"version"`
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"privateKey,omitempty"`
}

// Chunking limits. The OS keyring caps a single entry at 2560 bytes on
// Windows (CRED_MAX_CREDENTIAL_BLOB_SIZE) and roughly 4096 on macOS, so large
// secrets are split into chunks whose stored size stays safely under every
// platform limit. Generation-per-save account names make chunks unambiguous
// and unenumerable.
const (
	// inlineMaxBytes is the largest logical payload still stored as one
	// version-1 inline entry, keeping existing small entries byte-compatible.
	inlineMaxBytes = 2000
	// manifestMaxBytes bounds the marshaled v2 manifest, which is stored as a
	// single primary entry and must fit the same inline platform limit.
	manifestMaxBytes = inlineMaxBytes
	// chunkMaxBytes bounds one chunk's decoded content. Base64 encoding plus
	// the JSON wrapper keeps a full chunk (~2.4KB) well under the Windows
	// 2560-byte cap and macOS's ~4096-char limit.
	chunkMaxBytes = 1800
	// maxChunks bounds the chunk count a manifest may reference, so a
	// malicious manifest can never cause an unbounded read.
	maxChunks = 64
	// maxPrevious bounds the pending-cleanup refs a manifest may carry, so an
	// accumulating cleanup backlog can never inflate the primary (or a
	// malicious manifest the read path) without bound.
	maxPrevious = 16
	// maxAssembledBytes bounds the reassembled payload size.
	maxAssembledBytes = 64 * 1024
)

// chunkRef names one superseded generation still awaiting chunk cleanup. It is
// the unit of the manifest's Previous list: Save carries every pending ref
// into the next manifest so a cleanup failure can never orphan a generation.
type chunkRef struct {
	Generation string `json:"generation"`
	Chunks     int    `json:"chunks"`
}

// manifest is the version-2 primary value: it points at a generation's chunks
// instead of carrying the secrets inline. Generation is a fresh random id per
// save; chunk accounts derive from hostID+generation+index, so no secret
// content ever appears in an account name. Previous lists superseded
// generations whose chunks still await best-effort cleanup; the field is
// optional so manifests written by older builds (without it) stay compatible,
// and Get never reads Previous — it is bookkeeping only.
type manifest struct {
	Version    int        `json:"version"`
	Generation string     `json:"generation"`
	Chunks     int        `json:"chunks"`
	Previous   []chunkRef `json:"previous,omitempty"`
}

// chunkValue is one chunk's stored value. The payload bytes are base64-encoded
// so binary-safe storage is guaranteed; the index detects reordered or
// duplicated chunks.
type chunkValue struct {
	Index int    `json:"index"`
	Data  string `json:"data"`
}

// chunkAccount derives the keyring account for one chunk of hostID's
// generation. hostIDs and generations are UUIDs, so the composed name can
// never collide with the primary account (hostID) nor with another
// generation's chunks.
func chunkAccount(hostID, generation string, index int) string {
	return hostID + "#" + generation + "#" + strconv.Itoa(index)
}

// parseManifest reports whether raw is a version-2 manifest. Limits are
// enforced by the readers (decodeChunks), never here, so a truncated manifest
// still parses and Clear can remove the primary.
func parseManifest(raw string) (*manifest, bool) {
	var m manifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil || m.Version != 2 {
		return nil, false
	}
	return &m, true
}

// Store serialises keyring access for one service. A single per-service mutex
// serialises read-modify-write transactions (Save merges with the existing
// entry), so concurrent Saves for the same host can neither race nor drop a
// field. The lock is instance-scoped, never a package-level global.
type Store struct {
	backend Backend
	service string
	mu      sync.Mutex
	// gen produces the random generation for a chunked save. Tests inject a
	// deterministic generator; production uses a fresh UUID per save so chunk
	// accounts can never collide across generations.
	gen func() string
}

// New returns a Store backed by backend under the stable service name.
func New(backend Backend) *Store {
	return &Store{backend: backend, service: ServiceName, gen: uuid.NewString}
}

// Get returns the secrets for hostID. A missing entry returns (Secrets{},
// false, nil); a corrupt entry (inline or chunked) is an observable error
// whose message does not leak the stored payload.
func (s *Store) Get(hostID string) (Secrets, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(hostID)
}

// Save merges patch into the entry for hostID and stores it. Small payloads
// stay a version-1 inline entry; larger ones — or any save superseding a
// chunked generation — are stored as a version-2 manifest plus chunks,
// atomically: chunks are written first, then the manifest (the commit point),
// so the previous value stays readable at every step and a failed save never
// leaves a half-written generation. A patch with neither field set is rejected
// (observable error) so a caller can never mark a host saved without writing a
// real entry. A corrupt existing entry is also an observable error: Clear is
// the recovery path.
func (s *Store) Save(hostID string, patch SavePatch) error {
	if patch.Password == nil && patch.PrivateKey == nil {
		return &Error{Code: apperror.Unknown, Message: "No credential provided to save"}
	}
	if !s.backend.Available() {
		return &Error{Code: apperror.Unknown, Message: "Secure credential storage is unavailable"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, _, old, err := s.readLocked(hostID)
	if err != nil {
		return err
	}
	if patch.Password != nil {
		existing.Password = *patch.Password
	}
	if patch.PrivateKey != nil {
		existing.PrivateKey = *patch.PrivateKey
	}
	data, err := json.Marshal(payload{Version: 1, Password: existing.Password, PrivateKey: existing.PrivateKey})
	if err != nil {
		return &Error{Code: apperror.Unknown, Message: "Failed to encode credentials"}
	}
	// A fresh small payload stays a version-1 inline entry. Once a chunked
	// generation exists it must be superseded by a manifest carrying the old
	// refs: the inline format cannot reference pending chunks, so an inline
	// save over a v2 manifest would orphan them on a cleanup failure.
	if len(data) <= inlineMaxBytes && old == nil {
		if err := s.backend.Set(s.service, hostID, string(data)); err != nil {
			return s.backendError(err)
		}
		return nil
	}
	return s.saveChunked(hostID, old, data)
}

// saveChunked stores data across a fresh generation's chunks and commits the
// manifest as the new primary value. Any failure before the commit deletes
// the chunks written so far and leaves the previous value untouched; the only
// pre-commit cleanup is draining the backlog (old.Previous), never the live
// generation, so the old Get stays readable at every step before the commit.
// After the commit the superseded generations are drained best-effort and
// the manifest is rewritten without the refs whose chunks are gone, so a
// cleanup failure can never be mistaken for a failed save nor orphan old
// chunks.
func (s *Store) saveChunked(hostID string, old *manifest, data []byte) error {
	// decodeChunks refuses to assemble more than maxAssembledBytes, so a
	// payload over that limit must be rejected before any chunk write —
	// otherwise Get would read the committed value back as corrupt.
	if len(data) > maxAssembledBytes {
		return &Error{Code: apperror.Unknown, Message: "Stored credentials exceed the supported size limit"}
	}
	n := (len(data) + chunkMaxBytes - 1) / chunkMaxBytes
	if n > maxChunks {
		return &Error{Code: apperror.Unknown, Message: "Stored credentials exceed the OS keyring size limit"}
	}
	generation := s.gen()
	// Pre-commit cleanup may only touch the backlog (old.Previous): the old
	// current generation is still the live value the primary points at, so
	// draining it here would corrupt the old Get if this save then fails (an
	// error or crash before the commit must always leave the old credential
	// fully readable). The new manifest inherits whatever backlog could not be
	// drained plus the superseded current generation, so old chunks stay
	// addressable even when their cleanup fails.
	var backlog []chunkRef
	if old != nil {
		backlog = old.Previous
	}
	previous := buildPrevious(s.drainPrevious(hostID, backlog), old)
	if len(previous) > maxPrevious {
		// Persistent cleanup failures have accumulated more pending
		// generations than the protocol allows. Refuse without touching the
		// old primary — no ref is ever dropped.
		return &Error{Code: apperror.Unknown, Message: "Stored credentials have too many pending chunks; clear and retry"}
	}
	written := 0
	for i := 0; i < n; i++ {
		end := (i + 1) * chunkMaxBytes
		if end > len(data) {
			end = len(data)
		}
		value, err := json.Marshal(chunkValue{Index: i, Data: base64.StdEncoding.EncodeToString(data[i*chunkMaxBytes : end])})
		if err != nil {
			s.cleanupNewChunks(hostID, generation, written)
			return &Error{Code: apperror.Unknown, Message: "Failed to encode credentials"}
		}
		if err := s.backend.Set(s.service, chunkAccount(hostID, generation, i), string(value)); err != nil {
			s.cleanupNewChunks(hostID, generation, written)
			return s.backendError(err)
		}
		written++
	}
	manifestData, err := json.Marshal(manifest{Version: 2, Generation: generation, Chunks: n, Previous: previous})
	if err != nil {
		s.cleanupNewChunks(hostID, generation, written)
		return &Error{Code: apperror.Unknown, Message: "Failed to encode credentials"}
	}
	if len(manifestData) > manifestMaxBytes {
		s.cleanupNewChunks(hostID, generation, written)
		return &Error{Code: apperror.Unknown, Message: "Stored credentials exceed the OS keyring size limit"}
	}
	// The manifest write is the commit point: only now is the new value
	// visible.
	if err := s.backend.Set(s.service, hostID, string(manifestData)); err != nil {
		s.cleanupNewChunks(hostID, generation, written)
		return s.backendError(err)
	}
	// Committed: drain the superseded generations best-effort and rewrite the
	// manifest without the refs whose chunks are all gone. A rewrite failure
	// leaves the committed manifest with its full ref list intact for a later
	// Save or Clear to retry.
	if len(previous) > 0 {
		pending := s.drainPrevious(hostID, previous)
		if len(pending) != len(previous) {
			s.rewritePrevious(hostID, generation, n, pending)
		}
	}
	return nil
}

// cleanupNewChunks best-effort deletes chunks [0, written) of a generation
// that was never committed. Deletes are retried a bounded number of times so
// a transient failure does not orphan the whole generation; a persistent
// failure can still orphan it (an uncommitted generation is never referenced,
// so no protocol change can make it reachable again).
func (s *Store) cleanupNewChunks(hostID, generation string, written int) {
	for attempt := 0; attempt < 3; attempt++ {
		remaining := false
		for i := 0; i < written; i++ {
			if err := s.backend.Delete(s.service, chunkAccount(hostID, generation, i)); err != nil && !errors.Is(err, ErrNotFound) {
				remaining = true
			}
		}
		if !remaining {
			return
		}
	}
}

// buildPrevious collects the refs a new manifest must carry over: the
// backlog refs (old.Previous) that could not be drained before committing
// plus the superseded current generation. Only the backlog is ever drained
// pre-commit; the old current generation is never deleted until the new
// manifest has committed, so a failed save always leaves the old value
// readable. Generation ids are fresh per save, so duplicates can only come
// from a corrupt manifest — collapsing them keeps the list bounded.
func buildPrevious(pending []chunkRef, old *manifest) []chunkRef {
	var refs []chunkRef
	seen := map[string]bool{}
	for _, r := range pending {
		if r.Generation == "" || seen[r.Generation] {
			continue
		}
		seen[r.Generation] = true
		refs = append(refs, r)
	}
	if old != nil && old.Generation != "" && !seen[old.Generation] {
		refs = append(refs, chunkRef{Generation: old.Generation, Chunks: old.Chunks})
	}
	return refs
}

// drainPrevious best-effort deletes the chunks referenced by refs and returns
// the refs that could not be fully cleaned, so callers keep them in the
// manifest. Chunks already missing count as deleted. A malformed ref is never
// dropped: it stays pending rather than losing addressability.
func (s *Store) drainPrevious(hostID string, refs []chunkRef) []chunkRef {
	var pending []chunkRef
	for _, ref := range refs {
		if ref.Generation == "" || ref.Chunks <= 0 {
			pending = append(pending, ref)
			continue
		}
		complete := true
		for i := 0; i < ref.Chunks && i < maxChunks; i++ {
			if err := s.backend.Delete(s.service, chunkAccount(hostID, ref.Generation, i)); err != nil && !errors.Is(err, ErrNotFound) {
				complete = false
				break
			}
		}
		if !complete {
			pending = append(pending, ref)
		}
	}
	return pending
}

// rewritePrevious best-effort replaces the primary manifest with one carrying
// only the still-pending refs after a drain. A failure leaves the previously
// committed manifest (full refs) intact; a later Save or Clear retries.
func (s *Store) rewritePrevious(hostID, generation string, chunks int, pending []chunkRef) {
	data, err := json.Marshal(manifest{Version: 2, Generation: generation, Chunks: chunks, Previous: pending})
	if err != nil || len(data) > manifestMaxBytes {
		return
	}
	_ = s.backend.Set(s.service, hostID, string(data))
}

// Clear deletes the entry for hostID: pending generations first, then the
// current generation's chunks, then the primary. A missing entry counts as
// success (idempotent); real backend errors stay observable so HostsRemove
// stops instead of leaving a host with no recoverable secret. The primary is
// only deleted once every chunk it references (current and pending) is gone,
// so a mid-way failure keeps it — with its refs — for a retry, and a failure
// in the pending phase leaves the current value fully readable. A primary
// that is not a valid manifest is still deleted — orphaned chunks of an
// unparseable manifest are not discoverable, and generation randomness means
// chunk accounts were never enumerable.
func (s *Store) Clear(hostID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.backend.Get(s.service, hostID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return s.backendError(err)
	}
	if m, ok := parseManifest(raw); ok {
		for pi := 0; pi < len(m.Previous) && pi < maxPrevious; pi++ {
			ref := m.Previous[pi]
			for i := 0; i < ref.Chunks && i < maxChunks; i++ {
				if err := s.backend.Delete(s.service, chunkAccount(hostID, ref.Generation, i)); err != nil {
					if errors.Is(err, ErrNotFound) {
						continue
					}
					// The primary is retained so the secret stays recoverable
					// and a retry can finish the removal.
					return s.backendError(err)
				}
			}
		}
		for i := 0; i < m.Chunks && i < maxChunks; i++ {
			if err := s.backend.Delete(s.service, chunkAccount(hostID, m.Generation, i)); err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				// The primary is retained so the secret stays recoverable
				// and a retry can finish the removal.
				return s.backendError(err)
			}
		}
	}
	if err := s.backend.Delete(s.service, hostID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return s.backendError(err)
	}
	return nil
}

// Available reports whether the backend is likely usable (see Backend).
func (s *Store) Available() bool {
	return s.backend.Available()
}

// getLocked reads and decodes the entry. Callers must hold the mutex.
func (s *Store) getLocked(hostID string) (Secrets, bool, error) {
	secrets, found, _, err := s.readLocked(hostID)
	return secrets, found, err
}

// readLocked decodes the current secrets for hostID and returns the old chunk
// manifest (nil for inline or missing entries) so Save can clean up
// superseded chunks after committing a new value. Callers must hold the
// mutex.
func (s *Store) readLocked(hostID string) (Secrets, bool, *manifest, error) {
	raw, err := s.backend.Get(s.service, hostID)
	if errors.Is(err, ErrNotFound) {
		return Secrets{}, false, nil, nil
	}
	if err != nil {
		return Secrets{}, false, nil, s.backendError(err)
	}
	if m, ok := parseManifest(raw); ok {
		secrets, err := s.decodeChunks(hostID, m)
		if err != nil {
			return Secrets{}, false, nil, err
		}
		return secrets, true, m, nil
	}
	var p payload
	if err := json.Unmarshal([]byte(raw), &p); err != nil || p.Version != 1 {
		return Secrets{}, false, nil, corruptErr()
	}
	return Secrets{Password: p.Password, PrivateKey: p.PrivateKey}, true, nil, nil
}

// decodeChunks reassembles and decodes a chunked payload. Hard limits on chunk
// count, per-chunk size, total size and the pending-ref list keep a malicious
// manifest from causing an unbounded read; every violation surfaces as a
// secret-free corrupt error. Get reads only the current generation — Previous
// is cleanup bookkeeping and never contributes to the decoded value.
func (s *Store) decodeChunks(hostID string, m *manifest) (Secrets, error) {
	if m.Generation == "" || m.Chunks <= 0 || m.Chunks > maxChunks || len(m.Previous) > maxPrevious {
		return Secrets{}, corruptErr()
	}
	assembled := make([]byte, 0, min(m.Chunks*chunkMaxBytes, maxAssembledBytes))
	for i := 0; i < m.Chunks; i++ {
		raw, err := s.backend.Get(s.service, chunkAccount(hostID, m.Generation, i))
		if errors.Is(err, ErrNotFound) {
			return Secrets{}, corruptErr()
		}
		if err != nil {
			return Secrets{}, s.backendError(err)
		}
		var c chunkValue
		if err := json.Unmarshal([]byte(raw), &c); err != nil || c.Index != i {
			return Secrets{}, corruptErr()
		}
		part, err := base64.StdEncoding.DecodeString(c.Data)
		if err != nil {
			return Secrets{}, corruptErr()
		}
		if len(part) > chunkMaxBytes || len(assembled)+len(part) > maxAssembledBytes {
			return Secrets{}, corruptErr()
		}
		assembled = append(assembled, part...)
	}
	var p payload
	if err := json.Unmarshal(assembled, &p); err != nil || p.Version != 1 {
		return Secrets{}, corruptErr()
	}
	return Secrets{Password: p.Password, PrivateKey: p.PrivateKey}, nil
}

// backendError wraps a raw backend failure into an observable, secret-free
// Error. ErrTooLarge gets a specific hint; everything else stays generic.
func (s *Store) backendError(err error) error {
	if errors.Is(err, ErrTooLarge) {
		return &Error{Code: apperror.Unknown, Message: "Stored credentials exceed the OS keyring size limit"}
	}
	return &Error{Code: apperror.Unknown, Message: "Failed to access the OS credential store"}
}
