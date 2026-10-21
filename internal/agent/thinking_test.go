package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino-ext/components/model/openai"
)

// The thinking strength is the user's knob on the provider's reasoning
// parameter: it is sent when it is asked for, left out when the provider
// should decide, and a provider that refuses it does not cost the user the
// run.

func TestReasoningEffortMapsTheThinkingStrength(t *testing.T) {
	cases := []struct {
		strength string
		want     openai.ReasoningEffortLevel
	}{
		{ThinkingAuto, ""},
		{"", ""},
		{"nonsense", ""},
		{ThinkingLow, openai.ReasoningEffortLevelLow},
		{ThinkingMedium, openai.ReasoningEffortLevelMedium},
		{ThinkingHigh, openai.ReasoningEffortLevelHigh},
		{"HIGH", openai.ReasoningEffortLevelHigh},
		{" high ", openai.ReasoningEffortLevelHigh},
	}
	for _, tc := range cases {
		if got := reasoningEffort(tc.strength); got != tc.want {
			t.Fatalf("reasoningEffort(%q) = %q, want %q", tc.strength, got, tc.want)
		}
	}
}

// The reasoning parameter reaches the provider when the user asked for a
// strength, and stays away when the provider should decide.
func TestThinkingStrengthReachesTheProvider(t *testing.T) {
	cases := []struct {
		strength string
		want     string
	}{
		{ThinkingAuto, ""},
		{ThinkingLow, "low"},
		{ThinkingMedium, "medium"},
		{ThinkingHigh, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.strength, func(t *testing.T) {
			prov := &providerServer{frames: []string{
				"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n",
			}}
			srv := httptest.NewServer(prov)
			defer srv.Close()

			sink := &recSink{}
			svc := New(Deps{Tools: &fakeTools{}, Sink: sink})
			defer svc.DisposeAll()

			cfg := Config{
				BaseURL:          srv.URL + "/v1",
				Model:            "test-model",
				APIKey:           "sk-test-key",
				ThinkingStrength: tc.strength,
			}
			if err := svc.Prompt("s1", "host", "hello", cfg); err != nil {
				t.Fatalf("Prompt: %v", err)
			}
			waitFor(t, "the run to close", func() bool { return sink.count(EventDone) == 1 })

			_, _, bodies := prov.recorded()
			if len(bodies) != 1 {
				t.Fatalf("requests = %d, want one", len(bodies))
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(bodies[0]), &payload); err != nil {
				t.Fatalf("request body: %v (%s)", err, bodies[0])
			}
			got, _ := payload["reasoning_effort"].(string)
			if got != tc.want {
				t.Fatalf("reasoning_effort = %q, want %q", got, tc.want)
			}
			if tc.want == "" {
				if _, present := payload["reasoning_effort"]; present {
					t.Fatalf("the reasoning parameter must be left out: %s", bodies[0])
				}
			}
		})
	}
}

// A provider that refuses the reasoning parameter gets a second chance
// without it: the run continues instead of failing over a parameter the
// model does not know.
func TestProviderRejectingReasoningIsRetriedWithoutIt(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	sink := &recSink{}
	svc := New(Deps{Tools: &fakeTools{}, Sink: sink})
	defer svc.DisposeAll()

	cfg := Config{
		BaseURL:          srv.URL + "/v1",
		Model:            "test-model",
		APIKey:           "sk-test-key",
		ThinkingStrength: ThinkingHigh,
	}
	if err := svc.Prompt("s1", "host", "hello", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the run to close", func() bool { return sink.count(EventDone) == 1 })

	if errs := sink.errors(); len(errs) != 0 {
		t.Fatalf("a refused reasoning parameter must not fail the run: %+v", errs)
	}
	if got := sink.text(); got != "hi" {
		t.Fatalf("streamed text = %q, want the answer of the second attempt", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want the refused one and the retry", len(bodies))
	}
	var first, second map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &first); err != nil {
		t.Fatalf("first body: %v", err)
	}
	if err := json.Unmarshal([]byte(bodies[1]), &second); err != nil {
		t.Fatalf("second body: %v", err)
	}
	if got, _ := first["reasoning_effort"].(string); got != "high" {
		t.Fatalf("the first request carried reasoning_effort %q, want high", got)
	}
	if _, present := second["reasoning_effort"]; present {
		t.Fatalf("the retry must not carry the reasoning parameter: %s", bodies[1])
	}
	if strings.Contains(strings.Join(bodies, ""), "sk-test-key") {
		t.Fatal("the API key must not appear in the request body")
	}
}

// A provider that refuses the reasoning parameter and then fails for another
// reason reports that reason, without the key in it.
func TestReasoningRejectionIsNotRetriedWithoutTheReason(t *testing.T) {
	prov := &providerServer{
		status: http.StatusBadRequest,
		body:   `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort"}}`,
	}
	srv := httptest.NewServer(prov)
	defer srv.Close()

	sink := &recSink{}
	svc := New(Deps{Tools: &fakeTools{}, Sink: sink})
	defer svc.DisposeAll()

	cfg := Config{
		BaseURL:          srv.URL + "/v1",
		Model:            "test-model",
		APIKey:           "sk-test-key",
		ThinkingStrength: ThinkingHigh,
	}
	if err := svc.Prompt("s1", "host", "hello", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the run to close", func() bool { return sink.count(EventDone) == 1 })

	errs := sink.errors()
	if len(errs) != 1 {
		t.Fatalf("error events = %+v, want one", errs)
	}
	if strings.Contains(errs[0].Error.Message, "sk-test-key") {
		t.Fatalf("the key leaked into the error: %q", errs[0].Error.Message)
	}
	if !strings.Contains(errs[0].Error.Message, "reasoning") {
		t.Fatalf("error message = %q, want the provider's reason", errs[0].Error.Message)
	}
	// The run was retried once without the parameter, and the provider
	// refused that too.
	_, _, bodies := prov.recorded()
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want the refused one and the retry", len(bodies))
	}
}

// A provider failure that has nothing to do with the reasoning parameter is
// reported as it is: no second attempt, no hidden error.
func TestUnrelatedProviderFailureIsNotRetried(t *testing.T) {
	prov := &providerServer{
		status: http.StatusBadRequest,
		body:   `{"error":{"message":"invalid model name"}}`,
	}
	srv := httptest.NewServer(prov)
	defer srv.Close()

	sink := &recSink{}
	svc := New(Deps{Tools: &fakeTools{}, Sink: sink})
	defer svc.DisposeAll()

	cfg := Config{
		BaseURL:          srv.URL + "/v1",
		Model:            "test-model",
		APIKey:           "sk-test-key",
		ThinkingStrength: ThinkingHigh,
	}
	if err := svc.Prompt("s1", "host", "hello", cfg); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	waitFor(t, "the run to close", func() bool { return sink.count(EventDone) == 1 })

	_, _, bodies := prov.recorded()
	if len(bodies) != 1 {
		t.Fatalf("requests = %d, want one: an unrelated failure is not retried", len(bodies))
	}
}
