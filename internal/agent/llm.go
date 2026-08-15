package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"nodeshell/internal/apperror"
)

// Chat roles used in the transcript.
const (
	roleSystem    = "system"
	roleUser      = "user"
	roleAssistant = "assistant"
	roleTool      = "tool"
)

// Stream limits. They bound one turn's memory independently of what the
// endpoint sends: a server that never stops streaming is cut off instead of
// growing the process heap.
const (
	maxSSELineBytes       = 1 << 20
	maxAssistantTextBytes = 256 * 1024
	maxToolArgsBytes      = 64 * 1024
	maxToolCallsPerTurn   = 8
	maxErrorBodyBytes     = 4 * 1024
)

// chatMessage is one OpenAI-compatible chat message. A tool result carries
// ToolCallID; an assistant turn that called tools carries ToolCalls.
type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// toolSpec is one advertised tool (OpenAI "function" tool shape).
type toolSpec struct {
	Type     string       `json:"type"`
	Function functionSpec `json:"function"`
}

type functionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []toolSpec    `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
}

// streamChunk is one SSE payload. Only the fields the loop needs are decoded;
// everything else (usage, ids, fingerprints) is ignored.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string          `json:"content"`
			ToolCalls []deltaToolCall `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// deltaToolCall is a tool call fragment: name and id arrive once, arguments
// arrive in pieces that must be concatenated per index.
type deltaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// streamResult is one completed assistant turn.
type streamResult struct {
	text      string
	toolCalls []toolCall
}

// assistantMessage converts a finished turn into the transcript message. An
// assistant turn with tool calls must be recorded even when its text is
// empty, otherwise the following tool results have nothing to attach to.
func assistantMessage(res streamResult) chatMessage {
	return chatMessage{Role: roleAssistant, Content: res.text, ToolCalls: res.toolCalls}
}

// stream performs one streamed chat completion and returns the assistant text
// plus any tool calls. onDelta is invoked for each text fragment as it
// arrives, so the UI renders while the model is still writing.
func (s *Service) stream(ctx context.Context, cfg Config, msgs []chatMessage, onDelta func(string)) (streamResult, error) {
	body, err := json.Marshal(chatRequest{
		Model:    cfg.Model,
		Messages: msgs,
		Tools:    toolSpecs(),
		Stream:   true,
	})
	if err != nil {
		return streamResult{}, errf(apperror.Unknown, "Agent request could not be built")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, completionsURL(cfg.BaseURL), bytes.NewReader(body))
	if err != nil {
		return streamResult{}, errf(apperror.Unknown, "Agent endpoint is invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return streamResult{}, requestError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return streamResult{}, statusError(resp, cfg.APIKey)
	}
	return s.consume(ctx, resp.Body, cfg.APIKey, onDelta)
}

// consume reads the SSE body, emitting text fragments and assembling tool
// calls. Every accumulator is bounded, and a cancelled context surfaces as
// the context error so the caller can tell an abort from a failure.
func (s *Service) consume(ctx context.Context, body io.Reader, apiKey string, onDelta func(string)) (streamResult, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELineBytes)

	var text strings.Builder
	// Tool call fragments are keyed by the stream index, with order preserved
	// separately: the API guarantees an index per call, not a stable order.
	partials := make(map[int]*toolCall)
	var order []int

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return streamResult{}, err
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			// Any other SSE field (event, id, retry) carries no completion
			// data for this protocol.
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			// A single malformed chunk must not kill a run that is otherwise
			// streaming fine.
			continue
		}
		if chunk.Error != nil {
			return streamResult{}, errf(apperror.Unknown, "Agent endpoint error: %s",
				sanitize(chunk.Error.Message, apiKey))
		}
		for _, choice := range chunk.Choices {
			if delta := choice.Delta.Content; delta != "" {
				if text.Len()+len(delta) > maxAssistantTextBytes {
					return streamResult{}, errf(apperror.Unknown, "Agent response exceeded the size limit")
				}
				text.WriteString(delta)
				if onDelta != nil {
					onDelta(delta)
				}
			}
			for _, frag := range choice.Delta.ToolCalls {
				call, ok := partials[frag.Index]
				if !ok {
					if len(order) >= maxToolCallsPerTurn {
						return streamResult{}, errf(apperror.Unknown, "Agent requested too many tools at once")
					}
					call = &toolCall{Type: "function"}
					partials[frag.Index] = call
					order = append(order, frag.Index)
				}
				if frag.ID != "" {
					call.ID = frag.ID
				}
				if frag.Type != "" {
					call.Type = frag.Type
				}
				if frag.Function.Name != "" {
					call.Function.Name = frag.Function.Name
				}
				if args := frag.Function.Arguments; args != "" {
					if len(call.Function.Arguments)+len(args) > maxToolArgsBytes {
						return streamResult{}, errf(apperror.Unknown, "Agent tool arguments exceeded the size limit")
					}
					call.Function.Arguments += args
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return streamResult{}, ctxErr
		}
		if errors.Is(err, bufio.ErrTooLong) {
			return streamResult{}, errf(apperror.Unknown, "Agent response exceeded the size limit")
		}
		return streamResult{}, errf(apperror.Unknown, "Agent response stream failed")
	}

	calls := make([]toolCall, 0, len(order))
	for _, idx := range order {
		call := partials[idx]
		// A fragment set that never named a function cannot be executed and
		// would leave an unanswerable tool_call in the transcript.
		if call.Function.Name == "" {
			continue
		}
		if call.ID == "" {
			call.ID = "call_" + strings.TrimSpace(call.Function.Name)
		}
		calls = append(calls, *call)
	}
	return streamResult{text: text.String(), toolCalls: calls}, nil
}

