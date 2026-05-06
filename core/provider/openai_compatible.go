package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/plexara/plexara-agents/core/event"
)

// OpenAIConfig configures an [OpenAICompatible] provider.
//
// Auth: APIKey is preferred. If empty and APIKeyEnv is set, the value of
// that environment variable is used. Headers may carry additional auth
// or routing headers.
type OpenAIConfig struct {
	// BaseURL is the API root (e.g. "http://localhost:11434/v1"). Must not
	// include a trailing slash; the provider appends "/chat/completions".
	BaseURL string

	APIKey    string
	APIKeyEnv string
	Headers   map[string]string

	// HTTPClient is optional. If nil, a default client with sane dial,
	// header, and idle timeouts is used. Tests inject a custom client to
	// reach a [net/http/httptest.Server].
	HTTPClient *http.Client
}

// OpenAICompatible implements [Provider] against any server speaking the
// OpenAI Chat Completions API. Validated against Ollama, mlx-lm,
// llama.cpp's server, and vLLM.
type OpenAICompatible struct {
	cfg     OpenAIConfig
	client  *http.Client
	apiKey  string
	headers map[string]string // deep-copied from cfg.Headers at construction
	url     string
}

// ErrConfig is returned by [NewOpenAICompatible] when the configuration
// is missing required fields.
var ErrConfig = errors.New("provider: invalid configuration")

// NewOpenAICompatible builds an OpenAI-compatible provider from cfg. It
// resolves the API key from APIKey or APIKeyEnv at construction time so
// callers see configuration errors up-front.
func NewOpenAICompatible(cfg OpenAIConfig) (*OpenAICompatible, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("%w: BaseURL is required", ErrConfig)
	}

	apiKey := cfg.APIKey
	if apiKey == "" && cfg.APIKeyEnv != "" {
		apiKey = os.Getenv(cfg.APIKeyEnv)
	}

	client := cfg.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}

	// Deep-copy Headers so post-construction mutations on the caller's
	// map cannot affect future requests.
	var headers map[string]string
	if len(cfg.Headers) > 0 {
		headers = make(map[string]string, len(cfg.Headers))
		for k, v := range cfg.Headers {
			headers[k] = v
		}
	}

	url := strings.TrimSuffix(cfg.BaseURL, "/") + "/chat/completions"

	return &OpenAICompatible{
		cfg:     cfg,
		client:  client,
		apiKey:  apiKey,
		headers: headers,
		url:     url,
	}, nil
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		// No overall Timeout: streams may run for many minutes. Per-stage
		// limits are enforced via the dialer and ResponseHeaderTimeout.
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          10,
			MaxIdleConnsPerHost:   2,
		},
	}
}

// Name returns the provider's name for logging.
func (p *OpenAICompatible) Name() string { return "openai-compatible" }

