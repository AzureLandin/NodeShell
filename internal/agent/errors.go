package agent

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"

	"nodeshell/internal/apperror"
)

// mapRunError turns a failure of the Eino run into a coded error the UI can
// show. The provider message is kept because it is what makes a wrong model
// or a rejected key diagnosable, but it is bounded and the configured API key
// is redacted from it first.
func mapRunError(err error, cfg Config, maxIterations int) error {
	if err == nil {
		return nil
	}
	// A cancelled context is the user pressing stop, not a failure.
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errf(apperror.Timeout, "Agent request timed out")
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return providerError(apiErr.HTTPStatusCode, apiErr.Message, cfg.APIKey)
	}
	// Eino stops the loop when the iteration budget is exhausted.
	if errors.Is(err, adk.ErrExceedMaxIterations) ||
		strings.Contains(err.Error(), adk.ErrExceedMaxIterations.Error()) {
		return errf(apperror.Unknown, "Agent stopped after %d steps without finishing", maxIterations)
	}
	// A transport failure that ran out of time is a timeout, not an outage.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errf(apperror.Timeout, "Agent request timed out")
	}
	// A transport failure says nothing about the model or the key, and the
	// endpoint address is not worth putting on screen.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return errf(apperror.Unknown, "Agent endpoint is unreachable")
	}
	return errf(apperror.Unknown, "Agent request failed: %s", sanitize(err.Error(), cfg.APIKey))
}

// providerError renders a provider rejection. The status code is kept so a
// wrong key (401/403) stays distinguishable from a bad request.
func providerError(status int, message, apiKey string) error {
	text := sanitize(message, apiKey)
	if text == "" {
		if status == 401 || status == 403 {
			return errf(apperror.Unknown, "Agent endpoint rejected the API key (HTTP %d)", status)
		}
		return errf(apperror.Unknown, "Agent endpoint returned HTTP %d", status)
	}
	return errf(apperror.Unknown, "Agent endpoint returned HTTP %d: %s", status, text)
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
