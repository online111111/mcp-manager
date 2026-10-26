package manager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/config"
)

func TestHTTPDownstreamLostSessionReconnectsWithoutReplayingCall(t *testing.T) {
	remote := mcp.NewServer(&mcp.Implementation{Name: "lost-session-peer", Version: "1"}, nil)
	var executions atomic.Int32
	remote.AddTool(&mcp.Tool{Name: "count", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			executions.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
		})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remote }, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Protocol-Version") > "2025-11-25" {
			http.Error(w, "stateful compatibility endpoint", http.StatusBadRequest)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	manager, _ := setupTestManager(NewDefaultSessionFactory())
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Stop(context.Background())
	if err := manager.Apply(ctx, &config.ResolvedConfig{Servers: map[string]config.ResolvedServer{
		"remote": {ID: "remote", Enabled: true, Type: config.ServerTypeStreamableHTTP, URL: server.URL,
			MaxConcurrency: 4, StartupTimeout: time.Second, CallTimeout: time.Second},
	}}); err != nil {
		t.Fatal(err)
	}
	waitReady := func(previous uint64) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			status := manager.Status()["remote"]
			if status.State == StateReady && status.GenerationID > previous {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("did not become ready after lost HTTP session: %+v", manager.Status()["remote"])
	}
	waitReady(0)
	first := manager.Status()["remote"].GenerationID
	request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "remote__count"}}
	result, err := manager.RouteTool(ctx, request)
	if err != nil || result == nil || result.IsError {
		t.Fatalf("initial call failed: %v %+v", err, result)
	}
	closedSessions := 0
	for session := range remote.Sessions() {
		closedSessions++
		_ = session.Close()
	}
	if closedSessions != 1 {
		t.Fatalf("expected exactly one real stateful HTTP session, got %d", closedSessions)
	}
	// No standalone SSE: the next explicit call discovers the lost session.
	result, err = manager.RouteTool(ctx, request)
	if err == nil && result != nil && !result.IsError {
		t.Fatal("lost-session call was unexpectedly replayed")
	}
	waitReady(first)
	if got := executions.Load(); got != 1 {
		t.Fatalf("lost call was replayed; executions=%d", got)
	}
	result, err = manager.RouteTool(ctx, request)
	if err != nil || result == nil || result.IsError {
		t.Fatalf("new explicit call after reconnect failed: %v %+v", err, result)
	}
	if got := executions.Load(); got != 2 {
		t.Fatalf("expected exactly two explicit successful executions, got %d", got)
	}
}
