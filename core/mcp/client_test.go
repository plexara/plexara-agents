package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/plexara/plexara-agents/core/mcp"
)

func TestSplitName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		in         string
		wantServer string
		wantBare   string
		wantErr    bool
	}{
		{name: "simple", in: "plexara__datahub_search", wantServer: "plexara", wantBare: "datahub_search"},
		{name: "tool_with_separator", in: "plexara__a__b", wantServer: "plexara", wantBare: "a__b"},
		{name: "empty", in: "", wantErr: true},
		{name: "no_separator", in: "datahub_search", wantErr: true},
		{name: "leading_separator", in: "__tool", wantErr: true},
		{name: "trailing_separator", in: "server__", wantErr: true},
		{name: "only_separator", in: "__", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotServer, gotBare, err := mcp.SplitName(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Errorf("SplitName(%q) = %q, %q, nil; want error", tt.in, gotServer, gotBare)
				} else if !errors.Is(err, mcp.ErrInvalidName) {
					t.Errorf("SplitName(%q) err = %v; want errors.Is ErrInvalidName", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitName(%q): unexpected err: %v", tt.in, err)
			}
			if gotServer != tt.wantServer || gotBare != tt.wantBare {
				t.Errorf("SplitName(%q) = %q, %q; want %q, %q", tt.in, gotServer, gotBare, tt.wantServer, tt.wantBare)
			}
		})
	}
}

func TestJoinNameRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		server, bare string
	}{
		{"plexara", "datahub_search"},
		{"acme", "trino_query"},
		{"a", "b"},
		{"server-with-dashes", "tool_with_underscores"},
	}
	for _, tt := range tests {
		joined := mcp.JoinName(tt.server, tt.bare)
		gotServer, gotBare, err := mcp.SplitName(joined)
		if err != nil {
			t.Errorf("SplitName(JoinName(%q,%q)) = err: %v", tt.server, tt.bare, err)
			continue
		}
		if gotServer != tt.server || gotBare != tt.bare {
			t.Errorf("round trip %q,%q -> %q -> %q,%q", tt.server, tt.bare, joined, gotServer, gotBare)
		}
	}
}

func TestNew_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfgs []mcp.ServerConfig
	}{
		{name: "no_servers", cfgs: nil},
		{name: "empty_name", cfgs: []mcp.ServerConfig{{Name: "", Transport: mcp.TransportStdio, Endpoint: "x"}}},
		{name: "name_with_separator", cfgs: []mcp.ServerConfig{{Name: "a__b", Transport: mcp.TransportStdio, Endpoint: "x"}}},
		{name: "duplicate_name", cfgs: []mcp.ServerConfig{
			{Name: "x", Transport: mcp.TransportStdio, Endpoint: "a"},
			{Name: "x", Transport: mcp.TransportStdio, Endpoint: "b"},
		}},
		{name: "empty_endpoint", cfgs: []mcp.ServerConfig{{Name: "x", Transport: mcp.TransportStdio, Endpoint: ""}}},
		{name: "unknown_transport", cfgs: []mcp.ServerConfig{{Name: "x", Transport: "smoke-signal", Endpoint: "x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := mcp.New(tt.cfgs)
			if err == nil {
				t.Fatalf("New(%q) returned nil error", tt.name)
			}
			if !errors.Is(err, mcp.ErrConfig) {
				t.Errorf("err = %v; want errors.Is ErrConfig", err)
			}
		})
	}
}

func TestNew_OK(t *testing.T) {
	t.Parallel()

	c, err := mcp.New([]mcp.ServerConfig{
		{Name: "a", Transport: mcp.TransportStdio, Endpoint: "echo"},
		{Name: "b", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://x"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil client without error")
	}
}

// fakeServer returns an in-memory client transport wired to a real
// mcp.Server with the given tools registered. The server's connect
// goroutine is registered with the test's WaitGroup via t.Cleanup so
// that the test fully waits for it before exiting — without this,
// the goroutine could call t.Logf after the test had completed and
// crash with "Log in goroutine after test has completed".
func fakeServer(t *testing.T, name string, tools []sdkmcp.Tool) sdkmcp.Transport {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: name, Version: "v0.0.0"}, nil)
	for _, tt := range tools {
		sdkmcp.AddTool(server, &tt, func(_ context.Context, _ *sdkmcp.CallToolRequest, args map[string]any) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{
					&sdkmcp.TextContent{Text: "called " + tt.Name + " with " + sprintArgs(args)},
				},
			}, nil, nil
		})
	}
	clientT, serverT := sdkmcp.NewInMemoryTransports()
	var wg sync.WaitGroup
	wg.Add(1)
	connectErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		_, err := server.Connect(context.Background(), serverT, nil)
		connectErr <- err
	}()
	t.Cleanup(func() {
		wg.Wait()
		if err := <-connectErr; err != nil {
			t.Logf("server.Connect: %v", err)
		}
	})
	return clientT
}

