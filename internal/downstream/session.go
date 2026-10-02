package downstream

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/buildinfo"
	"github.com/online111111/mcp-manager/internal/catalog"
	"github.com/online111111/mcp-manager/internal/wirelimit"
)

// Session wraps an official MCP SDK ClientSession with lifecycle management,
// dirty-signal tracking for tool list changes, cursor pagination, and idempotent closing.
type Session struct {
	rawSession *mcp.ClientSession

	// Lifecycle
	closeOnce   sync.Once
	closedState atomic.Bool
	closed      chan struct{}
	closeErr    error

	// Tool list changed notification tracking
	dirty      atomic.Bool
	dirtyCount atomic.Uint64
	callbackCh chan struct{}
	changeCh   chan struct{}
}

// DialHTTP connects to a downstream MCP server over Streamable HTTP.
func DialHTTP(ctx context.Context, opts HTTPOptions) (*Session, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	httpClient, err := newHTTPClient(opts.Headers, opts.BaseRoundTripper)
	if err != nil {
		return nil, err
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint:             opts.Endpoint,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: opts.DisableStandaloneSSE,
		MaxRetries:           opts.MaxRetries,
	}

	return dialTransport(ctx, transport, opts.StartupTimeout, opts.ClientInfo, opts.OnToolListChanged)
}

// DialIO connects to a downstream MCP server over an IO transport (stdin/stdout pipes).
func DialIO(ctx context.Context, opts IOOptions) (*Session, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}

	transport := &legacyStdioTransport{base: &mcp.IOTransport{
		Reader: wirelimit.New(opts.Reader, wirelimit.JSONLines),
		Writer: opts.Writer,
	}}

	return dialTransport(ctx, transport, opts.StartupTimeout, opts.ClientInfo, opts.OnToolListChanged)
}

// Dial connects to a downstream MCP server using the transport specified in Options.
func Dial(ctx context.Context, opts Options) (*Session, error) {
	switch opts.Type {
	case TransportTypeStreamableHTTP:
		return DialHTTP(ctx, HTTPOptions{
			Endpoint:             opts.Endpoint,
			Headers:              opts.Headers,
			BaseRoundTripper:     opts.BaseRoundTripper,
			DisableStandaloneSSE: opts.DisableStandaloneSSE,
			MaxRetries:           opts.MaxRetries,
			StartupTimeout:       opts.StartupTimeout,
			ClientInfo:           opts.ClientInfo,
			OnToolListChanged:    opts.OnToolListChanged,
		})
	case TransportTypeIO:
		return DialIO(ctx, IOOptions{
			Reader:            opts.Reader,
			Writer:            opts.Writer,
			StartupTimeout:    opts.StartupTimeout,
			ClientInfo:        opts.ClientInfo,
			OnToolListChanged: opts.OnToolListChanged,
		})
	case "":
		return nil, fmt.Errorf("%w: transport type must not be empty", ErrUnsupportedTransport)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedTransport, opts.Type)
	}
}

func dialTransport(
	ctx context.Context,
	transport mcp.Transport,
	startupTimeout time.Duration,
	clientInfo *mcp.Implementation,
	onToolListChanged func(),
) (*Session, error) {
	if clientInfo == nil {
		clientInfo = &mcp.Implementation{
			Name:    buildinfo.Name,
			Version: buildinfo.Version,
		}
	}

	s := &Session{
		closed:     make(chan struct{}),
		callbackCh: make(chan struct{}, 16),
		changeCh:   make(chan struct{}, 16),
	}

	// Explicitly configure empty ClientCapabilities to prevent SDK from advertising roots/listChanged.
	// Do not register sampling or elicitation handlers.
	clientOpts := &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{},
		ToolListChangedHandler: func(reqCtx context.Context, req *mcp.ToolListChangedRequest) {
			s.handleToolListChanged()
		},
	}

	client := mcp.NewClient(clientInfo, clientOpts)
	precise := &preciseToolTransport{base: transport, pending: make(map[jsonrpc.ID]*preciseListCapture)}
	client.AddSendingMiddleware(precise.middleware)
	if httpTransport, ok := transport.(*mcp.StreamableClientTransport); ok {
		preciseHTTPTransport(httpTransport, precise)
	} else {
		transport = precise
	}

	connectCtx := ctx
	if startupTimeout > 0 {
		var cancel context.CancelFunc
		connectCtx, cancel = context.WithTimeout(ctx, startupTimeout)
		defer cancel()
	}

	cs, err := client.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, err
	}

	s.rawSession = cs

	// A peer disconnect must become visible to the coordinator so it can
	// retire the failed generation and reconnect. Watch the SDK, not only ctx.
	go func() {
		_ = cs.Wait()
		_ = s.Close()
	}()

	// Dedicated background worker for OnToolListChanged callback, ensuring the SDK handler never blocks.
	if onToolListChanged != nil {
		go func() {
			for {
				select {
				case <-s.closed:
					return
				case <-s.callbackCh:
					// A callback may start a refresh. Keep it outside the SDK
					// receive goroutine and coalesce repeated signals there.
					onToolListChanged()
				}
			}
		}()
	}

	// Monitor lifetime context: when ctx completes, close the session.
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.closed:
		}
	}()

	return s, nil
}

func (s *Session) handleToolListChanged() {
	s.dirty.Store(true)
	s.dirtyCount.Add(1)

	// Enqueue to callback channel without blocking SDK thread
	select {
	case s.callbackCh <- struct{}{}:
	default:
	}

	// Enqueue to public notification channel without blocking
	select {
	case s.changeCh <- struct{}{}:
	default:
	}
}

