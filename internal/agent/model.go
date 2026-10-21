package agent

import (
	"context"
	"net/http"

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
		BaseURL:    cfg.BaseURL,
		Model:      cfg.Model,
		APIKey:     cfg.APIKey,
		HTTPClient: client,
	})
}
