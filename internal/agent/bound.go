package agent

import (
	"context"
	"maps"

	"nodeshell/internal/apperror"
	"nodeshell/internal/mcpcli"
	"nodeshell/internal/permission"
)

// agentMCPTools maps the names advertised to the model onto MCP tool names.
// Session-management and local-path tools are deliberately absent: even a
// hallucinated call is refused before it reaches Runtime.Call.
var agentMCPTools = map[string]string{
	toolBash:      "run_command",
	toolSftpList:  "sftp_list",
	toolSftpRead:  "sftp_read",
	toolSftpWrite: "sftp_write",
}

// MCPCaller is the Runtime.CallWith surface BoundCaller dispatches into.
// *mcpcli.Runtime satisfies it.
type MCPCaller interface {
	CallWith(ctx context.Context, name string, args map[string]any, opts mcpcli.CallOpts) (any, error)
}

// BoundCaller is the in-app Agent's view of MCP: the same Call surface an
// external client uses, with the current GUI session injected and local-path
// / session-selection tools refused.
type BoundCaller struct {
	MCP MCPCaller
}

// Call runs one model-facing tool against the bound session. sessionId and
// localPath on args are dropped so the model cannot address another host or
// the local disk.
func (b *BoundCaller) Call(ctx context.Context, sessionID, title, name string, args map[string]any) (any, error) {
	mcpName, ok := agentMCPTools[name]
	if !ok {
		return nil, errf(apperror.Unknown, "unknown tool")
	}
	if b == nil || b.MCP == nil {
		return nil, errf(apperror.Unknown, "Remote tools are unavailable")
	}
	next := map[string]any{}
	if args != nil {
		next = maps.Clone(args)
	}
	delete(next, "sessionId")
	delete(next, "localPath")
	next["sessionId"] = sessionID
	if mcpName == "sftp_list" {
		next["chdir"] = false
	}
	return b.MCP.CallWith(ctx, mcpName, next, mcpcli.CallOpts{
		Source: permission.SourceAgent,
		Title:  title,
	})
}

// ToolCaller is the session-bound tool surface the agent loop dispatches
// into. Production is BoundCaller; tests inject a fake.
type ToolCaller interface {
	Call(ctx context.Context, sessionID, title, name string, args map[string]any) (any, error)
}
