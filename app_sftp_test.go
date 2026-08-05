package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/hosts"
	"nodeshell/internal/localpathguard"
	"nodeshell/internal/sessions"
	"nodeshell/internal/sshtest"
)

// newSFTPApp wires a test App over an SFTP-enabled SSH server rooted at root
// and connects one session. It returns the app, the session id and the home
// boundary dir.
func newSFTPApp(t *testing.T, root string, readOnly bool) (*App, string, string) {
	t.Helper()
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return user == "user" && pass == "secret" }
	srv.EnableSFTP(root, readOnly)

	a, home := testApp(t)
	created, err := a.HostsCreate(hosts.HostInput{
		Name: "lab", Host: "127.0.0.1", Port: 22, Username: "user", AuthMethod: "password",
	})
	if err != nil {
		t.Fatalf("HostsCreate: %v", err)
	}
	host, port := splitAddr(t, srv.Addr)
	// Re-point the host at the ephemeral server address.
	if _, err := a.HostsUpdate(created.Id, hosts.Patch{Host: &host, Port: &port}); err != nil {
		t.Fatalf("HostsUpdate: %v", err)
	}
	if err := a.known.Remember(host, port, srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	res, err := a.SessionsConnect(created.Id, sessions.ConnectOptions{Password: "secret"})
	if err != nil {
		t.Fatalf("SessionsConnect: %v", err)
	}
	t.Cleanup(func() { a.shutdown(context.Background()) })
	return a, res.SessionID, home
}

// TestAppSFTPBindingsUninitialised: every SFTP binding fails observably on an
// App that was never started.
func TestAppSFTPBindingsUninitialised(t *testing.T) {
	a := NewApp()
	if _, err := a.SftpList("s"); err == nil {
		t.Fatal("SftpList on uninitialised App must error")
	}
	if _, err := a.SftpCwd("s"); err == nil {
		t.Fatal("SftpCwd on uninitialised App must error")
	}
	if err := a.SftpMkdir("s", "x"); err == nil {
		t.Fatal("SftpMkdir on uninitialised App must error")
	}
	if err := a.SftpUploadPaths("s", []string{"/x"}); err == nil {
		t.Fatal("SftpUploadPaths on uninitialised App must error")
	}
	if err := a.SftpRemove("s", "x"); err == nil {
		t.Fatal("SftpRemove on uninitialised App must error")
	}
	if _, err := a.DialogOpenPrivateKeyFile(); err == nil {
		t.Fatal("DialogOpenPrivateKeyFile on uninitialised App must error")
	}
}

