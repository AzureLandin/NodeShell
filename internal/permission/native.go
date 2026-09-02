package permission

import (
	"context"
	"fmt"
	"strings"
)

// PromptFunc is the native-dialog seam. Production uses platformPrompt
// (MessageBox / osascript / zenity); tests inject a fake so a `go test`
// never pops a real window.
type PromptFunc func(req Request) Decision

// NativeGate prompts with a blocking OS dialog. Used only by --mcp when
// mcpPermissionMode is local (the process has no WebView). Yes is allow-once;
// No (or a missing dialog backend) is deny. external mode never constructs it.
type NativeGate struct {
	Prompt PromptFunc
}

// Ask runs the native dialog. ctx cancellation cannot dismiss MessageBox /
// osascript, but a cancelled ctx still fails closed if it wins the race
// after the dialog returns.
func (g *NativeGate) Ask(ctx context.Context, req Request) (Decision, error) {
	if err := ctx.Err(); err != nil {
		return DecisionDeny, err
	}
	prompt := g.Prompt
	if prompt == nil {
		prompt = platformPrompt
	}
	done := make(chan Decision, 1)
	go func() { done <- prompt(req) }()
	select {
	case d := <-done:
		if err := ctx.Err(); err != nil {
			return DecisionDeny, err
		}
		if d == DecisionAllowOnce || d == DecisionAllowSession {
			return d, nil
		}
		return DecisionDeny, nil
	case <-ctx.Done():
		return DecisionDeny, ctx.Err()
	}
}

// nativeBody is the OS-dialog text. English on purpose: the MCP process has
// no i18n, and the dialog must never include file contents or secrets.
func nativeBody(req Request) string {
	who := "The sidebar agent"
	if req.Source == SourceMCP {
		who = "An MCP client"
	}
	host := strings.TrimSpace(req.Title)
	if host == "" {
		host = "the remote host"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s wants to %s on %s.\n\n", who, nativeAction(req.Tool), host)
	if req.Summary != "" {
		fmt.Fprintf(&b, "%s\n", Truncate(req.Summary))
	}
	if req.Detail != "" {
		fmt.Fprintf(&b, "%s\n", Truncate(req.Detail))
	}
	b.WriteString("\nAllow this operation?")
	s := b.String()
	const max = 2000
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func nativeAction(tool string) string {
	switch tool {
	case "bash", "run_command":
		return "run a command"
	case "sftp_write":
		return "write a remote file"
	case "sftp_upload":
		return "upload a file"
	case "sftp_download":
		return "download a file"
	default:
		return "perform a sensitive action"
	}
}
