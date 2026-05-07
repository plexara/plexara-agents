// Package session is an append-only chat history with replay-friendly
// persistence.
//
// A [Session] is a value: copy it, ship it across goroutines, save it
// to disk. Persistence is JSON Lines — one header line followed by
// one envelope per message. The format is stable and forward-readable
// so that a future reader can ignore unknown envelope types without
// failing.
//
// Token-aware truncation lives behind a [Tokenizer] interface. v1 ships
// a simple byte-length heuristic ([LengthHeuristic]); a real
// model-aware tokenizer can swap in for v1.1 without touching callers.
package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/plexara/plexara-agents/core/provider"
)

// Session is an append-only chat history.
type Session struct {
	ID       string
	Created  time.Time
	Updated  time.Time
	Messages []provider.Message
}

// New constructs an empty Session with the given ID. ID is the
// caller's responsibility to choose; using a UUID is conventional but
// not required.
func New(id string) *Session {
	now := time.Now().UTC()
	return &Session{
		ID:       id,
		Created:  now,
		Updated:  now,
		Messages: nil,
	}
}

// Append adds a message and bumps Updated.
func (s *Session) Append(m provider.Message) {
	s.Messages = append(s.Messages, m)
	s.Updated = time.Now().UTC()
}

// Tokenizer estimates token count for a message.
//
// v1 uses [LengthHeuristic]; v1.1 may swap in a real model-aware
// tokenizer (e.g. via a /tokenize HTTP call to the runtime) without
// touching callers.
type Tokenizer interface {
	CountTokens(provider.Message) int
}

// LengthHeuristic estimates tokens as ceil(content-bytes /
// BytesPerToken). It is intentionally simple; the caller should treat
// its output as a *budget* not an oracle.
type LengthHeuristic struct {
	// BytesPerToken is the rough characters-per-token ratio.
	// Defaults to 4 (typical for English text under common BPE-style
	// tokenizers).
	BytesPerToken int
}

// CountTokens implements [Tokenizer].
func (h LengthHeuristic) CountTokens(m provider.Message) int {
	bp := h.BytesPerToken
	if bp <= 0 {
		bp = 4
	}
	bytes := len(m.Content) + len(m.ToolCallID)
	for _, tc := range m.ToolCalls {
		bytes += len(tc.ID) + len(tc.Name) + len(tc.Arguments)
	}
	if bytes == 0 {
		return 1 // every message costs at least one token of envelope.
	}
	return (bytes + bp - 1) / bp
}

// Truncate drops the oldest non-system messages until the total token
// budget is at or below maxTokens. The first message is preserved if
// it is a system message (so the model still sees its instructions).
// Multiple leading system messages are NOT all preserved; only the
// first one is.
//
// The system prompt is never dropped, even when it alone exceeds
// maxTokens — the result in that case is a session containing only
// the system prompt (still over budget; the caller must shrink their
// prompt to fit).
//
// If a single non-system message exceeds maxTokens on its own, it is
// dropped along with everything before it. The session may be left
// **empty** (no Messages at all when there is no leading system
// message) or with only the system prompt. Callers persisting large
// tool results should size their budget with that in mind, and
// callers who immediately persist after Truncate should be prepared
// to write out an empty conversation.
//
// If t is nil, [LengthHeuristic] is used.
func (s *Session) Truncate(maxTokens int, t Tokenizer) {
	if maxTokens <= 0 || len(s.Messages) == 0 {
		return
	}
	if t == nil {
		t = LengthHeuristic{}
	}

	// Identify a single leading system message to preserve.
	systemIdx := -1
	if len(s.Messages) > 0 && s.Messages[0].Role == provider.RoleSystem {
		systemIdx = 0
	}

	total := 0
	for _, m := range s.Messages {
		total += t.CountTokens(m)
	}
	if total <= maxTokens {
		return
	}

	// Drop oldest (after system, if any) until under budget.
	keepFrom := 0
	if systemIdx == 0 {
		keepFrom = 1
	}
	for total > maxTokens && keepFrom < len(s.Messages) {
		total -= t.CountTokens(s.Messages[keepFrom])
		keepFrom++
	}

	// Reassemble: system (if any) + tail starting at keepFrom.
	var kept []provider.Message
	if systemIdx == 0 {
		kept = append(kept, s.Messages[0])
	}
	if keepFrom < len(s.Messages) {
		kept = append(kept, s.Messages[keepFrom:]...)
	}
	s.Messages = kept
	s.Updated = time.Now().UTC()
}

// Wire envelope types. Each line in the persistence stream is one of
// these tagged objects; the leading "type" field discriminates.
const (
	envelopeHeader  = "session.header"
	envelopeMessage = "session.message"
)