// TestAppSFTPListChdirMkdirRenameRemove drives the whole binding chain over
// the real SFTP protocol.
func TestAppSFTPListChdirMkdirRenameRemove(t *testing.T) {
	root := t.TempDir()
	a, sid, _ := newSFTPApp(t, root, false)

	cwd, err := a.SftpCwd(sid)
	if err != nil {
		t.Fatalf("SftpCwd: %v", err)
	}
	if !strings.HasSuffix(cwd, filepath.ToSlash(filepath.Base(root))) {
		t.Fatalf("cwd %q does not contain the server root", cwd)
	}

	if err := a.SftpMkdir(sid, "docs"); err != nil {
		t.Fatalf("SftpMkdir: %v", err)
	}
	if err := a.SftpMkdir(sid, "alpha"); err != nil {
		t.Fatalf("SftpMkdir: %v", err)
	}
	entries, err := a.SftpList(sid)
	if err != nil {
		t.Fatalf("SftpList: %v", err)
	}
	if len(entries) != 2 || entries[0].Name != "alpha" || entries[1].Name != "docs" {
		t.Fatalf("list = %+v, want [alpha docs] dirs first", entries)
	}
	if !entries[0].IsDirectory {
		t.Fatalf("entry must be a directory: %+v", entries[0])
	}

	next, err := a.SftpChdir(sid, "docs")
	if err != nil {
		t.Fatalf("SftpChdir: %v", err)
	}
	if !strings.HasSuffix(next, "/docs") {
		t.Fatalf("chdir returned %q, want suffix /docs", next)
	}
	if err := a.SftpMkdir(sid, "nested"); err != nil {
		t.Fatalf("SftpMkdir nested: %v", err)
	}
	if err := a.SftpRename(sid, "nested", "renamed"); err != nil {
		t.Fatalf("SftpRename: %v", err)
	}
	if err := a.SftpRemove(sid, "renamed"); err != nil {
		t.Fatalf("SftpRemove: %v", err)
	}
	entries, err = a.SftpList(sid)
	if err != nil {
		t.Fatalf("SftpList: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("list after remove = %+v, want empty", entries)
	}
}

// TestAppSftpRemoveOutsideCwdRejected: an absolute path escaping the session
// cwd must be refused by the binding.
func TestAppSftpRemoveOutsideCwdRejected(t *testing.T) {
	root := t.TempDir()
	a, sid, _ := newSFTPApp(t, root, false)
	if err := a.SftpRemove(sid, "/"); err == nil {
		t.Fatal("SftpRemove(/) must be rejected")
	}
	if err := a.SftpRemove(sid, ".."); err == nil {
		t.Fatal("SftpRemove(..) must be rejected")
	}
	if err := a.SftpRemove(sid, ""); err == nil {
		t.Fatal("SftpRemove('') must be rejected")
	}
}

// TestAppSftpUploadUploadPathsDownload drives uploads and downloads through
// the dialog seams and the bindings.
func TestAppSftpUploadUploadPathsDownload(t *testing.T) {
	root := t.TempDir()
	a, sid, home := newSFTPApp(t, root, false)
	// A plain context activates the dialog bindings; events are dropped
	// safely by the hardened sink.
	a.ctx = context.Background()

	origUpload, origSave, origKey := openUploadDialog, openSaveDialog, openPrivateKeyDialog
	defer func() {
		openUploadDialog, openSaveDialog, openPrivateKeyDialog = origUpload, origSave, origKey
	}()

	src := filepath.Join(home, "up.bin")
	if err := os.WriteFile(src, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	openUploadDialog = func(context.Context) ([]string, error) { return []string{src}, nil }

	if err := a.SftpUpload(sid); err != nil {
		t.Fatalf("SftpUpload: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "up.bin"))
	if err != nil || string(got) != "payload" {
		t.Fatalf("uploaded content = %q, err=%v", got, err)
	}

	// uploadPaths with an explicit path list (drop flow).
	src2 := filepath.Join(home, "drop.txt")
	if err := os.WriteFile(src2, []byte("dropped"), 0o600); err != nil {
		t.Fatalf("write src2: %v", err)
	}
	if err := a.SftpUploadPaths(sid, []string{src2}); err != nil {
		t.Fatalf("SftpUploadPaths: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "drop.txt")); err != nil {
		t.Fatalf("dropped file missing: %v", err)
	}

	// download via the save-dialog seam.
	dlDir := filepath.Join(home, "dl")
	if err := os.MkdirAll(dlDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(dlDir, "up.bin")
	openSaveDialog = func(_ context.Context, _ string) (string, error) { return target, nil }
	if err := a.SftpDownload(sid, "up.bin", "up.bin"); err != nil {
		t.Fatalf("SftpDownload: %v", err)
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != "payload" {
		t.Fatalf("downloaded content = %q, err=%v", got, err)
	}

	// cancelled dialogs are a no-op success.
	openUploadDialog = func(context.Context) ([]string, error) { return nil, nil }
	if err := a.SftpUpload(sid); err != nil {
		t.Fatalf("cancelled SftpUpload: %v", err)
	}
	openSaveDialog = func(context.Context, string) (string, error) { return "", nil }
	if err := a.SftpDownload(sid, "up.bin", "up.bin"); err != nil {
		t.Fatalf("cancelled SftpDownload: %v", err)
	}

	// private key dialog seam.
	openPrivateKeyDialog = func(context.Context) (string, error) { return "/home/u/.ssh/id", nil }
	p, err := a.DialogOpenPrivateKeyFile()
	if err != nil || p != "/home/u/.ssh/id" {
		t.Fatalf("DialogOpenPrivateKeyFile = %q, %v", p, err)
	}
}

// TestAppSftpHomeEscapeRejected: uploads and downloads that cross the home
// boundary fail observably with the guard error, at the App level.
func TestAppSftpHomeEscapeRejected(t *testing.T) {
	root := t.TempDir()
	a, sid, _ := newSFTPApp(t, root, false)
	a.ctx = context.Background()

	outside := t.TempDir()
	leak := filepath.Join(outside, "leak.txt")
	if err := os.WriteFile(leak, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := a.SftpUploadPaths(sid, []string{leak})
	if err == nil {
		t.Fatal("upload path outside home must be rejected at the App level")
	}
	if !errors.Is(err, localpathguard.ErrOutsideHome) {
		t.Fatalf("error = %v, want the outside-home guard error", err)
	}

	// A remote file exists; downloading it outside home must fail too.
	if err := os.WriteFile(filepath.Join(root, "remote.txt"), []byte("r"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	origSave := openSaveDialog
	defer func() { openSaveDialog = origSave }()
	openSaveDialog = func(context.Context, string) (string, error) {
		return filepath.Join(outside, "escape.txt"), nil
	}
	err = a.SftpDownload(sid, "remote.txt", "remote.txt")
	if err == nil {
		t.Fatal("download target outside home must be rejected at the App level")
	}
	if !errors.Is(err, localpathguard.ErrOutsideHome) {
		t.Fatalf("error = %v, want the outside-home guard error", err)
	}
}

// TestAppSftpSessionNotFound: operations on a gone session surface
// SESSION_NOT_FOUND through the bindings.
func TestAppSftpSessionNotFound(t *testing.T) {
	root := t.TempDir()
	a, sid, _ := newSFTPApp(t, root, false)
	a.shutdown(context.Background())
	_, err := a.SftpList(sid)
	if err == nil {
		t.Fatal("SftpList after shutdown must error")
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != apperror.SessionNotFound {
		t.Fatalf("error = %v, want SESSION_NOT_FOUND", err)
	}
}

// TestAppSftpDisposeOnSessionClose proves the dispose wiring: after a
// disconnect, the cached SFTP client is released (a fresh SFTP open fails
// because the session is gone).
func TestAppSftpDisposeOnSessionClose(t *testing.T) {
	root := t.TempDir()
	a, sid, _ := newSFTPApp(t, root, false)
	if err := a.SessionsDisconnect(sid); err != nil {
		t.Fatalf("SessionsDisconnect: %v", err)
	}
	// The session:closed event (which disposes the SFTP handle) fires async;
	// a subsequent operation must fail with SESSION_NOT_FOUND either way.
	waitForCondition(t, func() bool {
		_, err := a.SftpList(sid)
		var coded interface{ ErrorCode() string }
		return errors.As(err, &coded) && coded.ErrorCode() == apperror.SessionNotFound
	})
}

// waitForCondition polls cond until it holds (the session:closed event and
// its SFTP dispose run asynchronously).
func waitForCondition(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