// chatRequest is the wire form of [Request] for the Chat Completions API.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Tools       []chatTool    `json:"tools,omitempty"`
	Stream      bool          `json:"stream"`
	Temperature *float32      `json:"temperature,omitempty"`
	TopP        *float32      `json:"top_p,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	StreamOpts  *streamOpts   `json:"stream_options,omitempty"`
}

type streamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string               `json:"type"`
	Function chatToolFunctionSpec `json:"function"`
}

type chatToolFunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func buildChatRequest(req Request) chatRequest {
	out := chatRequest{
		Model:       req.Model,
		Messages:    make([]chatMessage, 0, len(req.Messages)),
		Stream:      true,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		StreamOpts:  &streamOpts{IncludeUsage: true},
	}
	for _, m := range req.Messages {
		cm := chatMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		if len(m.ToolCalls) > 0 {
			cm.ToolCalls = make([]chatToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				cm.ToolCalls = append(cm.ToolCalls, chatToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: chatToolFunction{
						Name:      tc.Name,
						Arguments: string(tc.Arguments),
					},
				})
			}
		}
		out.Messages = append(out.Messages, cm)
	}
	for _, t := range req.Tools {
		// chatToolFunctionSpec(t) is a Go struct conversion: the two
		// types must have identical field names, types, and order. A
		// future field reorder or rename on either side becomes a
		// compile error rather than a silent wire-format drift.
		out.Tools = append(out.Tools, chatTool{
			Type:     "function",
			Function: chatToolFunctionSpec(t),
		})
	}
	return out
}

// Stream sends req to the configured chat-completions endpoint with
// stream=true and parses the resulting SSE response into events.
//
// Per spec §8.4, [event.ToolCallRequest] is emitted only when the
// runtime reports finish_reason: tool_calls. Argument deltas are
// accumulated per index and validated as JSON before emission.
func (p *OpenAICompatible) Stream(ctx context.Context, req Request) (<-chan event.Event, error) {
	body, err := json.Marshal(buildChatRequest(req))
	if err != nil {
		return nil, fmt.Errorf("provider: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("provider: do request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Read up to a small window for diagnostics, then close.
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("provider: http %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}

	out := make(chan event.Event)
	go p.runStream(ctx, resp.Body, out)
	return out, nil
}

// chatChunk is one SSE frame from the chat-completions endpoint.
type chatChunk struct {
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
}

type chatChoice struct {
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type chatDelta struct {
	Content   string              `json:"content,omitempty"`
	ToolCalls []chatToolCallDelta `json:"tool_calls,omitempty"`
}

type chatToolCallDelta struct {
	Index    int                       `json:"index"`
	ID       string                    `json:"id,omitempty"`
	Type     string                    `json:"type,omitempty"`
	Function chatToolCallFunctionDelta `json:"function,omitempty"`
}

type chatToolCallFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type toolCallAccumulator struct {
	ID   string
	Name string
	Args strings.Builder
}

// streamState carries the running state of an in-progress stream so
// that line/chunk handling can be split across helpers without losing
// context.
type streamState struct {
	toolCalls map[int]*toolCallAccumulator
	usage     event.Usage
	// dataBuf accumulates `data:` lines for a single SSE event. Per the
	// SSE spec, multiple `data:` lines before a blank-line boundary are
	// concatenated with `\n`. None of the runtimes we target today emit
	// multi-line frames, but the buffering is necessary for spec
	// correctness and future-proofs us against servers that do.
	dataBuf strings.Builder
}

// streamStatus tells the caller whether a sub-step wants to keep
// reading, has finished cleanly, or has aborted (cancelled/error).
type streamStatus int

const (
	streamContinue streamStatus = iota
	streamDone
	streamAbort
)

func (p *OpenAICompatible) runStream(ctx context.Context, body io.ReadCloser, out chan<- event.Event) {
	defer func() { _ = body.Close() }()
	defer close(out)

	send := func(e event.Event) bool {
		select {
		case out <- e:
			return true
		case <-ctx.Done():
			return false
		}
	}
	sendErr := func(err error) {
		_ = send(event.Error{Err: err})
	}

	scanner := bufio.NewScanner(body)
	// Allow up to 1 MiB per SSE line. The default 64 KiB ceiling is too
	// tight for some servers that emit large delta blobs.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	st := &streamState{toolCalls: map[int]*toolCallAccumulator{}}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		switch p.handleLine(scanner.Text(), st, send, sendErr) {
		case streamDone, streamAbort:
			return
		case streamContinue:
		}
	}
	if err := scanner.Err(); err != nil {
		sendErr(fmt.Errorf("provider: read stream: %w", err))
		return
	}
	// EOF without a closing blank line — flush any buffered `data:` lines.
	if st.dataBuf.Len() > 0 {
		switch p.dispatchData(st.dataBuf.String(), st, send, sendErr) {
		case streamDone, streamAbort:
			return
		case streamContinue:
		}
		st.dataBuf.Reset()
	}
	// Stream ended without a finish_reason or [DONE]; synthesize.
	hadToolCalls := len(st.toolCalls) > 0
	if !p.flushPending(st.toolCalls, sendErr, send) {
		return
	}
	reason := event.FinishReasonStop
	if hadToolCalls {
		reason = event.FinishReasonToolCalls
	}
	_ = send(event.Finish{Reason: reason, Usage: st.usage})
}

func (p *OpenAICompatible) handleLine(
	line string,
	st *streamState,
	send func(event.Event) bool,
	sendErr func(error),
) streamStatus {
	// Empty line is the SSE event boundary: dispatch the buffered data.
	if line == "" {
		if st.dataBuf.Len() == 0 {
			return streamContinue
		}
		data := st.dataBuf.String()
		st.dataBuf.Reset()
		return p.dispatchData(data, st, send, sendErr)
	}
	// SSE comment / keep-alive.
	if strings.HasPrefix(line, ":") {
		return streamContinue
	}
	// Non-`data:` field lines (e.g. `event:`, `id:`, `retry:`) are not
	// used by the chat-completions stream protocol; ignore them.
	if !strings.HasPrefix(line, "data:") {
		return streamContinue
	}
	// Per RFC 6, exactly one optional space after the colon is stripped.
	payload := strings.TrimPrefix(line, "data:")
	payload = strings.TrimPrefix(payload, " ")
	if st.dataBuf.Len() > 0 {
		st.dataBuf.WriteByte('\n')
	}
	st.dataBuf.WriteString(payload)
	return streamContinue
}

func (p *OpenAICompatible) dispatchData(
	data string,
	st *streamState,
	send func(event.Event) bool,
	sendErr func(error),
) streamStatus {
	data = strings.TrimSpace(data)
	if data == "" {
		return streamContinue
	}
	if data == "[DONE]" {
		hadToolCalls := len(st.toolCalls) > 0
		if !p.flushPending(st.toolCalls, sendErr, send) {
			return streamAbort
		}
		reason := event.FinishReasonStop
		if hadToolCalls {
			// Some runtimes terminate with [DONE] without ever sending
			// finish_reason: tool_calls. Preserve the tool-calls signal so
			// the agent loop dispatches the call rather than treating the
			// turn as complete.
			reason = event.FinishReasonToolCalls
		}
		_ = send(event.Finish{Reason: reason, Usage: st.usage})
		return streamDone
	}
	var chunk chatChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		sendErr(fmt.Errorf("provider: decode chunk: %w", err))
		return streamAbort
	}
	if chunk.Usage != nil {
		st.usage = event.Usage{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
			TotalTokens:      chunk.Usage.TotalTokens,
		}
	}
	for _, c := range chunk.Choices {
		switch p.handleChoice(c, st, send, sendErr) {
		case streamDone:
			return streamDone
		case streamAbort:
			return streamAbort
		case streamContinue:
		}
	}
	return streamContinue
}

func (p *OpenAICompatible) handleChoice(
	c chatChoice,
	st *streamState,
	send func(event.Event) bool,
	sendErr func(error),
) streamStatus {
	if c.Delta.Content != "" {
		if !send(event.TextDelta{Text: c.Delta.Content}) {
			return streamAbort
		}
	}
	for _, tc := range c.Delta.ToolCalls {
		acc, ok := st.toolCalls[tc.Index]
		if !ok {
			acc = &toolCallAccumulator{}
			st.toolCalls[tc.Index] = acc
		}
		if tc.ID != "" {
			acc.ID = tc.ID
		}
		if tc.Function.Name != "" {
			acc.Name = tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			acc.Args.WriteString(tc.Function.Arguments)
		}
	}
	if c.FinishReason == nil {
		return streamContinue
	}
	reason := event.FinishReason(*c.FinishReason)
	if reason == event.FinishReasonToolCalls {
		if !p.flushPending(st.toolCalls, sendErr, send) {
			return streamAbort
		}
	}
	if !send(event.Finish{Reason: reason, Usage: st.usage}) {
		return streamAbort
	}
	return streamDone
}

// flushPending emits one ToolCallRequest per accumulated tool call, in
// stable index order. Arguments are validated as JSON; an invalid
// payload aborts the stream with an Error event.
func (p *OpenAICompatible) flushPending(
	toolCalls map[int]*toolCallAccumulator,
	sendErr func(error),
	send func(event.Event) bool,
) bool {
	if len(toolCalls) == 0 {
		return true
	}
	indices := make([]int, 0, len(toolCalls))
	for i := range toolCalls {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	for _, i := range indices {
		acc := toolCalls[i]
		args := acc.Args.String()
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			sendErr(fmt.Errorf("provider: tool call %q has invalid JSON args: %s", acc.ID, args))
			return false
		}
		// Tool arguments must be a JSON object per the OpenAI Chat
		// Completions schema. A model that emits null/array/scalar will
		// be rejected by downstream MCP servers with a less-actionable
		// error; surface it here as an Error event instead.
		trimmed := strings.TrimLeft(args, " \t\n\r")
		if len(trimmed) == 0 || trimmed[0] != '{' {
			sendErr(fmt.Errorf("provider: tool call %q args must be a JSON object, got: %s", acc.ID, args))
			return false
		}
		if !send(event.ToolCallRequest{
			ID:        acc.ID,
			Name:      acc.Name,
			Arguments: json.RawMessage(args),
		}) {
			return false
		}
		delete(toolCalls, i)
	}
	return true
}
