package bridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func recoveryHub(t *testing.T, timeout time.Duration) (*mcp.Server, *httptest.Server, <-chan string, *atomic.Int64) {
	t.Helper()
	ids := make(chan string, 32)
	count := new(atomic.Int64)
	hub := mcp.NewServer(testImpl("recoverable-hub"), &mcp.ServerOptions{GetSessionID: func() string {
		id := fmt.Sprintf("bridge-session-%d", count.Add(1))
		ids <- id
		return id
	}})
	hub.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return hub }, &mcp.StreamableHTTPOptions{SessionTimeout: timeout})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Match Manager's stateful compatibility boundary rather than SDK 1.7's
		// new sessionless discover path (which would make SID tests vacuous).
		if r.Header.Get("Mcp-Protocol-Version") > "2025-11-25" {
			http.Error(w, "stateful endpoint supports MCP through 2025-11-25", http.StatusBadRequest)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return hub, server, ids, count
}

func startRecoveryBridge(t *testing.T, opts Options) (*mcp.ClientSession, <-chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	transport, stdin, stdout, cleanup := makePipes()
	opts.Stdin, opts.Stdout, opts.Stderr = stdin, stdout, io.Discard
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, opts)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		cleanup()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("bridge cleanup did not stop all session/reconnect work promptly")
		}
	})
	client := mcp.NewClient(testImpl("persistent-local-client"), nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session, done, cancel
}

func nextRecoverySID(t *testing.T, ids <-chan string) string {
	t.Helper()
	select {
	case id := <-ids:
		return id
	case <-time.After(2 * time.Second):
		t.Fatal("bridge did not initialize a fresh Hub session while keeping local stdio alive")
		return ""
	}
}

func deleteRecoverySession(t *testing.T, endpoint, sid string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("delete session: %s", resp.Status)
	}
}

func TestBridgeRecoversDeletedSessionWithoutClosingStdio(t *testing.T) {
	_, server, ids, _ := recoveryHub(t, 0)
	local, done, _ := startRecoveryBridge(t, Options{Endpoint: server.URL})
	old := nextRecoverySID(t, ids)
	deleteRecoverySession(t, server.URL, old)
	fresh := nextRecoverySID(t, ids)
	if fresh == old {
		t.Fatal("recovery reused the stale session ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := local.CallTool(ctx, &mcp.CallToolParams{Name: "echo"})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("new call over original stdio session failed: result=%v err=%v", result, err)
	}
	select {
	case err := <-done:
		t.Fatalf("bridge exited instead of recovering: %v", err)
	default:
	}
}

func TestBridgeKeepAlivePreventsShortIdleExpiry(t *testing.T) {
	_, server, ids, count := recoveryHub(t, 120*time.Millisecond)
	local, _, _ := startRecoveryBridge(t, Options{Endpoint: server.URL, KeepAlive: 20 * time.Millisecond})
	nextRecoverySID(t, ids)
	time.Sleep(420 * time.Millisecond)
	result, err := local.CallTool(context.Background(), &mcp.CallToolParams{Name: "echo"})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("call failed after idle keepalive: result=%v err=%v", result, err)
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("keepalive did not preserve the original session: sessions=%d", got)
	}
}
