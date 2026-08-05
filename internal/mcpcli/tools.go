package mcpcli

import (
	"context"
	"encoding/json"
	"fmt"

	"nodeshell/internal/apperror"
	"nodeshell/internal/sessions"
	"nodeshell/internal/sftpservice"
)

// Tool is one MCP tool definition served by tools/list (T1.7.4).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// HostDTO is the list_hosts row (exactly the TS shape; no secrets).
type HostDTO struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Username         string `json:"username"`
	AuthMethod       string `json:"authMethod"`
	CredentialsSaved bool   `json:"credentialsSaved"`
}

// SessionDTO is one list_sessions row.
type SessionDTO struct {
	SessionID string `json:"sessionId"`
	HostID    string `json:"hostId"`
	Title     string `json:"title"`
}

// ConnectResult is the connect_host result.
type ConnectResult struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
}

// OkSession is the disconnect_session result.
type OkSession struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"sessionId"`
}

// SftpListResult is the sftp_list result.
type SftpListResult struct {
	Cwd     string              `json:"cwd"`
	Entries []sftpservice.Entry `json:"entries"`
}

// SftpReadResult is the sftp_read result (resolved path + content).
type SftpReadResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// SftpWriteResult is the sftp_write result (resolved path).
type SftpWriteResult struct {
	OK   bool   `json:"ok"`
	Path string `json:"path"`
}

// SftpUploadResult is the sftp_upload result; RemoteName is omitted when the
// caller did not provide one.
type SftpUploadResult struct {
	OK         bool    `json:"ok"`
	LocalPath  string  `json:"localPath"`
	RemoteName *string `json:"remoteName,omitempty"`
}

// SftpDownloadResult is the sftp_download result.
type SftpDownloadResult struct {
	OK         bool   `json:"ok"`
	RemotePath string `json:"remotePath"`
	LocalPath  string `json:"localPath"`
}

// Tool definitions: names, descriptions and inputSchemas are byte-compatible
// with src/main/mcp-server.ts (zod v4-mini toJSONSchema, draft-07). The
// schema literals are captured from the installed SDK; parsing them at each
// Tools() call keeps the JSON exact.
func Tools() []Tool {
	return []Tool{
		{
			Name:        "list_hosts",
			Description: "List saved SSH hosts (no secrets).",
			InputSchema: mustSchema(`{"type":"object"}`),
		},
		{
			Name:        "list_sessions",
			Description: "List active MCP SSH sessions opened by this server process.",
			InputSchema: mustSchema(`{"type":"object"}`),
		},
		{
			Name:        "connect_host",
			Description: "Connect to a saved host and open an SSH session for commands/SFTP.",
			InputSchema: mustSchema(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"hostId":{"type":"string","description":"Host id from list_hosts"},"password":{"description":"Override password if not saved","type":"string"},"acceptHostKey":{"description":"Accept and store a new or changed host key. Default false — set true only after verifying the fingerprint.","type":"boolean"}},"required":["hostId"]}`),
		},
		{
			Name:        "disconnect_session",
			Description: "Disconnect an active MCP SSH session.",
			InputSchema: mustSchema(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"}},"required":["sessionId"]}`),
		},
		{
			Name:        "run_command",
			Description: "Run a non-interactive command on the remote host via SSH exec (not PTY).",
			InputSchema: mustSchema(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"command":{"type":"string","description":"Shell command to execute"},"timeoutMs":{"type":"integer","exclusiveMinimum":0,"maximum":300000}},"required":["sessionId","command"]}`),
		},
		{
			Name:        "sftp_list",
			Description: "List a remote directory. If path is given, chdir there first for this session.",
			InputSchema: mustSchema(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"path":{"description":"Remote directory path (absolute or relative)","type":"string"}},"required":["sessionId"]}`),
		},
		{
			Name:        "sftp_read",
			Description: "Read a remote text file (max 512KB).",
			InputSchema: mustSchema(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"path":{"type":"string"}},"required":["sessionId","path"]}`),
		},
		{
			Name:        "sftp_write",
			Description: "Write UTF-8 text to a remote file (creates/overwrites).",
			InputSchema: mustSchema(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"path":{"type":"string"},"content":{"type":"string"}},"required":["sessionId","path","content"]}`),
		},
		{
			Name:        "sftp_upload",
			Description: "Upload a local file under the user home directory to the remote session current directory.",
			InputSchema: mustSchema(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"localPath":{"type":"string","description":"Absolute local path under the user home directory"},"remoteName":{"type":"string"}},"required":["sessionId","localPath"]}`),
		},
		{
			Name:        "sftp_download",
			Description: "Download a remote file to a local path under the user home directory.",
			InputSchema: mustSchema(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"sessionId":{"type":"string"},"remotePath":{"type":"string"},"localPath":{"type":"string","description":"Absolute local path under the user home directory"}},"required":["sessionId","remotePath","localPath"]}`),
		},
	}
}

