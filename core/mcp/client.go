// Package mcp is a thin wrapper over [github.com/modelcontextprotocol/go-sdk/mcp]
// that drives one or more MCP servers concurrently.
//
// Its responsibilities, in increasing order of value-add:
//
//   - Manage the connection lifecycle for each configured server.
//   - Aggregate the tool catalog across servers, namespacing each tool
//     name as "<server>__<tool>" so the model sees a single flat list.
//   - Route tool calls back to the originating server.
//   - Retry transient tool-call failures with bounded backoff (on the
//     same session — see [Client.Call]; full transport reconnect is
//     not implemented in v1).
//   - Surface resources and prompts from each server.
//
// The wrapper is intentionally narrow: it does not buffer responses,
// implement caching, or transform tool descriptions. Those belong in
// the agent loop and the router (phases 5-7).
//
// # Subprocess environment
//
// Stdio transports inherit the parent process's environment. If the
// agent process holds secrets in env vars (API keys, SSO tokens),
// they are passed to every spawned MCP server subprocess. Operators
// running third-party MCP binaries should sanitize their env or wrap
// the binary in a launcher that scrubs unwanted variables. A future
// release may add a per-[ServerConfig] env-curation knob.
//
// # Lifecycle
//
// Construct a [Client] with [New], call [Client.Connect] exactly once,
// then use it. After [Client.Close], the client is terminal — calling
// [Client.Connect] again returns [ErrConfig]. Construct a new client
// to reconnect.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	// math/rand is correct here for non-cryptographic backoff jitter;
	// see Backoff.Delay. Both linters' suppressions are kept inline
	// because each scanner uses its own dialect:
	//   - semgrep:    nosemgrep on the import line (Semgrep's grammar)
	//   - gosec:      //nolint:gosec at the call site (golangci-lint)
	"math"
	"math/rand/v2" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/errgroup"
)

// NamespaceSeparator is the substring placed between server name and
// bare tool name in the namespaced wire form. Double underscore is
// chosen to remain legal under all model-provider tool-name regexes
// while staying trivially parseable.
const NamespaceSeparator = "__"

// Transport selects how a [Client] reaches a server.
type Transport string

// Supported transport kinds. Mirror the go-sdk's client transports.
const (
	TransportStdio          Transport = "stdio"
	TransportSSE            Transport = "sse"
	TransportStreamableHTTP Transport = "streamable_http"
)

// ServerConfig describes one MCP server to connect to.
type ServerConfig struct {
	// Name identifies the server in the namespace prefix used by the
	// catalog. Must be non-empty and not contain [NamespaceSeparator].
	Name string

	// Transport selects how to reach the server.
	Transport Transport

	// Endpoint is the URL (for sse / streamable_http) or the command
	// line (for stdio). Stdio command lines are split on whitespace
	// only — no shell-style quoting. For commands with whitespace in
	// arguments, set Endpoint to the executable and pass extra args
	// through StdioArgs.
	Endpoint string

	// StdioArgs are extra arguments passed to the stdio command after
	// any tokens parsed from Endpoint. Ignored for non-stdio.
	StdioArgs []string

	// Headers may carry auth tokens or routing headers for HTTP-based
	// transports. Ignored for stdio.
	Headers map[string]string
}

// Tool is one tool exposed by some MCP server. Name is the namespaced
// form ("server__tool"); Server and BareName recover the components.
type Tool struct {
	Name        string          // namespaced: server__tool
	Server      string          // origin server
	BareName    string          // tool name as the server reported it
	Description string          // server-supplied description
	InputSchema json.RawMessage // JSON Schema for arguments
}

// Resource is one resource exposed by some MCP server.
type Resource struct {
	Server      string
	URI         string
	Name        string
	Description string
	MIMEType    string
}

// Prompt is one prompt template exposed by some MCP server.
type Prompt struct {
	Server      string
	Name        string
	Description string
}

// ToolCall is a tool invocation request. Name is the namespaced form
// ("server__tool"); the client splits it to route to the right server.
type ToolCall struct {
	Name      string
	Arguments json.RawMessage
}

