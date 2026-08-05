package sftpservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/knownhosts"
	"nodeshell/internal/localpathguard"
	"nodeshell/internal/sshclient"
	"nodeshell/internal/sshtest"
)

// recordSink records emitted progress events.
type recordSink struct {
	mu     sync.Mutex
	events []ProgressEvent
}

func (s *recordSink) Emit(_ string, payload any) {
	s.mu.Lock()
	s.events = append(s.events, payload.(ProgressEvent))
	s.mu.Unlock()
}

func (s *recordSink) all() []ProgressEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ProgressEvent(nil), s.events...)
}

// newTestService connects a real SSH session to an SFTP-enabled test server
// rooted at root and wires a Service over it. home is the local-path
// boundary; sink records progress events.
func newTestService(t *testing.T, root, home string, readOnly bool) (*Service, *recordSink) {
	t.Helper()
	return newTestServiceWrapped(t, root, home, readOnly, nil)
}

// newTestServiceWrapped is newTestService with a client-wrap seam: wrap, when
// non-nil, is applied to every freshly opened SFTP client (used to simulate
// a spec-compliant server, a missing extension, or a truncated download).
func newTestServiceWrapped(t *testing.T, root, home string, readOnly bool, wrap func(sshclient.SFTPClient) sshclient.SFTPClient) (*Service, *recordSink) {
	t.Helper()
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return user == "user" && pass == "secret" }
	srv.EnableSFTP(root, readOnly)
	host, port := splitAddr(t, srv.Addr)
	k := knownhosts.New(t.TempDir())
	if err := k.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember host key: %v", err)
	}
	sess, err := sshclient.Connect(context.Background(), sshclient.Options{
		Host: host, Port: port, Username: "user", AuthMethod: "password",
		Password: "secret", HostKeys: k,
	})
	if err != nil {
		t.Fatalf("ssh connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	sink := &recordSink{}
	svc := New(Deps{Opener: &wrappingOpener{sess: sess, wrap: wrap}, Sink: sink, Home: home})
	return svc, sink
}

// wrappingOpener opens SFTP clients over one real SSH session and, when wrap
// is set, applies it to each fresh client before handing it out.
type wrappingOpener struct {
	sess *sshclient.Session
	wrap func(sshclient.SFTPClient) sshclient.SFTPClient
}

func (o *wrappingOpener) NewSFTPClient(string) (sshclient.SFTPClient, error) {
	c, err := o.sess.NewSFTPClient()
	if err != nil {
		return nil, err
	}
	if o.wrap != nil {
		return o.wrap(c), nil
	}
	return c, nil
}

// specRenameClient wraps an SFTPClient and simulates a spec-compliant SFTP v3
// server: a plain Rename whose target already exists is rejected (v3 rename
// must not overwrite), while PosixRename replaces atomically. With noPosix
// set, the posix-rename extension is reported absent so the fallback path
// runs. renameFail, when set, makes Rename fail for a matching oldpath.
type specRenameClient struct {
	sshclient.SFTPClient
	noPosix    bool
	renameFail func(oldpath, newpath string) bool
}

func (w *specRenameClient) Rename(oldpath, newpath string) error {
	if w.renameFail != nil && w.renameFail(oldpath, newpath) {
		return errors.New("simulated rename failure")
	}
	if _, err := w.SFTPClient.Lstat(newpath); err == nil {
		return errors.New("rename: target already exists")
	}
	return w.SFTPClient.Rename(oldpath, newpath)
}

func (w *specRenameClient) PosixRename(oldpath, newpath string) error {
	return w.SFTPClient.PosixRename(oldpath, newpath)
}

func (w *specRenameClient) HasExtension(name string) (string, bool) {
	if w.noPosix {
		return "", false
	}
	return w.SFTPClient.HasExtension(name)
}

// truncatedOpenClient wraps an SFTPClient and serves every Open through
// openReader, so a test can deliver fewer or more bytes than Stat reports.
type truncatedOpenClient struct {
	sshclient.SFTPClient
	openReader func() io.ReadCloser
}

func (w *truncatedOpenClient) Open(path string) (io.ReadCloser, error) {
	if w.openReader != nil {
		return w.openReader(), nil
	}
	return w.SFTPClient.Open(path)
}

// lyingStatClient wraps an SFTPClient and reports statSize for every Stat, so
// a test can simulate a server whose size report understates the real stream
// (a file that grows past the read cap after Stat — TOCTOU).
type lyingStatClient struct {
	sshclient.SFTPClient
	statSize int64
}

func (w *lyingStatClient) Stat(path string) (os.FileInfo, error) {
	fi, err := w.SFTPClient.Stat(path)
	if err != nil {
		return nil, err
	}
	return sizeInfo{FileInfo: fi, size: w.statSize}, nil
}

type sizeInfo struct {
	os.FileInfo
	size int64
}

func (s sizeInfo) Size() int64 { return s.size }

// openCountingClient wraps an SFTPClient and counts every Open, so a test can
// assert that an up-front rejection never touched the remote file.
type openCountingClient struct {
	sshclient.SFTPClient
	opens *int
}

func (w *openCountingClient) Open(path string) (io.ReadCloser, error) {
	*w.opens++
	return w.SFTPClient.Open(path)
}

// commitProbeClient wraps an SFTPClient, forces the no-posix-rename fallback
// path and records every Rename. lstatErr, when set, makes Lstat fail with
// that error for every path, so a commitUpload that wrongly falls through to
// a plain Rename is observable even when the real server would refuse it.
type commitProbeClient struct {
	sshclient.SFTPClient
	lstatErr error
	renames  []string
}

func (w *commitProbeClient) Lstat(p string) (os.FileInfo, error) {
	if w.lstatErr != nil {
		return nil, w.lstatErr
	}
	return w.SFTPClient.Lstat(p)
}

func (w *commitProbeClient) Rename(oldpath, newpath string) error {
	w.renames = append(w.renames, oldpath+" -> "+newpath)
	return w.SFTPClient.Rename(oldpath, newpath)
}

func (w *commitProbeClient) HasExtension(name string) (string, bool) { return "", false }

// permProbeClient wraps an SFTPClient and records every Chmod (with the mode
// argument), Rename and PosixRename in call order, so tests can assert that a
// commit chmods the temp with the old target's permissions before any rename.
// chmodFail, when set, makes Chmod fail with an opaque error. noPosix forces
// the no-posix-rename fallback path.
type permProbeClient struct {
	sshclient.SFTPClient
	noPosix   bool
	chmodFail bool
	mu        sync.Mutex
	calls     []string
	chmodPath string // path of the most recent Chmod call
}

func (w *permProbeClient) Chmod(p string, mode os.FileMode) error {
	w.mu.Lock()
	w.calls = append(w.calls, "chmod:"+strconv.FormatUint(uint64(mode.Perm()), 8))
	w.chmodPath = p
	w.mu.Unlock()
	if w.chmodFail {
		return errors.New("simulated chmod failure")
	}
	return w.SFTPClient.Chmod(p, mode)
}

func (w *permProbeClient) Rename(oldpath, newpath string) error {
	w.mu.Lock()
	w.calls = append(w.calls, "rename:"+oldpath+" -> "+newpath)
	w.mu.Unlock()
	return w.SFTPClient.Rename(oldpath, newpath)
}

func (w *permProbeClient) PosixRename(oldpath, newpath string) error {
	w.mu.Lock()
	w.calls = append(w.calls, "posix:"+oldpath+" -> "+newpath)
	w.mu.Unlock()
	return w.SFTPClient.PosixRename(oldpath, newpath)
}

func (w *permProbeClient) HasExtension(name string) (string, bool) {
	if w.noPosix {
		return "", false
	}
	return w.SFTPClient.HasExtension(name)
}

func (w *permProbeClient) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.calls...)
}

func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		t.Fatalf("bad addr %q", addr)
	}
	var port int
	for _, c := range addr[idx+1:] {
		port = port*10 + int(c-'0')
	}
	return addr[:idx], port
}

func remoteAbs(t *testing.T, root string) string {
	t.Helper()
	p, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	return filepath.ToSlash(p)
}

