package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/plexara/plexara-agents/core/event"
	"github.com/plexara/plexara-agents/core/provider"
)

func TestFakeProvider_RepliesInOrder(t *testing.T) {
	t.Parallel()

	want1 := []event.Event{
		event.TextDelta{Text: "hi"},
		event.Finish{Reason: event.FinishReasonStop},
	}
	want2 := []event.Event{
		event.TextDelta{Text: "again"},
		event.Finish{Reason: event.FinishReasonStop},
	}

	p := provider.NewFake([]provider.FakeScript{
		{Events: want1},
		{Events: want2},
	})

	got1 := drain(t, p, provider.Request{Model: "m1"})
	got2 := drain(t, p, provider.Request{Model: "m2"})

	if !sameEvents(got1, want1) {
		t.Errorf("first call events mismatch: got %v want %v", got1, want1)
	}
	if !sameEvents(got2, want2) {
		t.Errorf("second call events mismatch: got %v want %v", got2, want2)
	}

	calls := p.Calls()
	if len(calls) != 2 || calls[0].Model != "m1" || calls[1].Model != "m2" {
		t.Errorf("Calls() = %#v; want [{m1}, {m2}]", calls)
	}
}

func TestFakeProvider_InitError(t *testing.T) {
	t.Parallel()

	want := errors.New("nope")
	p := provider.NewFake([]provider.FakeScript{{InitError: want}})

	_, err := p.Stream(t.Context(), provider.Request{})
	if !errors.Is(err, want) {
		t.Errorf("Stream err = %v; want %v", err, want)
	}
}

func TestFakeProvider_Exhausted(t *testing.T) {
	t.Parallel()

	p := provider.NewFake([]provider.FakeScript{
		{Events: []event.Event{event.Finish{}}},
	})

	_ = drain(t, p, provider.Request{})
	_, err := p.Stream(t.Context(), provider.Request{})
	if !errors.Is(err, provider.ErrFakeExhausted) {
		t.Errorf("Stream err = %v; want ErrFakeExhausted", err)
	}
}

func TestFakeProvider_WithName(t *testing.T) {
	t.Parallel()

	p := provider.NewFake(nil, provider.WithFakeName("custom"))
	if got := p.Name(); got != "custom" {
		t.Errorf("Name() = %q; want %q", got, "custom")
	}
}

func TestFakeProvider_DefaultName(t *testing.T) {
	t.Parallel()

	if got := provider.NewFake(nil).Name(); got != "fake" {
		t.Errorf("Name() default = %q; want %q", got, "fake")
	}
}

func TestFakeProvider_ContextCancelled(t *testing.T) {
	t.Parallel()

	p := provider.NewFake([]provider.FakeScript{
		{Events: []event.Event{event.TextDelta{Text: "a"}, event.TextDelta{Text: "b"}}},
	})

	ctx, cancel := context.WithCancel(t.Context())
	ch, err := p.Stream(ctx, provider.Request{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	cancel()

	// Drain — channel must close (possibly without delivering all events).
	for range ch { //nolint:revive // Intentional empty drain.
	}
}

// drain reads all events from a Stream call until the channel closes.
func drain(t *testing.T, p provider.Provider, req provider.Request) []event.Event {
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

func sameEvents(a, b []event.Event) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		// Concrete-type comparison is fine for the simple values in
		// these tests (no slices, no pointers).
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