// ToolContent is one piece of content returned by a tool call.
//
// Type is one of: "text", "image", "audio", "resource", "resource_link",
// or "unknown" (the catch-all for SDK content variants this wrapper
// has not been taught about yet — Text holds a JSON dump in that case).
type ToolContent struct {
	Type     string
	Text     string
	Data     []byte // for image/audio: raw bytes (the SDK base64-decodes the wire form)
	MIMEType string
	URI      string // for resource and resource_link references
	Name     string // for resource_link
}

// ToolResult is the result of a [Client.Call].
type ToolResult struct {
	Content []ToolContent
	IsError bool
}

// Backoff configures the bounded retry policy used by [Client.Call]
// when an MCP-level CallTool errors. Zero fields fall back to package
// defaults. The same session is retried; this is not full transport
// reconnect (see the package doc).
type Backoff struct {
	Base        time.Duration // default 500ms
	Cap         time.Duration // default 30s
	MaxAttempts int           // default 5
}

func (b Backoff) base() time.Duration {
	if b.Base <= 0 {
		return 500 * time.Millisecond
	}
	return b.Base
}

func (b Backoff) cap() time.Duration {
	if b.Cap <= 0 {
		return 30 * time.Second
	}
	return b.Cap
}

func (b Backoff) maxAttempts() int {
	if b.MaxAttempts <= 0 {
		return 5
	}
	return b.MaxAttempts
}

// Delay returns the backoff delay for the given zero-based attempt
// using exponential growth with full jitter, clamped to [0, Cap].
// Negative attempts are treated as 0 (no panic on misuse).
func (b Backoff) Delay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	// Cap the shift to avoid wrap-around with large attempts. After
	// ~31 shifts the value already saturates the Cap clamp below;
	// 31 is also < int64 width to keep the shift operand sane.
	if attempt > 31 {
		attempt = 31
	}
	d := b.base() << attempt
	if d <= 0 || d > b.cap() {
		d = b.cap()
	}
	// Defense in depth: even with the cap-clamp, a caller-supplied
	// Cap of math.MaxInt64 would make int64(d)+1 overflow to
	// MinInt64, which would panic rand.Int64N. Subtract one before
	// the +1 so the maximum operand stays representable.
	if d == math.MaxInt64 {
		d = math.MaxInt64 - 1
	}
	// Full jitter: pick uniformly in [0, d]. Math/rand is appropriate
	// here — backoff jitter is not security-sensitive.
	return time.Duration(rand.Int64N(int64(d) + 1)) //nolint:gosec // jitter is non-cryptographic by design
}

// Sentinel errors callers may match with [errors.Is].
var (
	// ErrConfig is returned for invalid configuration at construction time.
	ErrConfig = errors.New("mcp: invalid configuration")
	// ErrUnknownServer is returned when a [ToolCall] names a server not in the client's config.
	ErrUnknownServer = errors.New("mcp: unknown server")
	// ErrServerUnavailable is returned after backoff is exhausted on a connection.
	ErrServerUnavailable = errors.New("mcp: server unavailable after backoff")
	// ErrInvalidName is returned by [SplitName] when the input is not a valid namespaced name.
	ErrInvalidName = errors.New("mcp: invalid namespaced tool name")
)

// Option configures a [Client] at construction time.
type Option func(*Client)

// WithImplementation overrides the [sdkmcp.Implementation] block sent
// to each server during initialization.
func WithImplementation(impl sdkmcp.Implementation) Option {
	return func(c *Client) { c.impl = impl }
}

// WithBackoff overrides the per-call retry backoff policy used by
// [Client.Call] when an MCP-level CallTool errors.
func WithBackoff(b Backoff) Option {
	return func(c *Client) { c.backoff = b }
}

// Dialer builds the underlying [sdkmcp.Transport] for a [ServerConfig].
// Tests inject a Dialer that returns in-memory transports; production
// callers leave the default in place.
type Dialer func(ctx context.Context, cfg ServerConfig) (sdkmcp.Transport, error)

// WithDialer overrides the transport builder used by [Client.Connect].
// Tests use this to wire an in-memory transport pair without spawning
// real subprocesses or HTTP servers.
func WithDialer(d Dialer) Option {
	return func(c *Client) { c.dialer = d }
}

