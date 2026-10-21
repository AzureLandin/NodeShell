package agent

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// deltaHandler forwards the streamed assistant text to the panel as the model
// produces it.
//
// The text is taken from the model stream rather than from the agent event on
// purpose. The event carries a copy of the same stream, and the agent runs
// ahead of the event consumer: the tools of a turn start as soon as the
// agent has the model's answer. Deltas taken from the event could therefore
// reach the panel after the tool card of the very same turn, and the panel
// closes the assistant entry when a tool card arrives. Taking the text from
// the model stream keeps the order the user reads: the answer, then the tool.
type deltaHandler struct {
	*adk.BaseChatModelAgentMiddleware
	svc       *Service
	sessionID string
	conv      *conversation
}

// WrapModel wraps the model so its output is reported while it streams.
func (h *deltaHandler) WrapModel(_ context.Context, m model.BaseModel[*schema.Message], _ *adk.ModelContext) (model.BaseModel[*schema.Message], error) {
	return &deltaModel{
		inner:     m,
		svc:       h.svc,
		sessionID: h.sessionID,
		conv:      h.conv,
	}, nil
}

// deltaModel is the model the agent talks to: it reports the text of every
// frame it hands out.
type deltaModel struct {
	inner     model.BaseModel[*schema.Message]
	svc       *Service
	sessionID string
	conv      *conversation
}

// Generate returns the whole answer at once; the panel is fed by the
// streaming path.
func (m *deltaModel) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.inner.Generate(ctx, in, opts...)
}

// Stream reports every text frame as a delta before the frame reaches the
// agent. The reasoning text is kept in the transcript but never rendered:
// the panel shows the answer, not the model's thinking.
func (m *deltaModel) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	stream, err := m.inner.Stream(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderWithConvert(stream, func(msg *schema.Message) (*schema.Message, error) {
		if msg != nil && msg.Content != "" {
			m.svc.emitIfCurrent(m.sessionID, m.conv, EventDelta, DeltaEvent{
				SessionID: m.sessionID,
				Delta:     msg.Content,
			})
		}
		return msg, nil
	}), nil
}
