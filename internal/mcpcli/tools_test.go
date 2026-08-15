package mcpcli

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/permission"
	"nodeshell/internal/sessions"
	"nodeshell/internal/sftpservice"
)

// schemaLiterals are the exact inputSchema JSON the SDK produces for the ten
// tools (zod v4-mini toJSONSchema with target draft-7), captured from
// src/main/mcp-server.ts through @modelcontextprotocol/sdk.
var schemaLiterals = map[string]string{
	"list_hosts":         `{"type":"object"}`,
	"list_sessions":      `{"type":"object"}`,
	"connect_host":       `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"hostId":{"type":"string","description":"Host id from list_hosts"},"password":{"description":"Override password if not saved","type":"string"},"acceptHostKey":{"description":"Accept and store a new or changed host key. Default false — set true only after verifying the fingerprint.","type":"boolean"}},"required":["hostId"]}`,
	"disconnect_session": `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"}},"required":["sessionId"]}`,
	"run_command":        `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"command":{"type":"string","description":"Shell command to execute"},"timeoutMs":{"type":"integer","exclusiveMinimum":0,"maximum":300000}},"required":["sessionId","command"]}`,
	"sftp_list":          `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"path":{"description":"Remote directory path (absolute or relative)","type":"string"}},"required":["sessionId"]}`,
	"sftp_read":          `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"path":{"type":"string"}},"required":["sessionId","path"]}`,
	"sftp_write":         `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"path":{"type":"string"},"content":{"type":"string"}},"required":["sessionId","path","content"]}`,
	"sftp_upload":        `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"localPath":{"type":"string","description":"Absolute local path under the user home directory"},"remoteName":{"type":"string"}},"required":["sessionId","localPath"]}`,
	"sftp_download":      `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"remotePath":{"type":"string"},"localPath":{"type":"string","description":"Absolute local path under the user home directory"}},"required":["sessionId","remotePath","localPath"]}`,
}

func schema(t *testing.T, name string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(schemaLiterals[name]), &m); err != nil {
		t.Fatalf("bad test schema literal for %s: %v", name, err)
	}
	return m
}

// TestToolsDefinitions: the ten tools are returned in the fixed order with
// the exact names, descriptions and inputSchemas of mcp-server.ts.
func TestToolsDefinitions(t *testing.T) {
	want := []struct{ name, description string }{
		{"list_hosts", "List saved SSH hosts (no secrets)."},
		{"list_sessions", "List active MCP SSH sessions opened by this server process."},
		{"connect_host", "Connect to a saved host and open an SSH session for commands/SFTP."},
		{"disconnect_session", "Disconnect an active MCP SSH session."},
		{"run_command", "Run a non-interactive command on the remote host via SSH exec (not PTY)."},
		{"sftp_list", "List a remote directory. If path is given, chdir there first for this session."},
		{"sftp_read", "Read a remote text file (max 512KB)."},
		{"sftp_write", "Write UTF-8 text to a remote file (creates/overwrites)."},
		{"sftp_upload", "Upload a local file under the user home directory to the remote session current directory."},
		{"sftp_download", "Download a remote file to a local path under the user home directory."},
	}
	got := Tools()
	if len(got) != len(want) {
		t.Fatalf("Tools() = %d tools, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Name != w.name {
			t.Fatalf("Tools()[%d].Name = %q, want %q", i, got[i].Name, w.name)
		}
		if got[i].Description != w.description {
			t.Fatalf("Tools()[%d].Description = %q, want %q", i, got[i].Description, w.description)
		}
		if !reflect.DeepEqual(got[i].InputSchema, schema(t, w.name)) {
			gotJSON, _ := json.Marshal(got[i].InputSchema)
			t.Fatalf("Tools()[%d] %s inputSchema = %s, want exact schema", i, w.name, gotJSON)
		}
	}
}

// TestCallUnknownTool: an unknown tool name is an error.
func TestCallUnknownTool(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	_, err := rt.Call(context.Background(), "no_such_tool", nil)
	if err == nil || !strings.Contains(err.Error(), "no_such_tool") {
		t.Fatalf("Call(unknown) error = %v, want an error naming the tool", err)
	}
	assertErrorCode(t, err, apperror.Unknown)
}

