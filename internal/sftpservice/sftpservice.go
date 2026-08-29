// Package sftpservice owns the per-session SFTP file operations: list, cwd,
// chdir, mkdir, rename, recursive remove, upload and download with transfer
// progress events, plus capped text read/write for MCP and the GUI editor.
// Remote paths are POSIX-normalised with the path package; local paths
// (upload sources, download targets) are checked against the user home
// directory through localpathguard. It depends only on the sshclient SFTP
// surface and a session resolver — never on Wails or the sessions package.
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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"nodeshell/internal/apperror"
	"nodeshell/internal/atomicfile"
	"nodeshell/internal/localpathguard"
	"nodeshell/internal/sshclient"
)

// EventTransferProgress is the Wails event name for transfer progress
// (matches IPC.sftpTransferProgress in the frontend).
const EventTransferProgress = "sftp:transferProgress"

// Entry is one directory listing row (matches SftpEntry in the adapter).
type Entry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	IsDirectory bool   `json:"isDirectory"`
	Size        int64  `json:"size"`
	ModifyTime  int64  `json:"modifyTime"` // Unix milliseconds
}

// ProgressEvent matches SftpTransferProgressEvent in the frontend. A failed
// transfer still emits done=true (Electron parity), then the error surfaces
// through the binding.
type ProgressEvent struct {
	SessionID   string `json:"sessionId"`
	Direction   string `json:"direction"` // "up" | "down"
	Name        string `json:"name"`
	Transferred int64  `json:"transferred"`
	Total       int64  `json:"total"`
	Done        bool   `json:"done"`
}

// Error carries a stable code for SFTP failures. Messages never include the
// remote path or a local absolute path, so nothing sensitive crosses IPC.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func (e *Error) ErrorCode() string { return e.Code }

