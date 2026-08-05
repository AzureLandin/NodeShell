// Package hosts persists host configurations in hosts.json, byte-compatible
// with the Electron build's {"hosts": HostConfig[]} layout.
package hosts

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"

	"nodeshell/internal/apperror"
	"nodeshell/internal/atomicfile"
)

// HostConfig mirrors src/shared/types.ts HostConfig. Field order and JSON tags
// keep the persisted file byte-compatible with the Electron build.
type HostConfig struct {
	Id                  string `json:"id"`
	Name                string `json:"name"`
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	AuthMethod          string `json:"authMethod"`
	PrivateKeyPath      string `json:"privateKeyPath,omitempty"`
	CredentialsPrompted bool   `json:"credentialsPrompted"`
	CredentialsSaved    bool   `json:"credentialsSaved"`
}

// HostInput mirrors HostInput = Omit<HostConfig, 'id'>.
type HostInput struct {
	Name                string `json:"name"`
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	AuthMethod          string `json:"authMethod"`
	PrivateKeyPath      string `json:"privateKeyPath,omitempty"`
	CredentialsPrompted bool   `json:"credentialsPrompted"`
	CredentialsSaved    bool   `json:"credentialsSaved"`
}

// Patch mirrors the Partial<HostInput> passed to hosts.update; nil fields are
// left unchanged.
type Patch struct {
	Name                *string `json:"name"`
	Host                *string `json:"host"`
	Port                *int    `json:"port"`
	Username            *string `json:"username"`
	AuthMethod          *string `json:"authMethod"`
	PrivateKeyPath      *string `json:"privateKeyPath"`
	CredentialsPrompted *bool   `json:"credentialsPrompted"`
	CredentialsSaved    *bool   `json:"credentialsSaved"`
}

// Error carries the stable config error code the frontend maps onto
// AppError.code (CONFIG_READ_FAILED, CONFIG_WRITE_FAILED, UNKNOWN).
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// ErrorCode lets apperror.Format carry the stable code across IPC.
func (e *Error) ErrorCode() string { return e.Code }

type hostsFile struct {
	Hosts []HostConfig `json:"hosts"`
}

// Store serves hosts from an in-memory cache after the first read and
// persists every mutation through the atomic writer.
//
// Concurrency: each read-modify-write transaction (Create/Update/Remove)
// holds the exclusive lock for its whole duration, so two writers can never
// read the same cache and clobber each other (which also made concurrent
// os.Rename calls collide on Windows with "Access is denied"). Reads copy
// under the shared lock; the first load runs under the exclusive lock so the
// cache pointer is published safely.
type Store struct {
	path  string
	mu    sync.RWMutex
	cache *hostsFile
}

// New returns a Store backed by <dir>/hosts.json.
func New(dir string) *Store {
	return &Store{path: filepath.Join(dir, "hosts.json")}
}

// List returns a copy of all hosts.
func (s *Store) List() ([]HostConfig, error) {
	if err := s.ensureLoaded(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]HostConfig, len(s.cache.Hosts))
	copy(out, s.cache.Hosts)
	return out, nil
}

// GetByID returns a copy of the host with the given id. A read failure is
// returned as an error (like the TS getById, which rejects with
// CONFIG_READ_FAILED); ok=false with a nil error means the id is unknown.
func (s *Store) GetByID(id string) (HostConfig, bool, error) {
	if err := s.ensureLoaded(); err != nil {
		return HostConfig{}, false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.cache.Hosts {
		if h.Id == id {
			return h, true, nil
		}
	}
	return HostConfig{}, false, nil
}

// Create appends a new host with a generated UUID id and persists it.
func (s *Store) Create(input HostInput) (HostConfig, error) {
	if err := s.ensureLoaded(); err != nil {
		return HostConfig{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	host := HostConfig{
		Id:                  uuid.NewString(),
		Name:                input.Name,
		Host:                input.Host,
		Port:                input.Port,
		Username:            input.Username,
		AuthMethod:          input.AuthMethod,
		PrivateKeyPath:      input.PrivateKeyPath,
		CredentialsPrompted: input.CredentialsPrompted,
		CredentialsSaved:    input.CredentialsSaved,
	}
	s.cache.Hosts = append(s.cache.Hosts, host)
	if err := s.writeLocked(); err != nil {
		return HostConfig{}, err
	}
	return host, nil
}

// Update applies patch to the host with the given id, keeping the id stable.
func (s *Store) Update(id string, patch Patch) (HostConfig, error) {
	if err := s.ensureLoaded(); err != nil {
		return HostConfig{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.cache.Hosts {
		if s.cache.Hosts[i].Id == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return HostConfig{}, &Error{Code: apperror.Unknown, Message: fmt.Sprintf("Host not found: %s", id)}
	}
	updated := s.cache.Hosts[idx]
	updated.Id = id
	applyPatch(&updated, patch)
	s.cache.Hosts[idx] = updated
	if err := s.writeLocked(); err != nil {
		return HostConfig{}, err
	}
	return updated, nil
}

// Remove deletes the host with the given id.
func (s *Store) Remove(id string) error {
	if err := s.ensureLoaded(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.cache.Hosts[:0]
	for _, h := range s.cache.Hosts {
		if h.Id != id {
			kept = append(kept, h)
		}
	}
	s.cache.Hosts = kept
	return s.writeLocked()
}

func applyPatch(h *HostConfig, p Patch) {
	if p.Name != nil {
		h.Name = *p.Name
	}
	if p.Host != nil {
		h.Host = *p.Host
	}
	if p.Port != nil {
		h.Port = *p.Port
	}
	if p.Username != nil {
		h.Username = *p.Username
	}
	if p.AuthMethod != nil {
		h.AuthMethod = *p.AuthMethod
	}
	if p.PrivateKeyPath != nil {
		h.PrivateKeyPath = *p.PrivateKeyPath
	}
	if p.CredentialsPrompted != nil {
		h.CredentialsPrompted = *p.CredentialsPrompted
	}
	if p.CredentialsSaved != nil {
		h.CredentialsSaved = *p.CredentialsSaved
	}
}

// ensureLoaded populates the cache on first use. It takes no lock; the
// first-load path runs under the exclusive lock so the cache pointer is never
// read while another goroutine writes it.
func (s *Store) ensureLoaded() error {
	s.mu.RLock()
	if s.cache != nil {
		s.mu.RUnlock()
		return nil
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		file, err := loadFile(s.path)
		if err != nil {
			return err
		}
		s.cache = file
	}
	return nil
}

func loadFile(path string) (*hostsFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &hostsFile{Hosts: []HostConfig{}}, nil
		}
		return nil, &Error{Code: apperror.ConfigReadFailed, Message: err.Error()}
	}
	var file hostsFile
	// Missing or non-array "hosts" is corrupt (matches the TS store); null is
	// not an array either, so a nil slice must be rejected too.
	if err := json.Unmarshal(raw, &file); err != nil || file.Hosts == nil {
		return nil, &Error{Code: apperror.ConfigReadFailed, Message: "Hosts file is corrupt"}
	}
	return &file, nil
}

// writeLocked persists the cache. The caller must hold the exclusive lock.
// Like the TS store, the in-memory cache reflects the mutation even when the
// disk write fails; the returned error keeps the failure observable.
func (s *Store) writeLocked() error {
	if err := atomicfile.WriteJSON(s.path, s.cache); err != nil {
		return &Error{Code: apperror.ConfigWriteFailed, Message: err.Error()}
	}
	return nil
}