// mustSchema parses an embedded tool-schema literal. The literals are
// compile-time constants, so a parse failure is a programming error.
func mustSchema(s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		panic("mcpcli: invalid tool schema literal: " + err.Error())
	}
	return m
}

// Call dispatches a tool invocation. args is the parsed JSON object (extra
// fields are ignored, like the SDK's zod strip); validation errors are coded
// errors and never panic. Business errors keep their stable codes for the
// server to format, and no message embeds a password or a path.
func (r *Runtime) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "list_hosts":
		return r.ListHosts()
	case "list_sessions":
		return r.ListSessions(), nil
	case "connect_host":
		hostID, err := argString(args, "hostId")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		password, err := argStringOpt(args, "password")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		acceptHostKey, err := argBoolOpt(args, "acceptHostKey")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		return r.ConnectHost(ctx, hostID, sessions.ConnectOptions{Password: password, AcceptHostKey: acceptHostKey})
	case "disconnect_session":
		sessionID, err := argString(args, "sessionId")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		r.DisconnectSession(sessionID)
		return OkSession{OK: true, SessionID: sessionID}, nil
	case "run_command":
		sessionID, err := argString(args, "sessionId")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		command, err := argString(args, "command")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		timeoutMs, err := argTimeoutMs(args)
		if err != nil {
			return nil, toolArgError(name, err)
		}
		return r.RunCommand(ctx, sessionID, command, timeoutMs)
	case "sftp_list":
		sessionID, err := argString(args, "sessionId")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		remotePath, err := argStringOpt(args, "path")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		return r.SftpList(ctx, sessionID, remotePath)
	case "sftp_read":
		sessionID, err := argString(args, "sessionId")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		remotePath, err := argString(args, "path")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		return r.SftpRead(ctx, sessionID, remotePath)
	case "sftp_write":
		sessionID, err := argString(args, "sessionId")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		remotePath, err := argString(args, "path")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		content, err := argString(args, "content")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		return r.SftpWrite(ctx, sessionID, remotePath, content)
	case "sftp_upload":
		sessionID, err := argString(args, "sessionId")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		localPath, err := argString(args, "localPath")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		remoteName, err := argStringOpt(args, "remoteName")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		return r.SftpUpload(ctx, sessionID, localPath, remoteName)
	case "sftp_download":
		sessionID, err := argString(args, "sessionId")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		remotePath, err := argString(args, "remotePath")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		localPath, err := argString(args, "localPath")
		if err != nil {
			return nil, toolArgError(name, err)
		}
		return r.SftpDownload(ctx, sessionID, remotePath, localPath)
	default:
		return nil, &Error{Code: apperror.Unknown, Message: fmt.Sprintf("Unknown tool: %s", name)}
	}
}

// toolArgError wraps an argument-validation failure with the tool name. The
// message never echoes the argument value.
func toolArgError(tool string, err error) error {
	return &Error{Code: apperror.Unknown, Message: tool + ": " + err.Error()}
}

func argString(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("argument %q is required", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", key)
	}
	return s, nil
}

func argStringOpt(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string", key)
	}
	return s, nil
}

func argBoolOpt(args map[string]any, key string) (bool, error) {
	v, ok := args[key]
	if !ok {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("argument %q must be a boolean", key)
	}
	return b, nil
}

// argIntOpt reads an optional integer argument from a JSON-decoded object,
// where numbers arrive as float64 (and possibly json.Number / int variants).
// A present value that is not a JSON integer — including null and numeric
// strings — is rejected (draft-07 "type":"integer" parity).
func argIntOpt(args map[string]any, key string) (int64, error) {
	v, ok := args[key]
	if !ok {
		return 0, nil
	}
	switch n := v.(type) {
	case float64:
		if n != float64(int64(n)) {
			return 0, fmt.Errorf("argument %q must be an integer", key)
		}
		return int64(n), nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, fmt.Errorf("argument %q must be an integer", key)
		}
		return i, nil
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	case int32:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("argument %q must be an integer", key)
	}
}

// argTimeoutMs reads the optional run_command timeoutMs: absent defaults to
// 60s; present must be an integer within [1, 300000] — strict, never clamped
// (draft-07 exclusiveMinimum 0 / maximum 300000 parity).
func argTimeoutMs(args map[string]any) (int64, error) {
	if _, ok := args["timeoutMs"]; !ok {
		return DefaultCommandTimeoutMs, nil
	}
	ms, err := argIntOpt(args, "timeoutMs")
	if err != nil {
		return 0, err
	}
	if ms < MinCommandTimeoutMs || ms > MaxCommandTimeoutMs {
		return 0, fmt.Errorf("argument %q is out of range", "timeoutMs")
	}
	return ms, nil
}
