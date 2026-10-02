package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// clientImpl creates a test client implementation descriptor.
func testImpl(name string) *mcp.Implementation {
	return &mcp.Implementation{
		Name:    name,
		Version: "1.0.0",
	}
}

// makePipes creates a client IOTransport and the corresponding server stdin / stdout streams.
func makePipes() (*mcp.IOTransport, io.Reader, io.Writer, func()) {
	c2sReader, c2sWriter := io.Pipe() // Client -> Server
	s2cReader, s2cWriter := io.Pipe() // Server -> Client

	clientTransport := &mcp.IOTransport{
		Reader: s2cReader,
		Writer: c2sWriter,
	}

	cleanup := func() {
		_ = c2sWriter.Close()
		_ = c2sReader.Close()
		_ = s2cWriter.Close()
		_ = s2cReader.Close()
	}

	return clientTransport, c2sReader, s2cWriter, cleanup
}

// TestBridge_InvalidEndpoint verifies that invalid or empty endpoint URLs return ErrInvalidEndpoint.
func TestBridge_InvalidEndpoint(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		endpoint string
	}{
		{"empty", ""},
		{"no_scheme", "127.0.0.1:8080/mcp"},
		{"ftp_scheme", "ftp://127.0.0.1:8080/mcp"},
		{"no_host", "http:///mcp"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Run(ctx, tc.endpoint, nil, nil, nil)
			if err == nil || !errors.Is(err, ErrInvalidEndpoint) {
				t.Fatalf("expected ErrInvalidEndpoint for %q, got %v", tc.endpoint, err)
			}
		})
	}
}

// TestBridge_ThreeHopCall tests the complete 3-hop pipeline:
// Client (IO Transport) -> Bridge Server -> Bridge Client (Streamable HTTP) -> Hub HTTP -> Downstream Tool.
func TestBridge_ThreeHopCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Setup fake downstream / Hub MCP Server
	hubServer := mcp.NewServer(testImpl("test-hub"), nil)

	var downstreamExecuted atomic.Bool
	hubServer.AddTool(&mcp.Tool{
		Name:        "echo_tool",
		Description: "Echoes input arguments",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"msg": map[string]any{"type": "string"},
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		downstreamExecuted.Store(true)

		var args struct {
			Msg string `json:"msg"`
		}
		if len(req.Params.Arguments) > 0 {
			_ = json.Unmarshal(req.Params.Arguments, &args)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{
					Text: "downstream:" + args.Msg,
				},
			},
		}, nil
	})

	// 2. Expose Hub over Streamable HTTP (Hop 2/3)
	httpHandler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return hubServer
	}, nil)
	ts := httptest.NewServer(httpHandler)
	defer ts.Close()

	// 3. Setup IO pipes for Bridge (Hop 1)
	clientTransport, serverIn, serverOut, pipeCleanup := makePipes()
	defer pipeCleanup()

	var stderrBuf bytes.Buffer

	bridgeErrCh := make(chan error, 1)
	go func() {
		bridgeErrCh <- RunWithOptions(ctx, Options{
			Endpoint: ts.URL,
			Stdin:    serverIn,
			Stdout:   serverOut,
			Stderr:   &stderrBuf,
		})
	}()

	// 4. Connect test Client over IO transport (representing stdio agent)
	client := mcp.NewClient(testImpl("test-client"), nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client failed to connect to bridge: %v", err)
	}

	// 5. Verify Client can list tools from Bridge (which fetched from Hub)
	toolsResult, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("client ListTools failed: %v", err)
	}
	if len(toolsResult.Tools) != 1 || toolsResult.Tools[0].Name != "echo_tool" {
		t.Fatalf("unexpected tools returned: %+v", toolsResult.Tools)
	}

	// 6. Execute CallTool through 3 hops
	callResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "echo_tool",
		Arguments: json.RawMessage(`{"msg":"hello-three-hop"}`),
	})
	if err != nil {
		t.Fatalf("client CallTool failed: %v", err)
	}

	if !downstreamExecuted.Load() {
		t.Fatalf("expected downstream tool handler to be executed")
	}

	if len(callResult.Content) == 0 {
		t.Fatalf("expected result content, got none")
	}
	tc, ok := callResult.Content[0].(*mcp.TextContent)
	if !ok || tc.Text != "downstream:hello-three-hop" {
		t.Fatalf("unexpected content returned: %+v", callResult.Content[0])
	}

	// Cleanup client session cleanly
	_ = clientSession.Close()
	pipeCleanup()

	select {
	case err := <-bridgeErrCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("bridge exited with error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not exit cleanly after client disconnected")
	}
}

