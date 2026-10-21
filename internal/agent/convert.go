package agent

import (
	"sort"

	"github.com/cloudwego/eino/schema"
)

// Chat roles used in the transcript. They are the OpenAI-compatible wire
// roles, so the transcript stays readable on its own.
const (
	roleSystem    = "system"
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"
)

// chatMessage is one OpenAI-compatible chat message. A tool result carries
// ToolCallID; an assistant turn that called tools carries ToolCalls.
// ReasoningContent keeps the model's thinking text: it is replayed to the
// provider on the next turn but never rendered in the UI.
type chatMessage struct {
	Role             string     `json:"role"`
	Content          string     `json:"content,omitempty"`
	ToolCalls        []toolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
}

// toolCall is one requested tool invocation.
type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

// functionCall names the tool and carries its JSON-encoded arguments.
type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// toSchemaMessages converts the NodeShell transcript into the message list the
// Eino agent replays to the model. The system prompt is not part of it: the
// agent instruction carries that.
func toSchemaMessages(msgs []chatMessage) []*schema.Message {
	out := make([]*schema.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toSchemaMessage(m))
	}
	return out
}

// toSchemaMessage converts one transcript message into its Eino counterpart.
func toSchemaMessage(m chatMessage) *schema.Message {
	switch m.Role {
	case roleSystem:
		return schema.SystemMessage(m.Content)
	case roleUser:
		return schema.UserMessage(m.Content)
	case roleAssistant:
		msg := schema.AssistantMessage(m.Content, toSchemaToolCalls(m.ToolCalls))
		msg.ReasoningContent = m.ReasoningContent
		return msg
	case roleTool:
		return schema.ToolMessage(m.Content, m.ToolCallID)
	default:
		return schema.UserMessage(m.Content)
	}
}

// toSchemaToolCalls converts the recorded tool calls into Eino tool calls.
func toSchemaToolCalls(calls []toolCall) []schema.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]schema.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, schema.ToolCall{
			ID:   c.ID,
			Type: c.Type,
			Function: schema.FunctionCall{
				Name:      c.Function.Name,
				Arguments: c.Function.Arguments,
			},
		})
	}
	return out
}

// fromSchemaMessage converts a message the Eino agent produced back into the
// transcript shape, so the NodeShell history stays the source of truth.
func fromSchemaMessage(msg *schema.Message) chatMessage {
	if msg == nil {
		return chatMessage{}
	}
	out := chatMessage{
		Role:             fromSchemaRole(msg.Role),
		Content:          msg.Content,
		ToolCallID:       msg.ToolCallID,
		ReasoningContent: msg.ReasoningContent,
	}
	if len(msg.ToolCalls) > 0 {
		out.ToolCalls = fromSchemaToolCalls(msg.ToolCalls)
	}
	return out
}

// fromSchemaRole maps an Eino role onto the transcript role.
func fromSchemaRole(role schema.RoleType) string {
	switch role {
	case schema.System:
		return roleSystem
	case schema.User:
		return roleUser
	case schema.Assistant:
		return roleAssistant
	case schema.Tool:
		return roleTool
	default:
		return string(role)
	}
}

// fromSchemaToolCalls converts the tool calls of one assistant message. The
// provider streams parallel calls with an index; keeping that order is what
// makes the tools run in the order the model asked for them.
func fromSchemaToolCalls(calls []schema.ToolCall) []toolCall {
	ordered := make([]schema.ToolCall, len(calls))
	copy(ordered, calls)
	if allIndexed(ordered) {
		sort.SliceStable(ordered, func(i, j int) bool {
			return *ordered[i].Index < *ordered[j].Index
		})
	}
	out := make([]toolCall, 0, len(ordered))
	for _, c := range ordered {
		out = append(out, toolCall{
			ID:   c.ID,
			Type: c.Type,
			Function: functionCall{
				Name:      c.Function.Name,
				Arguments: c.Function.Arguments,
			},
		})
	}
	return out
}

// allIndexed reports whether every call carries a stream index, i.e. whether
// the order can be recovered from the indices alone.
func allIndexed(calls []schema.ToolCall) bool {
	for _, c := range calls {
		if c.Index == nil {
			return false
		}
	}
	return len(calls) > 0
}
