package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/permission"
	"nodeshell/internal/sftpservice"
)

// Tool names served to the model. There is deliberately no session argument:
// the session is injected from the panel the user is looking at, so the model
// cannot address another host, and no local-filesystem tool exists at all.
const (
	toolBash      = "bash"
	toolSftpList  = "sftp_list"
	toolSftpRead  = "sftp_read"
	toolSftpWrite = "sftp_write"
)

// maxListEntries bounds one directory listing put into the transcript.
const maxListEntries = 200

// toolSpecs advertises the tool surface. Schemas stay minimal on purpose:
// every value is validated again on execution, and the remote limits (exec
// output cap, file size cap) are enforced by the underlying services.
func toolSpecs() []toolSpec {
	return []toolSpec{
		{
			Type: "function",
			Function: functionSpec{
				Name: toolBash,
				Description: "Run a non-interactive shell command on the remote host over SSH. " +
					"There is no TTY: commands cannot prompt, page or read stdin.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "Shell command to run on the remote host",
						},
						"timeoutMs": map[string]any{
							"type":        "integer",
							"description": "Optional timeout in milliseconds",
						},
					},
					"required": []any{"command"},
				},
			},
		},
		{
			Type: "function",
			Function: functionSpec{
				Name:        toolSftpList,
				Description: "List a directory on the remote host. Without a path, lists the session's current directory.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Remote directory path, absolute or relative to the current directory",
						},
					},
				},
			},
		},
		{
			Type: "function",
			Function: functionSpec{
				Name:        toolSftpRead,
				Description: fmt.Sprintf("Read a remote text file (max %d KiB).", MaxFileBytes/1024),
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "Remote file path"},
					},
					"required": []any{"path"},
				},
			},
		},
		{
			Type: "function",
			Function: functionSpec{
				Name: toolSftpWrite,
				Description: fmt.Sprintf("Write UTF-8 text to a remote file, replacing its contents (max %d KiB).",
					MaxFileBytes/1024),
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "Remote file path"},
						"content": map[string]any{"type": "string", "description": "Full new file contents"},
					},
					"required": []any{"path", "content"},
				},
			},
		},
	}
}

// runTool executes one tool call against the session. It returns the content
// to append to the transcript, a short display summary for the UI, and
// whether the call succeeded. A failure is never fatal to the run: the error
// text goes back to the model so it can correct itself, exactly like a failed
// command in a terminal.
func (s *Service) runTool(ctx context.Context, sessionID, title string, call toolCall) (string, string, bool) {
	name := call.Function.Name
	switch name {
	case toolBash:
		var args struct {
			Command   string `json:"command"`
			TimeoutMs int64  `json:"timeoutMs"`
		}
		if err := decodeArgs(call.Function.Arguments, &args); err != nil {
			return toolError(err), name, false
		}
		command := strings.TrimSpace(args.Command)
		if command == "" {
			return toolError(errf(apperror.Unknown, "command is required")), name, false
		}
		summary := truncateSummary(command)
		if err := s.authorize(ctx, sessionID, title, name, summary, ""); err != nil {
			return toolError(err), summary, false
		}
		if s.exec == nil {
			return toolError(errf(apperror.Unknown, "Remote commands are unavailable")), summary, false
		}
		out, err := s.exec.Exec(sessionID, ctx, command, s.commandTimeout(args.TimeoutMs))
		if err != nil {
			return toolError(err), summary, false
		}
		return capResult(out), summary, true

	case toolSftpList:
		var args struct {
			Path string `json:"path"`
		}
		if err := decodeArgs(call.Function.Arguments, &args); err != nil {
			return toolError(err), name, false
		}
		summary := args.Path
		if strings.TrimSpace(summary) == "" {
			summary = "."
		}
		if err := s.authorize(ctx, sessionID, title, name, summary, ""); err != nil {
			return toolError(err), summary, false
		}
		if s.files == nil {
			return toolError(errf(apperror.Unknown, "Remote files are unavailable")), summary, false
		}
		entries, err := s.files.List(sessionID, args.Path)
		if err != nil {
			return toolError(err), summary, false
		}
		return capResult(formatEntries(entries)), summary, true

	case toolSftpRead:
		var args struct {
			Path string `json:"path"`
		}
		if err := decodeArgs(call.Function.Arguments, &args); err != nil {
			return toolError(err), name, false
		}
		if strings.TrimSpace(args.Path) == "" {
			return toolError(errf(apperror.Unknown, "path is required")), name, false
		}
		if err := s.authorize(ctx, sessionID, title, name, args.Path, ""); err != nil {
			return toolError(err), args.Path, false
		}
		if s.files == nil {
			return toolError(errf(apperror.Unknown, "Remote files are unavailable")), args.Path, false
		}
		resolved, content, err := s.files.ReadText(sessionID, args.Path, MaxFileBytes)
		if err != nil {
			return toolError(err), args.Path, false
		}
		return capResult(content), resolved, true

	case toolSftpWrite:
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := decodeArgs(call.Function.Arguments, &args); err != nil {
			return toolError(err), name, false
		}
		if strings.TrimSpace(args.Path) == "" {
			return toolError(errf(apperror.Unknown, "path is required")), name, false
		}
		detail := fmt.Sprintf("%d bytes", len(args.Content))
		if err := s.authorize(ctx, sessionID, title, name, args.Path, detail); err != nil {
			return toolError(err), args.Path, false
		}
		if s.files == nil {
			return toolError(errf(apperror.Unknown, "Remote files are unavailable")), args.Path, false
		}
		resolved, err := s.files.WriteText(sessionID, args.Path, args.Content, MaxFileBytes)
		if err != nil {
			return toolError(err), args.Path, false
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), resolved), resolved, true

	default:
		return toolError(errf(apperror.Unknown, "unknown tool")), name, false
	}
}