// TestBridge_DynamicToolListNotification verifies that when Hub dynamically adds/removes tools,
// the bridge receives tools/list_changed, synchronizes with Hub, calls AddTool/RemoveTools on local server,
// and client receives tools/list_changed notification.
func TestBridge_DynamicToolListNotification(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	hubServer := mcp.NewServer(testImpl("test-hub"), nil)

	// Initial tool
	hubServer.AddTool(&mcp.Tool{
		Name:        "initial_tool",
		Description: "Initial tool description",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "initial"}}}, nil
	})

	httpHandler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return hubServer
	}, nil)
	ts := httptest.NewServer(httpHandler)
	defer ts.Close()

	clientTransport, serverIn, serverOut, pipeCleanup := makePipes()
	defer pipeCleanup()

	bridgeErrCh := make(chan error, 1)
	go func() {
		bridgeErrCh <- RunWithOptions(ctx, Options{
			Endpoint: ts.URL,
			Stdin:    serverIn,
			Stdout:   serverOut,
		})
	}()

	changedNotified := make(chan struct{}, 1)
	clientOpts := &mcp.ClientOptions{
		ToolListChangedHandler: func(ctx context.Context, req *mcp.ToolListChangedRequest) {
			select {
			case changedNotified <- struct{}{}:
			default:
			}
		},
	}

	client := mcp.NewClient(testImpl("test-client"), clientOpts)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}

	// Verify initial listing
	initialTools, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("initial ListTools failed: %v", err)
	}
	if len(initialTools.Tools) != 1 || initialTools.Tools[0].Name != "initial_tool" {
		t.Fatalf("unexpected initial tools: %+v", initialTools.Tools)
	}

	// Drain any startup notification before triggering dynamic update
	select {
	case <-changedNotified:
	default:
	}

	// Dynamically modify tools on Hub: remove initial_tool, add new_tool
	hubServer.RemoveTools("initial_tool")
	hubServer.AddTool(&mcp.Tool{
		Name:        "new_tool",
		Description: "New tool description",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "new"}}}, nil
	})

	// Wait for client to receive ToolListChanged notification from Bridge
	select {
	case <-changedNotified:
		// Notification received successfully
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for client ToolListChanged notification")
	}

	// Re-list tools on client, verify updated list
	updatedTools, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("updated ListTools failed: %v", err)
	}
	if len(updatedTools.Tools) != 1 || updatedTools.Tools[0].Name != "new_tool" {
		t.Fatalf("expected only [new_tool] after dynamic update, got len=%d name=%q", len(updatedTools.Tools), updatedTools.Tools[0].Name)
	}

	// Verify the new tool can be called
	callRes, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "new_tool"})
	if err != nil {
		t.Fatalf("call to new_tool failed: %v", err)
	}
	tc := callRes.Content[0].(*mcp.TextContent)
	if tc.Text != "new" {
		t.Fatalf("expected 'new', got %q", tc.Text)
	}

	_ = clientSession.Close()
	pipeCleanup()

	select {
	case err := <-bridgeErrCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("bridge exited with error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not exit cleanly")
	}
}