// TestCallListHosts: success DTO with exactly the seven fields.
func TestCallListHosts(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	got, err := rt.Call(context.Background(), "list_hosts", map[string]any{})
	if err != nil {
		t.Fatalf("Call(list_hosts): %v", err)
	}
	list, ok := got.([]HostDTO)
	if !ok {
		t.Fatalf("list_hosts result type = %T, want []HostDTO", got)
	}
	if len(list) != 1 || list[0].ID != "h1" || list[0].Port != 22 || !list[0].CredentialsSaved {
		t.Fatalf("list_hosts = %+v", list)
	}
	if list[0].Name != "lab" || list[0].Host != "192.0.2.10" || list[0].Username != "user" || list[0].AuthMethod != "password" {
		t.Fatalf("list_hosts DTO = %+v", list[0])
	}
}

// TestCallListSessions: success returns the session array; extra args ignored.
func TestCallListSessions(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(4, time.Minute, m, &fakeSFTP{}, nil)
	sid := connectOK(t, rt, "h1")
	got, err := rt.Call(context.Background(), "list_sessions", map[string]any{"bogus": 1})
	if err != nil {
		t.Fatalf("Call(list_sessions): %v", err)
	}
	list, ok := got.([]SessionDTO)
	if !ok {
		t.Fatalf("list_sessions result type = %T, want []SessionDTO", got)
	}
	if len(list) != 1 || list[0].SessionID != sid || list[0].HostID != "h1" || list[0].Title != "user@192.0.2.10" {
		t.Fatalf("list_sessions = %+v", list)
	}
}

// TestCallConnectHost: success with password/acceptHostKey; validation errors
// for missing/typed args; extra fields ignored.
func TestCallConnectHost(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)

	got, err := rt.Call(context.Background(), "connect_host", map[string]any{
		"hostId": "h1", "password": "secret", "acceptHostKey": true, "extra": "x",
	})
	if err != nil {
		t.Fatalf("Call(connect_host): %v", err)
	}
	res, ok := got.(ConnectResult)
	if !ok {
		t.Fatalf("connect_host result type = %T, want ConnectResult", got)
	}
	if res.SessionID == "" || res.Title != "user@192.0.2.10" {
		t.Fatalf("connect_host result = %+v", res)
	}
	_, opts, _ := m.snapshot()
	if opts.Password != "secret" || !opts.AcceptHostKey {
		t.Fatalf("manager options = %+v", opts)
	}

	// Missing required hostId.
	if _, err := rt.Call(context.Background(), "connect_host", map[string]any{}); err == nil {
		t.Fatal("connect_host without hostId must error")
	}
	// Wrong-typed hostId must not panic, and must not echo the value.
	if _, err := rt.Call(context.Background(), "connect_host", map[string]any{"hostId": 12345}); err == nil {
		t.Fatal("connect_host with numeric hostId must error")
	} else if strings.Contains(err.Error(), "12345") {
		t.Fatalf("validation error must not echo the argument value: %v", err)
	}
	// Wrong-typed optional password must not panic.
	if _, err := rt.Call(context.Background(), "connect_host", map[string]any{"hostId": "h1", "password": []any{}}); err == nil {
		t.Fatal("connect_host with non-string password must error")
	}
}

// TestCallDisconnectSession: success echoes the id; missing sessionId errors.
func TestCallDisconnectSession(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	sid := connectOK(t, rt, "h1")

	got, err := rt.Call(context.Background(), "disconnect_session", map[string]any{"sessionId": sid})
	if err != nil {
		t.Fatalf("Call(disconnect_session): %v", err)
	}
	res, ok := got.(OkSession)
	if !ok || !res.OK || res.SessionID != sid {
		t.Fatalf("disconnect_session result = %+v", got)
	}
	if len(rt.ListSessions()) != 0 {
		t.Fatal("session must be gone after disconnect_session")
	}
	if _, err := rt.Call(context.Background(), "disconnect_session", map[string]any{}); err == nil {
		t.Fatal("disconnect_session without sessionId must error")
	}
	// Unknown session is a no-op success (Electron parity).
	if _, err := rt.Call(context.Background(), "disconnect_session", map[string]any{"sessionId": "ghost"}); err != nil {
		t.Fatalf("disconnect_session of unknown session: %v", err)
	}
}

