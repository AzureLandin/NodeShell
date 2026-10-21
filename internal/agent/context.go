package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// Context budgeting.
//
// The numbers here are deliberately coarse. They do not predict the
// provider's tokenizer: they only decide when the transcript has grown so
// large that the next request would not fit, and how much of it may be kept
// verbatim afterwards.
//
// The estimate is characters/4, plus a fixed reserve for the tool schemas
// the agent advertises to the model. A window of 0 means "unlimited": the
// provider's own model limit decides and NodeShell never compacts anything.

// Thinking strengths and compression modes. They mirror the values the
// settings store persists.
const (
	// ThinkingAuto leaves the reasoning effort to the provider.
	ThinkingAuto = "auto"
	// ThinkingLow, ThinkingMedium and ThinkingHigh ask for a reasoning
	// effort explicitly.
	ThinkingLow    = "low"
	ThinkingMedium = "medium"
	ThinkingHigh   = "high"

	// CompressionOff disables the automatic compaction.
	CompressionOff = "off"
	// CompressionAuto lets the runtime compact its own transcript.
	CompressionAuto = "auto"
)

const (
	// charsPerToken is the rough characters-per-token ratio of the
	// estimator: four characters are about one token.
	charsPerToken = 4
	// toolSchemaReserveTokens is the room kept for the tool schemas the
	// agent advertises to the model on every request.
	toolSchemaReserveTokens = 1000
	// compressionThreshold is the share of the context window at which the
	// transcript is compacted.
	compressionThreshold = 0.8
	// summaryBudgetFraction is the share of the context window one summary
	// may occupy.
	summaryBudgetFraction = 0.2
	// keepRecentMessages is the number of recent messages kept verbatim
	// when the transcript is compacted.
	keepRecentMessages = 8
	// keepToolRounds is the number of recent complete tool rounds kept
	// verbatim when the transcript is compacted.
	keepToolRounds = 2
	// summaryTimeout bounds one summarisation request. A summary that runs
	// out of time is not fatal: the caller falls back to truncation.
	summaryTimeout = 60 * time.Second
	// summaryPrefix marks a compacted prefix inside the transcript.
	summaryPrefix = "[Conversation summary]"
)

// summaryInstruction asks the model for a summary that keeps the facts a
// later turn may still need.
const summaryInstruction = "You are compacting an SSH assistant conversation so the older turns can be replaced by a short summary. Keep, in this order: the purpose of the session and the host it works on, the important commands that were run and what they showed, the files that were read or written, the decisions that were made, the tasks that are still pending, the errors and limits that were hit, and the preferences the user stated. Answer with the summary only, as terse bullet points."

// estimateTokens estimates the tokens of a text. The ratio is rough on
// purpose: the estimate only decides when to compact.
func estimateTokens(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + charsPerToken - 1) / charsPerToken
}

// estimateContextTokens estimates the tokens one request would take: the
// instruction, the transcript and the tool schemas.
func estimateContextTokens(instruction string, msgs []chatMessage) int {
	chars := len(instruction)
	for _, m := range msgs {
		chars += len(m.Content)
		chars += len(m.ReasoningContent)
		for _, call := range m.ToolCalls {
			chars += len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	return estimateTokens(chars) + toolSchemaReserveTokens
}

// contextLimit is the number of tokens a conversation may use before the
// runtime compacts it. A window of 0 disables the budget entirely.
func contextLimit(window int) int {
	if window <= 0 {
		return 0
	}
	return window
}

// compressionEnabled reports whether the runtime may compact the
// transcript. It needs both a budget and the compression turned on: without
// one of them the plain history cap stays the only limit.
func compressionEnabled(cfg Config) bool {
	if contextLimit(cfg.ContextWindow) == 0 {
		return false
	}
	switch cfg.ContextCompression {
	case CompressionAuto:
		return true
	case CompressionOff:
		return false
	default:
		return false
	}
}

// overBudget reports whether the estimated context exceeds the compression
// threshold of the window.
func overBudget(instruction string, msgs []chatMessage, window int) bool {
	limit := contextLimit(window)
	if limit == 0 {
		return false
	}
	return float64(estimateContextTokens(instruction, msgs)) > float64(limit)*compressionThreshold
}

// summaryBudget is the number of tokens one summary may occupy.
func summaryBudget(window int) int {
	limit := contextLimit(window)
	if limit == 0 {
		return 0
	}
	return int(float64(limit) * summaryBudgetFraction)
}

// toolRound is one assistant turn that called tools, together with the
// results that answer it.
type toolRound struct {
	// start is the index of the assistant message.
	start int
	// end is the index after the last result of the round.
	end int
}

// toolRounds finds the complete tool rounds of a transcript. A round is
// complete when every call it made was answered: a half-answered round is
// reconciled after the run, not here.
func toolRounds(msgs []chatMessage) []toolRound {
	var rounds []toolRound
	for i := 0; i < len(msgs); i++ {
		if msgs[i].Role != roleAssistant || len(msgs[i].ToolCalls) == 0 {
			continue
		}
		j := i + 1
		for j < len(msgs) && msgs[j].Role == roleTool {
			j++
		}
		if j-i-1 != len(msgs[i].ToolCalls) {
			continue
		}
		rounds = append(rounds, toolRound{start: i, end: j})
		i = j - 1
	}
	return rounds
}

// splitForCompression splits a transcript into the older prefix to compact
// and the tail to keep verbatim.
//
// The tail always holds the current user message, the most recent complete
// tool rounds and the last few messages. It never starts in the middle of a
// tool round: a tool result whose call was cut away is rejected by the
// provider.
func splitForCompression(msgs []chatMessage, keepMessages, keepRounds int) (prefix, keep []chatMessage) {
	if len(msgs) == 0 {
		return nil, nil
	}
	from := len(msgs) - keepMessages
	if from < 0 {
		from = 0
	}
	// A tail that starts with a tool result orphans it: move the cut back
	// to the assistant message that called it.
	for from > 0 && msgs[from].Role == roleTool {
		from--
	}
	// The most recent complete tool rounds stay verbatim.
	rounds := toolRounds(msgs)
	for i := len(rounds) - 1; i >= 0 && i >= len(rounds)-keepRounds; i-- {
		if rounds[i].start < from {
			from = rounds[i].start
		}
	}
	return msgs[:from], msgs[from:]
}

// renderTranscript renders messages as plain text, so the summariser sees
// what happened and not just the conclusions.
func renderTranscript(msgs []chatMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case roleSystem:
			b.WriteString("system: " + m.Content + "\n")
		case roleUser:
			b.WriteString("user: " + m.Content + "\n")
		case roleAssistant:
			if m.Content != "" {
				b.WriteString("assistant: " + m.Content + "\n")
			}
			for _, call := range m.ToolCalls {
				b.WriteString("assistant called " + call.Function.Name + " " + call.Function.Arguments + "\n")
			}
		case roleTool:
			b.WriteString("tool result: " + m.Content + "\n")
		}
	}
	return b.String()
}

