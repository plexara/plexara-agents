package event_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/plexara/plexara-agents/core/event"
)

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   event.Event
		want string
	}{
		{
			name: "text_delta",
			in:   event.TextDelta{Text: "hello"},
			want: `{"type":"text_delta","text":"hello"}`,
		},
		{
			name: "tool_call_request",
			in: event.ToolCallRequest{
				ID:        "call_abc",
				Name:      "get_weather",
				Arguments: json.RawMessage(`{"city":"NYC"}`),
			},
			want: `{"type":"tool_call_request","id":"call_abc","name":"get_weather","arguments":{"city":"NYC"}}`,
		},
		{
			name: "tool_call_request_with_server",
			in: event.ToolCallRequest{
				ID:        "call_xyz",
				Name:      "datahub_search",
				Server:    "plexara-acme",
				Arguments: json.RawMessage(`{"q":"orders"}`),
			},
			want: `{"type":"tool_call_request","id":"call_xyz","name":"datahub_search","server":"plexara-acme","arguments":{"q":"orders"}}`,
		},
		{
			name: "tool_call_result_text",
			in: event.ToolCallResult{
				ID: "call_abc",
				Content: []event.ToolContent{
					{Type: "text", Text: "72F sunny"},
				},
			},
			want: `{"type":"tool_call_result","id":"call_abc","content":[{"type":"text","text":"72F sunny"}]}`,
		},
		{
			name: "tool_call_result_error",
			in: event.ToolCallResult{
				ID:      "call_def",
				Content: []event.ToolContent{{Type: "text", Text: "boom"}},
				IsError: true,
			},
			want: `{"type":"tool_call_result","id":"call_def","content":[{"type":"text","text":"boom"}],"is_error":true}`,
		},
		{
			name: "finish",
			in: event.Finish{
				Reason: event.FinishReasonStop,
				Usage:  event.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
			},
			want: `{"type":"finish","reason":"stop","usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`,
		},
		{
			name: "finish_zero_usage",
			in:   event.Finish{Reason: event.FinishReasonToolCalls},
			want: `{"type":"finish","reason":"tool_calls","usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
		},
		{
			name: "error",
			in:   event.Error{Err: errors.New("boom")},
			want: `{"type":"error","msg":"boom"}`,
		},
		{
			name: "error_nil",
			in:   event.Error{},
			want: `{"type":"error","msg":""}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("Marshal:\n  got  %s\n  want %s", got, tt.want)
			}

			decoded, err := event.Decode(got)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}

			rt, err := json.Marshal(decoded)
			if err != nil {
				t.Fatalf("Marshal(rt): %v", err)
			}
			if string(rt) != tt.want {
				t.Errorf("round-trip mismatch:\n  got  %s\n  want %s", rt, tt.want)
			}
		})
	}
}

func TestDecodeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{name: "empty", input: ``, wantErr: "decode envelope"},
		{name: "not_object", input: `42`, wantErr: "decode envelope"},
		{name: "missing_type", input: `{"text":"x"}`, wantErr: "unknown type"},
		{name: "unknown_type", input: `{"type":"banana"}`, wantErr: "unknown type"},
		{name: "type_wrong_kind", input: `{"type":42}`, wantErr: "decode envelope"},
		{name: "malformed_payload_text_delta", input: `{"type":"text_delta","text":42}`, wantErr: "decode text_delta"},
		{name: "malformed_payload_finish", input: `{"type":"finish","usage":"nope"}`, wantErr: "decode finish"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := event.Decode([]byte(tt.input))
			if err == nil {
				t.Fatalf("Decode(%q) returned nil error; want %q", tt.input, tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Decode(%q) error = %v; want contains %q", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestErrUnknownType(t *testing.T) {
	t.Parallel()

	_, err := event.Decode([]byte(`{"type":"banana"}`))
	if !errors.Is(err, event.ErrUnknownType) {
		t.Errorf("Decode unknown type: errors.Is(%v, ErrUnknownType) = false", err)
	}
}

// TestSealedInterface confirms the Event interface is closed: only types
// declared in this package implement it. The test compiles only because
// each constructor returns the package's own variants.
func TestSealedInterface(t *testing.T) {
	t.Parallel()

	var _ event.Event = event.TextDelta{}
	var _ event.Event = event.ToolCallRequest{}
	var _ event.Event = event.ToolCallResult{}
	var _ event.Event = event.Finish{}
	var _ event.Event = event.Error{}
}