// TestCallRunCommand: success returns the raw output string; validation
// errors for missing args; extra fields ignored; coded errors preserved.
func TestCallRunCommand(t *testing.T) {
	m := newFakeManager()
	m.execOut = "hello"
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	sid := connectOK(t, rt, "h1")

	got, err := rt.Call(context.Background(), "run_command", map[string]any{
		"sessionId": sid, "command": "echo hi", "timeoutMs": float64(150), "extra": "x",
	})
	if err != nil {
		t.Fatalf("Call(run_command): %v", err)
	}
	if got != "hello" {
		t.Fatalf("run_command output = %v, want %q", got, m.execOut)
	}

	for _, bad := range []map[string]any{
		{"command": "ls"},
		{"sessionId": sid},
		{"sessionId": 7, "command": "ls"},
		{"sessionId": sid, "command": "ls", "timeoutMs": "abc"},
		{"sessionId": sid, "command": "ls", "timeoutMs": []any{}},
	} {
		if _, err := rt.Call(context.Background(), "run_command", bad); err == nil {
			t.Fatalf("run_command with args %v must error", bad)
		}
	}
	// Coded error from the manager is preserved (SESSION_NOT_FOUND path via
	// unknown session in metadata).
	if _, err := rt.Call(context.Background(), "run_command", map[string]any{"sessionId": "ghost", "command": "ls"}); err == nil {
		t.Fatal("run_command on unknown session must error")
	} else {
		assertErrorCode(t, err, apperror.SessionNotFound)
	}
}

// TestCallSftpList: optional path chdirs first (side effect) then lists cwd.
func TestCallSftpList(t *testing.T) {
	m := newFakeManager()
	sf := &fakeSFTP{cwd: "/home/user/docs", entries: []sftpservice.Entry{{Name: "a.txt", Path: "/home/user/docs/a.txt"}}}
	rt := newTestRuntime(2, time.Minute, m, sf, nil)
	sid := connectOK(t, rt, "h1")

	got, err := rt.Call(context.Background(), "sftp_list", map[string]any{"sessionId": sid})
	if err != nil {
		t.Fatalf("Call(sftp_list): %v", err)
	}
	res := got.(SftpListResult)
	if res.Cwd != "/home/user/docs" || len(res.Entries) != 1 || res.Entries[0].Name != "a.txt" {
		t.Fatalf("sftp_list result = %+v", res)
	}
	chdirs, _, _, _, _ := sf.snapshot()
	if len(chdirs) != 0 {
		t.Fatalf("sftp_list without path must not chdir, got %v", chdirs)
	}

	if _, err := rt.Call(context.Background(), "sftp_list", map[string]any{"sessionId": sid, "path": "sub"}); err != nil {
		t.Fatalf("Call(sftp_list with path): %v", err)
	}
	chdirs, _, _, _, _ = sf.snapshot()
	if len(chdirs) != 1 || chdirs[0] != "sub" {
		t.Fatalf("sftp_list with path must chdir first, got %v", chdirs)
	}

	if _, err := rt.Call(context.Background(), "sftp_list", map[string]any{}); err == nil {
		t.Fatal("sftp_list without sessionId must error")
	}
	if _, err := rt.Call(context.Background(), "sftp_list", map[string]any{"sessionId": []any{1}}); err == nil {
		t.Fatal("sftp_list with wrong-typed sessionId must error")
	}
}

