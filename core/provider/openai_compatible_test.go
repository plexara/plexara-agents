package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/plexara/plexara-agents/core/event"
	"github.com/plexara/plexara-agents/core/provider"
)

func TestOpenAICompatible_Config(t *testing.T) {
	t.Parallel()

	t.Run("empty_base_url_rejected", func(t *testing.T) {
		t.Parallel()
		_, err := provider.NewOpenAICompatible(provider.OpenAIConfig{})
		if !errors.Is(err, provider.ErrConfig) {
			t.Errorf("err = %v; want errors.Is ErrConfig", err)
		}
	})

	t.Run("name", func(t *testing.T) {
		t.Parallel()
		p, err := provider.NewOpenAICompatible(provider.OpenAIConfig{BaseURL: "http://x"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if got := p.Name(); got != "openai-compatible" {
			t.Errorf("Name = %q; want openai-compatible", got)
		}
	})

	t.Run("custom_headers_passed_through", func(t *testing.T) {
		t.Parallel()

		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("X-Plexara-Test")
			writeSSE(w, ssePlain())
		}))
		t.Cleanup(srv.Close)

		p := mustOpenAI(t, provider.OpenAIConfig{
			BaseURL:    srv.URL + "/v1",
			Headers:    map[string]string{"X-Plexara-Test": "yes"},
			HTTPClient: srv.Client(),
		})
		drainAll(t, p, provider.Request{Model: "x"})

		if got != "yes" {
			t.Errorf("X-Plexara-Test = %q; want yes", got)
		}
	})

	t.Run("base_url_trailing_slash_trimmed", func(t *testing.T) {
		t.Parallel()

		var path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			writeSSE(w, ssePlain())
		}))
		t.Cleanup(srv.Close)

		p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1/", HTTPClient: srv.Client()})
		drainAll(t, p, provider.Request{Model: "x"})

		if path != "/v1/chat/completions" {
			t.Errorf("path = %q; want /v1/chat/completions", path)
		}
	})
}

// TestOpenAICompatible_AuthFromEnv is intentionally serial: t.Setenv
// forbids running in a parent or sibling that has called t.Parallel.
func TestOpenAICompatible_AuthFromEnv(t *testing.T) {
	const want = "from-env"
	t.Setenv("PLEXARA_TEST_KEY", want)

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		writeSSE(w, ssePlain())
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", APIKeyEnv: "PLEXARA_TEST_KEY", HTTPClient: srv.Client()})
	drainAll(t, p, provider.Request{Model: "x"})

	if got != "Bearer "+want {
		t.Errorf("Authorization = %q; want Bearer %s", got, want)
	}
}

func TestOpenAICompatible_AuthExplicitOverridesEnv(t *testing.T) {
	t.Setenv("PLEXARA_TEST_KEY", "from-env")

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		writeSSE(w, ssePlain())
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{
		BaseURL:    srv.URL + "/v1",
		APIKey:     "explicit",
		APIKeyEnv:  "PLEXARA_TEST_KEY",
		HTTPClient: srv.Client(),
	})
	drainAll(t, p, provider.Request{Model: "x"})

	if got != "Bearer explicit" {
		t.Errorf("Authorization = %q; want Bearer explicit", got)
	}
}

