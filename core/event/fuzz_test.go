package event_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/plexara/plexara-agents/core/event"
)

// FuzzDecode exercises the discriminator dispatch and per-variant
// unmarshaling against arbitrary input. The contract: Decode either
// returns an event whose marshaled form decodes back to an equivalent
// event, or it returns an error. It must never panic.
func FuzzDecode(f *testing.F) {
	seeds := []string{
		`{"type":"text_delta","text":"hi"}`,
		`{"type":"tool_call_request","id":"x","name":"y","arguments":{}}`,
		`{"type":"tool_call_result","id":"x","content":[]}`,
		`{"type":"finish","reason":"stop","usage":{}}`,
		`{"type":"error","msg":"boom"}`,
		`{"type":"unknown"}`,
		``,
		`{`,
		`null`,
		`{"type":"text_delta","text":null}`,
		`{"type":"finish","reason":42}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		evt, err := event.Decode([]byte(raw))
		if err != nil {
			// Errors are expected for bad input; nothing else to verify.
			return
		}
		// Round-trip: a decoded event must remarshal cleanly.
		out, err := json.Marshal(evt)
		if err != nil {
			t.Fatalf("Marshal of decoded event failed: %v\ninput: %q", err, raw)
		}
		// And the remarshaled form must decode again to the same shape.
		evt2, err := event.Decode(out)
		if err != nil {
			t.Fatalf("Decode(Marshal(Decode(%q))) failed: %v", raw, err)
		}
		out2, err := json.Marshal(evt2)
		if err != nil {
			t.Fatalf("Marshal of re-decoded event failed: %v", err)
		}
		if string(out) != string(out2) {
			t.Errorf("round-trip diverged:\n  first:  %s\n  second: %s", out, out2)
		}
	})
}

// FuzzDecodeNeverPanics is a narrower contract used in CI to catch
// panics in dispatch/unmarshal. Faster than the round-trip fuzz.
func FuzzDecodeNeverPanics(f *testing.F) {
	f.Add(``)
	f.Add(`{}`)
	f.Add(`{"type":"text_delta"}`)
	f.Fuzz(func(_ *testing.T, raw string) {
		_, err := event.Decode([]byte(raw))
		// Decode must not panic on any input. errors are fine.
		_ = err
		// Cross-check ErrUnknownType is well-formed: errors.Is must not panic.
		if err != nil {
			_ = errors.Is(err, event.ErrUnknownType)
		}
	})
}