// TestCallSftpRead: success returns the resolved path and content.
func TestCallSftpRead(t *testing.T) {
	m := newFakeManager()
	sf := &fakeSFTP{readResolved: "/home/user/notes.txt", readContent: "data"}
	rt := newTestRuntime(2, time.Minute, m, sf, nil)
	sid := connectOK(t, rt, "h1")

	got, err := rt.Call(context.Background(), "sftp_read", map[string]any{"sessionId": sid, "path": "notes.txt"})
	if err != nil {
		t.Fatalf("Call(sftp_read): %v", err)
	}
	res := got.(SftpReadResult)
	if res.Path != "/home/user/notes.txt" || res.Content != "data" {
		t.Fatalf("sftp_read result = %+v", res)
	}
	for _, bad := range []map[string]any{
		{"sessionId": sid},
		{"path": "x"},
		{"sessionId": sid, "path": 3},
	} {
		if _, err := rt.Call(context.Background(), "sftp_read", bad); err == nil {
			t.Fatalf("sftp_read with args %v must error", bad)
		}
	}
}

// TestCallSftpWrite: success returns the resolved path (WriteText resolves),
// and the content is handed over with the 512KiB cap enforced by the runtime.
func TestCallSftpWrite(t *testing.T) {
	m := newFakeManager()
	sf := &fakeSFTP{writeResolved: "/home/user/out.txt"}
	rt := newTestRuntime(2, time.Minute, m, sf, nil)
	sid := connectOK(t, rt, "h1")

	got, err := rt.Call(context.Background(), "sftp_write", map[string]any{"sessionId": sid, "path": "out.txt", "content": "hello"})
	if err != nil {
		t.Fatalf("Call(sftp_write): %v", err)
	}
	res := got.(SftpWriteResult)
	if !res.OK || res.Path != "/home/user/out.txt" {
		t.Fatalf("sftp_write result = %+v, want ok + resolved path", res)
	}
	_, writes, _, _, _ := sf.snapshot()
	if len(writes) != 1 || writes[0].path != "out.txt" || writes[0].content != "hello" {
		t.Fatalf("sftp_write handed %+v to the service", writes)
	}
	for _, bad := range []map[string]any{
		{"sessionId": sid, "path": "x"},
		{"sessionId": sid, "content": "x"},
		{"path": "x", "content": "x"},
		{"sessionId": sid, "path": "x", "content": 9},
	} {
		if _, err := rt.Call(context.Background(), "sftp_write", bad); err == nil {
			t.Fatalf("sftp_write with args %v must error", bad)
		}
	}
}

// TestCallSftpUpload: success echoes localPath and (when given) remoteName;
// validation errors; extra fields ignored.
func TestCallSftpUpload(t *testing.T) {
	m := newFakeManager()
	sf := &fakeSFTP{}
	rt := newTestRuntime(2, time.Minute, m, sf, nil)
	sid := connectOK(t, rt, "h1")

	got, err := rt.Call(context.Background(), "sftp_upload", map[string]any{"sessionId": sid, "localPath": "/home/me/a.txt"})
	if err != nil {
		t.Fatalf("Call(sftp_upload): %v", err)
	}
	res := got.(SftpUploadResult)
	if !res.OK || res.LocalPath != "/home/me/a.txt" || res.RemoteName != nil {
		t.Fatalf("sftp_upload result = %+v", res)
	}
	_, _, uploads, _, _ := sf.snapshot()
	if len(uploads) != 1 || uploads[0].localPath != "/home/me/a.txt" || uploads[0].remoteName != "" {
		t.Fatalf("sftp_upload without remoteName = %+v", uploads)
	}

	got, err = rt.Call(context.Background(), "sftp_upload", map[string]any{"sessionId": sid, "localPath": "/home/me/a.txt", "remoteName": "b.txt", "extra": true})
	if err != nil {
		t.Fatalf("Call(sftp_upload with remoteName): %v", err)
	}
	res = got.(SftpUploadResult)
	if res.RemoteName == nil || *res.RemoteName != "b.txt" {
		t.Fatalf("sftp_upload remoteName result = %+v", res)
	}
	_, _, uploads, _, _ = sf.snapshot()
	if uploads[1].remoteName != "b.txt" {
		t.Fatalf("sftp_upload remoteName must reach the service, got %+v", uploads[1])
	}

	for _, bad := range []map[string]any{
		{"sessionId": sid},
		{"localPath": "/home/me/a.txt"},
		{"sessionId": sid, "localPath": 4},
	} {
		if _, err := rt.Call(context.Background(), "sftp_upload", bad); err == nil {
			t.Fatalf("sftp_upload with args %v must error", bad)
		}
	}
}