func TestOpenAICompatible_Stream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		frames []string
		want   []event.Event
	}{
		{
			name:   "plain_text",
			frames: ssePlain(),
			want: []event.Event{
				event.TextDelta{Text: "Hello"},
				event.TextDelta{Text: " world"},
				event.Finish{Reason: event.FinishReasonStop, Usage: event.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}},
			},
		},
		{
			name: "single_tool_call_buffered",
			frames: []string{
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"NYC\"}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":4,"total_tokens":11}}`,
				`[DONE]`,
			},
			want: []event.Event{
				event.ToolCallRequest{
					ID: "call_a", Name: "get_weather",
					Arguments: json.RawMessage(`{"city":"NYC"}`),
				},
				event.Finish{Reason: event.FinishReasonToolCalls, Usage: event.Usage{PromptTokens: 7, CompletionTokens: 4, TotalTokens: 11}},
			},
		},
		{
			name: "two_tool_calls_in_index_order",
			frames: []string{
				// Interleave indices 0 and 1; output must still be ordered.
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"second","arguments":"{}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"first","arguments":"{\"k\":1}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			},
			want: []event.Event{
				event.ToolCallRequest{ID: "call_a", Name: "first", Arguments: json.RawMessage(`{"k":1}`)},
				event.ToolCallRequest{ID: "call_b", Name: "second", Arguments: json.RawMessage(`{}`)},
				event.Finish{Reason: event.FinishReasonToolCalls},
			},
		},
		{
			name: "text_then_tool_call",
			frames: []string{
				`{"choices":[{"index":0,"delta":{"content":"Let me check."}}]}`,
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_z","function":{"name":"lookup","arguments":"{}"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
				`[DONE]`,
			},
			want: []event.Event{
				event.TextDelta{Text: "Let me check."},
				event.ToolCallRequest{ID: "call_z", Name: "lookup", Arguments: json.RawMessage(`{}`)},
				event.Finish{Reason: event.FinishReasonToolCalls},
			},
		},
		{
			name: "empty_tool_args_become_empty_object",
			frames: []string{
				`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_e","function":{"name":"noop"}}]}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			},
			want: []event.Event{
				event.ToolCallRequest{ID: "call_e", Name: "noop", Arguments: json.RawMessage(`{}`)},
				event.Finish{Reason: event.FinishReasonToolCalls},
			},
		},
		{
			name: "sse_comments_ignored",
			frames: []string{
				": keep-alive comment",
				`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
				`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				`[DONE]`,
			},
			want: []event.Event{
				event.TextDelta{Text: "hi"},
				event.Finish{Reason: event.FinishReasonStop},
			},
		},
		{
			name: "done_without_finish_reason",
			frames: []string{
				`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
				`[DONE]`,
			},
			want: []event.Event{
				event.TextDelta{Text: "hi"},
				event.Finish{Reason: event.FinishReasonStop},
			},
		},
		{
			name: "stream_ends_without_done",
			frames: []string{
				`{"choices":[{"index":0,"delta":{"content":"truncated"}}]}`,
			},
			want: []event.Event{
				event.TextDelta{Text: "truncated"},
				event.Finish{Reason: event.FinishReasonStop},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeSSE(w, tt.frames)
			}))
			t.Cleanup(srv.Close)

			p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
			got := drainAll(t, p, provider.Request{Model: "x"})

			if !equalEvents(t, got, tt.want) {
				t.Errorf("events mismatch:\n got %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestOpenAICompatible_DoneWithPendingToolCallsKeepsToolCallsReason(t *testing.T) {
	t.Parallel()

	// Some runtimes terminate with [DONE] without ever sending
	// finish_reason: tool_calls. The provider must still emit
	// Finish{Reason: tool_calls} so the loop dispatches the call.
	frames := []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_z","type":"function","function":{"name":"lookup","arguments":"{\"k\":1}"}}]}}]}`,
		`[DONE]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, frames)
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	got := drainAll(t, p, provider.Request{Model: "x"})

	if len(got) != 2 {
		t.Fatalf("got %d events; want 2 (ToolCallRequest + Finish)", len(got))
	}
	if _, ok := got[0].(event.ToolCallRequest); !ok {
		t.Errorf("got[0] = %T; want ToolCallRequest", got[0])
	}
	finish, ok := got[1].(event.Finish)
	if !ok {
		t.Fatalf("got[1] = %T; want Finish", got[1])
	}
	if finish.Reason != event.FinishReasonToolCalls {
		t.Errorf("Finish.Reason = %q; want tool_calls (DONE flush must preserve the signal)", finish.Reason)
	}
}

func TestOpenAICompatible_MultiLineDataFrame(t *testing.T) {
	t.Parallel()

	// Per the SSE spec, multiple `data:` lines before a blank-line
	// boundary are concatenated with `\n`. Build a frame whose JSON
	// is split across two `data:` lines.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// First frame: JSON split across two `data:` lines.
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":\n")
		fmt.Fprint(w, "data: {\"content\":\"hello\"}}]}\n\n")
		// Second frame: terminator.
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	got := drainAll(t, p, provider.Request{Model: "x"})

	want := []event.Event{
		event.TextDelta{Text: "hello"},
		event.Finish{Reason: event.FinishReasonStop},
	}
	if !equalEvents(t, got, want) {
		t.Errorf("multi-line data: parse failed:\n  got %#v\n want %#v", got, want)
	}
}

func TestOpenAICompatible_ContentFilterFinishReason(t *testing.T) {
	t.Parallel()

	frames := []string{
		`{"choices":[{"index":0,"delta":{"content":"partial"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`,
		`[DONE]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, frames)
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	got := drainAll(t, p, provider.Request{Model: "x"})

	if len(got) != 2 {
		t.Fatalf("got %d events; want 2", len(got))
	}
	finish, ok := got[1].(event.Finish)
	if !ok {
		t.Fatalf("got[1] = %T; want Finish", got[1])
	}
	if finish.Reason != event.FinishReasonContentFilter {
		t.Errorf("Finish.Reason = %q; want content_filter", finish.Reason)
	}
}

func TestOpenAICompatible_OversizedLineSurfacesAsError(t *testing.T) {
	t.Parallel()

	// A single SSE line exceeding the 1 MiB scanner ceiling must
	// surface as an event.Error rather than panic, truncate silently,
	// or be parsed as malformed JSON.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// 2 MiB of `a` between two valid JSON braces.
		fmt.Fprint(w, "data: {\"x\":\"")
		blob := strings.Repeat("a", 2*1024*1024)
		fmt.Fprint(w, blob)
		fmt.Fprint(w, "\"}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	got := drainAll(t, p, provider.Request{Model: "x"})

	// Either the first event is an Error (scanner exceeded its buffer)
	// or — if some runtime supplies enough buffering — we tolerate other
	// outcomes as long as nothing panics. We assert the strict case.
	if len(got) == 0 {
		t.Fatal("got 0 events; want at least one")
	}
	errEvt, ok := got[0].(event.Error)
	if !ok {
		t.Fatalf("got[0] = %T; want event.Error for oversized line", got[0])
	}
	if !strings.Contains(errEvt.Err.Error(), "read stream") {
		t.Errorf("err = %v; want contains 'read stream'", errEvt.Err)
	}
}

func TestOpenAICompatible_NonObjectToolArgsEmitsError(t *testing.T) {
	t.Parallel()

	frames := []string{
		// The model emits `null` as the arguments — valid JSON but not
		// an object. MCP servers expect objects.
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_n","function":{"name":"f","arguments":"null"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, frames)
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	got := drainAll(t, p, provider.Request{Model: "x"})

	if len(got) == 0 {
		t.Fatal("got 0 events; want an Error")
	}
	errEvt, ok := got[0].(event.Error)
	if !ok {
		t.Fatalf("got[0] = %T; want event.Error", got[0])
	}
	if !strings.Contains(errEvt.Err.Error(), "must be a JSON object") {
		t.Errorf("err = %v; want contains 'must be a JSON object'", errEvt.Err)
	}
}

func TestOpenAICompatible_HeadersDeepCopiedAtConstruction(t *testing.T) {
	t.Parallel()

	// Construct the provider, then mutate the caller's Headers map.
	// Subsequent requests must NOT see the post-construction mutation.
	headers := map[string]string{"X-Plexara-Test": "original"}

	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Plexara-Test")
		writeSSE(w, ssePlain())
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{
		BaseURL:    srv.URL + "/v1",
		Headers:    headers,
		HTTPClient: srv.Client(),
	})

	// Mutate AFTER construction.
	headers["X-Plexara-Test"] = "MUTATED"

	drainAll(t, p, provider.Request{Model: "x"})

	if seen != "original" {
		t.Errorf("header = %q; want %q (mutation after New must not propagate)", seen, "original")
	}
}

func TestOpenAICompatible_InvalidToolArgsEmitsError(t *testing.T) {
	t.Parallel()

	frames := []string{
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_x","function":{"name":"f","arguments":"{not json"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, frames)
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	got := drainAll(t, p, provider.Request{Model: "x"})

	if len(got) != 1 {
		t.Fatalf("got %d events; want 1 (an Error)", len(got))
	}
	errEvt, ok := got[0].(event.Error)
	if !ok {
		t.Fatalf("got %T; want event.Error", got[0])
	}
	if !strings.Contains(errEvt.Err.Error(), "invalid JSON") {
		t.Errorf("err = %v; want contains 'invalid JSON'", errEvt.Err)
	}
}

func TestOpenAICompatible_DecodeChunkError(t *testing.T) {
	t.Parallel()

	frames := []string{`{not json}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, frames)
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	got := drainAll(t, p, provider.Request{Model: "x"})

	if len(got) != 1 {
		t.Fatalf("got %d events; want 1 (an Error)", len(got))
	}
	if _, ok := got[0].(event.Error); !ok {
		t.Errorf("got %T; want event.Error", got[0])
	}
}

func TestOpenAICompatible_HTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintln(w, "boom")
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	_, err := p.Stream(t.Context(), provider.Request{Model: "x"})
	if err == nil || !strings.Contains(err.Error(), "http 500") {
		t.Errorf("err = %v; want contains 'http 500'", err)
	}
}

func TestOpenAICompatible_RequestPayloadShape(t *testing.T) {
	t.Parallel()

	temp := float32(0.7)
	maxTok := 256

	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		writeSSE(w, ssePlain())
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	drainAll(t, p, provider.Request{
		Model:       "qwen3:30b",
		Messages:    []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
		Tools:       []provider.Tool{{Name: "t", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Temperature: &temp,
		MaxTokens:   &maxTok,
	})

	if captured["model"] != "qwen3:30b" {
		t.Errorf("model = %v; want qwen3:30b", captured["model"])
	}
	if captured["stream"] != true {
		t.Errorf("stream = %v; want true", captured["stream"])
	}
	if captured["temperature"] != float64(0.7) {
		t.Errorf("temperature = %v; want 0.7", captured["temperature"])
	}
	if captured["max_tokens"] != float64(256) {
		t.Errorf("max_tokens = %v; want 256", captured["max_tokens"])
	}
	tools, _ := captured["tools"].([]any)
	if len(tools) != 1 {
		t.Errorf("tools length = %d; want 1", len(tools))
	}
}

func TestOpenAICompatible_ChatHistoryShape(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		writeSSE(w, ssePlain())
	}))
	t.Cleanup(srv.Close)

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})

	drainAll(t, p, provider.Request{
		Model: "x",
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "be terse"},
			{Role: provider.RoleUser, Content: "weather?"},
			{
				Role:    provider.RoleAssistant,
				Content: "",
				ToolCalls: []provider.ToolCall{
					{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"NYC"}`)},
				},
			},
			{Role: provider.RoleTool, ToolCallID: "call_1", Content: "72F sunny"},
		},
	})

	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages length = %d; want 4", len(msgs))
	}

	asst, _ := msgs[2].(map[string]any)
	tcs, _ := asst["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("assistant tool_calls length = %d; want 1", len(tcs))
	}
	tc0, _ := tcs[0].(map[string]any)
	if tc0["id"] != "call_1" || tc0["type"] != "function" {
		t.Errorf("tool_call[0] = %#v; want id=call_1, type=function", tc0)
	}
	fn, _ := tc0["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("function.name = %v; want get_weather", fn["name"])
	}
	if fn["arguments"] != `{"city":"NYC"}` {
		t.Errorf("function.arguments = %v; want json string", fn["arguments"])
	}

	tool, _ := msgs[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_1" {
		t.Errorf("tool message = %#v; want role=tool, tool_call_id=call_1", tool)
	}
}