// summarize asks the provider for a summary of the compacted prefix. It
// returns an empty string when the provider cannot answer: the caller then
// falls back to a deterministic truncation and the prompt still runs.
//
// The summary is produced by the same provider and model as the run, with no
// tools attached, and with the cheapest reasoning effort the configuration
// allows.
func (s *Service) summarize(ctx context.Context, cfg Config, prefix []chatMessage, window int) string {
	if len(prefix) == 0 {
		return ""
	}
	summaryCfg := cfg
	summaryCfg.ThinkingStrength = summaryThinking(cfg.ThinkingStrength)
	chatModel, err := s.model(ctx, summaryCfg)
	if err != nil {
		return ""
	}
	limit := summaryBudget(window)
	ctx, cancel := context.WithTimeout(ctx, s.summaryDeadline())
	defer cancel()

	out, err := chatModel.Generate(ctx, []*schema.Message{
		schema.SystemMessage(summaryInstruction),
		schema.UserMessage(renderTranscript(prefix)),
	})
	if err != nil && reasoningRejected(err) && summaryCfg.ThinkingStrength != ThinkingAuto {
		// The provider does not know the reasoning parameter: ask again
		// without it.
		summaryCfg.ThinkingStrength = ThinkingAuto
		if retry, retryErr := s.model(ctx, summaryCfg); retryErr == nil {
			out, err = retry.Generate(ctx, []*schema.Message{
				schema.SystemMessage(summaryInstruction),
				schema.UserMessage(renderTranscript(prefix)),
			})
		}
	}
	if err != nil || out == nil {
		return ""
	}
	text := strings.TrimSpace(out.Content)
	if text == "" {
		return ""
	}
	if maxChars := limit * charsPerToken; maxChars > 0 && len(text) > maxChars {
		text = strings.TrimSpace(text[:maxChars]) + "…"
	}
	return summaryPrefix + "\n" + text
}

// summaryThinking is the reasoning effort used for a summary: the cheapest
// explicit level when the provider was asked for one, and the provider's
// own choice otherwise.
func summaryThinking(strength string) string {
	switch strings.ToLower(strings.TrimSpace(strength)) {
	case ThinkingLow, ThinkingMedium, ThinkingHigh:
		return ThinkingLow
	default:
		return ThinkingAuto
	}
}

// summaryDeadline bounds one summarisation request. It never exceeds the
// request timeout of the run.
func (s *Service) summaryDeadline() time.Duration {
	if s.requestTimeout > 0 && s.requestTimeout < summaryTimeout {
		return s.requestTimeout
	}
	return summaryTimeout
}