// Client is a connection to one or more MCP servers.
//
// Construct with [New]; call [Client.Connect] before any other method;
// always call [Client.Close] when done. After Close the client is
// terminal — see the package doc.
type Client struct {
	impl    sdkmcp.Implementation
	cfgs    []ServerConfig
	backoff Backoff
	dialer  Dialer

	mu               sync.RWMutex
	sessions         map[string]*sdkmcp.ClientSession
	catalog          *Catalog
	connectAttempted bool // latches on the first Connect call, success OR failure.
	closed           bool // set by Close; latches forever.
}

// New constructs a Client. It validates configuration and returns an
// error on the first problem; it does not dial. Call [Client.Connect]
// to establish sessions.
func New(cfgs []ServerConfig, opts ...Option) (*Client, error) {
	if len(cfgs) == 0 {
		return nil, fmt.Errorf("%w: at least one ServerConfig is required", ErrConfig)
	}
	seen := make(map[string]struct{}, len(cfgs))
	for i, cfg := range cfgs {
		if err := validateServerConfig(i, cfg, seen); err != nil {
			return nil, err
		}
	}
	c := &Client{
		impl: sdkmcp.Implementation{
			Name:    "plexara-agents",
			Version: "0.0.0-dev",
		},
		cfgs:     cfgs,
		dialer:   buildSDKTransport,
		sessions: make(map[string]*sdkmcp.ClientSession, len(cfgs)),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// validateServerConfig checks one ServerConfig and records its name
// in seen to detect duplicates. Returns an error wrapping ErrConfig.
func validateServerConfig(i int, cfg ServerConfig, seen map[string]struct{}) error {
	if cfg.Name == "" {
		return fmt.Errorf("%w: cfgs[%d].Name is empty", ErrConfig, i)
	}
	if strings.TrimSpace(cfg.Name) != cfg.Name {
		return fmt.Errorf("%w: cfgs[%d].Name %q has leading/trailing whitespace", ErrConfig, i, cfg.Name)
	}
	if strings.Contains(cfg.Name, NamespaceSeparator) {
		return fmt.Errorf("%w: cfgs[%d].Name %q contains the namespace separator %q",
			ErrConfig, i, cfg.Name, NamespaceSeparator)
	}
	if _, dup := seen[cfg.Name]; dup {
		return fmt.Errorf("%w: duplicate server name %q", ErrConfig, cfg.Name)
	}
	seen[cfg.Name] = struct{}{}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return fmt.Errorf("%w: cfgs[%d].Endpoint is empty or whitespace", ErrConfig, i)
	}
	switch cfg.Transport {
	case TransportStdio:
		if len(strings.Fields(cfg.Endpoint)) == 0 {
			return fmt.Errorf("%w: cfgs[%d].Endpoint has no command tokens", ErrConfig, i)
		}
	case TransportSSE, TransportStreamableHTTP:
		u, perr := url.Parse(cfg.Endpoint)
		if perr != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("%w: cfgs[%d].Endpoint %q is not a valid URL", ErrConfig, i, cfg.Endpoint)
		}
	default:
		return fmt.Errorf("%w: cfgs[%d].Transport %q is not supported", ErrConfig, i, cfg.Transport)
	}
	return nil
}

