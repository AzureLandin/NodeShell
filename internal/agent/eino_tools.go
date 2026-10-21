package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// toolDef describes one model-facing tool: the name the model calls, the
// description it sees and the parameters it may pass. Schemas stay minimal on
// purpose: every value is validated again on execution, and the remote limits
// (exec output cap, file size cap) are enforced by the underlying MCP runtime.
type toolDef struct {
	name   string
	desc   string
	params map[string]*schema.ParameterInfo
}

// toolDefs is the advertised tool surface. There is deliberately no session
// argument: the session is injected from the panel the user is looking at, so
// the model cannot address another host, and no local-filesystem tool exists
// at all.
func toolDefs() []toolDef {
	return []toolDef{
		{
			name: toolBash,
			desc: "Run a non-interactive shell command on the remote host over SSH. " +
				"There is no TTY: commands cannot prompt, page or read stdin.",
			params: map[string]*schema.ParameterInfo{
				"command": {
					Type:     schema.String,
					Desc:     "Shell command to run on the remote host",
					Required: true,
				},
				"timeoutMs": {
					Type: schema.Integer,
					Desc: "Optional timeout in milliseconds",
				},
			},
		},
		{
			name: toolSftpList,
			desc: "List a directory on the remote host. Without a path, lists the session's current directory.",
			params: map[string]*schema.ParameterInfo{
				"path": {
					Type: schema.String,
					Desc: "Remote directory path, absolute or relative to the current directory",
				},
			},
		},
		{
			name: toolSftpRead,
			desc: fmt.Sprintf("Read a remote text file (max %d KiB).", MaxFileBytes/1024),
			params: map[string]*schema.ParameterInfo{
				"path": {
					Type:     schema.String,
					Desc:     "Remote file path",
					Required: true,
				},
			},
		},
		{
			name: toolSftpWrite,
			desc: fmt.Sprintf("Write UTF-8 text to a remote file, replacing its contents (max %d KiB).",
				MaxFileBytes/1024),
			params: map[string]*schema.ParameterInfo{
				"path": {
					Type:     schema.String,
					Desc:     "Remote file path",
					Required: true,
				},
				"content": {
					Type:     schema.String,
					Desc:     "Full new file contents",
					Required: true,
				},
			},
		},
	}
}

// agentTool exposes one remote tool to the Eino agent. It never fails the
// run: a failed call is handed back to the model as text, exactly like a
// failed command in a terminal.
type agentTool struct {
	svc       *Service
	def       *toolDef
	sessionID string
	title     string
	conv      *conversation
}

// Info advertises the tool to the model.
func (t *agentTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        t.def.name,
		Desc:        t.def.desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(t.def.params),
	}, nil
}

// InvokableRun executes the tool against the bound session and returns the
// text the model sees. A failure is part of the answer, not the end of the
// run, so the error is returned as the tool result.
func (t *agentTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	out, summary, ok := t.svc.invokeTool(ctx, t.sessionID, t.title, t.def.name, argumentsInJSON)
	detail := ""
	if !ok {
		detail = firstLine(out)
	}
	t.svc.emitIfCurrent(t.sessionID, t.conv, EventTool, ToolEvent{
		SessionID: t.sessionID,
		CallID:    callIDFromContext(ctx),
		Name:      t.def.name,
		Summary:   summary,
		OK:        ok,
		Detail:    detail,
	})
	return out, nil
}

// callIDKey carries the model's tool-call id from the Eino tool middleware
// into the tool implementation, so the UI can pair a tool event with the
// call the model made.
type callIDKey struct{}

// withCallID returns a context carrying the tool-call id.
func withCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callIDKey{}, id)
}

// callIDFromContext returns the tool-call id carried by the context.
func callIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(callIDKey{}).(string)
	return id
}

// callIDMiddleware hands the tool-call id to the tool implementation, so the
// tool event can name the call the model made.
type callIDMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
}

// WrapInvokableToolCall injects the call id into the context of the wrapped
// tool call.
func (m *callIDMiddleware) WrapInvokableToolCall(_ context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		return endpoint(withCallID(ctx, tCtx.CallID), argumentsInJSON, opts...)
	}, nil
}
