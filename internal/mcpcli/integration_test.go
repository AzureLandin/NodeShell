package mcpcli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"nodeshell/internal/apperror"
	"nodeshell/internal/hosts"
	"nodeshell/internal/knownhosts"
	"nodeshell/internal/sessions"
	"nodeshell/internal/sftpservice"
	"nodeshell/internal/sshtest"
)

func splitPort(t *testing.T, addr string) int {
	t.Helper()
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		t.Fatalf("bad addr %q", addr)
	}
	var port int
	for _, c := range addr[idx+1:] {
		port = port*10 + int(c-'0')
	}
	return port
}

func callString(t *testing.T, rt *Runtime, tool string, args map[string]any) any {
	t.Helper()
	got, err := rt.Call(context.Background(), tool, args)
	if err != nil {
		t.Fatalf("Call(%s, %v): %v", tool, args, err)
	}
	return got
}

// TestMcpIntegrationFullChain drives two real SSH servers (the sshtest
// server serves either exec OR sftp on one connection, so exec and SFTP use
// separate hosts) through the whole tool set: connect, exec, sftp
// write/read/list, download, upload (default and explicit remote name),
// disconnect.
func TestMcpIntegrationFullChain(t *testing.T) {
	srvExec := sshtest.New(t)
	srvExec.PasswordOK = func(user, pass string) bool { return user == "user" && pass == "secret" }
	srvExec.OnExec = sshtest.EchoExec

	srvRoot := t.TempDir()
	srvSftp := sshtest.New(t)
	srvSftp.PasswordOK = func(user, pass string) bool { return user == "user" && pass == "secret" }
	srvSftp.EnableSFTP(srvRoot, false)

	home := t.TempDir()
	dir := t.TempDir()
	h := hosts.New(dir)
	execHost, err := h.Create(hosts.HostInput{Name: "exec", Host: "127.0.0.1", Port: splitPort(t, srvExec.Addr), Username: "user", AuthMethod: "password"})
	if err != nil {
		t.Fatalf("hosts.Create: %v", err)
	}
	sftpHost, err := h.Create(hosts.HostInput{Name: "sftp", Host: "127.0.0.1", Port: splitPort(t, srvSftp.Addr), Username: "user", AuthMethod: "password"})
	if err != nil {
		t.Fatalf("hosts.Create: %v", err)
	}
	k := knownhosts.New(dir)
	if err := k.Remember("127.0.0.1", splitPort(t, srvExec.Addr), srvExec.HostKeyFingerprint()); err != nil {
		t.Fatalf("knownhosts.Remember: %v", err)
	}
	if err := k.Remember("127.0.0.1", splitPort(t, srvSftp.Addr), srvSftp.HostKeyFingerprint()); err != nil {
		t.Fatalf("knownhosts.Remember: %v", err)
	}
	rt := New(Deps{Hosts: h, MaxSessions: 8, IdleTimeout: 10 * time.Minute})
	m := sessions.New(sessions.Deps{Hosts: h, HostKeys: k, Sink: rt.Sink()})
	rt.SetManager(m)
	svc := sftpservice.New(sftpservice.Deps{Opener: m, Home: home})
	rt.SetSFTP(svc)
	t.Cleanup(rt.DisposeAll)

	// list_hosts shows both saved hosts with the exact DTO.
	hostsList := callString(t, rt, "list_hosts", nil).([]HostDTO)
	if len(hostsList) != 2 || hostsList[0].ID != execHost.Id || hostsList[1].ID != sftpHost.Id {
		t.Fatalf("list_hosts = %+v", hostsList)
	}

	// connect_host (explicit password, no saved credentials).
	connExec := callString(t, rt, "connect_host", map[string]any{"hostId": execHost.Id, "password": "secret"}).(ConnectResult)
	if connExec.Title != "user@127.0.0.1" {
		t.Fatalf("connect title = %q", connExec.Title)
	}
	connSftp := callString(t, rt, "connect_host", map[string]any{"hostId": sftpHost.Id, "password": "secret"}).(ConnectResult)
	// The password must never end up in the metadata DTO.
	for _, s := range rt.ListSessions() {
		if strings.Contains(s.Title, "secret") {
			t.Fatal("session title leaks the password")
		}
	}

	// list_sessions shows the connected sessions in insertion order.
	ls := callString(t, rt, "list_sessions", nil).([]SessionDTO)
	if len(ls) != 2 || ls[0].SessionID != connExec.SessionID || ls[1].SessionID != connSftp.SessionID {
		t.Fatalf("list_sessions = %+v", ls)
	}

	// run_command echoes the command back through the real exec channel.
	out := callString(t, rt, "run_command", map[string]any{"sessionId": connExec.SessionID, "command": "echo hi"}).(string)
	if out != "echo hi" {
		t.Fatalf("run_command output = %q, want %q", out, "echo hi")
	}
	// timeoutMs is accepted.
	if _, err := rt.Call(context.Background(), "run_command", map[string]any{"sessionId": connExec.SessionID, "command": "ls", "timeoutMs": float64(5000)}); err != nil {
		t.Fatalf("run_command with timeoutMs: %v", err)
	}

	// sftp_write then sftp_read round-trip.
	wr := callString(t, rt, "sftp_write", map[string]any{"sessionId": connSftp.SessionID, "path": "notes.txt", "content": "hello world"}).(SftpWriteResult)
	if !wr.OK || !strings.HasSuffix(wr.Path, "notes.txt") {
		t.Fatalf("sftp_write result = %+v, want ok + resolved path", wr)
	}
	rd := callString(t, rt, "sftp_read", map[string]any{"sessionId": connSftp.SessionID, "path": "notes.txt"}).(SftpReadResult)
	if rd.Content != "hello world" || rd.Path != wr.Path {
		t.Fatalf("sftp_read = %+v, want content round-trip from %s", rd, wr.Path)
	}

	// sftp_list lists the remote cwd containing the written file.
	list := callString(t, rt, "sftp_list", map[string]any{"sessionId": connSftp.SessionID}).(SftpListResult)
	if len(list.Entries) != 1 || list.Entries[0].Name != "notes.txt" || !strings.HasPrefix(list.Cwd, "/") {
		t.Fatalf("sftp_list = %+v", list)
	}

	// sftp_download to a home-bound path.
	down := callString(t, rt, "sftp_download", map[string]any{"sessionId": connSftp.SessionID, "remotePath": "notes.txt", "localPath": filepath.Join(home, "notes.txt")}).(SftpDownloadResult)
	if !down.OK {
		t.Fatalf("sftp_download result = %+v", down)
	}
	raw, err := os.ReadFile(filepath.Join(home, "notes.txt"))
	if err != nil || string(raw) != "hello world" {
		t.Fatalf("downloaded file = %q, err %v", raw, err)
	}

	// sftp_upload: default basename then explicit remoteName.
	src := filepath.Join(home, "local.txt")
	if err := os.WriteFile(src, []byte("uploaded"), 0o600); err != nil {
		t.Fatalf("write local: %v", err)
	}
	up := callString(t, rt, "sftp_upload", map[string]any{"sessionId": connSftp.SessionID, "localPath": src}).(SftpUploadResult)
	if !up.OK || up.RemoteName != nil {
		t.Fatalf("sftp_upload result = %+v", up)
	}
	rd = callString(t, rt, "sftp_read", map[string]any{"sessionId": connSftp.SessionID, "path": "local.txt"}).(SftpReadResult)
	if rd.Content != "uploaded" {
		t.Fatalf("uploaded content = %q", rd.Content)
	}
	up2 := callString(t, rt, "sftp_upload", map[string]any{"sessionId": connSftp.SessionID, "localPath": src, "remoteName": "renamed.txt"}).(SftpUploadResult)
	if up2.RemoteName == nil || *up2.RemoteName != "renamed.txt" {
		t.Fatalf("sftp_upload remoteName result = %+v", up2)
	}
	rd = callString(t, rt, "sftp_read", map[string]any{"sessionId": connSftp.SessionID, "path": "renamed.txt"}).(SftpReadResult)
	if rd.Content != "uploaded" {
		t.Fatalf("renamed upload content = %q", rd.Content)
	}

	// disconnect_session.
	callString(t, rt, "disconnect_session", map[string]any{"sessionId": connExec.SessionID})
	callString(t, rt, "disconnect_session", map[string]any{"sessionId": connSftp.SessionID})
	if got := rt.ListSessions(); len(got) != 0 {
		t.Fatalf("ListSessions after disconnect = %+v", got)
	}
}

