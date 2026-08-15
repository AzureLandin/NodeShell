package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nodeshell/internal/apperror"
	"nodeshell/internal/mcpcli"
	"nodeshell/internal/permission"
)

type recMCP struct {
	name   string
	args   map[string]any
	opts   mcpcli.CallOpts
	calls  int
	result any
	err    error
}

func (r *recMCP) CallWith(_ context.Context, name string, args map[string]any, opts mcpcli.CallOpts) (any, error) {
	r.calls++
	r.name = name
	r.args = args
	r.opts = opts
	return r.result, r.err
}

func TestBoundCallerInjectsSessionAndDropsModelSession(t *testing.T) {
	mcp := &recMCP{result: "ok"}
	b := &BoundCaller{MCP: mcp}

	got, err := b.Call(context.Background(), "real-sid", "prod-web", toolBash, map[string]any{
		"command":   "df -h",
		"sessionId": "evil",
		"localPath": "/etc/passwd",
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got != "ok" {
		t.Fatalf("result = %v", got)
	}
	if mcp.name != "run_command" {
		t.Fatalf("mcp tool = %q, want run_command", mcp.name)
	}
	if mcp.args["sessionId"] != "real-sid" {
		t.Fatalf("injected session = %v, want real-sid (model sessionId must be overwritten)", mcp.args["sessionId"])
	}
	if _, ok := mcp.args["localPath"]; ok {
		t.Fatalf("localPath leaked into MCP args: %+v", mcp.args)
	}
	if mcp.args["command"] != "df -h" {
		t.Fatalf("command = %v", mcp.args["command"])
	}
	if mcp.opts.Source != permission.SourceAgent || mcp.opts.Title != "prod-web" {
		t.Fatalf("CallOpts = %+v, want SourceAgent + prod-web", mcp.opts)
	}
}

func TestBoundCallerRejectsSessionAndLocalPathTools(t *testing.T) {
	mcp := &recMCP{result: "should-not-run"}
	b := &BoundCaller{MCP: mcp}

	for _, name := range []string{
		"sftp_upload", "sftp_download", "connect_host", "disconnect_session",
		"list_hosts", "list_sessions", "run_command",
	} {
		_, err := b.Call(context.Background(), "s1", "host", name, map[string]any{
			"sessionId": "other", "localPath": "/tmp/x", "hostId": "h1",
		})
		if err == nil {
			t.Fatalf("%s must be refused", name)
		}
		if !strings.Contains(err.Error(), "unknown tool") {
			t.Fatalf("%s error = %v, want unknown tool", name, err)
		}
	}
	if mcp.calls != 0 {
		t.Fatalf("refused tools must not reach MCP, calls=%d", mcp.calls)
	}
}

func TestBoundCallerSftpListDisablesChdir(t *testing.T) {
	mcp := &recMCP{result: mcpcli.SftpListResult{}}
	b := &BoundCaller{MCP: mcp}

	if _, err := b.Call(context.Background(), "s1", "host", toolSftpList, map[string]any{
		"path": "/var/log", "chdir": true,
	}); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if mcp.name != "sftp_list" {
		t.Fatalf("mcp tool = %q", mcp.name)
	}
	if chdir, _ := mcp.args["chdir"].(bool); chdir {
		t.Fatalf("chdir = %v, want false so the GUI SFTP cwd is not moved", mcp.args["chdir"])
	}
	if mcp.args["sessionId"] != "s1" || mcp.args["path"] != "/var/log" {
		t.Fatalf("args = %+v", mcp.args)
	}
}

func TestBoundCallerUnknownToolIsCoded(t *testing.T) {
	b := &BoundCaller{MCP: &recMCP{}}
	_, err := b.Call(context.Background(), "s1", "host", "rm_rf", nil)
	if err == nil {
		t.Fatal("unknown tool must error")
	}
	var coded interface{ ErrorCode() string }
	if !errors.As(err, &coded) || coded.ErrorCode() != apperror.Unknown {
		t.Fatalf("error = %v, want coded UNKNOWN", err)
	}
}