// TestListMkdirRenameChdirCwd drives the directory operations over the real
// SFTP protocol: initial cwd resolves to the server root, mkdir creates,
// list sorts dirs first, chdir + cwd track the new directory, rename moves.
func TestListMkdirRenameChdirCwd(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	// Initial cwd is the realpath of the server root.
	cwd, err := svc.Cwd(sid)
	if err != nil {
		t.Fatalf("Cwd: %v", err)
	}
	if !strings.Contains(cwd, filepath.ToSlash(filepath.Base(root))) {
		t.Fatalf("initial cwd %q does not contain the server root %q", cwd, root)
	}

	if err := svc.Mkdir(sid, "docs"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := svc.Mkdir(sid, "alpha"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	entries, err := svc.List(sid, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 || !entries[0].IsDirectory || !entries[1].IsDirectory {
		t.Fatalf("List = %+v, want two directories sorted first", entries)
	}
	if entries[0].Name != "alpha" || entries[1].Name != "docs" {
		t.Fatalf("List order = [%s %s], want [alpha docs]", entries[0].Name, entries[1].Name)
	}
	if !strings.HasPrefix(entries[0].Path, "/") {
		t.Fatalf("entry path %q must be POSIX-absolute", entries[0].Path)
	}

	next, err := svc.Chdir(sid, "docs")
	if err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	got, err := svc.Cwd(sid)
	if err != nil {
		t.Fatalf("Cwd: %v", err)
	}
	if got != next {
		t.Fatalf("Cwd after chdir = %q, want %q", got, next)
	}
	if !strings.HasSuffix(got, "/docs") {
		t.Fatalf("cwd after chdir = %q, want suffix /docs", got)
	}

	// Rename under the new cwd.
	if err := svc.Mkdir(sid, "x"); err != nil {
		t.Fatalf("Mkdir x: %v", err)
	}
	if err := svc.Rename(sid, "x", "y"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	entries, err = svc.List(sid, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "y" {
		t.Fatalf("List after rename = %+v, want [y]", entries)
	}
}

// TestRemoveRecursive deletes a nested tree and a single file; the root
// directory itself survives.
func TestRemoveRecursive(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	if err := svc.Mkdir(sid, "tree"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := svc.Mkdir(sid, "tree/sub"); err != nil {
		t.Fatalf("Mkdir sub: %v", err)
	}
	if err := svc.Mkdir(sid, "tree/sub/deep"); err != nil {
		t.Fatalf("Mkdir deep: %v", err)
	}
	// The test server maps the SFTP root onto the real local root, so a file
	// written deep inside the tree is a remote leaf for the recursion.
	writeLocal(t, filepath.Join(root, "tree", "sub", "deep", "leaf.txt"), "leaf")

	if err := svc.Remove(sid, "tree"); err != nil {
		t.Fatalf("Remove tree: %v", err)
	}
	entries, err := svc.List(sid, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("List after remove = %+v, want empty", entries)
	}
	if _, err := os.Stat(filepath.Join(root, "tree")); !os.IsNotExist(err) {
		t.Fatalf("remote tree still exists locally: %v", err)
	}
}

// TestRemoveRejectsRootAndCwd guards the delete boundary: "/", ".", ".." and
// empty names are refused, and a name that would escape the cwd is refused.
func TestRemoveRejectsRootAndCwd(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	for _, bad := range []string{"", ".", "..", "/"} {
		if err := svc.Remove(sid, bad); err == nil {
			t.Fatalf("Remove(%q) must be rejected", bad)
		}
	}
	if err := svc.Remove(sid, ".."); err == nil {
		t.Fatal("Remove(..) must be rejected (escapes cwd)")
	}
}

// TestRemoveRejectsCwdAliases: names that normalise to the current directory
// ("./", "x/..") must be refused before any deletion — a destructive
// recursion over cwd would otherwise erase the whole directory.
func TestRemoveRejectsCwdAliases(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"
	if err := svc.Mkdir(sid, "keep"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	for _, bad := range []string{"./", "x/.."} {
		if err := svc.Remove(sid, bad); err == nil {
			t.Fatalf("Remove(%q) must be rejected (normalises to cwd)", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "keep")); err != nil {
		t.Fatalf("cwd contents deleted by a rejected remove: %v", err)
	}
}

// TestRemoveSymlinkNotFollowed proves a symlink to a directory is unlinked,
// never recursed into — the link target survives.
func TestRemoveSymlinkNotFollowed(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	if err := svc.Mkdir(sid, "real"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	writeLocal(t, filepath.Join(root, "real", "precious.txt"), "keep")
	makeLocalSymlink(t, filepath.Join(root, "real"), filepath.Join(root, "link"))
	// The listing shows "link" as a symlink followed to a directory.
	entries, err := svc.List(sid, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var linkFound bool
	for _, e := range entries {
		if e.Name == "link" {
			linkFound = true
			if !e.IsDirectory {
				t.Fatalf("symlink to dir must list as a directory, got %+v", e)
			}
		}
	}
	if !linkFound {
		t.Fatal("symlink entry not listed")
	}

	if err := svc.Remove(sid, "link"); err != nil {
		t.Fatalf("Remove link: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "real", "precious.txt")); err != nil {
		t.Fatalf("symlink target contents were removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); !os.IsNotExist(err) {
		t.Fatalf("symlink itself must be gone, got %v", err)
	}
}

// TestUploadDownloadRoundtrip uploads a file (temp + rename), downloads it
// to a fresh target and asserts byte equality; progress events carry
// sessionId/direction/name/done.
func TestUploadDownloadRoundtrip(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, sink := newTestService(t, root, home, false)
	const sid = "s1"

	content := strings.Repeat("hello sftp\n", 5000)
	src := filepath.Join(home, "data.bin")
	writeLocal(t, src, content)

	if err := svc.Upload(sid, src); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	// The remote file exists and the temp is gone.
	remotePath := filepath.Join(root, "data.bin")
	gotRemote, err := os.ReadFile(remotePath)
	if err != nil {
		t.Fatalf("read remote: %v", err)
	}
	if string(gotRemote) != content {
		t.Fatalf("remote content mismatch (%d bytes)", len(gotRemote))
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-upload-*"))
	if len(leftover) != 0 {
		t.Fatalf("upload temp files left behind: %v", leftover)
	}

	// Upload again (overwrite semantics): new content replaces the old file.
	writeLocal(t, src, "shorter")
	if err := svc.Upload(sid, src); err != nil {
		t.Fatalf("Upload overwrite: %v", err)
	}
	gotRemote, _ = os.ReadFile(remotePath)
	if string(gotRemote) != "shorter" {
		t.Fatalf("overwrite left stale content %q", gotRemote)
	}

	// Download to a fresh local target.
	dl := filepath.Join(home, "downloads")
	if err := os.MkdirAll(dl, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dlTarget := filepath.Join(dl, "data.bin")
	writeLocal(t, dlTarget, "old contents must survive a failed download")
	if err := svc.Download(sid, "data.bin", dlTarget); err != nil {
		t.Fatalf("Download: %v", err)
	}
	gotDL, err := os.ReadFile(dlTarget)
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	if string(gotDL) != "shorter" {
		t.Fatalf("download content = %q, want %q", gotDL, "shorter")
	}
	leftover, _ = filepath.Glob(filepath.Join(dl, ".nodeshell-download-*"))
	if len(leftover) != 0 {
		t.Fatalf("download temp files left behind: %v", leftover)
	}

	events := sink.all()
	if len(events) < 6 {
		t.Fatalf("progress events = %d, want >= 6 (up x2, down x2 with initial+done)", len(events))
	}
	for _, e := range events {
		if e.SessionID != sid {
			t.Fatalf("event sessionId = %q, want %q", e.SessionID, sid)
		}
	}
	// Each transfer ends with a done event naming the file.
	var upDone, downDone bool
	for _, e := range events {
		if e.Done {
			if e.Direction == "up" && e.Name == "data.bin" {
				upDone = true
			}
			if e.Direction == "down" && e.Name == "data.bin" {
				downDone = true
			}
		}
	}
	if !upDone || !downDone {
		t.Fatalf("missing done events: up=%v down=%v (events=%+v)", upDone, downDone, events)
	}
}

// TestDownloadFailsKeepsOldTargetAndCleansTemp uses a read-only server so the
// remote open fails; the previous local target must survive and no temp file
// may remain.
func TestDownloadFailsKeepsOldTarget(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, true)
	const sid = "s1"

	target := filepath.Join(home, "keep.txt")
	writeLocal(t, target, "previous")

	if err := svc.Download(sid, "whatever.txt", target); err == nil {
		t.Fatal("download from a read-only server must fail")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("target lost: %v", err)
	}
	if string(got) != "previous" {
		t.Fatalf("target content = %q, want %q", got, "previous")
	}
	leftover, _ := filepath.Glob(filepath.Join(home, ".nodeshell-download-*"))
	if len(leftover) != 0 {
		t.Fatalf("download temp left behind: %v", leftover)
	}
}

// TestUploadOverwritePrefersPosixRename simulates a spec-compliant server
// whose plain Rename refuses to overwrite an existing target: an overwriting
// upload must still succeed, so the service has to use the posix-rename
// extension rather than a bare Rename over the target.
func TestUploadOverwritePrefersPosixRename(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
		return &specRenameClient{SFTPClient: c}
	})
	const sid = "s1"

	src := filepath.Join(home, "data.txt")
	writeLocal(t, src, "first")
	if err := svc.Upload(sid, src); err != nil {
		t.Fatalf("first Upload: %v", err)
	}
	// The second upload replaces the existing remote target even though a
	// spec-compliant plain Rename would reject it.
	writeLocal(t, src, "second")
	if err := svc.Upload(sid, src); err != nil {
		t.Fatalf("overwrite Upload must succeed via posix-rename, got: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "data.txt"))
	if err != nil {
		t.Fatalf("read remote: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("remote content = %q, want %q", got, "second")
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-upload-*"))
	if len(leftover) != 0 {
		t.Fatalf("upload temp files left behind: %v", leftover)
	}
}

// TestUploadOverwriteFallbackNoExtension forces the fallback path (no
// posix-rename extension) on a server whose plain Rename is spec-compliant:
// the target is first moved to a same-directory backup, the temp takes its
// place, and the backup is removed — no temp or backup may remain.
func TestUploadOverwriteFallbackNoExtension(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
		return &specRenameClient{SFTPClient: c, noPosix: true}
	})
	const sid = "s1"

	src := filepath.Join(home, "data.txt")
	writeLocal(t, src, "first")
	if err := svc.Upload(sid, src); err != nil {
		t.Fatalf("first Upload: %v", err)
	}
	writeLocal(t, src, "second")
	if err := svc.Upload(sid, src); err != nil {
		t.Fatalf("overwrite Upload via fallback: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "data.txt"))
	if err != nil {
		t.Fatalf("read remote: %v", err)
	}
	if string(got) != "second" {
		t.Fatalf("remote content = %q, want %q", got, "second")
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-upload-*"))
	if len(leftover) != 0 {
		t.Fatalf("upload temp left behind: %v", leftover)
	}
	// No ".nodeshell-backup-*" file may survive the successful commit.
	backups, _ := filepath.Glob(filepath.Join(root, ".nodeshell-backup-*"))
	if len(backups) != 0 {
		t.Fatalf("upload backup left behind: %v", backups)
	}
}

// TestUploadFallbackCommitFailureRollsBack forces the fallback path and makes
// the final Rename(tmp, target) fail after the old target was moved aside:
// the upload must error, the old target must be restored, and neither the
// temp nor the backup may remain.
func TestUploadFallbackCommitFailureRollsBack(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
		var commitRenames int
		return &specRenameClient{
			SFTPClient: c,
			noPosix:    true,
			renameFail: func(oldpath, _ string) bool {
				if !strings.HasPrefix(path.Base(oldpath), ".nodeshell-upload-") {
					return false
				}
				commitRenames++
				// Fail the overwrite commit (the second temp->target rename),
				// after the old target has already been moved aside.
				return commitRenames == 2
			},
		}
	})
	const sid = "s1"

	src := filepath.Join(home, "data.txt")
	writeLocal(t, src, "original")
	if err := svc.Upload(sid, src); err != nil {
		t.Fatalf("first Upload: %v", err)
	}
	writeLocal(t, src, "new-content")
	if err := svc.Upload(sid, src); err == nil {
		t.Fatal("overwrite with a failing commit must error")
	}
	// The previous target is rolled back, not clobbered or lost.
	got, err := os.ReadFile(filepath.Join(root, "data.txt"))
	if err != nil {
		t.Fatalf("read remote: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("remote content = %q, want the rolled-back original %q", got, "original")
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-upload-*"))
	if len(leftover) != 0 {
		t.Fatalf("upload temp left behind: %v", leftover)
	}
	backups, _ := filepath.Glob(filepath.Join(root, ".nodeshell-backup-*"))
	if len(backups) != 0 {
		t.Fatalf("upload backup left behind: %v", backups)
	}
}

// TestCommitUploadLstatErrorRefusesRename: when Lstat on the target fails
// with a permission or an opaque error, commitUpload must map the error and
// never fall through to a plain Rename — a target that cannot be inspected
// must not be treated as absent. The upload temp is still cleaned up and the
// pre-existing target survives untouched.
func TestCommitUploadLstatErrorRefusesRename(t *testing.T) {
	for _, lstatErr := range []error{os.ErrPermission, errors.New("opaque transport failure")} {
		t.Run(lstatErr.Error(), func(t *testing.T) {
			root := t.TempDir()
			home := t.TempDir()
			writeLocal(t, filepath.Join(root, "data.txt"), "existing target")
			var w *commitProbeClient
			svc, _ := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
				w = &commitProbeClient{SFTPClient: c, lstatErr: lstatErr}
				return w
			})
			const sid = "s1"
			src := filepath.Join(home, "src.txt")
			writeLocal(t, src, "new content")

			if err := svc.Upload(sid, src); err == nil {
				t.Fatal("upload must fail when the target cannot be inspected")
			}
			if len(w.renames) != 0 {
				t.Fatalf("commitUpload called Rename (%v) despite a failing Lstat", w.renames)
			}
			leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-upload-*"))
			if len(leftover) != 0 {
				t.Fatalf("upload temp left behind: %v", leftover)
			}
			got, err := os.ReadFile(filepath.Join(root, "data.txt"))
			if err != nil || string(got) != "existing target" {
				t.Fatalf("pre-existing target clobbered: %q, err=%v", got, err)
			}
		})
	}
}

// TestUploadOverDirectoryTargetRejected: committing an upload over a target
// that is a directory must be refused — the fallback must never move the
// directory to a backup path (or delete it after the commit).
func TestUploadOverDirectoryTargetRejected(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
		return &specRenameClient{SFTPClient: c, noPosix: true}
	})
	const sid = "s1"

	if err := svc.Mkdir(sid, "data.txt"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	src := filepath.Join(home, "data.txt")
	writeLocal(t, src, "new content")

	if err := svc.Upload(sid, src); err == nil {
		t.Fatal("upload over a directory target must be rejected")
	}
	fi, err := os.Stat(filepath.Join(root, "data.txt"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("target directory lost or replaced: %v", err)
	}
	backups, _ := filepath.Glob(filepath.Join(root, ".nodeshell-backup-*"))
	if len(backups) != 0 {
		t.Fatalf("directory moved to backup: %v", backups)
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-upload-*"))
	if len(leftover) != 0 {
		t.Fatalf("upload temp left behind: %v", leftover)
	}
}

// TestUploadOverwritePreservesMode: overwriting an existing remote target
// must keep the old permissions on the new content — the temp is chmod'd to
// the old mode before the commit rename — on both the posix-rename path and
// the no-extension fallback. The real SFTP server applies chmod via
// os.Chmod, whose mode bits are not honoured on Windows, so the mode equality
// is asserted only where the OS supports it.
func TestUploadOverwritePreservesMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o755} {
		for _, noPosix := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode-%o-noPosix-%v", mode, noPosix), func(t *testing.T) {
				root := t.TempDir()
				home := t.TempDir()
				svc, _ := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
					return &specRenameClient{SFTPClient: c, noPosix: noPosix}
				})
				const sid = "s1"

				remotePath := filepath.Join(root, "data.txt")
				if err := os.WriteFile(remotePath, []byte("old content"), mode); err != nil {
					t.Fatalf("seed remote target: %v", err)
				}
				seeded, err := os.Stat(remotePath)
				if err != nil {
					t.Fatalf("stat seeded target: %v", err)
				}
				src := filepath.Join(home, "data.txt")
				writeLocal(t, src, "new content")

				if err := svc.Upload(sid, src); err != nil {
					t.Fatalf("Upload overwrite: %v", err)
				}
				got, err := os.ReadFile(remotePath)
				if err != nil {
					t.Fatalf("read remote: %v", err)
				}
				if string(got) != "new content" {
					t.Fatalf("remote content = %q, want the new content", got)
				}
				if runtime.GOOS != "windows" {
					fi, err := os.Stat(remotePath)
					if err != nil {
						t.Fatalf("stat remote: %v", err)
					}
					if fi.Mode().Perm() != seeded.Mode().Perm() {
						t.Fatalf("remote mode = %o, want %o preserved", fi.Mode().Perm(), seeded.Mode().Perm())
					}
				}
				leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-upload-*"))
				if len(leftover) != 0 {
					t.Fatalf("upload temp left behind: %v", leftover)
				}
			})
		}
	}
}

// TestUploadCommitChmodBeforeRename: when the target exists, commitUpload
// must chmod the temp with the old target's permissions BEFORE the commit
// rename, so a replaced file never exposes a default-created mode; when the
// target is absent it must skip the chmod and commit with a plain Rename.
func TestUploadCommitChmodBeforeRename(t *testing.T) {
	t.Run("existing target", func(t *testing.T) {
		root := t.TempDir()
		home := t.TempDir()
		remotePath := filepath.Join(root, "data.txt")
		if err := os.WriteFile(remotePath, []byte("old"), 0o600); err != nil {
			t.Fatalf("seed remote target: %v", err)
		}
		seeded, err := os.Stat(remotePath)
		if err != nil {
			t.Fatalf("stat seeded target: %v", err)
		}
		var w *permProbeClient
		svc, _ := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
			w = &permProbeClient{SFTPClient: c}
			return w
		})
		const sid = "s1"
		src := filepath.Join(home, "data.txt")
		writeLocal(t, src, "new")

		if err := svc.Upload(sid, src); err != nil {
			t.Fatalf("Upload overwrite: %v", err)
		}
		assertChmodPrecedesCommit(t, w.snapshot(), w.chmodPath, seeded.Mode().Perm())
	})

	t.Run("absent target", func(t *testing.T) {
		root := t.TempDir()
		home := t.TempDir()
		var w *permProbeClient
		svc, _ := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
			w = &permProbeClient{SFTPClient: c}
			return w
		})
		const sid = "s1"
		src := filepath.Join(home, "data.txt")
		writeLocal(t, src, "new")

		if err := svc.Upload(sid, src); err != nil {
			t.Fatalf("Upload to a fresh target: %v", err)
		}
		calls := w.snapshot()
		for _, c := range calls {
			if strings.HasPrefix(c, "chmod:") {
				t.Fatalf("absent target must not be chmod'd: %v", calls)
			}
			if strings.HasPrefix(c, "posix:") {
				t.Fatalf("absent target must commit with a plain Rename, got %v", calls)
			}
		}
		if len(calls) != 1 || !strings.HasPrefix(calls[0], "rename:") {
			t.Fatalf("commit calls = %v, want exactly one plain Rename", calls)
		}
		oldpath, _, _ := strings.Cut(strings.TrimPrefix(calls[0], "rename:"), " -> ")
		if base := path.Base(oldpath); !strings.HasPrefix(base, ".nodeshell-upload-") {
			t.Fatalf("rename source %q is not the upload temp", calls[0])
		}
	})
}

// TestUploadCommitChmodFailureKeepsTarget: when preserving permissions fails,
// the upload must error with a generic coded message, the previous target
// must survive untouched, no commit rename may be attempted, and the temp
// must be cleaned up.
func TestUploadCommitChmodFailureKeepsTarget(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	remotePath := filepath.Join(root, "data.txt")
	if err := os.WriteFile(remotePath, []byte("existing target"), 0o600); err != nil {
		t.Fatalf("seed remote target: %v", err)
	}
	var w *permProbeClient
	svc, _ := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
		w = &permProbeClient{SFTPClient: c, chmodFail: true}
		return w
	})
	const sid = "s1"
	src := filepath.Join(home, "data.txt")
	writeLocal(t, src, "new content")

	err := svc.Upload(sid, src)
	if err == nil {
		t.Fatal("upload must fail when the permission chmod fails")
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) {
		t.Fatalf("error = %T, want a coded error", err)
	}
	if msg := err.Error(); strings.Contains(msg, "simulated") || strings.Contains(msg, "existing") {
		t.Fatalf("error message %q leaks the underlying cause", msg)
	}
	got, err := os.ReadFile(remotePath)
	if err != nil {
		t.Fatalf("read remote: %v", err)
	}
	if string(got) != "existing target" {
		t.Fatalf("target content = %q, want the untouched original", got)
	}
	for _, c := range w.snapshot() {
		if strings.HasPrefix(c, "rename:") || strings.HasPrefix(c, "posix:") {
			t.Fatalf("commit rename attempted despite the chmod failure: %v", c)
		}
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-upload-*"))
	if len(leftover) != 0 {
		t.Fatalf("upload temp left behind: %v", leftover)
	}
}

// assertChmodPrecedesCommit checks the recorded probe calls: exactly the
// upload temp was chmod'd with wantPerm, and the chmod happened before the
// first commit rename/posix call.
func assertChmodPrecedesCommit(t *testing.T, calls []string, chmodPath string, wantPerm os.FileMode) {
	t.Helper()
	chmodIdx, commitIdx := -1, -1
	for i, c := range calls {
		switch {
		case strings.HasPrefix(c, "chmod:"):
			if chmodIdx >= 0 {
				t.Fatalf("more than one chmod call: %v", calls)
			}
			chmodIdx = i
		case strings.HasPrefix(c, "rename:"), strings.HasPrefix(c, "posix:"):
			if commitIdx < 0 {
				commitIdx = i
			}
		}
	}
	if chmodIdx < 0 {
		t.Fatalf("no chmod call recorded: %v", calls)
	}
	if commitIdx < 0 {
		t.Fatalf("no commit rename recorded: %v", calls)
	}
	if chmodIdx >= commitIdx {
		t.Fatalf("chmod must precede the commit rename (calls=%v)", calls)
	}
	if got := strings.TrimPrefix(calls[chmodIdx], "chmod:"); got != strconv.FormatUint(uint64(wantPerm), 8) {
		t.Fatalf("chmod mode = %s, want %o (calls=%v)", got, wantPerm, calls)
	}
	if base := path.Base(chmodPath); !strings.HasPrefix(base, ".nodeshell-upload-") {
		t.Fatalf("chmod path %q is not the upload temp", chmodPath)
	}
}

// TestDownloadOverwritePreservesMode: overwriting an existing local target
// keeps its permissions — the temp is chmod'd to the old mode before the
// atomic replace. The mode equality is asserted only where the OS honours
// mode bits (not Windows).
func TestDownloadOverwritePreservesMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o755} {
		t.Run(fmt.Sprintf("mode-%o", mode), func(t *testing.T) {
			root := t.TempDir()
			home := t.TempDir()
			svc, _ := newTestService(t, root, home, false)
			const sid = "s1"
			writeLocal(t, filepath.Join(root, "data.txt"), "remote content")

			target := filepath.Join(home, "data.txt")
			writeLocal(t, target, "old local content")
			if err := os.Chmod(target, mode); err != nil {
				t.Fatalf("chmod target: %v", err)
			}
			seeded, err := os.Stat(target)
			if err != nil {
				t.Fatalf("stat seeded target: %v", err)
			}

			if err := svc.Download(sid, "data.txt", target); err != nil {
				t.Fatalf("Download: %v", err)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read target: %v", err)
			}
			if string(got) != "remote content" {
				t.Fatalf("target content = %q, want the remote content", got)
			}
			if runtime.GOOS != "windows" {
				fi, err := os.Stat(target)
				if err != nil {
					t.Fatalf("stat target: %v", err)
				}
				if fi.Mode().Perm() != seeded.Mode().Perm() {
					t.Fatalf("target mode = %o, want %o preserved", fi.Mode().Perm(), seeded.Mode().Perm())
				}
			}
		})
	}
}

// TestDownloadNewTargetKeepsSafeDefaultMode: a download to a fresh path must
// keep the temp's 0600 default — never broadened. Asserted only where the OS
// honours mode bits.
func TestDownloadNewTargetKeepsSafeDefaultMode(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, false)
	const sid = "s1"
	writeLocal(t, filepath.Join(root, "data.txt"), "remote content")

	target := filepath.Join(home, "fresh.txt")
	if err := svc.Download(sid, "data.txt", target); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(target)
		if err != nil {
			t.Fatalf("stat target: %v", err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("fresh target mode = %o, want the 0600 temp default", fi.Mode().Perm())
		}
	}
}

