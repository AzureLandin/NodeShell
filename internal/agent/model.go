package agent

import (
	"context"
	"net/http"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

// ModelFactory builds the chat model for one run. It is the seam the tests
// use: a factory returns a scripted model, so a test never reaches a
// provider. Production uses the OpenAI-compatible model built from Config.
type ModelFactory func(ctx context.Context, cfg Config) (model.ToolCallingChatModel, error)

// newOpenAIModel builds the OpenAI-compatible chat model for one endpoint
// configuration. The API key only ever reaches the request header: it is not
// part of the model name, the base URL or any message.
func newOpenAIModel(ctx context.Context, cfg Config, client *http.Client) (model.ToolCallingChatModel, error) {
	return openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL:         cfg.BaseURL,
		Model:           cfg.Model,
		APIKey:          cfg.APIKey,
		HTTPClient:      client,
		ReasoningEffort: reasoningEffort(cfg.ThinkingStrength),
	})
}

// reasoningEffort maps the configured thinking strength onto the provider's
// reasoning parameter. "auto" - and anything unknown - leaves the parameter
// unset, so the provider decides how much it thinks.
func reasoningEffort(strength string) openai.ReasoningEffortLevel {
	switch strings.ToLower(strings.TrimSpace(strength)) {
	case ThinkingLow:
		return openai.ReasoningEffortLevelLow
	case ThinkingMedium:
		return openai.ReasoningEffortLevelMedium
	case ThinkingHigh:
		return openai.ReasoningEffortLevelHigh
	default:
		return ""
	}
}