// fallbackSummary builds a summary without the provider: the older turns are
// listed, newest first, until the budget is used up. It is what keeps a
// prompt alive when the provider cannot be reached.
func fallbackSummary(prefix []chatMessage, window int) string {
	maxChars := summaryBudget(window) * charsPerToken
	var b strings.Builder
	for i := len(prefix) - 1; i >= 0; i-- {
		m := prefix[i]
		var line string
		switch m.Role {
		case roleUser:
			line = "user asked: " + m.Content
		case roleAssistant:
			line = "assistant said: " + m.Content
			for _, call := range m.ToolCalls {
				line += "\nassistant called " + call.Function.Name + " " + call.Function.Arguments
			}
		case roleTool:
			line = "tool result: " + m.Content
		case roleSystem:
			line = "earlier summary: " + m.Content
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if maxChars > 0 && b.Len()+len(line)+1 > maxChars {
			break
		}
		b.WriteString("- " + line + "\n")
	}
	body := strings.TrimSpace(b.String())
	if body == "" {
		body = "- the earlier turns of this conversation were compacted"
	}
	return summaryPrefix + "\n" + body
}

// prepareContext compacts the transcript when the configured context window
// is nearly exhausted. The compacted transcript replaces the old one, so the
// next prompt sees the summary instead of the raw turns.
//
// Nothing happens without a context window or with the compression turned
// off: that is the legacy behaviour, where only the plain history cap
// applies.
func (s *Service) prepareContext(ctx context.Context, sessionID, title string, cfg Config, c *conversation) {
	if !compressionEnabled(cfg) {
		return
	}
	instruction := systemPrompt(title)
	history := s.snapshot(c)
	if !overBudget(instruction, history, cfg.ContextWindow) {
		return
	}
	compressed := s.compress(ctx, cfg, instruction, history)
	if len(compressed) == 0 {
		return
	}
	s.mu.Lock()
	// A conversation that was cleared or disposed meanwhile keeps its own
	// (empty) transcript.
	if s.convs[sessionID] == c {
		c.messages = compressed
	}
	s.mu.Unlock()
}

// compress compacts a transcript until it fits the budget: the older prefix
// becomes one summary, and when that is not enough the remainder is
// compacted again, then the older tool results are dropped, and finally the
// plain history cap applies.
func (s *Service) compress(ctx context.Context, cfg Config, instruction string, history []chatMessage) []chatMessage {
	window := cfg.ContextWindow

	prefix, keep := splitForCompression(history, keepRecentMessages, keepToolRounds)
	if len(prefix) == 0 {
		// Nothing older to compact: only the cap can help.
		return trimHistory(history)
	}
	compressed := append([]chatMessage{{Role: roleSystem, Content: s.summaryOf(ctx, cfg, prefix, window)}}, keep...)
	if !overBudget(instruction, compressed, window) {
		return compressed
	}

	// Still over budget: compact the remainder again, harder.
	prefix, keep = splitForCompression(compressed, keepRecentMessages/2, 1)
	if len(prefix) > 0 {
		compressed = append([]chatMessage{{Role: roleSystem, Content: s.summaryOf(ctx, cfg, prefix, window)}}, keep...)
		if !overBudget(instruction, compressed, window) {
			return compressed
		}
	}

	// Still over budget: the older tool results are the bulkiest part of a
	// tool-heavy transcript.
	compressed = dropOldToolResults(compressed, 1)
	if !overBudget(instruction, compressed, window) {
		return compressed
	}
	return trimHistory(compressed)
}

// summaryOf summarises a prefix, falling back to a deterministic truncation
// when the provider cannot answer.
func (s *Service) summaryOf(ctx context.Context, cfg Config, prefix []chatMessage, window int) string {
	if summary := s.summarize(ctx, cfg, prefix, window); summary != "" {
		return summary
	}
	return fallbackSummary(prefix, window)
}

// dropOldToolResults removes the results of the older tool rounds. The
// assistant message that called them goes with them: a tool call without its
// result is rejected by the provider, so the call is replaced by a short
// note instead.
func dropOldToolResults(msgs []chatMessage, keepRounds int) []chatMessage {
	rounds := toolRounds(msgs)
	if len(rounds) <= keepRounds {
		return msgs
	}
	out := msgs
	// The rounds are dropped from the newest to the oldest, so the indices
	// of the rounds that are still to be dropped stay valid.
	for i := len(rounds) - keepRounds - 1; i >= 0; i-- {
		out = dropToolRound(out, rounds[i])
	}
	return out
}

// dropToolRound replaces one tool round with a short assistant note.
func dropToolRound(msgs []chatMessage, r toolRound) []chatMessage {
	if r.start < 0 || r.end > len(msgs) || r.start >= r.end {
		return msgs
	}
	names := make([]string, 0, len(msgs[r.start].ToolCalls))
	for _, call := range msgs[r.start].ToolCalls {
		names = append(names, call.Function.Name)
	}
	note := chatMessage{
		Role: roleAssistant,
		Content: fmt.Sprintf("(earlier tool call: %s; its output was dropped to fit the context window)",
			strings.Join(names, ", ")),
	}
	out := make([]chatMessage, 0, len(msgs)-(r.end-r.start)+1)
	out = append(out, msgs[:r.start]...)
	out = append(out, note)
	out = append(out, msgs[r.end:]...)
	return out
}