// TestDownloadOverDirectoryTargetRejected: an existing directory as the
// download target is refused before any transfer and survives untouched.
func TestDownloadOverDirectoryTargetRejected(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, false)
	const sid = "s1"
	writeLocal(t, filepath.Join(root, "data.txt"), "remote content")

	target := filepath.Join(home, "adir")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := svc.Download(sid, "data.txt", target); err == nil {
		t.Fatal("download over a directory target must be rejected")
	}
	fi, err := os.Stat(target)
	if err != nil || !fi.IsDir() {
		t.Fatalf("target directory lost or replaced: %v", err)
	}
	leftover, _ := filepath.Glob(filepath.Join(home, ".nodeshell-download-*"))
	if len(leftover) != 0 {
		t.Fatalf("download temp left behind: %v", leftover)
	}
}

// TestDownloadShortReadRejected delivers fewer bytes than the remote Stat
// reports: the download must fail with a generic error, keep the previous
// target intact, clean the temp, and end with a done event carrying the
// partial count (never raised to 100%).
func TestDownloadShortReadRejected(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	remote := filepath.Join(root, "data.txt")
	writeLocal(t, remote, "a complete remote file") // real size 22 bytes
	svc, sink := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
		return &truncatedOpenClient{SFTPClient: c, openReader: func() io.ReadCloser {
			// A transport that cuts the stream off after 5 bytes.
			return io.NopCloser(strings.NewReader("a com"))
		}}
	})
	const sid = "s1"

	target := filepath.Join(home, "data.txt")
	writeLocal(t, target, "previous target")

	err := svc.Download(sid, "data.txt", target)
	if err == nil {
		t.Fatal("short-read download must fail, not commit a truncated file")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("target lost: %v", err)
	}
	if string(got) != "previous target" {
		t.Fatalf("target content = %q, want %q", got, "previous target")
	}
	leftover, _ := filepath.Glob(filepath.Join(home, ".nodeshell-download-*"))
	if len(leftover) != 0 {
		t.Fatalf("download temp left behind: %v", leftover)
	}

	// The final event is done=true with the partial count — never clamped to
	// the full size.
	events := sink.all()
	last, ok := lastDoneEvent(events)
	if !ok {
		t.Fatalf("no done event: %+v", events)
	}
	if last.Transferred >= last.Total {
		t.Fatalf("failed transfer must report the partial count, got transferred=%d total=%d", last.Transferred, last.Total)
	}
	if last.Transferred != 5 {
		t.Fatalf("partial transferred = %d, want 5 (bytes delivered before truncation)", last.Transferred)
	}
}

