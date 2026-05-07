package session_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/plexara/plexara-agents/core/session"
)

// FuzzLoad covers the JSON Lines decoder against arbitrary input.
// Contract: Load never panics; if it returns no error, the loaded
// Session must round-trip through Save back to a Load that returns
// the same Session.
//
// Fuzz seeds use a fixed timestamp (not time.Now()) so the corpus
// stays reproducible across runs. A regression caught today must
// still be reproducible by the same seed string tomorrow.
func FuzzLoad(f *testing.F) {
	const fixedHeader = `{"type":"session.header","id":"x","created":"2026-01-01T00:00:00Z","updated":"2026-01-01T00:00:00Z"}` + "\n"
	seeds := []string{
		fixedHeader + `{"type":"session.message","role":"user","content":"hi"}` + "\n",
		fixedHeader,
		fixedHeader + `{"type":"session.future_kind","x":1}` + "\n",
		``,
		`{"type":"session.header","id":"x"}` + "\n",
		`{"type":"session.message"}` + "\n",
		`{`,
		"\x00\x01\x02",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		s, err := session.Load(strings.NewReader(raw))
		if err != nil {
			return
		}
		// Round-trip: Save then Load again must yield equivalent session.
		var buf bytes.Buffer
		if err := s.Save(&buf); err != nil {
			t.Fatalf("Save after Load: %v", err)
		}
		s2, err := session.Load(&buf)
		if err != nil {
			t.Fatalf("Load after Save: %v\ninput: %q", err, raw)
		}
		if s2.ID != s.ID || len(s2.Messages) != len(s.Messages) {
			t.Errorf("round-trip diverged: ID %q vs %q, messages %d vs %d",
				s.ID, s2.ID, len(s.Messages), len(s2.Messages))
		}
	})
}