func errf(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// EventSink is the progress-event seam; the production implementation emits
// through the Wails runtime, tests record into a slice. A nil sink is a
// no-op.
type EventSink interface {
	Emit(event string, payload any)
}

type nopSink struct{}

func (nopSink) Emit(string, any) {}

// SFTPOpener opens an SFTP client over a session's SSH connection
// (*sessions.Manager satisfies it).
type SFTPOpener interface {
	NewSFTPClient(sessionID string) (sshclient.SFTPClient, error)
}

// Deps wires a Service. Home is the symlink-resolved user home boundary for
// local paths; an empty Home rejects every local path.
type Deps struct {
	Opener SFTPOpener
	Sink   EventSink
	Home   string
	UUID   func() string
}

// Service owns one lazily-created, reused SFTP client per session plus the
// session's current remote working directory.
type Service struct {
	opener   SFTPOpener
	sink     EventSink
	home     string
	uuid     func() string
	mu       sync.Mutex
	sessions map[string]*Session
}

// New builds a Service.
func New(d Deps) *Service {
	if d.Sink == nil {
		d.Sink = nopSink{}
	}
	if d.UUID == nil {
		d.UUID = uuid.NewString
	}
	return &Service{
		opener:   d.Opener,
		sink:     d.Sink,
		home:     d.Home,
		uuid:     d.UUID,
		sessions: map[string]*Session{},
	}
}

// Session is one session's SFTP handle: a lazily-created, reused client plus
// the pinned current working directory. ensure() is the lazy-once seam; the
// client is replaced only by Dispose.
type Session struct {
	sessionID string
	opener    SFTPOpener
	mu        sync.Mutex
	client    sshclient.SFTPClient
	cwd       string
}

// session returns (creating if needed) the handle for a session id.
func (s *Service) session(id string) (*Session, error) {
	if s.opener == nil {
		return nil, errf(apperror.Unknown, "SFTP is not initialised")
	}
	s.mu.Lock()
	ss := s.sessions[id]
	if ss == nil {
		ss = &Session{sessionID: id, opener: s.opener}
		s.sessions[id] = ss
	}
	s.mu.Unlock()
	return ss, nil
}

// ensure lazily opens the SFTP client once and pins the initial cwd to the
// realpath of "." (mirrors the Electron ensure). Concurrent callers share
// the first client; a failed open is retried on the next call. After
// Interrupt the client is gone but cwd stays: a reopen must not RealPath(".")
// or the GUI panel would jump back to remote home.
func (ss *Session) ensure() (sshclient.SFTPClient, string, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.client != nil {
		return ss.client, ss.cwd, nil
	}
	client, err := ss.opener.NewSFTPClient(ss.sessionID)
	if err != nil {
		return nil, "", err
	}
	if ss.cwd != "" {
		ss.client = client
		return client, ss.cwd, nil
	}
	cwd, err := client.RealPath(".")
	if err != nil {
		_ = client.Close()
		return nil, "", errf(apperror.Unknown, "Failed to resolve remote home directory")
	}
	ss.client = client
	ss.cwd = cwd
	return client, cwd, nil
}

// setCwd updates the pinned cwd after a successful chdir.
func (ss *Session) setCwd(cwd string) {
	ss.mu.Lock()
	ss.cwd = cwd
	ss.mu.Unlock()
}

// Dispose closes the session's SFTP client and drops the handle, so a later
// call resolves a fresh client (and fails with SESSION_NOT_FOUND once the
// SSH session itself is gone).
func (s *Service) Dispose(sessionID string) {
	s.mu.Lock()
	ss := s.sessions[sessionID]
	delete(s.sessions, sessionID)
	s.mu.Unlock()
	if ss == nil {
		return
	}
	ss.mu.Lock()
	c := ss.client
	ss.client = nil
	ss.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// Interrupt closes the cached SFTP client so an in-flight pkg/sftp call
// unblocks, but keeps the session handle and its pinned cwd. The next
// operation opens a fresh client at the same directory. Guest (in-app Agent)
// cancel uses this: Dispose would drop the handle the GUI panel shares and
// snap the panel back to remote home.
func (s *Service) Interrupt(sessionID string) {
	s.mu.Lock()
	ss := s.sessions[sessionID]
	s.mu.Unlock()
	if ss == nil {
		return
	}
	ss.mu.Lock()
	c := ss.client
	ss.client = nil
	ss.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// DisposeAll releases every session handle (app shutdown).
func (s *Service) DisposeAll() {
	s.mu.Lock()
	all := make([]*Session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		all = append(all, ss)
	}
	s.sessions = map[string]*Session{}
	s.mu.Unlock()
	for _, ss := range all {
		ss.mu.Lock()
		c := ss.client
		ss.client = nil
		ss.mu.Unlock()
		if c != nil {
			_ = c.Close()
		}
	}
}

// JoinRemote normalises a remote path name against cwd, POSIX-style: a
// leading "/" is absolute, anything else is joined under cwd; "." and ".."
// segments resolve; backslashes are treated as separators (legacy
// compatibility). The result always starts with "/" and never ends with "/"
// (except "/" itself). Pure and shared with the tests.
func JoinRemote(cwd, name string) string {
	var raw string
	if strings.HasPrefix(name, "/") {
		raw = name
	} else {
		base := strings.TrimSuffix(cwd, "/")
		raw = base + "/" + name
	}
	parts := strings.Split(strings.ReplaceAll(raw, "\\", "/"), "/")
	stack := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		default:
			stack = append(stack, part)
		}
	}
	return "/" + strings.Join(stack, "/")
}

// withinRoot reports whether p equals root or is a descendant of root
// (POSIX). Both are canonical: absolute, "/"-separated, no trailing slash.
func withinRoot(p, root string) bool {
	if p == root {
		return true
	}
	if root == "/" {
		return strings.HasPrefix(p, "/")
	}
	return strings.HasPrefix(p, root+"/")
}

// List returns the listing of the session's current remote directory, or of
// the joined remotePath when given. Directories sort first, then by name;
// symlinks are followed (matches the Electron listing), and a symlink whose
// target cannot be stat'ed surfaces as a plain file entry.
func (s *Service) List(sessionID, remotePath string) ([]Entry, error) {
	ss, err := s.session(sessionID)
	if err != nil {
		return nil, err
	}
	client, cwd, err := ss.ensure()
	if err != nil {
		return nil, err
	}
	target := cwd
	if remotePath != "" {
		target = JoinRemote(cwd, remotePath)
	}
	resolved := target
	if remotePath != "" {
		r, err := client.RealPath(target)
		if err != nil {
			return nil, mapRemoteErr(err)
		}
		resolved = r
	}
	listed, err := client.ReadDir(resolved)
	if err != nil {
		return nil, mapRemoteErr(err)
	}

	entries := make([]Entry, 0, len(listed))
	var symlinks []string
	for _, fi := range listed {
		name := fi.Name()
		if name == "." || name == ".." {
			continue
		}
		if isLink(fi) {
			symlinks = append(symlinks, name)
			continue
		}
		entries = append(entries, entryFromListing(resolved, fi))
	}
	for _, name := range symlinks {
		full := JoinRemote(resolved, name)
		fi, err := client.Stat(full) // follows the link
		if err != nil {
			entries = append(entries, Entry{Name: name, Path: full})
			continue
		}
		entries = append(entries, entryFromListing(resolved, fi))
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDirectory != entries[j].IsDirectory {
			return entries[i].IsDirectory
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// Cwd returns the session's current remote directory.
func (s *Service) Cwd(sessionID string) (string, error) {
	ss, err := s.session(sessionID)
	if err != nil {
		return "", err
	}
	_, cwd, err := ss.ensure()
	if err != nil {
		return "", err
	}
	return cwd, nil
}

// Chdir resolves remotePath against the current directory, requires it to be
// a directory, and returns the new cwd.
func (s *Service) Chdir(sessionID, remotePath string) (string, error) {
	ss, err := s.session(sessionID)
	if err != nil {
		return "", err
	}
	client, cwd, err := ss.ensure()
	if err != nil {
		return "", err
	}
	next := JoinRemote(cwd, remotePath)
	resolved, err := client.RealPath(next)
	if err != nil {
		return "", mapRemoteErr(err)
	}
	fi, err := client.Stat(resolved)
	if err != nil {
		return "", mapRemoteErr(err)
	}
	if !isDir(fi) {
		return "", errf(apperror.Unknown, "Not a directory")
	}
	ss.setCwd(resolved)
	return resolved, nil
}

// Mkdir creates one directory under the session's current remote directory.
func (s *Service) Mkdir(sessionID, name string) error {
	ss, err := s.session(sessionID)
	if err != nil {
		return err
	}
	client, cwd, err := ss.ensure()
	if err != nil {
		return err
	}
	target, err := resolveMutable(client, JoinRemote(cwd, name))
	if err != nil {
		return err
	}
	return mapRemoteErr(client.Mkdir(target))
}

// Rename moves fromName to toName under the session's current remote
// directory.
func (s *Service) Rename(sessionID, fromName, toName string) error {
	ss, err := s.session(sessionID)
	if err != nil {
		return err
	}
	client, cwd, err := ss.ensure()
	if err != nil {
		return err
	}
	from, err := client.RealPath(JoinRemote(cwd, fromName))
	if err != nil {
		return mapRemoteErr(err)
	}
	to, err := resolveMutable(client, JoinRemote(cwd, toName))
	if err != nil {
		return err
	}
	return mapRemoteErr(client.Rename(from, to))
}

// Remove deletes the remote path recursively. The target must resolve inside
// the session's current remote directory; "/", ".", "..", empty names and
// any name that normalises to the current directory ("./", "x/..") are
// rejected outright, as is a target whose realpath lands on the resolved
// cwd. Symlinks are unlinked, never followed — a symlink to a directory can
// never cause recursion outside the explicit target subtree.
func (s *Service) Remove(sessionID, remotePath string) error {
	ss, err := s.session(sessionID)
	if err != nil {
		return err
	}
	client, cwd, err := ss.ensure()
	if err != nil {
		return err
	}
	switch remotePath {
	case "", ".", "..", "/":
		return errf(apperror.Unknown, "Refusing to remove the remote root or current directory")
	}
	target := JoinRemote(cwd, remotePath)
	if target == cwd || target == "/" {
		return errf(apperror.Unknown, "Refusing to remove the remote root or current directory")
	}
	if !withinRoot(target, cwd) {
		return errf(apperror.Unknown, "Refusing to remove a path outside the current directory")
	}
	fi, err := client.Lstat(target)
	if err != nil {
		return mapRemoteErr(err)
	}
	if isLink(fi) {
		// The symlink itself is unlinked; its target is never touched.
		return mapRemoteErr(client.Remove(target))
	}
	// The recursion root is the symlink-resolved cwd, so a target whose
	// realpath lands on cwd (via any alias) is refused, never recursed.
	root, err := client.RealPath(cwd)
	if err != nil {
		return mapRemoteErr(err)
	}
	resolved, err := client.RealPath(target)
	if err != nil {
		return mapRemoteErr(err)
	}
	if resolved == root || resolved == "/" {
		return errf(apperror.Unknown, "Refusing to remove the remote root or current directory")
	}
	if !withinRoot(resolved, root) {
		return errf(apperror.Unknown, "Refusing to remove a path outside the current directory")
	}
	return s.removePath(client, resolved, root)
}

// removePath recursively deletes resolved (a directory or file) while never
// leaving the root subtree. Every child is re-verified lexically; a symlink
// is unlinked without following.
func (s *Service) removePath(client sshclient.SFTPClient, resolved, root string) error {
	fi, err := client.Lstat(resolved)
	if err != nil {
		return mapRemoteErr(err)
	}
	if isLink(fi) || !isDir(fi) {
		return mapRemoteErr(client.Remove(resolved))
	}
	listed, err := client.ReadDir(resolved)
	if err != nil {
		return mapRemoteErr(err)
	}
	for _, e := range listed {
		name := e.Name()
		if name == "." || name == ".." {
			continue
		}
		child := JoinRemote(resolved, name)
		if !withinRoot(child, root) {
			return errf(apperror.Unknown, "Refusing to remove a path outside the target subtree")
		}
		if err := s.removePath(client, child, root); err != nil {
			return err
		}
	}
	return mapRemoteErr(client.RemoveDirectory(resolved))
}

// Upload streams one local file into the session's current remote directory
// under its basename (UploadAs with an empty name).
func (s *Service) Upload(sessionID, localPath string) error {
	return s.UploadAs(sessionID, localPath, "")
}

// UploadAs streams one local file into the session's current remote
// directory under name — the explicit remote name when given (path segments
// resolve against the session cwd), otherwise the local basename. The remote
// write goes to a temp file that is renamed over the target on success, so a
// partial or failed transfer never leaves a truncated target in place. The
// local source must resolve inside the user home (symlinks followed and
// re-checked) and be a regular file — the home boundary is enforced here, at
// the service entry, never bypassed. An existing target is replaced (POSIX
// rename semantics, matching the Electron createWriteStream overwrite
// behaviour).
func (s *Service) UploadAs(sessionID, localPath, name string) error {
	ss, err := s.session(sessionID)
	if err != nil {
		return err
	}
	client, cwd, err := ss.ensure()
	if err != nil {
		return err
	}
	if name == "" {
		name = path.Base(strings.ReplaceAll(localPath, "\\", "/"))
	}
	if name == "" || name == "/" {
		return errf(apperror.Unknown, "Upload name is invalid")
	}
	safe, err := localpathguard.ResolveExisting(localPath, s.home)
	if err != nil {
		return err
	}
	info, err := os.Stat(safe)
	if err != nil {
		return errf(apperror.Unknown, "Failed to inspect the local file")
	}
	if !info.Mode().IsRegular() {
		return errf(apperror.Unknown, "Upload source is not a regular file")
	}
	target, err := resolveMutable(client, JoinRemote(cwd, name))
	if err != nil {
		return err
	}
	local, err := os.Open(safe)
	if err != nil {
		return errf(apperror.Unknown, "Failed to open the local file")
	}
	defer local.Close()
	total := info.Size()

	tmpName := path.Join(path.Dir(target), ".nodeshell-upload-"+s.uuid())
	transfer := newTransfer(s.sink, sessionID, "up", name, total)
	transfer.start()
	if err := s.uploadFile(client, local, tmpName, target, transfer); err != nil {
		transfer.fail()
		return err
	}
	transfer.finish()
	return nil
}

// uploadFile streams local into a remote temp file and commits it over
// target; any failure removes the temp so a partial file never overwrites
// the complete target.
func (s *Service) uploadFile(client sshclient.SFTPClient, local *os.File, tmpName, target string, transfer *transfer) error {
	remote, err := client.Create(tmpName)
	if err != nil {
		return mapRemoteErr(err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = remote.Close()
			_ = client.Remove(tmpName)
		}
	}()
	pr := &progressReader{r: local, onRead: transfer.add}
	if _, err := io.Copy(remote, pr); err != nil {
		return mapRemoteErr(err)
	}
	if err := remote.Close(); err != nil {
		return mapRemoteErr(err)
	}
	if err := s.commitUpload(client, tmpName, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// commitUpload moves the uploaded temp over target, preserving an existing
// target's permissions. The target is inspected first: absent takes a plain
// atomic Rename; present-regular is replaced keeping its mode — the temp is
// chmod'd to the old permissions BEFORE any rename, so a replaced file never
// exposes a default-created mode — preferring the posix-rename extension (an
// atomic overwrite) when the server advertises it. Without the extension,
// the target is first moved aside to a same-directory backup (fresh UUID —
// never a collision), the temp takes its place, and the backup is removed on
// success; a failed commit restores the backup so the previous target is
// never lost. A target that turns out to be a directory is refused — the
// fallback never moves (or later deletes) a directory. Any other Lstat
// failure (permission, transport) is mapped and never mistaken for "target
// absent", so a bare Rename is never attempted over a target that could not
// be inspected. A failed permission chmod aborts before any rename: the old
// target is untouched and the temp is removed by the caller's cleanup. Every
// error passes through mapRemoteErr so no path text crosses the boundary.
func (s *Service) commitUpload(client sshclient.SFTPClient, tmpName, target string) error {
	fi, err := client.Lstat(target)
	switch {
	case err == nil:
		if isDir(fi) {
			return errf(apperror.Unknown, "Path is a directory")
		}
	case errors.Is(err, os.ErrNotExist):
		if err := client.Rename(tmpName, target); err != nil {
			return mapRemoteErr(err)
		}
		return nil
	default:
		return mapRemoteErr(err)
	}
	if err := client.Chmod(tmpName, fi.Mode().Perm()); err != nil {
		return mapRemoteErr(err)
	}
	if _, ok := client.HasExtension("posix-rename@openssh.com"); ok {
		if err := client.PosixRename(tmpName, target); err != nil {
			return mapRemoteErr(err)
		}
		return nil
	}
	backup := path.Join(path.Dir(target), ".nodeshell-backup-"+s.uuid())
	if err := client.Rename(target, backup); err != nil {
		return mapRemoteErr(err)
	}
	if err := client.Rename(tmpName, target); err != nil {
		_ = client.Rename(backup, target) // best-effort restore
		return mapRemoteErr(err)
	}
	_ = client.Remove(backup)
	return nil
}

// ReadText reads a remote text file with a hard byte cap. The path resolves
// against the session's current remote directory (POSIX: relative names are
// joined under cwd, absolute names used verbatim) and is realpath'd first —
// the same resolution as the Electron resolveExistingPath, symlinks
// followed. A directory or a file whose reported size exceeds maxBytes is
// rejected before any data is read; the stream itself still accumulates a
// hard cap, so a file that grows after Stat (TOCTOU) can never deliver more
// than maxBytes. Content is the raw bytes as a Go string: invalid UTF-8 is
// preserved verbatim and becomes U+FFFD on JSON marshal, matching
// Buffer.toString("utf8") in the Electron tool. The returned path is the
// resolved remote path. maxBytes <= 0 is refused outright, and so is
// maxBytes == math.MaxInt64: the maxBytes+1 stream sentinel would overflow to
// a negative count and io.CopyN would stop at once, silently returning empty
// content. The 512KiB runtime cap comes from the caller; no business limit is
// set here.
func (s *Service) ReadText(sessionID, remotePath string, maxBytes int64) (string, string, error) {
	if maxBytes <= 0 || maxBytes == math.MaxInt64 {
		return "", "", errf(apperror.Unknown, "Text size limit is invalid")
	}
	ss, err := s.session(sessionID)
	if err != nil {
		return "", "", err
	}
	client, cwd, err := ss.ensure()
	if err != nil {
		return "", "", err
	}
	resolved, err := client.RealPath(JoinRemote(cwd, remotePath))
	if err != nil {
		return "", "", mapRemoteErr(err)
	}
	fi, err := client.Stat(resolved)
	if err != nil {
		return "", "", mapRemoteErr(err)
	}
	if isDir(fi) {
		return "", "", errf(apperror.Unknown, "Path is a directory")
	}
	if fi.Size() > maxBytes {
		return "", "", errf(apperror.Unknown, "File is too large")
	}
	file, err := client.Open(resolved)
	if err != nil {
		return "", "", mapRemoteErr(err)
	}
	defer file.Close()
	var buf bytes.Buffer
	written, err := io.CopyN(&buf, file, maxBytes+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "", mapRemoteErr(err)
	}
	if written > maxBytes {
		return "", "", errf(apperror.Unknown, "File is too large")
	}
	return resolved, buf.String(), nil
}

// WriteText writes UTF-8 text to a remote file, replacing any existing
// target, and returns the resolved remote path of the written target (the
// MCP sftp_write tool echoes it). The content is rejected up front when its
// UTF-8 byte length exceeds maxBytes, before any network transfer. The write
// goes to a same-directory temp file that is committed over the target only
// on success (commitUpload), so a failed write never truncates an existing
// target: an existing target keeps its permissions, a fresh target keeps the
// server's umask-constrained default mode, and on any failure the previous
// target survives and the temp is removed. maxBytes <= 0 is refused outright.
func (s *Service) WriteText(sessionID, remotePath, content string, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		return "", errf(apperror.Unknown, "Text size limit is invalid")
	}
	if int64(len(content)) > maxBytes {
		return "", errf(apperror.Unknown, "Content is too large")
	}
	ss, err := s.session(sessionID)
	if err != nil {
		return "", err
	}
	client, cwd, err := ss.ensure()
	if err != nil {
		return "", err
	}
	target, err := resolveMutable(client, JoinRemote(cwd, remotePath))
	if err != nil {
		return "", err
	}
	tmpName := path.Join(path.Dir(target), ".nodeshell-write-"+s.uuid())
	if err := s.writeTempAndCommit(client, tmpName, target, []byte(content)); err != nil {
		return "", err
	}
	return target, nil
}

// writeTempAndCommit creates a remote temp, writes data, closes it and commits
// it over target via commitUpload; any failure removes the temp so a partial
// write never replaces the complete target.
func (s *Service) writeTempAndCommit(client sshclient.SFTPClient, tmpName, target string, data []byte) error {
	remote, err := client.Create(tmpName)
	if err != nil {
		return mapRemoteErr(err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = remote.Close()
			_ = client.Remove(tmpName)
		}
	}()
	n, err := remote.Write(data)
	if err != nil {
		return mapRemoteErr(err)
	}
	if n != len(data) {
		return errf(apperror.Unknown, "Failed to write the remote file")
	}
	if err := remote.Close(); err != nil {
		return mapRemoteErr(err)
	}
	if err := s.commitUpload(client, tmpName, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// UploadPaths validates every local path against the home boundary, skips
// entries that are not regular files, and uploads the rest sequentially.
// Home violations are strict: the whole call fails observably so the UI can
// surface them.
func (s *Service) UploadPaths(sessionID string, localPaths []string) error {
	if len(localPaths) == 0 {
		return nil
	}
	var pending []string
	for _, p := range localPaths {
		if p == "" {
			continue
		}
		safe, err := localpathguard.ResolveExisting(p, s.home)
		if err != nil {
			return err
		}
		info, err := os.Stat(safe)
		if err != nil {
			return errf(apperror.Unknown, "Failed to inspect the local file")
		}
		if info.IsDir() {
			continue // directories are skipped, matching the Electron drop UI
		}
		pending = append(pending, safe)
	}
	for _, p := range pending {
		if err := s.Upload(sessionID, p); err != nil {
			return err
		}
	}
	return nil
}

// Download streams a remote file to localPath. The local target must stay
// inside the home boundary (ResolveTarget: existing symlinks are resolved
// and rejected when they escape). An existing regular target keeps its
// permissions across the replace (the temp is chmod'd to the old mode before
// the atomic rename); a directory target is refused. Data is written to a
// same-directory temp file, synced and atomically replaced over the target,
// so a failed transfer keeps the previous target intact and cleans up the
// temp.
func (s *Service) Download(sessionID, remotePath, localPath string) error {
	ss, err := s.session(sessionID)
	if err != nil {
		return err
	}
	client, cwd, err := ss.ensure()
	if err != nil {
		return err
	}
	remote := JoinRemote(cwd, remotePath)
	resolvedRemote, err := client.RealPath(remote)
	if err != nil {
		return mapRemoteErr(err)
	}
	fi, err := client.Stat(resolvedRemote)
	if err != nil {
		return mapRemoteErr(err)
	}
	if isDir(fi) {
		return errf(apperror.Unknown, "Path is a directory")
	}
	safeTarget, err := localpathguard.ResolveTarget(localPath, s.home)
	if err != nil {
		return err
	}
	// Preserve the mode of an existing regular target; an absent target
	// keeps the temp's 0600 default. ResolveTarget has already resolved an
	// existing symlink to an inside-home path, so Stat of safeTarget never
	// follows an escaping link.
	var existingPerm *os.FileMode
	if fi, err := os.Stat(safeTarget); err == nil {
		if fi.IsDir() {
			return errf(apperror.Unknown, "Target is a directory")
		}
		perm := fi.Mode().Perm()
		existingPerm = &perm
	} else if !errors.Is(err, os.ErrNotExist) {
		return errf(apperror.Unknown, "Failed to inspect the local target")
	}
	name := path.Base(resolvedRemote)

	remoteFile, err := client.Open(resolvedRemote)
	if err != nil {
		return mapRemoteErr(err)
	}
	transfer := newTransfer(s.sink, sessionID, "down", name, fi.Size())
	transfer.start()
	if err := s.downloadFile(remoteFile, filepath.Dir(safeTarget), safeTarget, existingPerm, fi.Size(), transfer); err != nil {
		transfer.fail()
		return err
	}
	transfer.finish()
	return nil
}

// downloadFile streams the remote file into a same-directory temp, syncs,
// closes and atomically replaces the target. The stream must deliver exactly
// expected bytes: a short read (truncated transfer) or a longer-than-declared
// stream is refused with a generic error, so a silently corrupted target is
// never committed. When existingPerm is set (an existing regular target), the
// temp is chmod'd to that mode before the replace so the overwritten target
// keeps its permissions; a fresh target keeps the temp's 0600 default. Any
// failure keeps the previous target intact, closes the remote file and
// removes the temp.
func (s *Service) downloadFile(remoteFile io.ReadCloser, dir, target string, existingPerm *os.FileMode, expected int64, transfer *transfer) error {
	tmp, err := os.CreateTemp(dir, ".nodeshell-download-*")
	if err != nil {
		_ = remoteFile.Close()
		return errf(apperror.Unknown, "Failed to create the local temp file")
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	pr := &progressReader{r: remoteFile, onRead: transfer.add}
	n, err := io.Copy(tmp, pr)
	if err != nil {
		_ = remoteFile.Close()
		return mapRemoteErr(err)
	}
	if n != expected {
		_ = remoteFile.Close()
		return errf(apperror.Unknown, "Remote file changed during download")
	}
	if err := remoteFile.Close(); err != nil {
		return mapRemoteErr(err)
	}
	if err := tmp.Sync(); err != nil {
		return errf(apperror.Unknown, "Failed to flush the local file")
	}
	if err := tmp.Close(); err != nil {
		return errf(apperror.Unknown, "Failed to close the local file")
	}
	if existingPerm != nil {
		// The temp is chmod'd before the atomic replace so the overwritten
		// target never exposes a default-created mode.
		if err := os.Chmod(tmpName, *existingPerm); err != nil {
			return errf(apperror.Unknown, "Failed to preserve the local file permissions")
		}
	}
	if err := atomicfile.Replace(tmpName, target); err != nil {
		return errf(apperror.Unknown, "Failed to finalise the download")
	}
	cleanup = false
	return nil
}

// transfer throttles and emits progress events. Initial and final events
// always fire; increments are throttled to 80ms (Electron parity).
type transfer struct {
	sink        EventSink
	sessionID   string
	direction   string
	name        string
	total       int64
	transferred int64
	lastEmit    time.Time
}

func newTransfer(sink EventSink, sessionID, direction, name string, total int64) *transfer {
	return &transfer{sink: sink, sessionID: sessionID, direction: direction, name: name, total: total}
}

func (t *transfer) emit(done bool) {
	now := time.Now()
	if !done && now.Sub(t.lastEmit) < 80*time.Millisecond {
		return
	}
	t.lastEmit = now
	t.sink.Emit(EventTransferProgress, ProgressEvent{
		SessionID:   t.sessionID,
		Direction:   t.direction,
		Name:        t.name,
		Transferred: t.transferred,
		Total:       t.total,
		Done:        done,
	})
}

func (t *transfer) add(n int) {
	t.transferred += int64(n)
	t.emit(false)
}

func (t *transfer) start() { t.emit(false) }

// finish emits the final done event; the transferred count is clamped to the
// total so a size mismatch never shows more than 100%.
func (t *transfer) finish() {
	if t.total > 0 && t.transferred < t.total {
		t.transferred = t.total
	}
	t.emit(true)
}

// fail emits done=true with the partial count (Electron parity: the old
// transferStream emitted a final event on the failure path too) so the UI
// clears its progress badge; the error itself surfaces via the binding.
func (t *transfer) fail() { t.emit(true) }

// contextProgressReader reports every byte read through onRead and checks ctx on every read.
type contextProgressReader struct {
	ctx    context.Context
	r      io.Reader
	onRead func(int)
}

func (c *contextProgressReader) Read(b []byte) (int, error) {
	if c.ctx != nil {
		if err := c.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, err := c.r.Read(b)
	if n > 0 && c.onRead != nil {
		c.onRead(n)
	}
	return n, err
}

// UploadWithContext streams a local file to remoteDir with context cancellation and progress updates.
func (s *Service) UploadWithContext(
	ctx context.Context,
	sessionID, remoteDir, localPath string,
	onProgress func(transferred, total int64, finalizing bool),
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	safe, err := localpathguard.ResolveExisting(localPath, s.home)
	if err != nil {
		return err
	}
	info, err := os.Stat(safe)
	if err != nil {
		return errf(apperror.Unknown, "Failed to inspect the local file")
	}
	if !info.Mode().IsRegular() {
		return errf(apperror.Unknown, "Upload source is not a regular file")
	}
	ss, err := s.session(sessionID)
	if err != nil {
		return err
	}
	client, cwd, err := ss.ensure()
	if err != nil {
		return err
	}
	if remoteDir == "" {
		remoteDir = cwd
	}
	name := filepath.Base(safe)
	target, err := resolveMutable(client, JoinRemote(remoteDir, name))
	if err != nil {
		return err
	}
	local, err := os.Open(safe)
	if err != nil {
		return errf(apperror.Unknown, "Failed to open the local file")
	}
	defer local.Close()
	total := info.Size()

	tmpName := path.Join(path.Dir(target), ".nodeshell-upload-"+s.uuid())
	remote, err := client.Create(tmpName)
	if err != nil {
		return mapRemoteErr(err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = remote.Close()
			_ = client.Remove(tmpName)
		}
	}()

	var transferred int64
	var lastEmit time.Time
	if onProgress != nil {
		onProgress(0, total, false)
		lastEmit = time.Now()
	}

	cr := &contextProgressReader{
		ctx: ctx,
		r:   local,
		onRead: func(n int) {
			transferred += int64(n)
			now := time.Now()
			if onProgress != nil && now.Sub(lastEmit) >= 80*time.Millisecond {
				lastEmit = now
				onProgress(transferred, total, false)
			}
		},
	}

	if _, err := io.Copy(remote, cr); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return ctx.Err()
		}
		return mapRemoteErr(err)
	}
	if err := remote.Close(); err != nil {
		return mapRemoteErr(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if onProgress != nil {
		onProgress(total, total, true)
	}
	if err := s.commitUpload(client, tmpName, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// DownloadWithContext streams a remote file to localPath with context cancellation and progress updates.
func (s *Service) DownloadWithContext(
	ctx context.Context,
	sessionID, remotePath, localPath string,
	onProgress func(transferred, total int64, finalizing bool),
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ss, err := s.session(sessionID)
	if err != nil {
		return err
	}
	client, cwd, err := ss.ensure()
	if err != nil {
		return err
	}
	var remote string
	if strings.HasPrefix(remotePath, "/") {
		remote = path.Clean(remotePath)
	} else {
		remote = JoinRemote(cwd, remotePath)
	}
	resolvedRemote, err := client.RealPath(remote)
	if err != nil {
		return mapRemoteErr(err)
	}
	fi, err := client.Stat(resolvedRemote)
	if err != nil {
		return mapRemoteErr(err)
	}
	if isDir(fi) {
		return errf(apperror.Unknown, "Path is a directory")
	}
	safeTarget, err := localpathguard.ResolveTarget(localPath, s.home)
	if err != nil {
		return err
	}
	var existingPerm *os.FileMode
	if stat, err := os.Stat(safeTarget); err == nil {
		if stat.IsDir() {
			return errf(apperror.Unknown, "Target is a directory")
		}
		perm := stat.Mode().Perm()
		existingPerm = &perm
	} else if !errors.Is(err, os.ErrNotExist) {
		return errf(apperror.Unknown, "Failed to inspect the local target")
	}

	remoteFile, err := client.Open(resolvedRemote)
	if err != nil {
		return mapRemoteErr(err)
	}
	defer remoteFile.Close()

	total := fi.Size()
	dir := filepath.Dir(safeTarget)
	tmp, err := os.CreateTemp(dir, ".nodeshell-download-*")
	if err != nil {
		return errf(apperror.Unknown, "Failed to create the local temp file")
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	var transferred int64
	var lastEmit time.Time
	if onProgress != nil {
		onProgress(0, total, false)
		lastEmit = time.Now()
	}

	cr := &contextProgressReader{
		ctx: ctx,
		r:   remoteFile,
		onRead: func(n int) {
			transferred += int64(n)
			now := time.Now()
			if onProgress != nil && now.Sub(lastEmit) >= 80*time.Millisecond {
				lastEmit = now
				onProgress(transferred, total, false)
			}
		},
	}

	n, err := io.Copy(tmp, cr)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return ctx.Err()
		}
		return mapRemoteErr(err)
	}
	if n != total {
		return errf(apperror.Unknown, "Remote file changed during download")
	}
	if err := tmp.Sync(); err != nil {
		return errf(apperror.Unknown, "Failed to flush the local file")
	}
	if err := tmp.Close(); err != nil {
		return errf(apperror.Unknown, "Failed to close the local file")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if onProgress != nil {
		onProgress(total, total, true)
	}
	if existingPerm != nil {
		if err := os.Chmod(tmpName, *existingPerm); err != nil {
			return errf(apperror.Unknown, "Failed to preserve the local file permissions")
		}
	}
	if err := atomicfile.Replace(tmpName, safeTarget); err != nil {
		return errf(apperror.Unknown, "Failed to finalise the download")
	}
	cleanup = false
	return nil
}

// progressReader reports every byte read through onRead.
type progressReader struct {
	r      io.Reader
	onRead func(int)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.onRead(n)
	}
	return n, err
}

// resolveMutable resolves a remote path that may not exist yet (mkdir,
// rename target, upload target): realpath when it exists, otherwise
// realpath the parent and re-attach the leaf.
func resolveMutable(client sshclient.SFTPClient, joined string) (string, error) {
	resolved, err := client.RealPath(joined)
	if err == nil {
		return resolved, nil
	}
	parent := JoinRemote(joined, "..")
	absParent, err := client.RealPath(parent)
	if err != nil {
		return "", errf(apperror.Unknown, "Failed to resolve the remote parent directory")
	}
	base := path.Base(joined)
	if base == "" || base == "/" || base == "." {
		return absParent, nil
	}
	return JoinRemote(absParent, base), nil
}

func entryFromListing(dir string, fi os.FileInfo) Entry {
	return Entry{
		Name:        fi.Name(),
		Path:        JoinRemote(dir, fi.Name()),
		IsDirectory: isDir(fi),
		Size:        fi.Size(),
		ModifyTime:  fi.ModTime().UnixMilli(),
	}
}

// isDir reports whether fi is a directory. pkg/sftp maps the raw SFTP mode
// bits (lstat-like from READDIR) onto os.FileMode, so symlinks and dirs are
// distinguishable without touching the raw FileStat.
func isDir(fi os.FileInfo) bool { return fi.IsDir() }

// isLink reports whether fi is a symlink.
func isLink(fi os.FileInfo) bool { return fi.Mode()&os.ModeSymlink != 0 }

// mapRemoteErr converts a pkg/sftp failure into a coded Error. Messages stay
// generic — a raw status, transport or local-path error can carry a path or
// remote path text, so nothing about the remote layout (or a local absolute
// path from an os.PathError on the download path) is echoed to the frontend.
func mapRemoteErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return errf(apperror.Unknown, "Remote path does not exist")
	case errors.Is(err, os.ErrPermission):
		return errf(apperror.Unknown, "Permission denied")
	case errors.Is(err, io.EOF):
		return errf(apperror.Unknown, "Remote connection ended")
	}
	return errf(apperror.Unknown, "Remote operation failed")
}
