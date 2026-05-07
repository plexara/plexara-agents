package session_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/plexara/plexara-agents/core/provider"
	"github.com/plexara/plexara-agents/core/session"
)

func TestNew(t *testing.T) {
	t.Parallel()

	s := session.New("abc")
	if s.ID != "abc" {
		t.Errorf("ID = %q; want abc", s.ID)
	}
	if s.Created.IsZero() || s.Updated.IsZero() {
		t.Errorf("timestamps are zero")
	}
	if !s.Created.Equal(s.Updated) {
		t.Errorf("Created != Updated at construction")
	}
	if s.Messages != nil {
		t.Errorf("Messages = %v; want nil", s.Messages)
	}
}

func TestAppend_BumpsUpdated(t *testing.T) {
	t.Parallel()

	s := session.New("x")
	original := s.Updated
	time.Sleep(2 * time.Millisecond)
	s.Append(provider.Message{Role: provider.RoleUser, Content: "hi"})
	if !s.Updated.After(original) {
		t.Errorf("Updated did not advance: was %v, now %v", original, s.Updated)
	}
	if len(s.Messages) != 1 {
		t.Errorf("len(Messages) = %d; want 1", len(s.Messages))
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	t.Parallel()

	s := session.New("session-1")
	s.Append(provider.Message{Role: provider.RoleSystem, Content: "be terse"})
	s.Append(provider.Message{Role: provider.RoleUser, Content: "what's the weather"})
	s.Append(provider.Message{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{
			ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"NYC"}`),
		}},
	})
	s.Append(provider.Message{Role: provider.RoleTool, ToolCallID: "call_1", Content: "72F sunny"})

	var buf bytes.Buffer
	if err := s.Save(&buf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := session.Load(&buf)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.ID != s.ID {
		t.Errorf("ID = %q; want %q", loaded.ID, s.ID)
	}
	if !loaded.Created.Equal(s.Created) {
		t.Errorf("Created = %v; want %v", loaded.Created, s.Created)
	}
	if len(loaded.Messages) != len(s.Messages) {
		t.Fatalf("len(Messages) = %d; want %d", len(loaded.Messages), len(s.Messages))
	}
	for i := range s.Messages {
		got := loaded.Messages[i]
		want := s.Messages[i]
		if got.Role != want.Role || got.Content != want.Content || got.ToolCallID != want.ToolCallID {
			t.Errorf("Messages[%d] mismatch: got %#v want %#v", i, got, want)
		}
		if len(got.ToolCalls) != len(want.ToolCalls) {
			t.Errorf("Messages[%d] ToolCalls len mismatch: %d vs %d", i, len(got.ToolCalls), len(want.ToolCalls))
		}
	}
}

func TestLoad_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "non_header_first", in: `{"type":"session.message","role":"user"}` + "\n"},
		{name: "header_not_json", in: "not-json\n"},
		{name: "garbage_line", in: header() + "not-json\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := session.Load(strings.NewReader(tt.in))
			if err == nil {
				t.Fatalf("Load(%q) returned nil; want error", tt.name)
			}
			if !errors.Is(err, session.ErrFormat) {
				t.Errorf("err = %v; want errors.Is ErrFormat", err)
			}
		})
	}
}

func TestLoad_SkipsUnknownEnvelopes(t *testing.T) {
	t.Parallel()

	in := header() +
		`{"type":"session.message","role":"user","content":"hi"}` + "\n" +
		`{"type":"session.future_kind","payload":{"x":1}}` + "\n" +
		`{"type":"session.message","role":"assistant","content":"hello"}` + "\n"

	s, err := session.Load(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Messages) != 2 {
		t.Errorf("len(Messages) = %d; want 2 (unknown envelope dropped)", len(s.Messages))
	}
}

// TestLoad_RejectsMissingType pins the contract that unknown FUTURE
// envelope types are skipped silently for forward compat, but a line
// with NO "type" field is a producer bug and must be loud.
func TestLoad_RejectsMissingType(t *testing.T) {
	t.Parallel()

	in := header() + `{"role":"user","content":"hi"}` + "\n"
	_, err := session.Load(strings.NewReader(in))
	if err == nil {
		t.Fatal("Load returned nil; want ErrFormat for missing type field")
	}
	if !errors.Is(err, session.ErrFormat) {
		t.Errorf("err = %v; want errors.Is ErrFormat", err)
	}
	if !strings.Contains(err.Error(), "missing required") {
		t.Errorf("err = %v; want it to mention the missing field", err)
	}
}

// TestSave_PersistedFormatUsesSnakeCase pins the on-disk wire format
// so future readers (other tools, other languages) interoperate. The
// envelope-mirror types in session.go MUST keep snake_case keys; this
// test fails if anyone removes a JSON tag.
func TestSave_PersistedFormatUsesSnakeCase(t *testing.T) {
	t.Parallel()

	s := session.New("x")
	s.Append(provider.Message{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{
			ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"NYC"}`),
		}},
	})
	var buf bytes.Buffer
	if err := s.Save(&buf); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out := buf.String()

	// Required snake_case keys somewhere in the output.
	for _, want := range []string{
		`"type":"session.header"`,
		`"created":"`, `"updated":"`,
		`"type":"session.message"`,
		`"role":"assistant"`,
		`"tool_calls":[{`,
		`"id":"call_1"`,
		`"name":"get_weather"`,
		`"arguments":{"city":"NYC"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Save output missing %q; full=%s", want, out)
		}
	}
	// Forbid Pascal-case leakage from upstream provider.ToolCall.
	for _, bad := range []string{`"ID":`, `"Name":`, `"Arguments":`} {
		if strings.Contains(out, bad) {
			t.Errorf("Save output leaked Pascal-case key %q; full=%s", bad, out)
		}
	}
}

func TestLoad_SkipsBlankLines(t *testing.T) {
	t.Parallel()

	in := header() + "\n" +
		`{"type":"session.message","role":"user","content":"hi"}` + "\n\n"

	s, err := session.Load(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Messages) != 1 {
		t.Errorf("len(Messages) = %d; want 1", len(s.Messages))
	}
}

func TestLengthHeuristic_DefaultsAndZero(t *testing.T) {
	t.Parallel()

	h := session.LengthHeuristic{}
	if got := h.CountTokens(provider.Message{}); got != 1 {
		t.Errorf("empty message tokens = %d; want 1 (envelope cost)", got)
	}
	// 16-byte content with default BytesPerToken=4 -> 4 tokens.
	if got := h.CountTokens(provider.Message{Content: strings.Repeat("a", 16)}); got != 4 {
		t.Errorf("16-byte tokens = %d; want 4", got)
	}
	// Custom ratio.
	h2 := session.LengthHeuristic{BytesPerToken: 8}
	if got := h2.CountTokens(provider.Message{Content: strings.Repeat("a", 16)}); got != 2 {
		t.Errorf("16-byte at 8bpt = %d; want 2", got)
	}
}

func TestTruncate_DropsOldestNonSystem(t *testing.T) {
	t.Parallel()

	// LengthHeuristic{} = ceil(content-bytes / 4):
	//   "be terse" (8)   -> 2 tokens
	//   "first"   (5)    -> 2 tokens
	//   "answer-1" (8)   -> 2 tokens
	//   "second"  (6)    -> 2 tokens
	//   "answer-2" (8)   -> 2 tokens
	// Total 10. A budget of 7 forces dropping 2 of the 4 non-system
	// messages, oldest-first.
	s := session.New("x")
	s.Append(provider.Message{Role: provider.RoleSystem, Content: "be terse"})
	s.Append(provider.Message{Role: provider.RoleUser, Content: "first"})
	s.Append(provider.Message{Role: provider.RoleAssistant, Content: "answer-1"})
	s.Append(provider.Message{Role: provider.RoleUser, Content: "second"})
	s.Append(provider.Message{Role: provider.RoleAssistant, Content: "answer-2"})

	s.Truncate(7, session.LengthHeuristic{})

	if len(s.Messages) < 2 || s.Messages[0].Role != provider.RoleSystem {
		t.Fatalf("expected system + at least one tail message; got %+v", roles(s.Messages))
	}
	// The "first" user message must be dropped.
	for _, m := range s.Messages {
		if m.Content == "first" {
			t.Errorf("oldest user message survived truncation")
		}
	}
}

func TestTruncate_NoOpUnderBudget(t *testing.T) {
	t.Parallel()

	s := session.New("x")
	s.Append(provider.Message{Role: provider.RoleUser, Content: "hi"})
	s.Truncate(1000, session.LengthHeuristic{})
	if len(s.Messages) != 1 {
		t.Errorf("under-budget Truncate dropped messages: %v", roles(s.Messages))
	}
}

func TestTruncate_ZeroBudgetIsNoOp(t *testing.T) {
	t.Parallel()

	s := session.New("x")
	s.Append(provider.Message{Role: provider.RoleUser, Content: "hi"})
	s.Truncate(0, session.LengthHeuristic{})
	s.Truncate(-5, session.LengthHeuristic{})
	if len(s.Messages) != 1 {
		t.Errorf("non-positive budget should not mutate; got %d msgs", len(s.Messages))
	}
}

func TestTruncate_NilTokenizerDefaults(t *testing.T) {
	t.Parallel()

	s := session.New("x")
	s.Append(provider.Message{Role: provider.RoleUser, Content: "hi"})
	// Should not panic; behaves like the default LengthHeuristic.
	s.Truncate(1000, nil)
	if len(s.Messages) != 1 {
		t.Errorf("nil tokenizer changed message count")
	}
}

func TestTruncate_PreservesSystemEvenWhenSingleMessage(t *testing.T) {
	t.Parallel()

	s := session.New("x")
	s.Append(provider.Message{Role: provider.RoleSystem, Content: strings.Repeat("a", 1000)})
	s.Append(provider.Message{Role: provider.RoleUser, Content: "hi"})

	// Budget far below system message size — system should still survive.
	s.Truncate(5, session.LengthHeuristic{})

	if len(s.Messages) == 0 || s.Messages[0].Role != provider.RoleSystem {
		t.Errorf("system message was dropped under tiny budget; got %v", roles(s.Messages))
	}
}

// --- helpers ---

func header() string {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return `{"type":"session.header","id":"x","created":"` + now + `","updated":"` + now + `"}` + "\n"
}

func roles(ms []provider.Message) []provider.Role {
	out := make([]provider.Role, len(ms))
	for i, m := range ms {
		out[i] = m.Role
	}
	return out
}
