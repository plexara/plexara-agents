package provider

import (
	"context"
	"errors"
	"sync"

	"github.com/plexara/plexara-agents/core/event"
)

// FakeScript is one scripted Stream call's behavior.
//
// If InitError is non-nil, Stream returns that error immediately. Otherwise
// the events are emitted in order on the returned channel and the channel
// is closed.
type FakeScript struct {
	Events    []event.Event
	InitError error
}

// FakeProvider is an in-memory [Provider] for tests.
//
// It plays a sequence of [FakeScript] entries: the first call to Stream
// uses the first entry, the second call uses the second, and so on.
// After the scripts are exhausted, Stream returns [ErrFakeExhausted].
//
// Captured calls are available via [FakeProvider.Calls] for assertions.
type FakeProvider struct {
	nameStr string

	mu      sync.Mutex
	scripts []FakeScript
	calls   []Request
	idx     int
}

// FakeOption configures a [FakeProvider].
type FakeOption func(*FakeProvider)

// WithFakeName overrides the provider name reported by [FakeProvider.Name].
func WithFakeName(name string) FakeOption {
	return func(f *FakeProvider) { f.nameStr = name }
}

// ErrFakeExhausted is returned by [FakeProvider.Stream] after all
// scripted entries have been consumed.
var ErrFakeExhausted = errors.New("provider: fake script exhausted")

// NewFake constructs a [FakeProvider] that plays scripts in order.
func NewFake(scripts []FakeScript, opts ...FakeOption) *FakeProvider {
	f := &FakeProvider{
		nameStr: "fake",
		scripts: scripts,
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

// Name returns the provider's name for logging.
func (f *FakeProvider) Name() string { return f.nameStr }

// Calls returns a copy of the requests received so far, in order.
func (f *FakeProvider) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Request, len(f.calls))
	copy(out, f.calls)
	return out
}

// Stream replays the next script in turn. If no scripts remain, it
// returns [ErrFakeExhausted].
func (f *FakeProvider) Stream(ctx context.Context, req Request) (<-chan event.Event, error) {
	f.mu.Lock()
	if f.idx >= len(f.scripts) {
		f.mu.Unlock()
		return nil, ErrFakeExhausted
	}
	script := f.scripts[f.idx]
	f.idx++
	f.calls = append(f.calls, req)
	f.mu.Unlock()

	if script.InitError != nil {
		return nil, script.InitError
	}

	out := make(chan event.Event, len(script.Events))
	go func() {
		defer close(out)
		for _, e := range script.Events {
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