// TestCallSftpDownload: success echoes both paths; validation errors.
func TestCallSftpDownload(t *testing.T) {
	m := newFakeManager()
	sf := &fakeSFTP{}
	rt := newTestRuntime(2, time.Minute, m, sf, nil)
	sid := connectOK(t, rt, "h1")

	got, err := rt.Call(context.Background(), "sftp_download", map[string]any{"sessionId": sid, "remotePath": "a.txt", "localPath": "/home/me/a.txt"})
	if err != nil {
		t.Fatalf("Call(sftp_download): %v", err)
	}
	res := got.(SftpDownloadResult)
	if !res.OK || res.RemotePath != "a.txt" || res.LocalPath != "/home/me/a.txt" {
		t.Fatalf("sftp_download result = %+v", res)
	}
	for _, bad := range []map[string]any{
		{"sessionId": sid, "remotePath": "x"},
		{"sessionId": sid, "localPath": "x"},
		{"remotePath": "x", "localPath": "x"},
		{"sessionId": sid, "remotePath": "x", "localPath": false},
	} {
		if _, err := rt.Call(context.Background(), "sftp_download", bad); err == nil {
			t.Fatalf("sftp_download with args %v must error", bad)
		}
	}
}

// TestCallPreservesCodedErrors: limit and session errors stay coded.
func TestCallPreservesCodedErrors(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(1, time.Minute, m, &fakeSFTP{}, nil)
	connectOK(t, rt, "h1")
	_, err := rt.Call(context.Background(), "connect_host", map[string]any{"hostId": "h1"})
	assertErrorCode(t, err, apperror.McpSessionLimit)

	sf := &fakeSFTP{writeErr: &sftpservice.Error{Code: apperror.Unknown, Message: "Remote operation failed"}}
	rt2 := newTestRuntime(2, time.Minute, newFakeManager(), sf, nil)
	sid2 := connectOK(t, rt2, "h1")
	_, err = rt2.Call(context.Background(), "sftp_write", map[string]any{"sessionId": sid2, "path": "x", "content": "y"})
	assertErrorCode(t, err, apperror.Unknown)
}

// TestCallNilArgsSafe: nil / empty args behave like an empty object.
func TestCallNilArgsSafe(t *testing.T) {
	m := newFakeManager()
	rt := newTestRuntime(2, time.Minute, m, &fakeSFTP{}, nil)
	if _, err := rt.Call(context.Background(), "list_hosts", nil); err != nil {
		t.Fatalf("Call(list_hosts, nil): %v", err)
	}
	if _, err := rt.Call(context.Background(), "connect_host", nil); err == nil {
		t.Fatal("connect_host with nil args must error (missing hostId)")
	}
	_ = sessions.ConnectOptions{}
}

// TestCallRejectsNullOptionalFields (I2 RED): zod/draft-07 strict parity — an
// optional field that is present with a null value is invalid (a null is not
// a string/boolean/integer). The old implementation treated present-null as
// absent and accepted these calls.
func TestCallRejectsNullOptionalFields(t *testing.T) {
	m := newFakeManager()
	sf := &fakeSFTP{}
	rt := newTestRuntime(4, time.Minute, m, sf, nil)
	sid := connectOK(t, rt, "h1")

	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"connect password null", "connect_host", map[string]any{"hostId": "h1", "password": nil}},
		{"connect acceptHostKey null", "connect_host", map[string]any{"hostId": "h1", "acceptHostKey": nil}},
		{"run timeoutMs null", "run_command", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": nil}},
		{"sftp_list path null", "sftp_list", map[string]any{"sessionId": sid, "path": nil}},
		{"sftp_upload remoteName null", "sftp_upload", map[string]any{"sessionId": sid, "localPath": "/home/me/a.txt", "remoteName": nil}},
	}
	for _, tc := range cases {
		if _, err := rt.Call(context.Background(), tc.tool, tc.args); err == nil {
			t.Fatalf("%s: a present-null optional value must be rejected", tc.name)
		}
	}
}