// TestBridge_Cancellation verifies that context cancellation at the client propagates
// all the way to the downstream tool handler.
func TestBridge_Cancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	hubServer := mcp.NewServer(testImpl("test-hub"), nil)

	downstreamStarted := make(chan struct{})
	downstreamCancelled := make(chan struct{})

	hubServer.AddTool(&mcp.Tool{
		Name:        "blocking_tool",
		InputSchema: map[string]any{"type": "object"},
	}, func(reqCtx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(downstreamStarted)
		select {
		case <-reqCtx.Done():
			close(downstreamCancelled)
			return nil, reqCtx.Err()
		case <-time.After(10 * time.Second):
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
		}
	})

	httpHandler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return hubServer
	}, nil)
	ts := httptest.NewServer(httpHandler)
	defer ts.Close()

	clientTransport, serverIn, serverOut, pipeCleanup := makePipes()
	defer pipeCleanup()

	bridgeErrCh := make(chan error, 1)
	go func() {
		bridgeErrCh <- RunWithOptions(ctx, Options{
			Endpoint: ts.URL,
			Stdin:    serverIn,
			Stdout:   serverOut,
		})
	}()

	client := mcp.NewClient(testImpl("test-client"), nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}

	callCtx, callCancel := context.WithCancel(ctx)
	callErrCh := make(chan error, 1)

	go func() {
		_, err := clientSession.CallTool(callCtx, &mcp.CallToolParams{
			Name: "blocking_tool",
		})
		callErrCh <- err
	}()

	// Wait until downstream tool starts executing
	select {
	case <-downstreamStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("downstream tool did not start in time")
	}

	// Cancel the call at the client
	callCancel()

	// Verify cancellation reached downstream handler
	select {
	case <-downstreamCancelled:
		// Propagated successfully!
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not propagate through bridge to downstream")
	}

	// Verify client call returned error
	select {
	case err := <-callErrCh:
		if err == nil {
			t.Fatal("expected error from cancelled call, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client CallTool did not unblock after cancellation")
	}

	_ = clientSession.Close()
	pipeCleanup()

	select {
	case <-bridgeErrCh:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not exit cleanly")
	}
}

// syncWriter records all writes safely for inspecting stdout purity.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buf.Bytes()...)
}

// TestBridge_StdoutPurity verifies that stdout receives ONLY valid MCP JSON-RPC protocol messages.
// No logs, banners, or stray text must appear on stdout.
func TestBridge_StdoutPurity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	hubServer := mcp.NewServer(testImpl("test-hub"), nil)
	hubServer.AddTool(&mcp.Tool{
		Name:        "greet",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "hi"}}}, nil
	})

	httpHandler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return hubServer
	}, nil)
	ts := httptest.NewServer(httpHandler)
	defer ts.Close()

	c2sReader, c2sWriter := io.Pipe()
	s2cReader, s2cWriter := io.Pipe()

	// Tap all stdout data
	stdoutCapture := &syncWriter{}
	teeStdout := io.MultiWriter(s2cWriter, stdoutCapture)

	bridgeErrCh := make(chan error, 1)
	go func() {
		bridgeErrCh <- RunWithOptions(ctx, Options{
			Endpoint: ts.URL,
			Stdin:    c2sReader,
			Stdout:   teeStdout,
		})
	}()

	clientTransport := &mcp.IOTransport{
		Reader: s2cReader,
		Writer: c2sWriter,
	}
	client := mcp.NewClient(testImpl("test-client"), nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}

	// Perform initialize, list, call
	_, err = clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	_, err = clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "greet"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	_ = clientSession.Close()
	_ = c2sWriter.Close()
	_ = c2sReader.Close()
	_ = s2cWriter.Close()
	_ = s2cReader.Close()

	select {
	case <-bridgeErrCh:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge did not exit cleanly")
	}

	// Verify all captured stdout bytes are valid JSON-RPC lines
	captured := stdoutCapture.Bytes()
	if len(captured) == 0 {
		t.Fatal("expected stdout messages, got none")
	}

	scanner := bufio.NewScanner(bytes.NewReader(captured))
	lineCount := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lineCount++

		var msg struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("stdout line %d is not valid JSON (%v): %s", lineCount, err, string(line))
		}
		if msg.JSONRPC != "2.0" {
			t.Fatalf("stdout line %d does not have jsonrpc=2.0: %s", lineCount, string(line))
		}
	}

	if lineCount == 0 {
		t.Fatal("expected at least one non-empty protocol line on stdout")
	}
}

