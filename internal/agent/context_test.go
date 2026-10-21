package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

// The context layer keeps one conversation inside the configured window:
// it estimates the size of a request, compacts the older turns into a
// summary and never lets a failing compaction cost the user their prompt.

const (
	// testWindow is the context budget the compaction tests run with. It is
	// the smallest budget the settings store allows.
	testWindow = 4096
	// bigChunk is the size of one seeded transcript message.
	bigChunk = 1500
)

// --- the estimator ---

// The estimate is characters/4 plus a fixed reserve for the tool schemas.
func TestEstimateTokensCountsCharactersAndToolReserve(t *testing.T) {
	cases := []struct {
		chars int
		want  int
	}{
		{0, 0},
		{1, 1},
		{4, 1},
		{5, 2},
		{400, 100},
	}
	for _, tc := range cases {
		if got := estimateTokens(tc.chars); got != tc.want {
			t.Fatalf("estimateTokens(%d) = %d, want %d", tc.chars, got, tc.want)
		}
	}

	msgs := []chatMessage{{Role: roleUser, Content: strings.Repeat("a", 400)}}
	instruction := "system prompt"
	got := estimateContextTokens(instruction, msgs)
	want := estimateTokens(len(instruction)+400) + toolSchemaReserveTokens
	if got != want {
		t.Fatalf("estimateContextTokens = %d, want %d", got, want)
	}
	if want != 1104 {
		t.Fatalf("the estimate changed: %d tokens for 413 characters plus the reserve", want)
	}
}

// The tool schemas are counted for every request, and the tool calls of the
// transcript count as well.
func TestEstimateCountsTheToolCallsOfTheTranscript(t *testing.T) {
	msgs := []chatMessage{{
		Role: roleAssistant,
		ToolCalls: []toolCall{{
			ID:       "c1",
			Type:     "function",
			Function: functionCall{Name: toolBash, Arguments: `{"command":"ls"}`},
		}},
	}}
	plain := estimateContextTokens("", msgs)
	if plain != toolSchemaReserveTokens+estimateTokens(len(toolBash)+len(`{"command":"ls"}`)) {
		t.Fatalf("estimateContextTokens = %d, want the reserve plus the call", plain)
	}
	msgs[0].ToolCalls[0].Function.Arguments = strings.Repeat("x", 400)
	if got := estimateContextTokens("", msgs); got <= plain {
		t.Fatalf("estimateContextTokens = %d, want more than %d", got, plain)
	}
}

// The compaction starts when the estimate passes 80% of the window.
func TestOverBudgetUsesTheCompressionThreshold(t *testing.T) {
	instruction := systemPrompt("host")
	base := estimateContextTokens(instruction, nil)
	threshold := float64(testWindow) * compressionThreshold
	room := int(threshold) - base
	if room <= 0 {
		t.Fatalf("the test window is too small: %d tokens of room", room)
	}
	under := []chatMessage{{Role: roleUser, Content: strings.Repeat("a", room*charsPerToken)}}
	if overBudget(instruction, under, testWindow) {
		t.Fatal("a transcript at the threshold must not trigger the compaction")
	}
	over := []chatMessage{{Role: roleUser, Content: strings.Repeat("a", room*charsPerToken+1)}}
	if !overBudget(instruction, over, testWindow) {
		t.Fatal("a transcript above the threshold must trigger the compaction")
	}
	// Without a window there is no budget at all.
	if overBudget(instruction, over, 0) {
		t.Fatal("a window of 0 must never trigger the compaction")
	}
}

// --- the split ---

// The compaction never splits a tool round: the kept tail starts at the
// assistant message that called the tools, and every result in it has its
// call next to it.
func TestSplitForCompressionKeepsCompleteToolRounds(t *testing.T) {
	msgs := []chatMessage{
		{Role: roleUser, Content: "first"},
		{Role: roleAssistant, ToolCalls: []toolCall{{
			ID: "c1", Type: "function",
			Function: functionCall{Name: toolBash, Arguments: `{"command":"ls"}`},
		}}},
		{Role: roleTool, ToolCallID: "c1", Content: "out"},
		{Role: roleUser, Content: "second"},
		{Role: roleAssistant, ToolCalls: []toolCall{{
			ID: "c2", Type: "function",
			Function: functionCall{Name: toolBash, Arguments: `{"command":"df"}`},
		}}},
		{Role: roleTool, ToolCallID: "c2", Content: "out"},
		{Role: roleUser, Content: "third"},
	}
	prefix, keep := splitForCompression(msgs, 2, 2)

	if len(prefix) == 0 {
		t.Fatal("the older turn must be compacted")
	}
	if keep[0].Role != roleAssistant {
		t.Fatalf("the kept tail starts with a %q, want the assistant of a tool round", keep[0].Role)
	}
	calls := map[string]bool{}
	for _, m := range keep {
		for _, call := range m.ToolCalls {
			calls[call.ID] = true
		}
	}
	for _, m := range keep {
		if m.Role != roleTool {
			continue
		}
		if !calls[m.ToolCallID] {
			t.Fatalf("the kept tail orphaned the result of %q", m.ToolCallID)
		}
	}
	// The most recent complete rounds are kept verbatim.
	if !hasCall(keep, "c1") || !hasCall(keep, "c2") {
		t.Fatalf("the kept tail lost a tool round: %+v", keep)
	}
}