// TestDownloadLongReadRejected delivers more bytes than the remote Stat
// reports: an oversized stream is equally refused.
func TestDownloadLongReadRejected(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	remote := filepath.Join(root, "data.txt")
	writeLocal(t, remote, "tiny") // real size 4 bytes
	svc, _ := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
		return &truncatedOpenClient{SFTPClient: c, openReader: func() io.ReadCloser {
			return io.NopCloser(strings.NewReader(strings.Repeat("x", 100)))
		}}
	})
	const sid = "s1"

	target := filepath.Join(home, "data.txt")
	writeLocal(t, target, "previous target")

	if err := svc.Download(sid, "data.txt", target); err == nil {
		t.Fatal("long-read download must fail")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("target lost: %v", err)
	}
	if string(got) != "previous target" {
		t.Fatalf("target content = %q, want %q", got, "previous target")
	}
	leftover, _ := filepath.Glob(filepath.Join(home, ".nodeshell-download-*"))
	if len(leftover) != 0 {
		t.Fatalf("download temp left behind: %v", leftover)
	}
}

// lastDoneEvent returns the last emitted done=true event.
func lastDoneEvent(events []ProgressEvent) (ProgressEvent, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Done {
			return events[i], true
		}
	}
	return ProgressEvent{}, false
}

