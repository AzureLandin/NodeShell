package keyring

import (
	"errors"
	"testing"

	zalando "github.com/zalando/go-keyring"

	"nodeshell/internal/credentials"
)

// The production backend is exercised only through its error mapping: the
// real OS keyring must never receive probe/test secrets (no side-effect-free
// probe exists), so these tests verify the sentinel translation a missing
// entry, an oversized value, and unrelated failures produce.
func TestMapErrNotFound(t *testing.T) {
	if !errors.Is(mapErr(zalando.ErrNotFound), credentials.ErrNotFound) {
		t.Fatal("zalando ErrNotFound must map to credentials.ErrNotFound")
	}
}

func TestMapErrTooBig(t *testing.T) {
	if !errors.Is(mapErr(zalando.ErrSetDataTooBig), credentials.ErrTooLarge) {
		t.Fatal("zalando ErrSetDataTooBig must map to credentials.ErrTooLarge")
	}
}

func TestMapErrPassthrough(t *testing.T) {
	raw := errors.New("keyring exploded")
	if got := mapErr(raw); !errors.Is(got, raw) {
		t.Fatalf("unrelated errors must pass through unchanged, got %v", got)
	}
}

func TestMapErrNil(t *testing.T) {
	if err := mapErr(nil); err != nil {
		t.Fatalf("mapErr(nil) = %v, want nil", err)
	}
}

func TestBackendAvailable(t *testing.T) {
	b := NewBackend()
	if !b.Available() {
		t.Fatal("production backend must report attempts-available")
	}
}
