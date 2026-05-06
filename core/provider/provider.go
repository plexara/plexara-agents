// Package provider defines the agent's view of an inference backend.
//
// A [Provider] takes a [Request] (chat-style messages, tool catalog,
// sampling parameters) and returns a channel of [event.Event] values.
// Implementations buffer protocol-level details — SSE framing, partial
// tool-call deltas — so the agent loop sees a clean event stream.
//
// v1 ships a single implementation: [OpenAICompatible], which targets
// any server speaking the OpenAI Chat Completions API. That covers
// Ollama, mlx-lm, llama.cpp's server, and vLLM. Switching between them
// is a configuration change.
package provider

import (
	"context"
	"encoding/json"

	"github.com/plexara/plexara-agents/core/event"
)

// Provider produces a stream of [event.Event] values in response to a [Request].
//
// Implementations satisfy the streaming-discipline contract from spec
// §8.4: tool-call events are emitted only when the runtime signals the
// call is complete; arguments are always complete JSON.
type Provider interface {
	// Stream initiates a request. The returned channel is closed when the
	// stream ends — on completion (with a [event.Finish]), on error (with
	// a [event.Error]), or on context cancellation. Callers must drain
	// the channel until it is closed.
	Stream(ctx context.Context, req Request) (<-chan event.Event, error)

	// Name identifies the provider for logging and diagnostics.
	Name() string
}

// Request is a single chat-style call to a [Provider].
//
// Sampling fields use pointers so the zero value means "use the
// provider's default" rather than literally zero. Setting Temperature
// to a non-nil pointer with value 0 means "deterministic sampling".
type Request struct {
	// Model is the runtime-side model identifier (e.g. "qwen3:30b-a3b").
	Model string

	// Messages is the chat history, in order. The last entry is normally
	// the user's most recent input.
	Messages []Message

	// Tools is the catalog of tools the model may call this turn. May be
	// empty.
	Tools []Tool

	Temperature *float32
	TopP        *float32
	MaxTokens   *int
}

// Role enumerates the message authors recognized by chat-style providers.
type Role string

// Recognized message roles. Mirror the OpenAI Chat Completions schema.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry in a chat history.
//
// Assistant messages may carry [Message.ToolCalls]. Tool messages carry
// [Message.ToolCallID] referring to the assistant turn that requested
// the call. This shape mirrors the OpenAI Chat Completions schema and
// is the lowest-friction representation across the runtimes we target.
type Message struct {
	Role    Role
	Content string

	ToolCalls []ToolCall

	ToolCallID string
}

// ToolCall is a tool invocation embedded in an assistant message. Used
// when persisting or replaying a session that already has tool calls in
// the history.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Tool is a callable function the model may invoke. Parameters is a
// JSON Schema describing the expected arguments.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}
