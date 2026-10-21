package agent

import (
	"context"
	"errors"
	"io"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"nodeshell/internal/apperror"
)

// einoAgentName identifies the agent inside the Eino runtime. It shows up in
// the run path of the framework's own events, never in the UI.
const einoAgentName = "nodeshell"

// defaultMaxIterations is Eino's own iteration budget. It applies when
// Deps.MaxTurns is 0, i.e. when the caller does not pin a turn limit.
const defaultMaxIterations = 20

// runEino drives one accepted prompt through the Eino agent and returns the
// messages the run added to the transcript, in the order they were produced.
//
// The NodeShell transcript stays the source of truth: it is replayed to the
// agent as the model input, and whatever the agent produced is handed back so
// the caller can append it.
//
// A provider that refuses the reasoning parameter is given a second chance
// without it: the run continues instead of failing over a parameter the
// model does not know.
func (s *Service) runEino(ctx context.Context, sessionID, title string, cfg Config, c *conversation) ([]chatMessage, error) {
	produced, err := s.runEinoAttempt(ctx, sessionID, title, cfg, c)
	if err == nil {
		return produced, nil
	}
	// Only a run that produced nothing is retried: nothing was executed and
	// the transcript is untouched, so the second attempt starts from the
	// same place.
	if len(produced) > 0 || !reasoningRejected(err) {
		return produced, mapRunError(err, cfg, s.maxIterations())
	}
	fallback := cfg
	fallback.ThinkingStrength = ThinkingAuto
	produced, retryErr := s.runEinoAttempt(ctx, sessionID, title, fallback, c)
	if retryErr != nil {
		return produced, mapRunError(retryErr, fallback, s.maxIterations())
	}
	return produced, nil
}

// runEinoAttempt runs the Eino agent once and returns the raw error, so the
// caller can tell a provider rejection apart from any other failure.
func (s *Service) runEinoAttempt(ctx context.Context, sessionID, title string, cfg Config, c *conversation) ([]chatMessage, error) {
	chatModel, err := s.model(ctx, cfg)
	if err != nil {
		return nil, err
	}
	instruction, history := modelInput(title, s.snapshot(c))

	einoAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        einoAgentName,
		Description: "The NodeShell in-app assistant for one SSH session.",
		Instruction: instruction,
		Model:       chatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: s.buildTools(sessionID, title, c),
				// The tools act on one remote host, so they run one after
				// another: parallel calls would interleave writes on the
				// same session.
				ExecuteSequentially: true,
				// A hallucinated tool is a failed call, not a failed run:
				// the model gets the refusal as text and can pick another
				// tool instead of losing the whole answer.
				UnknownToolsHandler: func(ctx context.Context, name, _ string) (string, error) {
					s.emitIfCurrent(sessionID, c, EventTool, ToolEvent{
						SessionID: sessionID,
						CallID:    compose.GetToolCallID(ctx),
						Name:      name,
						Summary:   name,
						OK:        false,
						Detail:    "error: unknown tool",
					})
					return "error: unknown tool", nil
				},
			},
		},
		MaxIterations: s.maxIterations(),
		Handlers: []adk.ChatModelAgentMiddleware{
			&callIDMiddleware{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}},
			&historyCapHandler{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}},
			&deltaHandler{
				BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
				svc:                          s,
				sessionID:                    sessionID,
				conv:                         c,
			},
		},
	})
	if err != nil {
		return nil, errf(apperror.Unknown, "Agent could not be started")
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           einoAgent,
		EnableStreaming: true,
	})
	iter := runner.Run(ctx, history)

	var produced []chatMessage
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return produced, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		msg, msgErr := consumeMessageOutput(event.Output.MessageOutput)
		if msg != nil {
			produced = append(produced, fromSchemaMessage(msg))
		}
		if msgErr != nil {
			return produced, msgErr
		}
	}
	return produced, nil
}

// modelInput splits the transcript into the instruction and the messages the
// model sees. A compacted transcript starts with its summary; that summary
// belongs to the system prompt, so the model reads one system message
// instead of two.
func modelInput(title string, history []chatMessage) (string, []*schema.Message) {
	instruction := systemPrompt(title)
	msgs := history
	if len(msgs) > 0 && msgs[0].Role == roleSystem {
		instruction = instruction + "\n\n" + msgs[0].Content
		msgs = msgs[1:]
	}
	return instruction, toSchemaMessages(msgs)
}

// buildTools binds the remote tools to the session the prompt named.
func (s *Service) buildTools(sessionID, title string, c *conversation) []tool.BaseTool {
	defs := toolDefs()
	out := make([]tool.BaseTool, 0, len(defs))
	for i := range defs {
		out = append(out, &agentTool{
			svc:       s,
			def:       &defs[i],
			sessionID: sessionID,
			title:     title,
			conv:      c,
		})
	}
	return out
}

// maxIterations is the iteration budget of one run: the pinned turn limit, or
// Eino's own default when the caller did not pin one.
func (s *Service) maxIterations() int {
	if s.maxTurns > 0 {
		return s.maxTurns
	}
	return defaultMaxIterations
}

// historyCapHandler keeps the replayed transcript within the history cap, so
// a long tool run cannot grow the request without bound. The cap is applied
// to what the model sees; the NodeShell transcript keeps its own copy.
type historyCapHandler struct {
	*adk.BaseChatModelAgentMiddleware
}

// BeforeModelRewriteState trims the messages before every model call.
func (h *historyCapHandler) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	state.Messages = trimStateMessages(state.Messages)
	return ctx, state, nil
}

// trimStateMessages caps the replayed transcript from the front. The system
// prompt stays: it is the agent's instruction, not part of the conversation
// the cap applies to.
func trimStateMessages(msgs []*schema.Message) []*schema.Message {
	head := 0
	if len(msgs) > 0 && msgs[0].Role == schema.System {
		head = 1
	}
	if len(msgs)-head <= MaxHistoryMessages {
		return msgs
	}
	tail := msgs[len(msgs)-MaxHistoryMessages:]
	// A leading tool result is an orphan: the call it answers was cut away,
	// and the endpoint rejects that.
	for len(tail) > 0 && tail[0].Role == schema.Tool {
		tail = tail[1:]
	}
	out := make([]*schema.Message, 0, head+len(tail))
	out = append(out, msgs[:head]...)
	out = append(out, tail...)
	return out
}

// consumeMessageOutput turns one agent event into the message it carries.
// A streamed assistant turn is drained frame by frame, so the transcript
// keeps the whole turn and nothing is left unread in the framework.
func consumeMessageOutput(mv *adk.MessageVariant) (*schema.Message, error) {
	if !mv.IsStreaming {
		return mv.Message, nil
	}
	stream := mv.MessageStream
	if stream == nil {
		return nil, nil
	}
	defer stream.Close()

	var chunks []*schema.Message
	for {
		frame, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return concatChunks(chunks), err
		}
		chunks = append(chunks, frame)
	}
	return concatChunks(chunks), nil
}

// concatChunks merges the frames of one streamed message. A frame that cannot
// be merged is dropped rather than failing the run: the text already reached
// the UI.
func concatChunks(chunks []*schema.Message) *schema.Message {
	switch len(chunks) {
	case 0:
		return nil
	case 1:
		return chunks[0]
	}
	msg, err := schema.ConcatMessages(chunks)
	if err != nil {
		return chunks[len(chunks)-1]
	}
	return msg
}