// TestUploadToReadOnlyServerCleansTemp: a write that the server refuses
// leaves no temp file and never creates the target.
func TestUploadToReadOnlyServerCleansTemp(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, true)
	const sid = "s1"

	src := filepath.Join(home, "src.txt")
	writeLocal(t, src, "data")

	if err := svc.Upload(sid, src); err == nil {
		t.Fatal("upload to a read-only server must fail")
	}
	if _, err := os.Stat(filepath.Join(root, "src.txt")); !os.IsNotExist(err) {
		t.Fatalf("target created despite failure: %v", err)
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-upload-*"))
	if len(leftover) != 0 {
		t.Fatalf("upload temp left behind: %v", leftover)
	}
}

// TestUploadPathsHomeEscapeRejected: a local path outside home fails the
// whole batch observably; directories are skipped; nothing transfers.
func TestUploadPathsHomeEscapeRejected(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, false)
	const sid = "s1"

	outside := t.TempDir()
	writeLocal(t, filepath.Join(outside, "x.txt"), "x")
	writeLocal(t, filepath.Join(home, "ok.txt"), "ok")

	err := svc.UploadPaths(sid, []string{filepath.Join(outside, "x.txt")})
	if err == nil {
		t.Fatal("upload path outside home must be rejected")
	}
	if !errors.Is(err, localpathguard.ErrOutsideHome) {
		t.Fatalf("error = %v, want the outside-home guard error", err)
	}
	// Nothing was uploaded.
	if _, err := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaped upload created a remote file")
	}

	// A directory in the batch is skipped, the valid file still uploads.
	dirInHome := filepath.Join(home, "adir")
	if err := os.MkdirAll(dirInHome, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := svc.UploadPaths(sid, []string{dirInHome, filepath.Join(home, "ok.txt")}); err != nil {
		t.Fatalf("UploadPaths with a directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "ok.txt")); err != nil {
		t.Fatalf("valid file not uploaded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "adir")); !os.IsNotExist(err) {
		t.Fatalf("directory must not be uploaded")
	}
}

// TestUploadDirectHomeEscapeRejected: the public Upload entry must enforce
// the home boundary itself, not rely on UploadPaths doing it — a caller that
// passes a path outside home directly must be refused.
func TestUploadDirectHomeEscapeRejected(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, false)
	const sid = "s1"

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "x.txt")
	writeLocal(t, outsideFile, "x")

	err := svc.Upload(sid, outsideFile)
	if err == nil {
		t.Fatal("direct Upload of a path outside home must be rejected")
	}
	if !errors.Is(err, localpathguard.ErrOutsideHome) {
		t.Fatalf("error = %v, want the outside-home guard error", err)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaped upload created a remote file")
	}
}

// TestUploadDirectSymlinkEscapeRejected: a symlink inside home that resolves
// outside home must be rejected by the direct Upload entry.
func TestUploadDirectSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, false)
	const sid = "s1"

	outside := t.TempDir()
	victim := filepath.Join(outside, "secret.txt")
	writeLocal(t, victim, "x")
	link := filepath.Join(home, "escape.txt")
	makeLocalSymlink(t, victim, link)

	err := svc.Upload(sid, link)
	if err == nil {
		t.Fatal("direct Upload through a symlink escaping home must be rejected")
	}
	if !errors.Is(err, localpathguard.ErrOutsideHome) {
		t.Fatalf("error = %v, want the outside-home guard error", err)
	}
}

// TestUploadDirectDirectoryRejected: the direct Upload entry requires a
// regular file; a directory is refused before any transfer.
func TestUploadDirectDirectoryRejected(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, false)
	const sid = "s1"

	dir := filepath.Join(home, "adir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := svc.Upload(sid, dir); err == nil {
		t.Fatal("direct Upload of a directory must be rejected")
	}
	if _, err := os.Stat(filepath.Join(root, "adir")); !os.IsNotExist(err) {
		t.Fatalf("directory uploaded to the remote side")
	}
}

// TestDownloadHomeEscapeRejected: the local download target must stay inside
// home.
func TestDownloadHomeEscapeRejected(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, false)
	const sid = "s1"

	writeLocal(t, filepath.Join(root, "data.txt"), "remote data")
	outside := t.TempDir()
	target := filepath.Join(outside, "escaped.txt")

	gotErr := svc.Download(sid, "data.txt", target)
	if gotErr == nil {
		t.Fatal("download target outside home must be rejected")
	}
	if !errors.Is(gotErr, localpathguard.ErrOutsideHome) {
		t.Fatalf("error = %v, want the outside-home guard error", gotErr)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("escaped download created a local file")
	}
}

// TestDownloadSymlinkTargetRejected: a download target that is a symlink
// pointing outside home is rejected (never written through).
func TestDownloadSymlinkTargetRejected(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, false)
	const sid = "s1"

	writeLocal(t, filepath.Join(root, "data.txt"), "remote data")
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	writeLocal(t, victim, "do not clobber")
	link := filepath.Join(home, "download-link")
	makeLocalSymlink(t, victim, link)

	if err := svc.Download(sid, "data.txt", link); err == nil {
		t.Fatal("download onto a symlink escaping home must be rejected")
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("victim lost: %v", err)
	}
	if string(got) != "do not clobber" {
		t.Fatalf("victim content = %q", got)
	}
}

// TestMapRemoteErrNoPathLeak: the unclassified fallback message must be
// fixed and generic — never err.Error(), which can carry a local absolute
// path or remote path text (e.g. an os.PathError from a local io.Copy on the
// download path).
func TestMapRemoteErrNoPathLeak(t *testing.T) {
	secretAbs := filepath.Join(string(os.PathSeparator), "Users", "alice", "Documents", "private", "id_rsa")
	remoteText := "/srv/secret/deploy.key"
	err := &os.PathError{Op: "read", Path: secretAbs, Err: errors.New(remoteText)}
	msg := mapRemoteErr(err).Error()
	for _, leak := range []string{secretAbs, "alice", "private", remoteText} {
		if strings.Contains(msg, leak) {
			t.Fatalf("mapRemoteErr message %q leaks %q", msg, leak)
		}
	}
}

// TestMapRemoteErrClassified pins the classified mappings and the generic
// fallback wording.
func TestMapRemoteErrClassified(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{os.ErrNotExist, "Remote path does not exist"},
		{os.ErrPermission, "Permission denied"},
		{io.EOF, "Remote connection ended"},
		{errors.New("opaque transport failure"), "Remote operation failed"},
	}
	for _, c := range cases {
		if msg := mapRemoteErr(c.err).Error(); msg != c.want {
			t.Errorf("mapRemoteErr(%v) = %q, want %q", c.err, msg, c.want)
		}
	}
}

// --- text read/write (T1.7.1): ReadText / WriteText with a byte cap ---