// completionsURL appends the chat-completions path to the configured base
// URL. The base URL is expected to include the provider's version prefix
// (e.g. https://api.openai.com/v1), matching the OPENAI_BASE_URL convention.
func completionsURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/chat/completions"
}

// requestError maps a transport failure. A cancelled or expired context wins
// over the transport message, which would otherwise report the cancellation
// as an opaque network error.
func requestError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return errf(apperror.Timeout, "Agent request timed out")
		}
		return ctxErr
	}
	return errf(apperror.Unknown, "Agent endpoint is unreachable")
}

// statusError turns a non-2xx response into a coded error. The provider
// message is forwarded because it is what makes a wrong model or a rejected
// key diagnosable, but it is bounded and the configured key is redacted from
// it first.
func statusError(resp *http.Response, apiKey string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	message := providerMessage(raw)
	if message == "" {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return errf(apperror.Unknown, "Agent endpoint rejected the API key (HTTP %d)", resp.StatusCode)
		}
		return errf(apperror.Unknown, "Agent endpoint returned HTTP %d", resp.StatusCode)
	}
	return errf(apperror.Unknown, "Agent endpoint returned HTTP %d: %s", resp.StatusCode,
		sanitize(message, apiKey))
}

// providerMessage extracts the error text from the common OpenAI-compatible
// error envelopes, falling back to the raw body.
func providerMessage(raw []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &envelope) == nil {
		if envelope.Error.Message != "" {
			return envelope.Error.Message
		}
		if envelope.Message != "" {
			return envelope.Message
		}
	}
	return strings.TrimSpace(string(raw))
}

// sanitize bounds a provider message and removes the configured API key from
// it, so a server that echoes the credential cannot put it on screen.
func sanitize(message, apiKey string) string {
	out := strings.TrimSpace(message)
	if key := strings.TrimSpace(apiKey); key != "" {
		out = strings.ReplaceAll(out, key, "[redacted]")
	}
	out = strings.Join(strings.Fields(out), " ")
	const max = 300
	if len(out) > max {
		return out[:max] + "…"
	}
	return out
}
