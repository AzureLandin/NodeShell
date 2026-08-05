// Package atomicfile writes JSON via a same-directory temp file and an atomic
// rename, so a crash or failed write never truncates the last valid version.
package atomicfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// rename is a seam so tests can simulate a failed replace.
var rename = os.Rename

// WriteJSON marshals v with 2-space indentation and atomically replaces path.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return Write(path, data)
}

// Write atomically replaces path with data (JSON or arbitrary bytes).
//
// Strategy: write a unique temp file in the same directory, flush + close it,
// then rename over the target. On Windows Go's os.Rename uses MoveFileEx with
// MOVEFILE_REPLACE_EXISTING, so replacing an existing target is atomic in
// place; on POSIX rename(2) replaces atomically. Any failure before the
// rename removes the temp file and leaves the last valid version intact.
func Write(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return err
	}
	// Sync before close so the rename never publishes a partially flushed file.
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := Replace(tmpName, path); err != nil {
		return err
	}
	done = true
	return nil
}

// Replace moves tmpName over target atomically: a same-directory rename with
// the bounded retry used by WriteJSON. The caller must have flushed and
// closed tmpName first (downloads write to a same-dir temp, sync + close,
// then Replace so a crash or failure never truncates the previous target).
func Replace(tmpName, target string) error {
	return replace(tmpName, target)
}

// replace moves tmpName over path, retrying briefly on failure. Windows can
// transiently refuse the underlying MoveFileEx with ERROR_ACCESS_DENIED while
// antivirus or the search indexer holds a short-lived handle on the freshly
// written temp file or the target; a bounded retry absorbs that so a healthy
// machine never surfaces a spurious CONFIG_WRITE_FAILED. A persistent error
// (e.g. a read-only directory) is returned after the last attempt.
func replace(tmpName, path string) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		if err = rename(tmpName, path); err == nil {
			return nil
		}
		time.Sleep(time.Duration(1<<attempt) * time.Millisecond)
	}
	return err
}