// hasCall reports whether the messages carry the call with that id.
func hasCall(msgs []chatMessage, id string) bool {
	for _, m := range msgs {
		for _, call := range m.ToolCalls {
			if call.ID == id {
				return true
			}
		}
	}
	return false
}

// --- the service ---

// seededHistory is a transcript that already fills the context window: it
// holds two complete tool rounds and enough text to trip the threshold.
func seededHistory() []chatMessage {
	chunk := strings.Repeat("x", bigChunk)
	call := func(id string) []toolCall {
		return []toolCall{{
			ID: id, Type: "function",
			Function: functionCall{Name: toolBash, Arguments: `{"command":"ls"}`},
		}}
	}
	return []chatMessage{
		{Role: roleUser, Content: chunk},
		{Role: roleAssistant, Content: chunk},
		{Role: roleAssistant, ToolCalls: call("c1")},
		{Role: roleTool, ToolCallID: "c1", Content: chunk},
		{Role: roleUser, Content: chunk},
		{Role: roleAssistant, ToolCalls: call("c2")},
		{Role: roleTool, ToolCallID: "c2", Content: chunk},
		{Role: roleUser, Content: chunk},
		{Role: roleAssistant, Content: chunk},
	}
}

// A transcript that outgrows the window is compacted, and the summary
// replaces the older turns in the transcript the next prompt replays.
func TestCompressionRewritesTheTranscriptWithTheSummary(t *testing.T) {
	h := newHarness(t, []fakeTurn{{chunks: []string{"ok"}}}, nil)
	h.model.setSummaries("- the user asked about the disk")
	h.seedHistory("s1", seededHistory())

	cfg := h.cfg()
	cfg.ContextWindow = testWindow
	cfg.ContextCompression = CompressionAuto
	if err := h.svc.Prompt("s1", "host", "and now?", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	history := h.history("s1")
	if len(history) == 0 {
		t.Fatal("the transcript is empty")
	}
	if history[0].Role != roleSystem {
		t.Fatalf("the transcript starts with a %q, want the summary", history[0].Role)
	}
	if !strings.HasPrefix(history[0].Content, summaryPrefix) {
		t.Fatalf("the summary is not marked: %q", history[0].Content)
	}
	if !strings.Contains(history[0].Content, "the user asked about the disk") {
		t.Fatalf("the summary is not the one the provider wrote: %q", history[0].Content)
	}
	// The compacted turn is gone from the transcript: the kept tail starts
	// at the assistant turn that called the first tool.
	if len(history) < 2 {
		t.Fatalf("the transcript is too short: %+v", history)
	}
	if history[1].Role != roleAssistant || !hasCall(history[1:], "c1") {
		t.Fatalf("the compacted turn survived: %+v", history[1])
	}
	// The provider was asked for the summary of the older turns only.
	calls := h.model.summaryCalls()
	if len(calls) != 1 {
		t.Fatalf("summary calls = %d, want one", len(calls))
	}
	// The model read the summary: it is part of the system prompt.
	last := h.model.request(h.model.callCount() - 1)
	if len(last) == 0 || last[0].Role != schema.System {
		t.Fatalf("the request has no system prompt: %+v", last)
	}
	if !strings.Contains(last[0].Content, "the user asked about the disk") {
		t.Fatalf("the model did not see the summary: %q", last[0].Content)
	}
	// The compacted transcript fits the window again.
	if overBudget(systemPrompt("host"), history, testWindow) {
		t.Fatal("the compacted transcript still exceeds the window")
	}
}

// A provider that cannot summarise costs the user nothing: the run still
// happens, with a deterministic summary in place of the model's one.
func TestSummaryFailureFallsBackToTruncation(t *testing.T) {
	h := newHarness(t, []fakeTurn{{chunks: []string{"ok"}}}, nil)
	h.model.failSummary(errors.New("provider is down"))
	h.seedHistory("s1", seededHistory())

	cfg := h.cfg()
	cfg.ContextWindow = testWindow
	cfg.ContextCompression = CompressionAuto
	if err := h.svc.Prompt("s1", "host", "and now?", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	if errs := h.sink.errors(); len(errs) != 0 {
		t.Fatalf("a failed summary must not fail the prompt: %+v", errs)
	}
	if got := h.sink.text(); got != "ok" {
		t.Fatalf("streamed text = %q, want the answer of the run", got)
	}
	history := h.history("s1")
	if len(history) == 0 || history[0].Role != roleSystem {
		t.Fatalf("the transcript does not start with a summary: %+v", history)
	}
	if !strings.HasPrefix(history[0].Content, summaryPrefix) {
		t.Fatalf("the fallback summary is not marked: %q", history[0].Content)
	}
	if !strings.Contains(history[0].Content, "user asked") {
		t.Fatalf("the fallback summary keeps nothing: %q", history[0].Content)
	}
}

// A prompt that cannot fit the window is rejected before the run starts.
func TestOversizedPromptIsRejectedBeforeTheRun(t *testing.T) {
	h := newHarness(t, []fakeTurn{{chunks: []string{"ok"}}}, nil)

	cfg := h.cfg()
	cfg.ContextWindow = testWindow
	cfg.ContextCompression = CompressionAuto
	err := h.svc.Prompt("s1", "host", strings.Repeat("x", 20000), cfg)
	if err == nil {
		t.Fatal("a prompt that outgrows the window must be rejected")
	}
	if !strings.Contains(err.Error(), "context window") {
		t.Fatalf("error = %v, want the context window message", err)
	}
	if h.sink.count(EventDone) != 0 {
		t.Fatal("a rejected prompt must not start a run")
	}
	if got := h.history("s1"); len(got) != 0 {
		t.Fatalf("a rejected prompt must not reach the transcript: %+v", got)
	}
}

// Without a context window nothing is compacted: the provider's own limit
// decides, as before.
func TestContextWindowZeroKeepsTheLegacyBehaviour(t *testing.T) {
	h := newHarness(t, []fakeTurn{{chunks: []string{"ok"}}}, nil)
	seeded := seededHistory()
	h.seedHistory("s1", seeded)

	cfg := h.cfg()
	if err := h.svc.Prompt("s1", "host", "and now?", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	history := h.history("s1")
	if len(history) != len(seeded)+2 {
		t.Fatalf("the transcript holds %d messages, want %d", len(history), len(seeded)+2)
	}
	for _, m := range history {
		if m.Role == roleSystem {
			t.Fatal("a transcript without a window must not be compacted")
		}
	}
	if calls := h.model.summaryCalls(); len(calls) != 0 {
		t.Fatalf("summary calls = %d, want none", len(calls))
	}
}

// The compaction is off unless it was asked for: the plain history cap stays
// the only limit.
func TestCompressionOffKeepsTheLegacyBehaviour(t *testing.T) {
	h := newHarness(t, []fakeTurn{{chunks: []string{"ok"}}}, nil)
	seeded := seededHistory()
	h.seedHistory("s1", seeded)

	cfg := h.cfg()
	cfg.ContextWindow = testWindow
	cfg.ContextCompression = CompressionOff
	if err := h.svc.Prompt("s1", "host", "and now?", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	history := h.history("s1")
	if len(history) != len(seeded)+2 {
		t.Fatalf("the transcript holds %d messages, want %d", len(history), len(seeded)+2)
	}
	for _, m := range history {
		if m.Role == roleSystem {
			t.Fatal("a transcript with the compaction off must not be compacted")
		}
	}
	if calls := h.model.summaryCalls(); len(calls) != 0 {
		t.Fatalf("summary calls = %d, want none", len(calls))
	}
}

// The summary never eats more than its share of the window.
func TestSummaryIsCappedToItsShareOfTheWindow(t *testing.T) {
	h := newHarness(t, []fakeTurn{{chunks: []string{"ok"}}}, nil)
	h.model.setSummaries(strings.Repeat("s", 100000))
	h.seedHistory("s1", seededHistory())

	cfg := h.cfg()
	cfg.ContextWindow = testWindow
	cfg.ContextCompression = CompressionAuto
	if err := h.svc.Prompt("s1", "host", "and now?", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	history := h.history("s1")
	if len(history) == 0 || history[0].Role != roleSystem {
		t.Fatalf("the transcript does not start with a summary: %+v", history)
	}
	limit := summaryBudget(testWindow) * charsPerToken
	if len(history[0].Content) > limit+len(summaryPrefix)+2 {
		t.Fatalf("the summary holds %d characters, want at most %d", len(history[0].Content), limit)
	}
}

// A transcript that stays over the budget after the compaction still runs:
// the older tool results are dropped and the plain history cap applies.
func TestOverBudgetTranscriptDropsTheOlderToolResults(t *testing.T) {
	h := newHarness(t, []fakeTurn{{chunks: []string{"ok"}}}, nil)
	h.model.setSummaries("- first summary", "- second summary")

	chunk := strings.Repeat("x", 6000)
	call := func(id string) []toolCall {
		return []toolCall{{
			ID: id, Type: "function",
			Function: functionCall{Name: toolBash, Arguments: `{"command":"ls"}`},
		}}
	}
	var seeded []chatMessage
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("c%d", i)
		seeded = append(seeded,
			chatMessage{Role: roleAssistant, ToolCalls: call(id)},
			chatMessage{Role: roleTool, ToolCallID: id, Content: chunk},
		)
	}
	seeded = append([]chatMessage{{Role: roleUser, Content: chunk}}, seeded...)
	h.seedHistory("s1", seeded)

	cfg := h.cfg()
	cfg.ContextWindow = testWindow
	cfg.ContextCompression = CompressionAuto
	if err := h.svc.Prompt("s1", "host", "and now?", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	if errs := h.sink.errors(); len(errs) != 0 {
		t.Fatalf("the run must survive an oversized transcript: %+v", errs)
	}
	history := h.history("s1")
	if len(history) == 0 {
		t.Fatal("the transcript is empty")
	}
	if history[0].Role != roleSystem {
		t.Fatalf("the transcript starts with a %q, want the summary", history[0].Role)
	}
	// The older tool results are gone: at most one round survived.
	kept := 0
	for _, m := range history {
		if m.Role == roleTool {
			kept++
		}
	}
	if kept > 2 {
		t.Fatalf("the transcript kept %d tool results, want at most one round", kept)
	}
	if overBudget(systemPrompt("host"), history, testWindow) {
		t.Fatal("the compacted transcript still exceeds the window")
	}
}

// The compaction needs both a budget and the compression turned on.
func TestCompressionNeedsAWindowAndTheMode(t *testing.T) {
	cases := []struct {
		window      int
		compression string
		want        bool
	}{
		{0, CompressionAuto, false},
		{0, CompressionOff, false},
		{testWindow, CompressionOff, false},
		{testWindow, "", false},
		{testWindow, "sometimes", false},
		{testWindow, CompressionAuto, true},
	}
	for _, tc := range cases {
		cfg := Config{ContextWindow: tc.window, ContextCompression: tc.compression}
		if got := compressionEnabled(cfg); got != tc.want {
			t.Fatalf("compressionEnabled(window=%d, mode=%q) = %t, want %t",
				tc.window, tc.compression, got, tc.want)
		}
	}
}

// The summary is written with the cheapest reasoning effort the provider
// was asked for.
func TestSummaryUsesTheCheapestReasoningEffort(t *testing.T) {
	if got := summaryThinking(ThinkingHigh); got != ThinkingLow {
		t.Fatalf("summaryThinking(high) = %q, want %q", got, ThinkingLow)
	}
	if got := summaryThinking(ThinkingMedium); got != ThinkingLow {
		t.Fatalf("summaryThinking(medium) = %q, want %q", got, ThinkingLow)
	}
	if got := summaryThinking(ThinkingAuto); got != ThinkingAuto {
		t.Fatalf("summaryThinking(auto) = %q, want %q", got, ThinkingAuto)
	}
	if got := summaryThinking("nonsense"); got != ThinkingAuto {
		t.Fatalf("summaryThinking(nonsense) = %q, want %q", got, ThinkingAuto)
	}
}

// The summary is asked of the same provider and model as the run, with the
// cheapest reasoning effort.
func TestSummaryRunsOnTheSameProviderWithALowEffort(t *testing.T) {
	h := newHarness(t, []fakeTurn{{chunks: []string{"ok"}}}, nil)
	h.model.setSummaries("- the user asked about the disk")
	h.seedHistory("s1", seededHistory())

	cfg := h.cfg()
	cfg.ContextWindow = testWindow
	cfg.ContextCompression = CompressionAuto
	cfg.ThinkingStrength = ThinkingHigh
	if err := h.svc.Prompt("s1", "host", "and now?", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	h.waitDone(t, 1)

	seen := h.seenConfigs()
	if len(seen) != 2 {
		t.Fatalf("model factory calls = %d, want the run and the summary", len(seen))
	}
	efforts := map[string]int{}
	for _, cfg := range seen {
		efforts[cfg.ThinkingStrength]++
	}
	if efforts[ThinkingLow] != 1 {
		t.Fatalf("the summary did not ask for the lowest effort: %+v", seen)
	}
	if efforts[ThinkingHigh] != 1 {
		t.Fatalf("the run did not ask for the configured effort: %+v", seen)
	}
	if seen[0].Model != seen[1].Model || seen[0].BaseURL != seen[1].BaseURL {
		t.Fatalf("the summary left the provider: %+v", seen)
	}
}