// Connect dials every configured server in parallel, then snapshots
// the aggregated tool catalog. If any server fails to connect, all
// already-opened sessions are closed and the error is returned.
//
// Connect must be called exactly once per [Client]. A second call —
// regardless of whether the first succeeded, failed, or was followed
// by [Client.Close] — returns [ErrConfig]. To retry after a failed
// Connect, construct a new client.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("%w: Connect on closed client; construct a new Client", ErrConfig)
	}
	if c.connectAttempted {
		return fmt.Errorf("%w: Connect already called (success or failure latches the gate)", ErrConfig)
	}
	c.connectAttempted = true

	g, gctx := errgroup.WithContext(ctx)
	var sessMu sync.Mutex
	sessions := make(map[string]*sdkmcp.ClientSession, len(c.cfgs))

	for _, cfg := range c.cfgs {
		g.Go(func() error {
			tr, err := c.dialer(gctx, cfg)
			if err != nil {
				return fmt.Errorf("server %q: build transport: %w", cfg.Name, err)
			}
			sdkClient := sdkmcp.NewClient(&c.impl, nil)
			session, err := sdkClient.Connect(gctx, tr, nil)
			if err != nil {
				return fmt.Errorf("server %q: connect: %w", cfg.Name, err)
			}
			sessMu.Lock()
			sessions[cfg.Name] = session
			sessMu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		// Close any partially-opened sessions before returning.
		for _, s := range sessions {
			_ = s.Close()
		}
		return err
	}
	c.sessions = sessions

	cat, err := c.fetchCatalogLocked(ctx)
	if err != nil {
		// We have sessions but the catalog refresh failed. Close
		// everything to keep state consistent.
		for _, s := range c.sessions {
			_ = s.Close()
		}
		c.sessions = map[string]*sdkmcp.ClientSession{}
		return fmt.Errorf("fetch catalog: %w", err)
	}
	c.catalog = cat
	return nil
}

// Close closes every active session and clears the cached catalog,
// returning the first error seen. Subsequent calls are no-ops.
// After Close, [Client.Catalog] returns an empty catalog and any
// [Client.Call] / [Client.Resources] / [Client.Prompts] returns
// [ErrUnknownServer].
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var firstErr error
	for name, s := range c.sessions {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close %q: %w", name, err)
		}
	}
	c.sessions = map[string]*sdkmcp.ClientSession{}
	c.catalog = nil
	c.closed = true
	return firstErr
}

// Catalog returns the aggregated tool catalog snapshot taken at
// [Client.Connect] time. The returned value is a defensive copy;
// mutating it has no effect on the client.
func (c *Client) Catalog() *Catalog {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.catalog == nil {
		return &Catalog{}
	}
	return c.catalog.copy()
}

// Resources lists resources exposed by the named server. Pagination
// is handled by the SDK iterator; servers exposing more resources
// than fit in a single page are still fully enumerated. Returns
// [ErrUnknownServer] if no such server is configured or if [Client.Close]
// has already been called.
//
// The client lock is held only long enough to look up the session
// pointer; the iteration runs lock-free so a slow ListResources on
// one server cannot starve concurrent operations on other servers.
// A concurrent [Client.Close] may close the underlying session
// mid-iteration; the SDK surfaces this as a transport error rather
// than a panic.
func (c *Client) Resources(ctx context.Context, server string) ([]Resource, error) {
	c.mu.RLock()
	sess, ok := c.sessions[server]
	closed := c.closed
	c.mu.RUnlock()
	if closed || !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownServer, server)
	}
	var out []Resource
	for r, err := range sess.Resources(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list resources: %w", err)
		}
		out = append(out, Resource{
			Server: server, URI: r.URI, Name: r.Name,
			Description: r.Description, MIMEType: r.MIMEType,
		})
	}
	return out, nil
}

// Prompts lists prompt templates exposed by the named server.
// Pagination is handled by the SDK iterator. Returns [ErrUnknownServer]
// if no such server is configured or if [Client.Close] has already been
// called. The client lock is released before iteration begins (see
// [Client.Resources] for the rationale).
func (c *Client) Prompts(ctx context.Context, server string) ([]Prompt, error) {
	c.mu.RLock()
	sess, ok := c.sessions[server]
	closed := c.closed
	c.mu.RUnlock()
	if closed || !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownServer, server)
	}
	var out []Prompt
	for p, err := range sess.Prompts(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list prompts: %w", err)
		}
		out = append(out, Prompt{
			Server: server, Name: p.Name, Description: p.Description,
		})
	}
	return out, nil
}

