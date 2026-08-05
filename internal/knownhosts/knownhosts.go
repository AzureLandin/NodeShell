// Package knownhosts persists verified host key fingerprints in
// known_hosts.json as a host:port -> fingerprint map, compatible with the
// Electron build.
package knownhosts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"nodeshell/internal/apperror"
	"nodeshell/internal/atomicfile"
)

// ErrNotLoaded is returned by the synchronous check when the cache has not
// been loaded yet (the async Check auto-loads, mirroring the TS store).
var ErrNotLoaded = errors.New("KnownHosts cache not loaded; call load() first")

// Error carries the stable config error code the frontend maps onto
// AppError.code (CONFIG_READ_FAILED, CONFIG_WRITE_FAILED). Messages are
// generic — a filesystem path is never surfaced to the frontend.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrorCode lets apperror.Format carry the stable code across IPC.
func (e *Error) ErrorCode() string { return e.Code }

// CheckResult mirrors the TS HostKeyCheck union: status is one of
// "unknown", "ok" or "changed"; Previous is only set for "changed".
type CheckResult struct {
	Status   string `json:"status"`
	Previous string `json:"previous,omitempty"`
}

// Store keeps a fingerprint per "host:port" key.
type Store struct {
	mu     sync.Mutex
	path   string
	cache  map[string]string // nil until Load
	writer func(path string, v any) error
}

// New returns a Store backed by <dir>/known_hosts.json.
func New(dir string) *Store {
	return &Store{path: filepath.Join(dir, "known_hosts.json")}
}

// write persists v atomically through the package-internal writer seam
// (defaults to atomicfile.WriteJSON; tests inject failures).
func (s *Store) write(v any) error {
	if s.writer != nil {
		return s.writer(s.path, v)
	}
	return atomicfile.WriteJSON(s.path, v)
}

// Load reads the store into memory (call once at process start).
func (s *Store) Load() error {
	cache, err := s.read()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = cache
	return nil
}

// Check returns the host-key verdict, auto-loading when not loaded yet.
func (s *Store) Check(host string, port int, fingerprint string) (CheckResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		cache, err := s.read()
		if err != nil {
			return CheckResult{}, err
		}
		s.cache = cache
	}
	return s.checkLocked(host, port, fingerprint), nil
}

// CheckSync returns the verdict from memory without touching disk; it fails
// when the cache is not loaded (the TS checkSync throws in that case).
func (s *Store) CheckSync(host string, port int, fingerprint string) (CheckResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		return CheckResult{}, ErrNotLoaded
	}
	return s.checkLocked(host, port, fingerprint), nil
}

// Remember records the fingerprint for host:port and persists the store.
// Copy-on-write: the cache is replaced only after the write succeeds, so a
// failed write never leaves a trusted-in-memory, absent-on-disk fingerprint.
func (s *Store) Remember(host string, port int, fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		cache, err := s.read()
		if err != nil {
			return err
		}
		s.cache = cache
	}
	next := make(map[string]string, len(s.cache)+1)
	for k, v := range s.cache {
		next[k] = v
	}
	next[fmt.Sprintf("%s:%d", host, port)] = fingerprint
	if err := s.write(next); err != nil {
		return &Error{Code: apperror.ConfigWriteFailed, Message: "Failed to write known hosts"}
	}
	s.cache = next
	return nil
}

func (s *Store) checkLocked(host string, port int, fingerprint string) CheckResult {
	previous, ok := s.cache[fmt.Sprintf("%s:%d", host, port)]
	if !ok {
		return CheckResult{Status: "unknown"}
	}
	if previous == fingerprint {
		return CheckResult{Status: "ok"}
	}
	return CheckResult{Status: "changed", Previous: previous}
}

func (s *Store) read() (map[string]string, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, &Error{Code: apperror.ConfigReadFailed, Message: "Failed to read known hosts"}
	}
	var file map[string]string
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, &Error{Code: apperror.ConfigReadFailed, Message: "Known hosts file is corrupt"}
	}
	return file, nil
}