func TestOpenAICompatible_ContextCancel(t *testing.T) {
	t.Parallel()

	// Server that holds the connection open and emits one frame, then waits.
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-gate
	}))
	t.Cleanup(func() { close(gate); srv.Close() })

	p := mustOpenAI(t, provider.OpenAIConfig{BaseURL: srv.URL + "/v1", HTTPClient: srv.Client()})
	ctx, cancel := context.WithCancel(t.Context())

	ch, err := p.Stream(ctx, provider.Request{Model: "x"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Pull at least one event then cancel.
	select {
	case e := <-ch:
		if _, ok := e.(event.TextDelta); !ok {
			t.Errorf("first event = %T; want TextDelta", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first event")
	}
	cancel()

	// Channel must close shortly after cancel.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("channel did not close after context cancel")
		}
	}
}

func TestOpenAICompatible_DialFailure(t *testing.T) {
	t.Parallel()

	// Use an address we know nothing listens on.
	p := mustOpenAI(t, provider.OpenAIConfig{
		BaseURL:    "http://127.0.0.1:1",
		HTTPClient: &http.Client{Timeout: 500 * time.Millisecond},
	})

	_, err := p.Stream(t.Context(), provider.Request{Model: "x"})
	if err == nil {
		t.Fatal("Stream returned nil error; want a dial error")
	}
}

// --- helpers -----------------------------------------------------------------

func mustOpenAI(t *testing.T, cfg provider.OpenAIConfig) *provider.OpenAICompatible {
	t.Helper()
	p, err := provider.NewOpenAICompatible(cfg)
	if err != nil {
		t.Fatalf("NewOpenAICompatible: %v", err)
	}
	return p
}

func writeSSE(w http.ResponseWriter, frames []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	for _, f := range frames {
		if strings.HasPrefix(f, ":") {
			fmt.Fprintf(w, "%s\n\n", f)
		} else {
			fmt.Fprintf(w, "data: %s\n\n", f)
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func ssePlain() []string {
	return []string{
		`{"choices":[{"index":0,"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":" world"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`,
		`[DONE]`,
	}
}

func drainAll(t *testing.T, p provider.Provider, req provider.Request) []event.Event {
	t.Helper()
	ch, err := p.Stream(t.Context(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var out []event.Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}

func equalEvents(t *testing.T, got, want []event.Event) bool {
	t.Helper()
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		gb, _ := json.Marshal(got[i])
		wb, _ := json.Marshal(want[i])
		if string(gb) != string(wb) {
			t.Logf("event[%d]:\n  got  %s\n  want %s", i, gb, wb)
			return false
		}
	}
	return true
}