// TestWriteTextReadTextRoundtrip writes text through WriteText, verifies the
// remote file byte-for-byte and that no temp remains, then reads it back with
// ReadText and checks the content plus the resolved POSIX-absolute path.
func TestWriteTextReadTextRoundtrip(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	content := "hello sftp\nsecond line\n"
	if _, err := svc.WriteText(sid, "notes.txt", content, 1<<20); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	gotRemote, err := os.ReadFile(filepath.Join(root, "notes.txt"))
	if err != nil {
		t.Fatalf("read remote: %v", err)
	}
	if string(gotRemote) != content {
		t.Fatalf("remote content = %q, want %q", gotRemote, content)
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-write-*"))
	if len(leftover) != 0 {
		t.Fatalf("write temp files left behind: %v", leftover)
	}

	resolved, got, err := svc.ReadText(sid, "notes.txt", 1<<20)
	if err != nil {
		t.Fatalf("ReadText: %v", err)
	}
	if got != content {
		t.Fatalf("ReadText content = %q, want %q", got, content)
	}
	if !strings.HasPrefix(resolved, "/") || !strings.HasSuffix(resolved, "/notes.txt") {
		t.Fatalf("ReadText resolved path = %q, want a POSIX-absolute path ending /notes.txt", resolved)
	}
}

// TestReadTextCapExactAndOver: a file of exactly maxBytes reads back in full;
// one byte more is rejected by the Stat size check before any data is read,
// with a coded UNKNOWN error that never names the remote path.
func TestReadTextCapExactAndOver(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"
	const cap = 32

	exact := strings.Repeat("x", cap)
	writeLocal(t, filepath.Join(root, "exact.txt"), exact)
	if _, got, err := svc.ReadText(sid, "exact.txt", cap); err != nil || got != exact {
		t.Fatalf("ReadText(exact cap) = (_, %d bytes, %v), want the full %d bytes", len(got), err, cap)
	}

	writeLocal(t, filepath.Join(root, "over.txt"), strings.Repeat("x", cap+1))
	err := func() error {
		_, _, err := svc.ReadText(sid, "over.txt", cap)
		return err
	}()
	if err == nil {
		t.Fatal("ReadText of a file over the cap must be rejected")
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != apperror.Unknown {
		t.Fatalf("ReadText error = %v, want a coded UNKNOWN error", err)
	}
	if msg := err.Error(); strings.Contains(msg, "over.txt") {
		t.Fatalf("ReadText error %q leaks the remote path", msg)
	}
}

// TestReadTextNonPositiveCapRejected: maxBytes <= 0 is refused outright.
func TestReadTextNonPositiveCapRejected(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"
	writeLocal(t, filepath.Join(root, "f.txt"), "data")

	for _, cap := range []int64{0, -1} {
		if _, _, err := svc.ReadText(sid, "f.txt", cap); err == nil {
			t.Fatalf("ReadText with maxBytes=%d must be rejected", cap)
		}
	}
}

// TestReadTextMaxInt64CapRejected: maxBytes == math.MaxInt64 is a meaningless
// cap — the stream-copy sentinel (maxBytes+1) would overflow to a negative
// count and io.CopyN would stop at once, silently returning empty content for
// any real file. It is refused up front (before any session or network
// action) with a coded UNKNOWN error that never names the remote path, and
// the remote file is never opened.
func TestReadTextMaxInt64CapRejected(t *testing.T) {
	root := t.TempDir()
	opened := 0
	svc, _ := newTestServiceWrapped(t, root, t.TempDir(), false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
		return &openCountingClient{SFTPClient: c, opens: &opened}
	})
	const sid = "s1"
	writeLocal(t, filepath.Join(root, "f.txt"), "small content")

	_, got, err := svc.ReadText(sid, "f.txt", math.MaxInt64)
	if err == nil {
		t.Fatalf("ReadText with maxBytes=math.MaxInt64 must be rejected, got success with content %q", got)
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != apperror.Unknown {
		t.Fatalf("ReadText error = %v, want a coded UNKNOWN error", err)
	}
	if msg := err.Error(); strings.Contains(msg, "f.txt") {
		t.Fatalf("ReadText error %q leaks the remote path", msg)
	}
	if opened != 0 {
		t.Fatalf("ReadText opened the remote file %d time(s), want 0", opened)
	}
}

// TestReadTextDirectoryRejected: a directory target is refused before any
// data is read.
func TestReadTextDirectoryRejected(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	if err := svc.Mkdir(sid, "adir"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, _, err := svc.ReadText(sid, "adir", 1<<20); err == nil {
		t.Fatal("ReadText of a directory must be rejected")
	}
}

// TestReadTextStreamGrowsPastCapRejected: when Stat under-reports the size
// (TOCTOU growth) or the stream itself delivers more than the cap, the
// streaming hard cap must reject instead of returning oversized content.
func TestReadTextStreamGrowsPastCapRejected(t *testing.T) {
	cases := []struct {
		name string
		seed string
		wrap func(sshclient.SFTPClient) sshclient.SFTPClient
	}{
		{
			name: "stat-undersizes",
			seed: strings.Repeat("x", 48), // real content > cap
			wrap: func(c sshclient.SFTPClient) sshclient.SFTPClient {
				return &lyingStatClient{SFTPClient: c, statSize: 4}
			},
		},
		{
			name: "stream-grows",
			seed: strings.Repeat("x", 16), // real content fits, stream lies
			wrap: func(c sshclient.SFTPClient) sshclient.SFTPClient {
				return &truncatedOpenClient{SFTPClient: c, openReader: func() io.ReadCloser {
					return io.NopCloser(strings.NewReader(strings.Repeat("x", 40)))
				}}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			home := t.TempDir()
			const cap = 32
			writeLocal(t, filepath.Join(root, "data.txt"), c.seed)
			svc, _ := newTestServiceWrapped(t, root, home, false, c.wrap)
			const sid = "s1"
			if _, _, err := svc.ReadText(sid, "data.txt", cap); err == nil {
				t.Fatal("ReadText must reject a stream that exceeds the cap")
			}
		})
	}
}

// TestReadTextInvalidUTF8Preserved: invalid UTF-8 bytes come back verbatim in
// the Go string (JSON marshal downstream replaces them with U+FFFD, matching
// Buffer.toString("utf8") in the Electron tool).
func TestReadTextInvalidUTF8Preserved(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	raw := []byte{0xff, 0xfe, 'a', 0xc3, 0x28, 'b'}
	if err := os.WriteFile(filepath.Join(root, "bad.txt"), raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, got, err := svc.ReadText(sid, "bad.txt", 1<<20)
	if err != nil {
		t.Fatalf("ReadText: %v", err)
	}
	if !bytes.Equal([]byte(got), raw) {
		t.Fatalf("ReadText content = %x, want the raw bytes %x", []byte(got), raw)
	}
}

// TestReadTextFollowsSymlink: reading through a symlink returns the target's
// content (RealPath + Stat/Open follow links, matching the Electron
// resolveExistingPath; the pkg/sftp test server's realpath does not itself
// resolve links, so only the content is pinned).
func TestReadTextFollowsSymlink(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	writeLocal(t, filepath.Join(root, "real.txt"), "target content")
	makeLocalSymlink(t, filepath.Join(root, "real.txt"), filepath.Join(root, "link.txt"))
	resolved, got, err := svc.ReadText(sid, "link.txt", 1<<20)
	if err != nil {
		t.Fatalf("ReadText via symlink: %v", err)
	}
	if got != "target content" {
		t.Fatalf("content = %q, want the symlink target content", got)
	}
	if !strings.HasPrefix(resolved, "/") {
		t.Fatalf("resolved path = %q, want POSIX-absolute", resolved)
	}
}

// TestWriteTextCapUTF8Bytes: the cap counts UTF-8 bytes, not runes. Content
// whose byte length equals maxBytes writes; one extra byte is rejected before
// anything is transferred, an existing target is left untouched and no temp
// remains.
func TestWriteTextCapUTF8Bytes(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"
	const cap = 10

	exact := strings.Repeat("é", 5) // 10 UTF-8 bytes
	if _, err := svc.WriteText(sid, "exact.txt", exact, cap); err != nil {
		t.Fatalf("WriteText(exact byte cap): %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "exact.txt"))
	if err != nil || string(got) != exact {
		t.Fatalf("exact-cap content = %q (err %v), want %q", got, err, exact)
	}

	// A target that would have been replaced survives the rejected write.
	writeLocal(t, filepath.Join(root, "keep.txt"), "old content")
	over := strings.Repeat("é", 5) + "a" // 11 UTF-8 bytes, one over the cap
	if _, err := svc.WriteText(sid, "keep.txt", over, cap); err == nil {
		t.Fatal("WriteText over the byte cap must be rejected")
	}
	kept, err := os.ReadFile(filepath.Join(root, "keep.txt"))
	if err != nil || string(kept) != "old content" {
		t.Fatalf("kept target = %q (err %v), want the untouched old content", kept, err)
	}
	if _, err := os.Stat(filepath.Join(root, "exact.txt")); err != nil {
		t.Fatalf("exact-cap write lost after the rejected write: %v", err)
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-write-*"))
	if len(leftover) != 0 {
		t.Fatalf("write temp left behind: %v", leftover)
	}
}

// TestWriteTextNonPositiveCapRejected: maxBytes <= 0 is refused outright.
func TestWriteTextNonPositiveCapRejected(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	for _, cap := range []int64{0, -1} {
		if _, err := svc.WriteText(sid, "f.txt", "data", cap); err == nil {
			t.Fatalf("WriteText with maxBytes=%d must be rejected", cap)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "f.txt")); !os.IsNotExist(err) {
		t.Fatalf("rejected write created the target: %v", err)
	}
}

// TestWriteTextZeroLengthContent: the cap only bounds content, so an empty
// payload is a valid write that leaves a zero-byte remote file and no temp.
// Coverage for the existing contract, not a new behaviour.
func TestWriteTextZeroLengthContent(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	if _, err := svc.WriteText(sid, "empty.txt", "", 1<<20); err != nil {
		t.Fatalf("WriteText(empty content): %v", err)
	}
	fi, err := os.Stat(filepath.Join(root, "empty.txt"))
	if err != nil {
		t.Fatalf("stat remote: %v", err)
	}
	if fi.Size() != 0 {
		t.Fatalf("remote size = %d, want 0", fi.Size())
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-write-*"))
	if len(leftover) != 0 {
		t.Fatalf("write temp left behind: %v", leftover)
	}
}

// TestWriteTextOverwritePreservesMode: overwriting an existing remote target
// keeps its permissions on the new content (the temp is chmod'd to the old
// mode before the commit rename). Asserted only where the OS honours mode
// bits.
func TestWriteTextOverwritePreservesMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o755} {
		t.Run(fmt.Sprintf("mode-%o", mode), func(t *testing.T) {
			root := t.TempDir()
			svc, _ := newTestService(t, root, t.TempDir(), false)
			const sid = "s1"

			remotePath := filepath.Join(root, "data.txt")
			if err := os.WriteFile(remotePath, []byte("old content"), mode); err != nil {
				t.Fatalf("seed remote target: %v", err)
			}
			seeded, err := os.Stat(remotePath)
			if err != nil {
				t.Fatalf("stat seeded target: %v", err)
			}
			if _, err := svc.WriteText(sid, "data.txt", "new content", 1<<20); err != nil {
				t.Fatalf("WriteText overwrite: %v", err)
			}
			got, err := os.ReadFile(remotePath)
			if err != nil || string(got) != "new content" {
				t.Fatalf("remote content = %q (err %v), want the new content", got, err)
			}
			if runtime.GOOS != "windows" {
				fi, err := os.Stat(remotePath)
				if err != nil {
					t.Fatalf("stat remote: %v", err)
				}
				if fi.Mode().Perm() != seeded.Mode().Perm() {
					t.Fatalf("remote mode = %o, want %o preserved", fi.Mode().Perm(), seeded.Mode().Perm())
				}
			}
			leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-write-*"))
			if len(leftover) != 0 {
				t.Fatalf("write temp left behind: %v", leftover)
			}
		})
	}
}

// TestWriteTextFreshTargetSafeMode: a brand-new target never ends up with
// broadened permissions — it keeps the server's umask-constrained create
// default (never wider than 0644, owner-readable). Asserted only where the OS
// honours mode bits.
func TestWriteTextFreshTargetSafeMode(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	if _, err := svc.WriteText(sid, "fresh.txt", "content", 1<<20); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(root, "fresh.txt"))
		if err != nil {
			t.Fatalf("stat fresh target: %v", err)
		}
		perm := fi.Mode().Perm()
		if perm&^0o644 != 0 || perm&0o400 == 0 {
			t.Fatalf("fresh target mode = %o, want a safe default (owner-readable, no wider than 0644)", perm)
		}
	}
}

// TestWriteTextToReadOnlyServerKeepsOldTarget: a server that refuses the write
// must leave the previous target intact, create nothing and clean the temp.
func TestWriteTextToReadOnlyServerKeepsOldTarget(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), true)
	const sid = "s1"

	writeLocal(t, filepath.Join(root, "keep.txt"), "previous")
	if _, err := svc.WriteText(sid, "keep.txt", "new", 1<<20); err == nil {
		t.Fatal("write to a read-only server must fail")
	}
	got, err := os.ReadFile(filepath.Join(root, "keep.txt"))
	if err != nil || string(got) != "previous" {
		t.Fatalf("target content = %q (err %v), want the untouched previous", got, err)
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-write-*"))
	if len(leftover) != 0 {
		t.Fatalf("write temp left behind: %v", leftover)
	}
}

// TestWriteTextCommitFailureKeepsOldTarget forces the no-posix-rename fallback
// and fails the temp->target commit rename after the old target was moved
// aside: the write must error, the previous target must be rolled back, and
// neither the temp nor the backup may remain.
func TestWriteTextCommitFailureKeepsOldTarget(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	writeLocal(t, filepath.Join(root, "keep.txt"), "previous")
	svc, _ := newTestServiceWrapped(t, root, home, false, func(c sshclient.SFTPClient) sshclient.SFTPClient {
		var commits int
		return &specRenameClient{
			SFTPClient: c,
			noPosix:    true,
			renameFail: func(oldpath, _ string) bool {
				if !strings.HasPrefix(path.Base(oldpath), ".nodeshell-write-") {
					return false
				}
				commits++
				return commits == 1
			},
		}
	})
	const sid = "s1"

	_, err := svc.WriteText(sid, "keep.txt", "new content", 1<<20)
	if err == nil {
		t.Fatal("write with a failing commit must error")
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != apperror.Unknown {
		t.Fatalf("error = %v, want a coded UNKNOWN error", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "keep.txt"))
	if err != nil || string(got) != "previous" {
		t.Fatalf("target content = %q (err %v), want the rolled-back previous", got, err)
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-write-*"))
	if len(leftover) != 0 {
		t.Fatalf("write temp left behind: %v", leftover)
	}
	backups, _ := filepath.Glob(filepath.Join(root, ".nodeshell-backup-*"))
	if len(backups) != 0 {
		t.Fatalf("write backup left behind: %v", backups)
	}
}

// TestReadWriteTextPosixPathSemantics: a relative path resolves under the
// session's current remote directory; an absolute path is used verbatim,
// never re-joined under cwd a second time.
func TestReadWriteTextPosixPathSemantics(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	if err := svc.Mkdir(sid, "sub"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := svc.WriteText(sid, "sub/notes.txt", "relative write", 1<<20); err != nil {
		t.Fatalf("WriteText relative: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub", "notes.txt")); err != nil {
		t.Fatalf("relative write landed outside cwd: %v", err)
	}

	cwd, err := svc.Cwd(sid)
	if err != nil {
		t.Fatalf("Cwd: %v", err)
	}
	// The absolute form of the same file is the POSIX join of cwd and the
	// relative name; it must resolve verbatim.
	abs := JoinRemote(cwd, "sub/notes.txt")
	absPath, got, err := svc.ReadText(sid, abs, 1<<20)
	if err != nil {
		t.Fatalf("ReadText absolute: %v", err)
	}
	if got != "relative write" {
		t.Fatalf("absolute read content = %q, want %q", got, "relative write")
	}
	if absPath != abs {
		t.Fatalf("resolved path = %q, want %q", absPath, abs)
	}

	relPath, got, err := svc.ReadText(sid, "sub/notes.txt", 1<<20)
	if err != nil {
		t.Fatalf("ReadText relative: %v", err)
	}
	if got != "relative write" || relPath != abs {
		t.Fatalf("relative read = (%q, %q), want (%q, %q)", relPath, got, abs, "relative write")
	}

	absWrite := JoinRemote(cwd, "sub/abs.txt")
	if _, err := svc.WriteText(sid, absWrite, "absolute write", 1<<20); err != nil {
		t.Fatalf("WriteText absolute: %v", err)
	}
	gotAbs, err := os.ReadFile(filepath.Join(root, "sub", "abs.txt"))
	if err != nil || string(gotAbs) != "absolute write" {
		t.Fatalf("absolute write landed elsewhere: %q (err %v)", gotAbs, err)
	}
}

// --- lifecycle: lazy create, reuse, dispose ---

// fakeClient is a controllable SFTPClient fake for lifecycle tests.
type fakeClient struct {
	mu      sync.Mutex
	id      int
	closed  bool
	onClose func(int)
}

func (f *fakeClient) RealPath(string) (string, error)       { return "/", nil }
func (f *fakeClient) ReadDir(string) ([]os.FileInfo, error) { return nil, nil }
func (f *fakeClient) Stat(string) (os.FileInfo, error)      { return fakeInfo{dir: true}, nil }
func (f *fakeClient) Lstat(string) (os.FileInfo, error)     { return fakeInfo{dir: true}, nil }
func (f *fakeClient) Open(string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *fakeClient) Create(string) (io.WriteCloser, error) { return nopWriteCloser{}, nil }
func (f *fakeClient) Mkdir(string) error                    { return nil }
func (f *fakeClient) MkdirAll(string) error                 { return nil }
func (f *fakeClient) Remove(string) error                   { return nil }
func (f *fakeClient) RemoveDirectory(string) error          { return nil }
func (f *fakeClient) Rename(string, string) error           { return nil }
func (f *fakeClient) PosixRename(string, string) error      { return nil }
func (f *fakeClient) Chmod(string, os.FileMode) error       { return nil }
func (f *fakeClient) HasExtension(string) (string, bool)    { return "", false }

func (f *fakeClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if f.onClose != nil {
		f.onClose(f.id)
	}
	return nil
}

type fakeInfo struct{ dir bool }

func (fakeInfo) Name() string  { return "f" }
func (f fakeInfo) Size() int64 { return 0 }
func (f fakeInfo) Mode() os.FileMode {
	if f.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (fakeInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (f fakeInfo) IsDir() bool      { return f.dir }
func (fakeInfo) Sys() any           { return nil }

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// countingOpener hands out numbered fake clients and records open/close
// order.
type countingOpener struct {
	mu     sync.Mutex
	next   int
	open   []int
	closed []int
}

func (o *countingOpener) NewSFTPClient(string) (sshclient.SFTPClient, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.next++
	id := o.next
	o.open = append(o.open, id)
	return &fakeClient{id: id, onClose: func(n int) {
		o.mu.Lock()
		o.closed = append(o.closed, n)
		o.mu.Unlock()
	}}, nil
}

func (o *countingOpener) snapshot() (open, closed []int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]int(nil), o.open...), append([]int(nil), o.closed...)
}

// TestLazyOnceReuse: several operations on one session reuse a single SFTP
// client; Dispose closes it; the next operation opens a fresh one.
func TestLazyOnceReuse(t *testing.T) {
	opener := &countingOpener{}
	svc := New(Deps{Opener: opener, Home: t.TempDir()})
	const sid = "s1"

	if _, err := svc.Cwd(sid); err != nil {
		t.Fatalf("Cwd: %v", err)
	}
	if _, err := svc.List(sid, ""); err != nil {
		t.Fatalf("List: %v", err)
	}
	if err := svc.Mkdir(sid, "x"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	open, closed := opener.snapshot()
	if len(open) != 1 || len(closed) != 0 {
		t.Fatalf("after 3 ops: open=%v closed=%v, want one client open and none closed", open, closed)
	}

	svc.Dispose(sid)
	open, closed = opener.snapshot()
	if len(closed) != 1 || closed[0] != open[0] {
		t.Fatalf("after Dispose: closed=%v want the single opened client closed", closed)
	}

	if _, err := svc.Cwd(sid); err != nil {
		t.Fatalf("Cwd after dispose: %v", err)
	}
	open2, closed2 := opener.snapshot()
	if len(open2) != 2 || len(closed2) != 1 {
		t.Fatalf("after reopen: open=%v closed=%v, want a fresh client", open2, closed2)
	}
}

// TestConcurrentOpsShareOneClient: concurrent operations on one session never
// open more than one client.
func TestConcurrentOpsShareOneClient(t *testing.T) {
	opener := &countingOpener{}
	svc := New(Deps{Opener: opener, Home: t.TempDir()})
	const sid = "s1"

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.List(sid, "")
			_, _ = svc.Cwd(sid)
		}()
	}
	wg.Wait()
	open, _ := opener.snapshot()
	if len(open) != 1 {
		t.Fatalf("concurrent ops opened %d clients, want 1 (lazy once)", len(open))
	}
}

// TestDisposeAllClosesEverything: shutdown-style disposal closes every
// session client once.
func TestDisposeAllClosesEverything(t *testing.T) {
	opener := &countingOpener{}
	svc := New(Deps{Opener: opener, Home: t.TempDir()})
	for _, sid := range []string{"a", "b"} {
		if _, err := svc.Cwd(sid); err != nil {
			t.Fatalf("Cwd(%s): %v", sid, err)
		}
	}
	svc.DisposeAll()
	open, closed := opener.snapshot()
	if len(closed) != 2 {
		t.Fatalf("DisposeAll closed %d clients, want 2 (open=%v closed=%v)", len(closed), open, closed)
	}
}

// TestSessionNotFound: the opener error (SESSION_NOT_FOUND from the sessions
// manager) surfaces unchanged.
func TestSessionNotFound(t *testing.T) {
	svc := New(Deps{
		Opener: openerFunc(func(string) (sshclient.SFTPClient, error) {
			return nil, &Error{Code: "SESSION_NOT_FOUND", Message: "Session not found: nope"}
		}),
		Home: t.TempDir(),
	})
	_, err := svc.Cwd("nope")
	var coded interface{ ErrorCode() string }
	if err == nil || !errors.As(err, &coded) || coded.ErrorCode() != "SESSION_NOT_FOUND" {
		t.Fatalf("error = %v, want SESSION_NOT_FOUND", err)
	}
}

type openerFunc func(sessionID string) (sshclient.SFTPClient, error)

func (f openerFunc) NewSFTPClient(sessionID string) (sshclient.SFTPClient, error) {
	return f(sessionID)
}

// --- MCP runtime support (T1.7.3): WriteText resolved path, UploadAs ---

// TestWriteTextReturnsResolvedPath: WriteText returns the resolved remote
// path of the written target (realpath of the parent re-attached to the
// leaf), POSIX-absolute, so the MCP sftp_write tool can echo the true path.
func TestWriteTextReturnsResolvedPath(t *testing.T) {
	root := t.TempDir()
	svc, _ := newTestService(t, root, t.TempDir(), false)
	const sid = "s1"

	resolved, err := svc.WriteText(sid, "notes.txt", "content", 1<<20)
	if err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if !strings.HasPrefix(resolved, "/") || !strings.HasSuffix(resolved, "/notes.txt") {
		t.Fatalf("WriteText resolved path = %q, want a POSIX-absolute path ending /notes.txt", resolved)
	}
	// The returned path must be the actual target: reading it back yields the
	// same resolved path.
	readPath, _, err := svc.ReadText(sid, "notes.txt", 1<<20)
	if err != nil {
		t.Fatalf("ReadText: %v", err)
	}
	if readPath != resolved {
		t.Fatalf("WriteText path %q != ReadText path %q", resolved, readPath)
	}
}

// TestUploadAsRemoteName: UploadAs places the local file under the explicit
// remote name (with path segments resolved against the session cwd), and the
// basename default matches Upload.
func TestUploadAsRemoteName(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, false)
	const sid = "s1"

	src := filepath.Join(home, "data.bin")
	writeLocal(t, src, "payload")

	if err := svc.UploadAs(sid, src, "renamed.txt"); err != nil {
		t.Fatalf("UploadAs: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "renamed.txt"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("remote renamed file = %q (err %v), want payload", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "data.bin")); !os.IsNotExist(err) {
		t.Fatal("UploadAs must not also create the basename file")
	}
	leftover, _ := filepath.Glob(filepath.Join(root, ".nodeshell-upload-*"))
	if len(leftover) != 0 {
		t.Fatalf("upload temp left behind: %v", leftover)
	}

	// A remoteName with path segments resolves against the session cwd.
	if err := svc.Mkdir(sid, "sub"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := svc.UploadAs(sid, src, "sub/inner.txt"); err != nil {
		t.Fatalf("UploadAs with nested name: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(root, "sub", "inner.txt"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("nested remote file = %q (err %v), want payload", got, err)
	}

	// Upload (no remoteName) keeps the basename behaviour.
	if err := svc.Upload(sid, src); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "data.bin")); err != nil {
		t.Fatalf("Upload basename file missing: %v", err)
	}
}

// TestUploadAsHomeEscapeRejected: UploadAs enforces the home boundary on the
// local source exactly like Upload.
func TestUploadAsHomeEscapeRejected(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, false)
	const sid = "s1"

	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "x.txt")
	writeLocal(t, outsideFile, "x")

	err := svc.UploadAs(sid, outsideFile, "x.txt")
	if err == nil {
		t.Fatal("UploadAs of a path outside home must be rejected")
	}
	if !errors.Is(err, localpathguard.ErrOutsideHome) {
		t.Fatalf("error = %v, want the outside-home guard error", err)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaped UploadAs created a remote file")
	}
}

// TestUploadAsEmptyRemoteNameFallsBackToBasename: an empty remoteName behaves
// exactly like Upload (basename).
func TestUploadAsEmptyRemoteNameFallsBackToBasename(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	svc, _ := newTestService(t, root, home, false)
	const sid = "s1"

	src := filepath.Join(home, "data.bin")
	writeLocal(t, src, "payload")

	if err := svc.UploadAs(sid, src, ""); err != nil {
		t.Fatalf("UploadAs with empty remoteName: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "data.bin")); err != nil {
		t.Fatalf("basename file missing after UploadAs with empty name: %v", err)
	}
}

// --- helpers ---

func writeLocal(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func makeLocalSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable (no privilege): %v", err)
	}
}
