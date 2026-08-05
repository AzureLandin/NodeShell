package sshclient

import (
	"io"
	"os"

	"github.com/pkg/sftp"

	"golang.org/x/crypto/ssh"

	"nodeshell/internal/apperror"
)

// SFTPClient is the minimal SFTP surface the app layer consumes: the subset
// of *sftp.Client the SFTP service uses. It is defined here (not in the sftp
// package) so a session can hand one out without leaking either the raw SSH
// client or the concrete pkg/sftp client to the app layer.
type SFTPClient interface {
	ReadDir(path string) ([]os.FileInfo, error)
	Stat(path string) (os.FileInfo, error)
	Lstat(path string) (os.FileInfo, error)
	RealPath(path string) (string, error)
	Open(path string) (io.ReadCloser, error)
	Create(path string) (io.WriteCloser, error)
	Mkdir(path string) error
	MkdirAll(path string) error
	Remove(path string) error
	RemoveDirectory(path string) error
	Rename(oldpath, newpath string) error
	PosixRename(oldpath, newpath string) error
	Chmod(path string, mode os.FileMode) error
	HasExtension(name string) (string, bool)
	Close() error
}

// sftpClientAdapter adapts *sftp.Client to the SFTPClient interface. Only
// Open and Create differ (pkg/sftp returns *sftp.File); everything else
// passes through verbatim.
type sftpClientAdapter struct{ c *sftp.Client }

func (a *sftpClientAdapter) ReadDir(path string) ([]os.FileInfo, error) { return a.c.ReadDir(path) }
func (a *sftpClientAdapter) Stat(path string) (os.FileInfo, error)      { return a.c.Stat(path) }
func (a *sftpClientAdapter) Lstat(path string) (os.FileInfo, error)     { return a.c.Lstat(path) }
func (a *sftpClientAdapter) RealPath(path string) (string, error)       { return a.c.RealPath(path) }
func (a *sftpClientAdapter) Open(path string) (io.ReadCloser, error)    { return a.c.Open(path) }
func (a *sftpClientAdapter) Create(path string) (io.WriteCloser, error) { return a.c.Create(path) }
func (a *sftpClientAdapter) Mkdir(path string) error                    { return a.c.Mkdir(path) }
func (a *sftpClientAdapter) MkdirAll(path string) error                 { return a.c.MkdirAll(path) }
func (a *sftpClientAdapter) Remove(path string) error                   { return a.c.Remove(path) }
func (a *sftpClientAdapter) RemoveDirectory(path string) error {
	return a.c.RemoveDirectory(path)
}
func (a *sftpClientAdapter) Rename(oldpath, newpath string) error {
	return a.c.Rename(oldpath, newpath)
}
func (a *sftpClientAdapter) PosixRename(oldpath, newpath string) error {
	return a.c.PosixRename(oldpath, newpath)
}
func (a *sftpClientAdapter) Chmod(path string, mode os.FileMode) error {
	return a.c.Chmod(path, mode)
}
func (a *sftpClientAdapter) HasExtension(name string) (string, bool) {
	return a.c.HasExtension(name)
}
func (a *sftpClientAdapter) Close() error { return a.c.Close() }

// newSFTPClient is the SFTP factory seam: production opens a pkg/sftp client
// over the SSH connection; tests swap it to observe open/close bookkeeping
// without a real transport.
var newSFTPClient = func(client *ssh.Client) (SFTPClient, error) {
	c, err := sftp.NewClient(client)
	if err != nil {
		return nil, err
	}
	return &sftpClientAdapter{c: c}, nil
}

// NewSFTPClient opens an SFTP client over this session's SSH connection. The
// returned client must be Closed to release the SFTP channel; the SFTP
// service owns that lifecycle (lazy create, reuse, dispose on session end).
func (s *Session) NewSFTPClient() (SFTPClient, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, &Error{Code: apperror.Unknown, Message: "Session is closed"}
	}
	s.mu.Unlock()
	return newSFTPClient(s.client)
}