// Call invokes a namespaced tool. The Name field of req must be in
// "server__tool" form.
//
// Retry policy: on ANY error from the underlying CallTool — including
// server-returned semantic errors such as invalid arguments — Call
// retries up to [Backoff.MaxAttempts] times with exponential backoff.
// Context cancellation and deadline are the only short-circuit
// conditions. This is intentionally broad for v1; a future release
// may classify jsonrpc2 error codes to short-circuit non-transient
// failures (`-32601` Method-not-found, `-32602` Invalid-params).
// Until then, callers that want fail-fast semantics for a specific
// tool should pass a context with a tight deadline. After backoff is
// exhausted Call returns [ErrServerUnavailable] wrapping the last
// underlying error.
//
// Tool-level "is this an error result?" reporting flows through
// [ToolResult.IsError], not Go errors — those come back from the
// server with an OK transport response.
func (c *Client) Call(ctx context.Context, req ToolCall) (ToolResult, error) {
	server, bare, err := SplitName(req.Name)
	if err != nil {
		return ToolResult{}, err
	}
	c.mu.RLock()
	sess, ok := c.sessions[server]
	c.mu.RUnlock()
	if !ok {
		return ToolResult{}, fmt.Errorf("%w: %q", ErrUnknownServer, server)
	}

	var args any
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return ToolResult{}, fmt.Errorf("decode arguments: %w", err)
		}
	}

	maxAttempts := c.backoff.maxAttempts()
	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			delay := c.backoff.Delay(attempt - 1)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ToolResult{}, ctx.Err()
			}
		}
		res, err := sess.CallTool(ctx, &sdkmcp.CallToolParams{Name: bare, Arguments: args})
		if err == nil {
			return convertToolResult(res), nil
		}
		// Non-retryable: context cancellation, no point retrying.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ToolResult{}, err
		}
		lastErr = err
	}
	return ToolResult{}, fmt.Errorf("%w: %q: %w", ErrServerUnavailable, server, lastErr)
}

// fetchCatalogLocked snapshots the tool list from every connected
// server. Pagination is handled by the SDK iterator; the catalog is
// sorted deterministically (server name, then bare tool name) so that
// two Connect calls produce identical Tools ordering. The caller must
// hold c.mu.
func (c *Client) fetchCatalogLocked(ctx context.Context) (*Catalog, error) {
	cat := &Catalog{
		Tools:         []Tool{},
		ToolsByServer: make(map[string][]Tool, len(c.sessions)),
	}
	// Iterate sessions in stable name order so partial failures point
	// at a deterministic server.
	names := make([]string, 0, len(c.sessions))
	for name := range c.sessions {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		sess := c.sessions[name]
		// Initialize as []Tool{} (not nil) so a server that exposes
		// zero tools yields a non-nil empty slice in ToolsByServer,
		// matching cat.Tools' shape and avoiding nil-vs-empty caller
		// surprises.
		serverTools := []Tool{}
		for t, err := range sess.Tools(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("server %q: list tools: %w", name, err)
			}
			schemaBytes, mErr := json.Marshal(t.InputSchema)
			if mErr != nil {
				return nil, fmt.Errorf("server %q tool %q: marshal input schema: %w", name, t.Name, mErr)
			}
			tool := Tool{
				Name:        JoinName(name, t.Name),
				Server:      name,
				BareName:    t.Name,
				Description: t.Description,
				InputSchema: schemaBytes,
			}
			serverTools = append(serverTools, tool)
		}
		// Stable order within a server's tool list.
		sort.Slice(serverTools, func(i, j int) bool {
			return serverTools[i].BareName < serverTools[j].BareName
		})
		cat.ToolsByServer[name] = serverTools
		cat.Tools = append(cat.Tools, serverTools...)
	}
	return cat, nil
}

// convertToolResult maps a go-sdk CallToolResult into our wire-public form.
// Defensive against nil — a nil result yields a zero-value ToolResult.
func convertToolResult(r *sdkmcp.CallToolResult) ToolResult {
	if r == nil {
		return ToolResult{}
	}
	out := ToolResult{IsError: r.IsError}
	for _, c := range r.Content {
		out.Content = append(out.Content, contentToToolContent(c))
	}
	return out
}