func sprintArgs(args map[string]any) string {
	b, _ := json.Marshal(args)
	return string(b)
}

func TestConnect_AggregatesCatalog(t *testing.T) {
	t.Parallel()

	transports := map[string]sdkmcp.Transport{
		"plexara": fakeServer(t, "plexara", []sdkmcp.Tool{
			{Name: "datahub_search", Description: "search the data hub"},
			{Name: "trino_query", Description: "run sql"},
		}),
		"fs": fakeServer(t, "fs", []sdkmcp.Tool{
			{Name: "read_file", Description: "read a file"},
		}),
	}
	dialer := func(_ context.Context, cfg mcp.ServerConfig) (sdkmcp.Transport, error) {
		tr, ok := transports[cfg.Name]
		if !ok {
			t.Fatalf("unexpected server name %q", cfg.Name)
		}
		return tr, nil
	}

	c, err := mcp.New([]mcp.ServerConfig{
		{Name: "plexara", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://stub"},
		{Name: "fs", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://stub"},
	}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	cat := c.Catalog()
	if got, want := len(cat.Tools), 3; got != want {
		t.Fatalf("len(cat.Tools) = %d; want %d", got, want)
	}

	// Confirm namespacing.
	gotNames := map[string]bool{}
	for _, tool := range cat.Tools {
		gotNames[tool.Name] = true
	}
	for _, want := range []string{"plexara__datahub_search", "plexara__trino_query", "fs__read_file"} {
		if !gotNames[want] {
			t.Errorf("missing tool %q in catalog: %v", want, gotNames)
		}
	}

	// ToolsByServer index.
	if got := len(cat.ToolsByServer["plexara"]); got != 2 {
		t.Errorf("ToolsByServer[plexara] = %d tools; want 2", got)
	}
	if got := len(cat.ToolsByServer["fs"]); got != 1 {
		t.Errorf("ToolsByServer[fs] = %d tools; want 1", got)
	}
}

func TestCall_Routes(t *testing.T) {
	t.Parallel()

	transports := map[string]sdkmcp.Transport{
		"plexara": fakeServer(t, "plexara", []sdkmcp.Tool{
			{Name: "datahub_search", Description: "search"},
		}),
	}
	dialer := func(_ context.Context, cfg mcp.ServerConfig) (sdkmcp.Transport, error) {
		return transports[cfg.Name], nil
	}

	c, err := mcp.New([]mcp.ServerConfig{
		{Name: "plexara", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://stub"},
	}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	res, err := c.Call(ctx, mcp.ToolCall{
		Name:      "plexara__datahub_search",
		Arguments: json.RawMessage(`{"q":"orders"}`),
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError {
		t.Errorf("IsError = true; want false")
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("unexpected content %#v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "datahub_search") {
		t.Errorf("response %q did not echo tool name", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, `"q":"orders"`) {
		t.Errorf("response %q did not echo args", res.Content[0].Text)
	}
}

func TestCall_UnknownServer(t *testing.T) {
	t.Parallel()

	transports := map[string]sdkmcp.Transport{
		"plexara": fakeServer(t, "plexara", nil),
	}
	dialer := func(_ context.Context, cfg mcp.ServerConfig) (sdkmcp.Transport, error) {
		return transports[cfg.Name], nil
	}

	c, err := mcp.New([]mcp.ServerConfig{
		{Name: "plexara", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://stub"},
	}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Call(t.Context(), mcp.ToolCall{Name: "missing__tool"})
	if !errors.Is(err, mcp.ErrUnknownServer) {
		t.Errorf("err = %v; want errors.Is ErrUnknownServer", err)
	}
}

func TestCall_InvalidName(t *testing.T) {
	t.Parallel()

	transports := map[string]sdkmcp.Transport{
		"plexara": fakeServer(t, "plexara", nil),
	}
	dialer := func(_ context.Context, cfg mcp.ServerConfig) (sdkmcp.Transport, error) {
		return transports[cfg.Name], nil
	}

	c, err := mcp.New([]mcp.ServerConfig{
		{Name: "plexara", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://stub"},
	}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Call(t.Context(), mcp.ToolCall{Name: "no-separator"})
	if !errors.Is(err, mcp.ErrInvalidName) {
		t.Errorf("err = %v; want errors.Is ErrInvalidName", err)
	}
}

func TestConnect_ParallelFailureClosesAll(t *testing.T) {
	t.Parallel()

	good := fakeServer(t, "good", []sdkmcp.Tool{{Name: "t", Description: "ok"}})
	dialer := func(_ context.Context, cfg mcp.ServerConfig) (sdkmcp.Transport, error) {
		switch cfg.Name {
		case "good":
			return good, nil
		case "bad":
			return nil, errors.New("dial refused")
		default:
			return nil, errors.New("unexpected")
		}
	}

	c, err := mcp.New([]mcp.ServerConfig{
		{Name: "good", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://x"},
		{Name: "bad", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://x"},
	}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = c.Connect(t.Context())
	if err == nil {
		t.Fatal("Connect returned nil; want error from bad server")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("err = %v; want it to mention the bad server", err)
	}
	// After a failed Connect, the client must be left in an unusable
	// state — empty catalog and Call routing returning ErrUnknownServer.
	if got := c.Catalog().Tools; len(got) != 0 {
		t.Errorf("Catalog().Tools = %d entries; want 0 after failed Connect", len(got))
	}
	if _, callErr := c.Call(t.Context(), mcp.ToolCall{Name: "good__t"}); !errors.Is(callErr, mcp.ErrUnknownServer) {
		t.Errorf("Call after failed Connect: err = %v; want ErrUnknownServer", callErr)
	}
}

func TestBackoff_Bounded(t *testing.T) {
	t.Parallel()

	b := mcp.Backoff{Base: 100 * time.Millisecond, Cap: 1 * time.Second, MaxAttempts: 3}
	for attempt := range 10 {
		d := b.Delay(attempt)
		if d < 0 || d > b.Cap {
			t.Errorf("attempt %d: delay %v out of [0, %v]", attempt, d, b.Cap)
		}
	}
}

// TestBackoff_NegativeAttemptIsClamped guards Delay against negative
// shift counts. Before the clamp was added, Delay(-1) panicked at
// runtime with "negative shift amount".
func TestBackoff_NegativeAttemptIsClamped(t *testing.T) {
	t.Parallel()

	b := mcp.Backoff{Base: 100 * time.Millisecond, Cap: 1 * time.Second}
	d := b.Delay(-1)
	if d < 0 || d > b.Cap {
		t.Errorf("Delay(-1) = %v; want in [0, %v]", d, b.Cap)
	}
}

// TestBackoff_GrowsAcrossAttempts confirms Delay actually scales —
// without growth, a constant zero would still pass TestBackoff_Bounded.
// The mean over many samples at high attempts must exceed the mean at
// attempt 0.
func TestBackoff_GrowsAcrossAttempts(t *testing.T) {
	t.Parallel()

	b := mcp.Backoff{Base: 100 * time.Millisecond, Cap: 1 * time.Second}
	const samples = 200

	mean := func(attempt int) time.Duration {
		var total time.Duration
		for range samples {
			total += b.Delay(attempt)
		}
		return total / samples
	}

	low := mean(0)
	high := mean(5)
	if high <= low {
		t.Errorf("mean Delay(5)=%v not greater than mean Delay(0)=%v; backoff is not growing", high, low)
	}
	// At attempts >> log2(Cap/Base), the saturated value should sit
	// near Cap/2 with full jitter. Sanity-check that high-mean is
	// within an order of magnitude of Cap/2.
	if high < b.Cap/8 {
		t.Errorf("mean Delay(5)=%v much smaller than Cap/8=%v; jitter range looks broken", high, b.Cap/8)
	}
}

func TestBackoff_Defaults(t *testing.T) {
	t.Parallel()

	b := mcp.Backoff{}
	d := b.Delay(0)
	if d < 0 || d > 30*time.Second {
		t.Errorf("default Delay(0) = %v; out of bounds", d)
	}
}

func TestResources_UnknownServer(t *testing.T) {
	t.Parallel()

	transports := map[string]sdkmcp.Transport{
		"x": fakeServer(t, "x", nil),
	}
	dialer := func(_ context.Context, cfg mcp.ServerConfig) (sdkmcp.Transport, error) {
		return transports[cfg.Name], nil
	}

	c, err := mcp.New([]mcp.ServerConfig{{Name: "x", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://x"}}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Resources(t.Context(), "missing")
	if !errors.Is(err, mcp.ErrUnknownServer) {
		t.Errorf("Resources err = %v; want ErrUnknownServer", err)
	}
	_, err = c.Prompts(t.Context(), "missing")
	if !errors.Is(err, mcp.ErrUnknownServer) {
		t.Errorf("Prompts err = %v; want ErrUnknownServer", err)
	}
}

func TestConnect_TwiceErrors(t *testing.T) {
	t.Parallel()

	transports := map[string]sdkmcp.Transport{
		"x": fakeServer(t, "x", nil),
	}
	dialer := func(_ context.Context, cfg mcp.ServerConfig) (sdkmcp.Transport, error) {
		return transports[cfg.Name], nil
	}

	c, err := mcp.New([]mcp.ServerConfig{{Name: "x", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://x"}}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if err := c.Connect(t.Context()); err == nil {
		t.Error("second Connect returned nil; want error")
	}
}

// TestConnect_FailedConnectLatchesGate pins the contract: a Client
// that failed to Connect is terminal — a retry returns ErrConfig.
// Callers must construct a fresh Client to retry.
func TestConnect_FailedConnectLatchesGate(t *testing.T) {
	t.Parallel()

	dialer := func(_ context.Context, _ mcp.ServerConfig) (sdkmcp.Transport, error) {
		return nil, errors.New("dial refused")
	}
	c, err := mcp.New([]mcp.ServerConfig{
		{Name: "x", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://x"},
	}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Connect(t.Context()); err == nil {
		t.Fatal("first Connect returned nil; want dial error")
	}
	if err := c.Connect(t.Context()); !errors.Is(err, mcp.ErrConfig) {
		t.Errorf("second Connect after failure: err = %v; want errors.Is ErrConfig", err)
	}
}

// TestConnect_AfterCloseRejected pins the documented lifecycle: a
// closed Client refuses Connect, and Close also clears the catalog.
func TestConnect_AfterCloseRejected(t *testing.T) {
	t.Parallel()

	transports := map[string]sdkmcp.Transport{
		"x": fakeServer(t, "x", []sdkmcp.Tool{{Name: "tool"}}),
	}
	dialer := func(_ context.Context, cfg mcp.ServerConfig) (sdkmcp.Transport, error) {
		return transports[cfg.Name], nil
	}

	c, err := mcp.New([]mcp.ServerConfig{{Name: "x", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://x"}}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := len(c.Catalog().Tools); got != 1 {
		t.Fatalf("pre-Close Catalog.Tools = %d; want 1", got)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After Close, Catalog must be empty, Call must report
	// ErrUnknownServer, and Connect must report ErrConfig.
	if got := len(c.Catalog().Tools); got != 0 {
		t.Errorf("post-Close Catalog.Tools = %d; want 0 (catalog must be cleared)", got)
	}
	if _, err := c.Call(t.Context(), mcp.ToolCall{Name: "x__tool"}); !errors.Is(err, mcp.ErrUnknownServer) {
		t.Errorf("post-Close Call err = %v; want ErrUnknownServer", err)
	}
	if err := c.Connect(t.Context()); !errors.Is(err, mcp.ErrConfig) {
		t.Errorf("post-Close Connect err = %v; want ErrConfig", err)
	}
}

func TestCatalog_DeterministicOrdering(t *testing.T) {
	t.Parallel()

	// Two servers with multiple tools each, registered in arbitrary
	// order. The catalog must come out sorted by (server, bare-tool-
	// name) every time, even though the underlying sessions map is
	// randomized.
	transports := map[string]sdkmcp.Transport{
		"alpha": fakeServer(t, "alpha", []sdkmcp.Tool{
			{Name: "zebra"},
			{Name: "antelope"},
			{Name: "moose"},
		}),
		"bravo": fakeServer(t, "bravo", []sdkmcp.Tool{
			{Name: "yak"},
			{Name: "elk"},
		}),
	}
	dialer := func(_ context.Context, cfg mcp.ServerConfig) (sdkmcp.Transport, error) {
		return transports[cfg.Name], nil
	}

	c, err := mcp.New([]mcp.ServerConfig{
		{Name: "alpha", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://x"},
		{Name: "bravo", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://x"},
	}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	want := []string{
		"alpha__antelope", "alpha__moose", "alpha__zebra",
		"bravo__elk", "bravo__yak",
	}
	cat := c.Catalog()
	got := make([]string, len(cat.Tools))
	for i, tool := range cat.Tools {
		got[i] = tool.Name
	}
	if len(got) != len(want) {
		t.Fatalf("len(tools) = %d; want %d (full=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tools[%d] = %q; want %q (full got=%v)", i, got[i], want[i], got)
		}
	}
}

// fakeServerWithPageSize wires a server whose ListTools/ListResources/
// ListPrompts pagination cap is set explicitly. Used to verify the
// client iterators traverse multiple pages.
//
// Same goroutine-vs-test-end discipline as fakeServer.
func fakeServerWithPageSize(t *testing.T, name string, pageSize int, tools []sdkmcp.Tool) sdkmcp.Transport {
	t.Helper()
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: name, Version: "v0.0.0"},
		&sdkmcp.ServerOptions{PageSize: pageSize},
	)
	for _, tt := range tools {
		sdkmcp.AddTool(server, &tt, func(_ context.Context, _ *sdkmcp.CallToolRequest, args map[string]any) (*sdkmcp.CallToolResult, any, error) {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{
					&sdkmcp.TextContent{Text: "ok " + tt.Name + " " + sprintArgs(args)},
				},
			}, nil, nil
		})
	}
	clientT, serverT := sdkmcp.NewInMemoryTransports()
	var wg sync.WaitGroup
	wg.Add(1)
	connectErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		_, err := server.Connect(context.Background(), serverT, nil)
		connectErr <- err
	}()
	t.Cleanup(func() {
		wg.Wait()
		if err := <-connectErr; err != nil {
			t.Logf("server.Connect: %v", err)
		}
	})
	return clientT
}

func TestConnect_PaginatesAcrossMultiplePages(t *testing.T) {
	t.Parallel()

	// Force the server to break tools across pages of size 1 so the
	// client iterator must traverse multiple pages to enumerate them.
	transports := map[string]sdkmcp.Transport{
		"big": fakeServerWithPageSize(t, "big", 1, []sdkmcp.Tool{
			{Name: "tool_a"},
			{Name: "tool_b"},
			{Name: "tool_c"},
			{Name: "tool_d"},
			{Name: "tool_e"},
		}),
	}
	dialer := func(_ context.Context, cfg mcp.ServerConfig) (sdkmcp.Transport, error) {
		return transports[cfg.Name], nil
	}

	c, err := mcp.New([]mcp.ServerConfig{
		{Name: "big", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://x"},
	}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	cat := c.Catalog()
	if got, want := len(cat.Tools), 5; got != want {
		t.Errorf("len(Tools) = %d across paginated pages; want %d", got, want)
	}
	want := []string{"big__tool_a", "big__tool_b", "big__tool_c", "big__tool_d", "big__tool_e"}
	for i, w := range want {
		if i >= len(cat.Tools) || cat.Tools[i].Name != w {
			t.Errorf("Tools[%d] = %v; want %q (full=%v)", i, safeName(cat.Tools, i), w, toolNames(cat.Tools))
			break
		}
	}
}

func safeName(ts []mcp.Tool, i int) string {
	if i < 0 || i >= len(ts) {
		return "<oob>"
	}
	return ts[i].Name
}

func toolNames(ts []mcp.Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func TestCatalog_DefensiveCopy(t *testing.T) {
	t.Parallel()

	transports := map[string]sdkmcp.Transport{
		"x": fakeServer(t, "x", []sdkmcp.Tool{{Name: "tool"}}),
	}
	dialer := func(_ context.Context, cfg mcp.ServerConfig) (sdkmcp.Transport, error) {
		return transports[cfg.Name], nil
	}

	c, err := mcp.New([]mcp.ServerConfig{{Name: "x", Transport: mcp.TransportStreamableHTTP, Endpoint: "http://x"}}, mcp.WithDialer(dialer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	cat := c.Catalog()
	if len(cat.Tools) != 1 {
		t.Fatalf("len(Tools) = %d; want 1", len(cat.Tools))
	}
	cat.Tools[0].Name = "MUTATED"
	delete(cat.ToolsByServer, "x")

	cat2 := c.Catalog()
	if cat2.Tools[0].Name == "MUTATED" {
		t.Errorf("Tools mutation propagated to internal state")
	}
	if _, ok := cat2.ToolsByServer["x"]; !ok {
		t.Errorf("ToolsByServer mutation propagated to internal state")
	}
}