// TestMcpHomeEscape: upload sources and download targets outside the user
// home are rejected with coded errors that never echo the path.
func TestMcpHomeEscape(t *testing.T) {
	srvRoot := t.TempDir()
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.EnableSFTP(srvRoot, false)

	home := t.TempDir()
	rt, _, _, _, created := newIntegrationRuntimeWithServer(t, srv, home)
	t.Cleanup(rt.DisposeAll)
	sid := callString(t, rt, "connect_host", map[string]any{"hostId": created.Id, "password": "secret"}).(ConnectResult).SessionID

	// A real file outside the home boundary.
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	_, err := rt.Call(context.Background(), "sftp_upload", map[string]any{"sessionId": sid, "localPath": outsideFile})
	assertErrorCode(t, err, apperror.Unknown)
	if strings.Contains(err.Error(), outsideFile) {
		t.Fatalf("upload error must not echo the local path: %v", err)
	}

	// A download target outside home.
	outsideTarget := filepath.Join(outside, "out.txt")
	_, err = rt.Call(context.Background(), "sftp_download", map[string]any{"sessionId": sid, "remotePath": "notes.txt", "localPath": outsideTarget})
	assertErrorCode(t, err, apperror.Unknown)
	if strings.Contains(err.Error(), outsideTarget) {
		t.Fatalf("download error must not echo the local path: %v", err)
	}
}