func contentToToolContent(c sdkmcp.Content) ToolContent {
	switch v := c.(type) {
	case *sdkmcp.TextContent:
		return ToolContent{Type: "text", Text: v.Text}
	case *sdkmcp.ImageContent:
		return ToolContent{Type: "image", Data: v.Data, MIMEType: v.MIMEType}
	case *sdkmcp.AudioContent:
		return ToolContent{Type: "audio", Data: v.Data, MIMEType: v.MIMEType}
	case *sdkmcp.EmbeddedResource:
		tc := ToolContent{Type: "resource"}
		if v.Resource != nil {
			tc.URI = v.Resource.URI
			tc.MIMEType = v.Resource.MIMEType
			tc.Text = v.Resource.Text
			tc.Data = v.Resource.Blob
		}
		return tc
	case *sdkmcp.ResourceLink:
		return ToolContent{
			Type:     "resource_link",
			URI:      v.URI,
			Name:     v.Name,
			MIMEType: v.MIMEType,
		}
	default:
		// Unknown content type — round-trip via JSON so callers at
		// least see something rather than dropping it silently. If the
		// payload itself fails to marshal (e.g. embeds an unsupported
		// kind), fall back to a diagnostic Text rather than the empty
		// string.
		raw, mErr := json.Marshal(v)
		if mErr != nil {
			return ToolContent{
				Type: "unknown",
				Text: fmt.Sprintf("%T: marshal failed: %v", v, mErr),
			}
		}
		return ToolContent{Type: "unknown", Text: string(raw)}
	}
}

// SplitName parses a namespaced "server__tool" string into its parts.
// Returns [ErrInvalidName] if the input lacks the separator or has
// an empty server or tool component.
//
// A bare tool name may itself contain the separator. SplitName splits
// on the FIRST occurrence: "s__a__b" → ("s", "a__b").
func SplitName(s string) (server, bare string, err error) {
	idx := strings.Index(s, NamespaceSeparator)
	if idx <= 0 {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidName, s)
	}
	server = s[:idx]
	bare = s[idx+len(NamespaceSeparator):]
	if bare == "" {
		return "", "", fmt.Errorf("%w: %q (empty tool component)", ErrInvalidName, s)
	}
	return server, bare, nil
}

// JoinName produces a namespaced "server__tool" string.
func JoinName(server, bare string) string {
	return server + NamespaceSeparator + bare
}

// httpClientWithHeaders returns nil if headers is empty (the SDK
// supplies its own default client). Otherwise it returns a client
// whose Transport wraps http.DefaultTransport and injects the given
// headers on every outbound request.
func httpClientWithHeaders(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return nil
	}
	// Defensive copy so post-construction mutation by the caller
	// cannot retroactively change request headers.
	h := make(map[string]string, len(headers))
	for k, v := range headers {
		h[k] = v
	}
	return &http.Client{Transport: &headerInjectingRoundTripper{base: http.DefaultTransport, headers: h}}
}

type headerInjectingRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (rt *headerInjectingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request before mutating headers per http.RoundTripper contract.
	out := req.Clone(req.Context())
	for k, v := range rt.headers {
		out.Header.Set(k, v)
	}
	return rt.base.RoundTrip(out)
}

// buildSDKTransport constructs the sdk's transport for a config.
// All errors wrap [ErrConfig] and include the server name so failures
// at Connect time are diagnosable through the public sentinel.
func buildSDKTransport(_ context.Context, cfg ServerConfig) (sdkmcp.Transport, error) {
	switch cfg.Transport {
	case TransportStdio:
		argv := strings.Fields(cfg.Endpoint)
		if len(argv) == 0 {
			// New() rejects this; defense in depth.
			return nil, fmt.Errorf("%w: server %q: stdio Endpoint has no command tokens", ErrConfig, cfg.Name)
		}
		argv = append(argv, cfg.StdioArgs...)
		// G204 nolint: argv comes from operator-controlled ServerConfig,
		// not user input. The package doc warns operators about env
		// inheritance; treat the command as trusted.
		cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // cfg-controlled by maintainer
		return &sdkmcp.CommandTransport{Command: cmd}, nil
	case TransportSSE:
		return &sdkmcp.SSEClientTransport{
			Endpoint:   cfg.Endpoint,
			HTTPClient: httpClientWithHeaders(cfg.Headers),
		}, nil
	case TransportStreamableHTTP:
		return &sdkmcp.StreamableClientTransport{
			Endpoint:   cfg.Endpoint,
			HTTPClient: httpClientWithHeaders(cfg.Headers),
		}, nil
	default:
		return nil, fmt.Errorf("%w: server %q: unsupported transport %q", ErrConfig, cfg.Name, cfg.Transport)
	}
}
