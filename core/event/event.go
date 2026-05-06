// Package event defines the closed sum type of events emitted by a
// [provider.Provider] while streaming a model response.
//
// Consumers (the agent loop, CLI renderers, session writers) range over an
// event channel and switch on the concrete type. The unexported [Event]
// sealing method keeps the type set closed within this package: outside
// code cannot add new variants, which makes exhaustiveness checks
// meaningful.
//
// Events round-trip through JSON for session persistence and replay. Each
// concrete type marshals with a leading "type" discriminator. [Decode]
// reads that discriminator and returns the matching concrete value behind
// the [Event] interface.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Event is the closed sum of streaming-protocol events. Implementations
// live in this package; [Event.isEvent] cannot be satisfied from outside.
type Event interface {
	isEvent()
}

// FinishReason describes why a streamed turn ended.
type FinishReason string

// Reasons a streamed turn may end. Mirror the OpenAI Chat Completions
// finish_reason values. The agent loop emits an [Error] event for
// failures rather than a Finish with an error reason; there is
// intentionally no FinishReasonError constant.
const (
	FinishReasonStop          FinishReason = "stop"
	FinishReasonToolCalls     FinishReason = "tool_calls"
	FinishReasonLength        FinishReason = "length"
	FinishReasonContentFilter FinishReason = "content_filter"
)

// Usage records token accounting for a streamed response. Zero values
// indicate the runtime did not report usage (Ollama and llama.cpp
// often omit it).
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ToolContent is one piece of content returned by a tool call.
//
// MCP tools may return text, image references, or resource references in a
// single call. Type discriminates between them.
type ToolContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// Discriminator strings used in the JSON envelope. Externally visible
// because session readers and other tools may match on these.
const (
	TypeTextDelta       = "text_delta"
	TypeToolCallRequest = "tool_call_request"
	TypeToolCallResult  = "tool_call_result"
	TypeFinish          = "finish"
	TypeError           = "error"
)

// TextDelta is a chunk of plain text streamed from the model.
type TextDelta struct {
	Text string `json:"text"`
}

func (TextDelta) isEvent() {}

// MarshalJSON wraps the value with the type discriminator required by [Decode].
func (e TextDelta) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: TypeTextDelta, Text: e.Text})
}

// ToolCallRequest is the model asking to invoke a tool.
//
// The provider buffers streaming tool-call deltas and emits this event
// only when the runtime signals the call is complete. Arguments is always
// a complete JSON document; never partial. See spec §8.4.
type ToolCallRequest struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Server    string          `json:"server,omitempty"`
	Arguments json.RawMessage `json:"arguments"`
}

func (ToolCallRequest) isEvent() {}

// MarshalJSON wraps the value with the type discriminator required by [Decode].
func (e ToolCallRequest) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Server    string          `json:"server,omitempty"`
		Arguments json.RawMessage `json:"arguments"`
	}{Type: TypeToolCallRequest, ID: e.ID, Name: e.Name, Server: e.Server, Arguments: e.Arguments})
}

// ToolCallResult is the result of dispatching a [ToolCallRequest] to its
// MCP server. The agent loop builds this after each tool round-trip.
type ToolCallResult struct {
	ID      string        `json:"id"`
	Content []ToolContent `json:"content"`
	IsError bool          `json:"is_error,omitempty"`
}

func (ToolCallResult) isEvent() {}

// MarshalJSON wraps the value with the type discriminator required by [Decode].
func (e ToolCallResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type    string        `json:"type"`
		ID      string        `json:"id"`
		Content []ToolContent `json:"content"`
		IsError bool          `json:"is_error,omitempty"`
	}{Type: TypeToolCallResult, ID: e.ID, Content: e.Content, IsError: e.IsError})
}

// Finish marks the end of a streamed turn.
type Finish struct {
	Reason FinishReason `json:"reason"`
	Usage  Usage        `json:"usage"`
}

func (Finish) isEvent() {}

// MarshalJSON wraps the value with the type discriminator required by [Decode].
func (e Finish) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type   string       `json:"type"`
		Reason FinishReason `json:"reason"`
		Usage  Usage        `json:"usage"`
	}{Type: TypeFinish, Reason: e.Reason, Usage: e.Usage})
}

// Error is a streaming error.
//
// Round-tripping through JSON loses the original error's wrapping: on
// decode, Err is rebuilt as a plain string-only error. This is an
// acceptable trade-off for replay; consumers needing the original chain
// should capture it before serialization.
//
// Error has both [Error.MarshalJSON] and [Error.UnmarshalJSON]. The
// asymmetric pair is load-bearing — Err is a non-marshal-friendly
// `error` value, so encoding writes a Msg string and decoding rebuilds
// Err from that string with [errors.New]. The other event variants
// have only MarshalJSON; their fields are JSON-friendly, so the
// default reflection-based decode is sufficient.
type Error struct {
	Err error
}

func (Error) isEvent() {}

// MarshalJSON wraps the value with the type discriminator required by [Decode].
func (e Error) MarshalJSON() ([]byte, error) {
	msg := ""
	if e.Err != nil {
		msg = e.Err.Error()
	}
	return json.Marshal(struct {
		Type string `json:"type"`
		Msg  string `json:"msg"`
	}{Type: TypeError, Msg: msg})
}

// UnmarshalJSON rebuilds Err from the serialized message string.
func (e *Error) UnmarshalJSON(data []byte) error {
	var v struct {
		Msg string `json:"msg"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("event: decode error envelope: %w", err)
	}
	if v.Msg != "" {
		e.Err = errors.New(v.Msg)
	} else {
		e.Err = nil
	}
	return nil
}

// ErrUnknownType is returned by [Decode] when the "type" discriminator
// does not match a known event variant.
var ErrUnknownType = errors.New("event: unknown type")

// Decode parses a single Event from JSON. The leading "type" field
// selects the concrete variant.
func Decode(data []byte) (Event, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("event: decode envelope: %w", err)
	}
	switch head.Type {
	case TypeTextDelta:
		var e TextDelta
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("event: decode text_delta: %w", err)
		}
		return e, nil
	case TypeToolCallRequest:
		var e ToolCallRequest
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("event: decode tool_call_request: %w", err)
		}
		return e, nil
	case TypeToolCallResult:
		var e ToolCallResult
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("event: decode tool_call_result: %w", err)
		}
		return e, nil
	case TypeFinish:
		var e Finish
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("event: decode finish: %w", err)
		}
		return e, nil
	case TypeError:
		var e Error
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, fmt.Errorf("event: decode error: %w", err)
		}
		return e, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, head.Type)
	}
}