// authorize asks the permission service before a tool runs. Nil Auth allows
// (tests). The request never carries file contents or secrets — only a
// command, a path, or a write size.
func (s *Service) authorize(ctx context.Context, sessionID, title, tool, summary, detail string) error {
	if s.auth == nil {
		return nil
	}
	return s.auth.Authorize(ctx, permission.Request{
		Source:    permission.SourceAgent,
		Tool:      tool,
		SessionID: sessionID,
		Title:     title,
		Summary:   permission.Truncate(summary),
		Detail:    detail,
	})
}

// commandTimeout clamps a model-supplied timeout into (0, execTimeout]; an
// absent or invalid value falls back to the service default, so the model can
// shorten a command but never outlast the configured bound.
func (s *Service) commandTimeout(ms int64) time.Duration {
	if ms <= 0 {
		return s.execTimeout
	}
	d := time.Duration(ms) * time.Millisecond
	if d > s.execTimeout {
		return s.execTimeout
	}
	if d < time.Second {
		return time.Second
	}
	return d
}

// decodeArgs parses the streamed argument JSON. Empty arguments are treated as
// an empty object, which some providers send for a no-parameter call.
func decodeArgs(raw string, out any) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		trimmed = "{}"
	}
	if err := json.Unmarshal([]byte(trimmed), out); err != nil {
		return errf(apperror.Unknown, "arguments are not valid JSON")
	}
	return nil
}

// toolError renders a failure for the model. The message is the coded,
// path-free text the underlying services produce, prefixed so the model reads
// it as a failed call rather than as file content.
func toolError(err error) string {
	return "error: " + err.Error()
}

// capResult bounds one tool result and marks a truncation, so the model knows
// it received a prefix rather than the whole output.
func capResult(out string) string {
	if len(out) <= MaxToolResultBytes {
		if out == "" {
			return "(no output)"
		}
		return out
	}
	return out[:MaxToolResultBytes] + "\n… output truncated"
}

// formatEntries renders a listing as one line per entry: type marker, size,
// modification time and name. Compact on purpose — the model pays for every
// byte of it.
func formatEntries(entries []sftpservice.Entry) string {
	var b strings.Builder
	for i, e := range entries {
		if i >= maxListEntries {
			fmt.Fprintf(&b, "… %d more entries\n", len(entries)-maxListEntries)
			break
		}
		kind := "-"
		if e.IsDirectory {
			kind = "d"
		}
		fmt.Fprintf(&b, "%s %10d %s %s\n", kind, e.Size,
			time.UnixMilli(e.ModifyTime).UTC().Format("2006-01-02 15:04"), e.Name)
	}
	if b.Len() == 0 {
		return "(empty directory)"
	}
	return b.String()
}

// truncateSummary bounds a command shown in the tool event.
func truncateSummary(s string) string {
	line := strings.Join(strings.Fields(s), " ")
	const max = 160
	if len(line) > max {
		return line[:max] + "…"
	}
	return line
}