// TestCallRunCommandTimeoutStrict (I2 RED): timeoutMs must be a JSON integer
// within [1, 300000] — never clamped, never a numeric string. The old
// implementation accepted numeric strings (ParseInt), clamped out-of-range
// values and defaulted zero/negative to 60s.
func TestCallRunCommandTimeoutStrict(t *testing.T) {
	m := newFakeManager()
	m.execOut = "out"
	rt := newTestRuntime(4, time.Minute, m, &fakeSFTP{}, nil)
	sid := connectOK(t, rt, "h1")

	reject := []struct {
		name string
		args map[string]any
	}{
		{"numeric string", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": "5000"}},
		{"null", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": nil}},
		{"zero", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": float64(0)}},
		{"negative", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": float64(-1)}},
		{"above max", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": float64(300001)}},
		{"fraction", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": float64(1.5)}},
		{"bool", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": true}},
	}
	for _, tc := range reject {
		if _, err := rt.Call(context.Background(), "run_command", tc.args); err == nil {
			t.Fatalf("run_command timeoutMs=%s must be rejected", tc.name)
		}
	}

	// Missing timeoutMs defaults to 60s; Go integer numeric types from unit
	// callers are accepted consistently, and the bounds are inclusive.
	accept := []struct {
		name string
		args map[string]any
	}{
		{"missing", map[string]any{"sessionId": sid, "command": "ls"}},
		{"int", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": 150}},
		{"int64", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": int64(150)}},
		{"int32", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": int32(150)}},
		{"float64 integral", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": float64(150)}},
		{"min bound", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": float64(1)}},
		{"max bound", map[string]any{"sessionId": sid, "command": "ls", "timeoutMs": float64(300000)}},
	}
	for _, tc := range accept {
		if _, err := rt.Call(context.Background(), "run_command", tc.args); err != nil {
			t.Fatalf("run_command timeoutMs=%s: %v", tc.name, err)
		}
	}
	// The default (missing) timeout reaches the manager as exactly 60s.
	_, _, timeouts := m.snapshot()
	if len(timeouts) == 0 || timeouts[0] != DefaultCommandTimeoutMs*time.Millisecond {
		t.Fatalf("default exec timeouts = %v, want first = 60s", timeouts)
	}
}

// TestCallSensitiveToolsHonourPermissionGate: a denied ask must not exec or
// write, while list/read still run. Nil Auth (the other tests) remains allow.
func TestCallSensitiveToolsHonourPermissionGate(t *testing.T) {
	m := newFakeManager()
	m.execOut = "should-not-run"
	sf := &fakeSFTP{cwd: "/home", writeResolved: "/tmp/x"}
	auth := permission.NewService(permission.ServiceDeps{Gate: permission.DenyGate{}})
	rt := New(Deps{
		Hosts: newFakeHostStore(testHost("h1", "lab")), Manager: m, SFTP: sf,
		MaxSessions: 2, IdleTimeout: time.Minute, Auth: auth,
	})
	sid := connectOK(t, rt, "h1")

	_, err := rt.Call(context.Background(), "run_command", map[string]any{
		"sessionId": sid, "command": "id",
	})
	assertErrorCode(t, err, apperror.PermissionDenied)
	if _, _, timeouts := m.snapshot(); len(timeouts) != 0 {
		t.Fatalf("run_command ran despite deny: timeouts=%v", timeouts)
	}

	_, err = rt.Call(context.Background(), "sftp_write", map[string]any{
		"sessionId": sid, "path": "/tmp/x", "content": "SECRET",
	})
	assertErrorCode(t, err, apperror.PermissionDenied)
	if _, writes, _, _, _ := sf.snapshot(); len(writes) != 0 {
		t.Fatalf("sftp_write ran despite deny: %+v", writes)
	}

	if _, err := rt.Call(context.Background(), "sftp_list", map[string]any{"sessionId": sid}); err != nil {
		t.Fatalf("sftp_list under deny gate: %v", err)
	}
}