// RawSession returns the underlying SDK ClientSession.
func (s *Session) RawSession() *mcp.ClientSession {
	return s.rawSession
}

// IsClosed returns true if the session has been closed.
func (s *Session) IsClosed() bool {
	return s.closedState.Load()
}

// IsDirty returns true if a tools/list_changed notification was received since the last clear.
func (s *Session) IsDirty() bool {
	return s.dirty.Load()
}

// ClearDirty resets the dirty flag and returns its previous state.
func (s *Session) ClearDirty() bool {
	return s.dirty.Swap(false)
}

// DirtyCount returns the total number of tools/list_changed notifications received by this session.
func (s *Session) DirtyCount() uint64 {
	return s.dirtyCount.Load()
}

// ToolListChangedChan returns a read-only channel signaled when tool list changes are reported.
func (s *Session) ToolListChangedChan() <-chan struct{} {
	return s.changeCh
}

// Close closes the session idempotently.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.closedState.Store(true)
		close(s.closed)
		if s.rawSession != nil {
			s.closeErr = s.rawSession.Close()
		}
	})
	return s.closeErr
}

// CallTool calls a tool on the downstream server using raw JSON arguments.
func (s *Session) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	if s.IsClosed() {
		return nil, ErrSessionClosed
	}
	params := &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	}
	return s.rawSession.CallTool(ctx, params)
}

// CallToolWithParams calls a tool on the downstream server using full CallToolParams.
func (s *Session) CallToolWithParams(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	if s.IsClosed() {
		return nil, ErrSessionClosed
	}
	if params == nil {
		return s.rawSession.CallTool(ctx, nil)
	}

	// Protocol/client identity metadata is scoped to the incoming MCP hop. SDK
	// v1.7 injects these keys for 2026-07-28 sessions, and forwarding them into
	// another session can contradict that session's negotiated protocol.
	forwarded := *params
	forwarded.Meta = cloneForwardableMeta(params.Meta)
	return s.rawSession.CallTool(ctx, &forwarded)
}

func cloneForwardableMeta(meta mcp.Meta) mcp.Meta {
	if len(meta) == 0 {
		return nil
	}
	out := make(mcp.Meta, len(meta))
	for key, value := range meta {
		switch key {
		case mcp.MetaKeyProtocolVersion, mcp.MetaKeyClientInfo, mcp.MetaKeyClientCapabilities:
			continue
		default:
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ListTools retrieves a single page of tools using explicit ListToolsParams.
func (s *Session) ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	if s.IsClosed() {
		return nil, ErrSessionClosed
	}
	return s.rawSession.ListTools(ctx, params)
}

// ListAllTools pages through all tools until NextCursor is empty.
// It detects duplicate cursors (infinite pagination loops) and duplicate tool names.
// If a tools/list_changed notification arrives while fetching, it re-fetches once.
// Subsequent notifications are left pending for the next cycle.
func (s *Session) ListAllTools(ctx context.Context) ([]*mcp.Tool, error) {
	if s.IsClosed() {
		return nil, ErrSessionClosed
	}

	// First pass
	s.ClearDirty()
	tools, err := s.fetchPages(ctx)
	if err != nil {
		return nil, err
	}

	// If dirty was not signaled during first fetch, we are done
	if !s.IsDirty() {
		return tools, nil
	}

	// A change occurred during discovery: re-fetch once, merging any later change to the next round
	s.ClearDirty()
	tools, err = s.fetchPages(ctx)
	if err != nil {
		return nil, err
	}

	return tools, nil
}

func (s *Session) fetchPages(ctx context.Context) ([]*mcp.Tool, error) {
	var allTools []*mcp.Tool
	discoveryBytes := 0
	seenCursors := make(map[string]struct{})
	seenNames := make(map[string]struct{})
	cursor := ""

	for page := 0; ; page++ {
		if page >= catalog.MaxPaginationPages {
			return nil, fmt.Errorf("tool discovery exceeded maximum of %d pages", catalog.MaxPaginationPages)
		}
		if s.IsClosed() {
			return nil, ErrSessionClosed
		}

		params := &mcp.ListToolsParams{Cursor: cursor}
		res, err := s.rawSession.ListTools(ctx, params)
		if err != nil {
			return nil, err
		}
		if res == nil {
			break
		}

		for _, t := range res.Tools {
			if t == nil {
				continue
			}
			if _, exists := seenNames[t.Name]; exists {
				return nil, fmt.Errorf("%w: tool %q", ErrDuplicateToolName, t.Name)
			}
			encoded, err := json.Marshal(t)
			if err != nil {
				return nil, fmt.Errorf("invalid tool definition encoding")
			}
			discoveryBytes += len(encoded)
			if len(allTools) >= 4096 || discoveryBytes > catalog.MaxCatalogJSONBytes {
				return nil, fmt.Errorf("tool discovery exceeds 4096 definitions or 8 MiB")
			}
			seenNames[t.Name] = struct{}{}
			allTools = append(allTools, t)
		}

		nextCursor := res.NextCursor
		if nextCursor == "" {
			break
		}

		if _, exists := seenCursors[nextCursor]; exists {
			return nil, fmt.Errorf("%w: cursor %q was already seen", ErrDuplicateCursor, nextCursor)
		}
		if nextCursor == cursor {
			return nil, fmt.Errorf("%w: server returned identical cursor %q", ErrDuplicateCursor, nextCursor)
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
	}

	return allTools, nil
}
