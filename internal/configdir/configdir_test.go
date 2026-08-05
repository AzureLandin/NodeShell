package configdir

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestDataDirJoinsNodeshellUnderBase(t *testing.T) {
	orig := userConfigDir
	base := t.TempDir()
	userConfigDir = func() (string, error) { return base, nil }
	t.Cleanup(func() { userConfigDir = orig })

	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir: %v", err)
	}
	want := filepath.Join(base, "nodeshell")
	if dir != want {
		t.Fatalf("DataDir() = %q, want %q", dir, want)
	}
}

func TestDataDirPropagatesBaseResolutionError(t *testing.T) {
	orig := userConfigDir
	userConfigDir = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { userConfigDir = orig })

	if _, err := DataDir(); err == nil {
		t.Fatal("expected error when base resolution fails")
	}
}