// TestBridge_HubSessionFailure verifies that if the Hub terminates or closes abruptly,
// the bridge detects the failure and exits promptly with an error.
func TestBridge_HubSessionFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	hubServer := mcp.NewServer(testImpl("test-hub"), nil)
	hubServer.AddTool(&mcp.Tool{
		Name:        "tool1",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})

	httpHandler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return hubServer
	}, nil)
	ts := httptest.NewServer(httpHandler)

	clientTransport, serverIn, serverOut, pipeCleanup := makePipes()
	defer pipeCleanup()

	bridgeErrCh := make(chan error, 1)
	go func() {
		bridgeErrCh <- RunWithOptions(ctx, Options{
			Endpoint: ts.URL,
			Stdin:    serverIn,
			Stdout:   serverOut,
		})
	}()

	client := mcp.NewClient(testImpl("test-client"), nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}

	// Force-close active SSE sockets; Close alone gracefully waits for them
	// and can hide a missing disconnect behind the parent context deadline.
	cutAt := time.Now()
	ts.CloseClientConnections()
	ts.Close()

	// Wait for bridge to detect Hub session termination and exit
	select {
	case err := <-bridgeErrCh:
		if err == nil {
			t.Fatal("expected bridge to exit with error when Hub terminates, got nil")
		}
		if ctx.Err() != nil {
			t.Fatalf("parent context expired instead of prompt disconnect: %v", ctx.Err())
		}
		if elapsed := time.Since(cutAt); elapsed > 3*time.Second {
			t.Fatalf("disconnect was not prompt: %s", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bridge did not exit within timeout after Hub terminated")
	}

	_ = clientSession.Close()
	pipeCleanup()
}

// TestBridge_MultipleBridgesSharedHub verifies that two independent bridges can connect
// to the same Hub simultaneously without conflict or duplicated downstream resources.
func TestBridge_MultipleBridgesSharedHub(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	hubServer := mcp.NewServer(testImpl("test-hub"), nil)
	var callCount atomic.Int64

	hubServer.AddTool(&mcp.Tool{
		Name:        "shared_tool",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		callCount.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "shared-ok"}}}, nil
	})

	httpHandler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return hubServer
	}, nil)
	ts := httptest.NewServer(httpHandler)
	defer ts.Close()

	// Setup Bridge 1
	c1Transport, s1In, s1Out, cleanup1 := makePipes()
	defer cleanup1()

	go func() {
		_ = RunWithOptions(ctx, Options{
			Endpoint: ts.URL,
			Stdin:    s1In,
			Stdout:   s1Out,
		})
	}()

	// Setup Bridge 2
	c2Transport, s2In, s2Out, cleanup2 := makePipes()
	defer cleanup2()

	go func() {
		_ = RunWithOptions(ctx, Options{
			Endpoint: ts.URL,
			Stdin:    s2In,
			Stdout:   s2Out,
		})
	}()

	// Connect Client 1 to Bridge 1
	cl1 := mcp.NewClient(testImpl("c1"), nil)
	cs1, err := cl1.Connect(ctx, c1Transport, nil)
	if err != nil {
		t.Fatalf("client 1 connect: %v", err)
	}
	defer cs1.Close()

	// Connect Client 2 to Bridge 2
	cl2 := mcp.NewClient(testImpl("c2"), nil)
	cs2, err := cl2.Connect(ctx, c2Transport, nil)
	if err != nil {
		t.Fatalf("client 2 connect: %v", err)
	}
	defer cs2.Close()

	// Concurrent tool calls from both clients
	var wg sync.WaitGroup
	wg.Add(2)
	errCh := make(chan error, 2)

	go func() {
		defer wg.Done()
		res, err := cs1.CallTool(ctx, &mcp.CallToolParams{Name: "shared_tool"})
		if err != nil {
			errCh <- err
			return
		}
		if res.Content[0].(*mcp.TextContent).Text != "shared-ok" {
			errCh <- errors.New("client 1 unexpected content")
		}
	}()

	go func() {
		defer wg.Done()
		res, err := cs2.CallTool(ctx, &mcp.CallToolParams{Name: "shared_tool"})
		if err != nil {
			errCh <- err
			return
		}
		if res.Content[0].(*mcp.TextContent).Text != "shared-ok" {
			errCh <- errors.New("client 2 unexpected content")
		}
	}()

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent call failed: %v", err)
		}
	}

	if count := callCount.Load(); count != 2 {
		t.Fatalf("expected 2 calls, got %d", count)
	}
}
