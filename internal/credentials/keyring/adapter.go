// Package keyring provides the production credentials.Backend backed by the
// OS keyring through github.com/zalando/go-keyring's narrow Set/Get/Delete
// interface (Windows Credential Manager, macOS Keychain, Linux Secret
// Service). The adapter holds no state and adds no crypto of its own; the
// library stores the value opaquely.
//
// There is no side-effect-free "is available" probe, so Available always
// reports true ("attempts are available"); a real failure — e.g. Linux
// without a Secret Service daemon — surfaces as an observable error on Set.
package keyring

import (
	"errors"

	zalando "github.com/zalando/go-keyring"

	"nodeshell/internal/credentials"
)

// Backend adapts the OS keyring to the credentials domain.
type Backend struct{}

// NewBackend returns a Backend using the OS keyring.
func NewBackend() *Backend {
	return &Backend{}
}

// Set stores value under service/account in the OS keyring.
func (b *Backend) Set(service, account, value string) error {
	return mapErr(zalando.Set(service, account, value))
}

// Get returns the value stored under service/account, mapping a missing
// entry onto credentials.ErrNotFound.
func (b *Backend) Get(service, account string) (string, error) {
	value, err := zalando.Get(service, account)
	if err != nil {
		if errors.Is(err, zalando.ErrNotFound) {
			return "", credentials.ErrNotFound
		}
		return "", mapErr(err)
	}
	return value, nil
}

// Delete removes the entry under service/account, mapping a missing entry
// onto credentials.ErrNotFound (which the domain treats as success on Clear).
func (b *Backend) Delete(service, account string) error {
	if err := zalando.Delete(service, account); err != nil {
		if errors.Is(err, zalando.ErrNotFound) {
			return credentials.ErrNotFound
		}
		return mapErr(err)
	}
	return nil
}

// Available reports that the OS keyring is worth attempting. Actual
// availability is only observable through Set errors.
func (b *Backend) Available() bool { return true }

// mapErr translates library sentinels onto domain sentinels and passes
// everything else through unchanged (the domain wraps it into a generic,
// secret-free message).
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, zalando.ErrNotFound) {
		return credentials.ErrNotFound
	}
	if errors.Is(err, zalando.ErrSetDataTooBig) {
		return credentials.ErrTooLarge
	}
	return err
}