// newIntegrationRuntimeWithServer wires the production chain over a caller-
// provided server.
func newIntegrationRuntimeWithServer(t *testing.T, srv *sshtest.Server, home string) (*Runtime, *sshtest.Server, *sessions.Manager, *sftpservice.Service, hosts.HostConfig) {
	t.Helper()
	dir := t.TempDir()
	h := hosts.New(dir)
	created, err := h.Create(hosts.HostInput{Name: "lab", Host: "127.0.0.1", Port: splitPort(t, srv.Addr), Username: "user", AuthMethod: "password"})
	if err != nil {
		t.Fatalf("hosts.Create: %v", err)
	}
	k := knownhosts.New(dir)
	if err := k.Remember("127.0.0.1", splitPort(t, srv.Addr), srv.HostKeyFingerprint()); err != nil {
		t.Fatalf("knownhosts.Remember: %v", err)
	}
	rt := New(Deps{Hosts: h, MaxSessions: 8, IdleTimeout: 10 * time.Minute})
	m := sessions.New(sessions.Deps{Hosts: h, HostKeys: k, Sink: rt.Sink()})
	rt.SetManager(m)
	svc := sftpservice.New(sftpservice.Deps{Opener: m, Home: home})
	rt.SetSFTP(svc)
	return rt, srv, m, svc, created
}

