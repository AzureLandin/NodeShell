package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"nodeshell/internal/apperror"
	"nodeshell/internal/mcpcli"
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
// output cap, file size cap) are enforced by the underlying MCP runtime.
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
	args, err := decodeArgsMap(call.Function.Arguments)
	if err != nil {
		return toolError(err), name, false
	}
	if name == toolBash {
		command, _ := args["command"].(string)
		command = strings.TrimSpace(command)
		if command == "" {
			return toolError(errf(apperror.Unknown, "command is required")), name, false
		}
		args["command"] = command
		args["timeoutMs"] = int64(s.commandTimeout(intFromArgs(args, "timeoutMs")) / time.Millisecond)
	}
	summary := toolSummary(name, args)
	if s.tools == nil {
		return toolError(errf(apperror.Unknown, "Remote tools are unavailable")), summary, false
	}
	result, err := s.tools.Call(ctx, sessionID, title, name, args)
	if err != nil {
		return toolError(err), summary, false
	}
	return formatToolResult(name, args, result), summary, true
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

// decodeArgsMap parses the streamed argument JSON. Empty arguments are treated
// as an empty object, which some providers send for a no-parameter call.
func decodeArgsMap(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		trimmed = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
		return nil, errf(apperror.Unknown, "arguments are not valid JSON")
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

func intFromArgs(args map[string]any, key string) int64 {
	v, ok := args[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	default:
		return 0
	}
}

func stringFromArgs(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func toolSummary(name string, args map[string]any) string {
	switch name {
	case toolBash:
		return truncateSummary(stringFromArgs(args, "command"))
	case toolSftpList:
		path := strings.TrimSpace(stringFromArgs(args, "path"))
		if path == "" {
			return "."
		}
		return path
	case toolSftpRead, toolSftpWrite:
		return stringFromArgs(args, "path")
	default:
		return name
	}
}

func formatToolResult(name string, args map[string]any, result any) string {
	switch name {
	case toolBash:
		out, _ := result.(string)
		return capResult(out)
	case toolSftpList:
		if res, ok := result.(mcpcli.SftpListResult); ok {
			return capResult(formatEntries(res.Entries))
		}
	case toolSftpRead:
		if res, ok := result.(mcpcli.SftpReadResult); ok {
			return capResult(res.Content)
		}
	case toolSftpWrite:
		path := stringFromArgs(args, "path")
		if res, ok := result.(mcpcli.SftpWriteResult); ok && res.Path != "" {
			path = res.Path
		}
		return fmt.Sprintf("wrote %d bytes to %s", len(stringFromArgs(args, "content")), path)
	}
	return capResult(fmt.Sprint(result))
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