type headerEnvelope struct {
	Type    string    `json:"type"`
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// messageEnvelope MUST mirror every field of [provider.Message] that
// belongs in a persisted session. Adding a field to provider.Message
// requires updating this envelope (and the Save / Load mappings) in
// the same change; otherwise the on-disk format silently loses data.
//
// Tool calls go through [toolCallEnvelope] rather than embedding the
// upstream [provider.ToolCall] directly because the upstream type has
// no JSON tags — relying on its default Pascal-case marshaling would
// freeze a wire format that no other reader could interoperate with.
type messageEnvelope struct {
	Type       string             `json:"type"`
	Role       provider.Role      `json:"role"`
	Content    string             `json:"content,omitempty"`
	ToolCalls  []toolCallEnvelope `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
}

// toolCallEnvelope mirrors [provider.ToolCall] with explicit
// snake_case JSON tags so the persisted format is stable and matches
// the OpenAI Chat Completions schema providers expect.
type toolCallEnvelope struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

func toolCallsToEnvelopes(tcs []provider.ToolCall) []toolCallEnvelope {
	if len(tcs) == 0 {
		return nil
	}
	out := make([]toolCallEnvelope, len(tcs))
	for i, tc := range tcs {
		out[i] = toolCallEnvelope{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
	}
	return out
}

func envelopesToToolCalls(envs []toolCallEnvelope) []provider.ToolCall {
	if len(envs) == 0 {
		return nil
	}
	out := make([]provider.ToolCall, len(envs))
	for i, e := range envs {
		out[i] = provider.ToolCall{ID: e.ID, Name: e.Name, Arguments: e.Arguments}
	}
	return out
}

// Save writes the session as JSON Lines: a header line followed by
// one message line per [provider.Message] in [Session.Messages].
func (s *Session) Save(w io.Writer) error {
	bw := bufio.NewWriter(w)
	header := headerEnvelope{
		Type: envelopeHeader, ID: s.ID, Created: s.Created, Updated: s.Updated,
	}
	if err := writeLine(bw, header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	for i, m := range s.Messages {
		env := messageEnvelope{
			Type:       envelopeMessage,
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  toolCallsToEnvelopes(m.ToolCalls),
			ToolCallID: m.ToolCallID,
		}
		if err := writeLine(bw, env); err != nil {
			return fmt.Errorf("write message %d: %w", i, err)
		}
	}
	return bw.Flush()
}

func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	_, err = w.Write([]byte{'\n'})
	return err
}

// Sentinel errors callers may match with [errors.Is].
var (
	// ErrFormat is returned when the on-disk format is malformed in a
	// way that prevents a usable session from being recovered.
	ErrFormat = errors.New("session: malformed persistence format")
)

// Load reads a session from JSON Lines. The first line must be a
// header envelope; subsequent lines are messages or unknown envelopes
// (which are skipped, so a future writer can add new envelope types
// without breaking forward compatibility).
func Load(r io.Reader) (*Session, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("read header: %w", err)
		}
		return nil, fmt.Errorf("%w: empty input", ErrFormat)
	}
	var head headerEnvelope
	if err := json.Unmarshal(scanner.Bytes(), &head); err != nil {
		return nil, fmt.Errorf("%w: header: %w", ErrFormat, err)
	}
	if head.Type != envelopeHeader {
		return nil, fmt.Errorf("%w: first envelope type = %q, want %q", ErrFormat, head.Type, envelopeHeader)
	}
	s := &Session{
		ID:      head.ID,
		Created: head.Created,
		Updated: head.Updated,
	}
	lineNo := 1
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrFormat, lineNo, err)
		}
		switch probe.Type {
		case envelopeMessage:
			var env messageEnvelope
			if err := json.Unmarshal(line, &env); err != nil {
				return nil, fmt.Errorf("%w: line %d: decode message: %w", ErrFormat, lineNo, err)
			}
			s.Messages = append(s.Messages, provider.Message{
				Role:       env.Role,
				Content:    env.Content,
				ToolCalls:  envelopesToToolCalls(env.ToolCalls),
				ToolCallID: env.ToolCallID,
			})
		case "":
			// A line whose JSON parses but lacks a "type" field is a
			// producer bug, not a forward-compat case. Loud-fail rather
			// than silently dropping data.
			return nil, fmt.Errorf("%w: line %d: envelope missing required \"type\" field", ErrFormat, lineNo)
		default:
			// Forward-compat: skip unknown (non-empty) types silently. A
			// future writer can introduce new types without breaking us.
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return s, nil
}