// TestMcpFileSizeCaps: the 512KiB MCP file cap is enforced exactly on write
// and read; 512KiB passes, one byte more is rejected.
func TestMcpFileSizeCaps(t *testing.T) {
	srvRoot := t.TempDir()
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.EnableSFTP(srvRoot, false)

	home := t.TempDir()
	rt, _, _, svc, created := newIntegrationRuntimeWithServer(t, srv, home)
	t.Cleanup(rt.DisposeAll)
	sid := callString(t, rt, "connect_host", map[string]any{"hostId": created.Id, "password": "secret"}).(ConnectResult).SessionID

	exact := strings.Repeat("a", MaxFileBytes)
	wr := callString(t, rt, "sftp_write", map[string]any{"sessionId": sid, "path": "exact.txt", "content": exact}).(SftpWriteResult)
	if !wr.OK {
		t.Fatalf("sftp_write at exactly %d bytes must succeed: %+v", MaxFileBytes, wr)
	}
	rd := callString(t, rt, "sftp_read", map[string]any{"sessionId": sid, "path": "exact.txt"}).(SftpReadResult)
	if rd.Content != exact {
		t.Fatalf("sftp_read round-trip mismatch: got %d bytes, want %d", len(rd.Content), len(exact))
	}

	// One byte over the cap is rejected up front on write.
	_, err := rt.Call(context.Background(), "sftp_write", map[string]any{"sessionId": sid, "path": "over.txt", "content": exact + "a"})
	if err == nil {
		t.Fatal("sftp_write one byte over 512KiB must error")
	}
	assertErrorCode(t, err, apperror.Unknown)

	// A remote file larger than 512KiB is rejected on read (written through
	// the raw service with a larger cap).
	if _, err := svc.WriteText(sid, "big.txt", strings.Repeat("b", MaxFileBytes+1), 1<<20); err != nil {
		t.Fatalf("raw WriteText of a big file: %v", err)
	}
	if _, err := rt.Call(context.Background(), "sftp_read", map[string]any{"sessionId": sid, "path": "big.txt"}); err == nil {
		t.Fatal("sftp_read of a >512KiB file must error")
	} else {
		assertErrorCode(t, err, apperror.Unknown)
	}
}

// TestMcpRemoteCloseRemovesSession: when the server closes the shell after
// connect, the closed event removes the metadata so list_sessions updates
// without an explicit disconnect.
func TestMcpRemoteCloseRemovesSession(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	shellSeen := make(chan struct{})
	srv.OnShell = func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		go func() {
			for req := range reqs {
				switch req.Type {
				case "pty-req", "shell", "exec":
					_ = req.Reply(true, nil)
					if req.Type == "shell" {
						close(shellSeen)
					}
				default:
					_ = req.Reply(false, nil)
				}
			}
		}()
		<-shellSeen
		_ = ch.Close() // remote close after the shell is up
	}

	home := t.TempDir()
	rt, _, _, _, created := newIntegrationRuntimeWithServer(t, srv, home)
	t.Cleanup(rt.DisposeAll)

	conn := callString(t, rt, "connect_host", map[string]any{"hostId": created.Id, "password": "secret"}).(ConnectResult)
	if len(rt.ListSessions()) != 1 {
		t.Fatalf("ListSessions right after connect = %+v", rt.ListSessions())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if len(rt.ListSessions()) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("remote close did not remove the session metadata: %+v", rt.ListSessions())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := rt.Call(context.Background(), "run_command", map[string]any{"sessionId": conn.SessionID, "command": "ls"}); err == nil {
		t.Fatal("run_command on a remote-closed session must error")
	} else {
		assertErrorCode(t, err, apperror.SessionNotFound)
	}
}

// TestMcpDisposeAllEndsSessions: after DisposeAll every session is gone and
// operations fail with SESSION_NOT_FOUND.
func TestMcpDisposeAllEndsSessions(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.EnableSFTP(t.TempDir(), false)

	home := t.TempDir()
	rt, _, _, _, created := newIntegrationRuntimeWithServer(t, srv, home)
	sid := callString(t, rt, "connect_host", map[string]any{"hostId": created.Id, "password": "secret"}).(ConnectResult).SessionID

	rt.DisposeAll()
	if got := rt.ListSessions(); len(got) != 0 {
		t.Fatalf("ListSessions after DisposeAll = %+v", got)
	}
	_, err := rt.Call(context.Background(), "run_command", map[string]any{"sessionId": sid, "command": "ls"})
	assertErrorCode(t, err, apperror.SessionNotFound)
}
