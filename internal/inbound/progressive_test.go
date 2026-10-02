package inbound

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func progressiveSession(t *testing.T, ctx context.Context, endpoint string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "progressive-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint}, nil)
	if err != nil {
		t.Fatalf("connect progressive endpoint: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestProgressiveHTTPFixedThreeTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, cleanup := setupTestServer(t, nil, nil)
	defer cleanup()
	if _, err := srv.publisher.PublishServer("fs", []*mcp.Tool{makeValidTool("read_file"), makeValidTool("write_file")}, nil); err != nil {
		t.Fatal(err)
	}
	session := progressiveSession(t, ctx, srv.URL()+"/mcp/progressive")
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"hub_search_tools": true, "hub_describe_tool": true, "hub_call_tool": true}
	if len(list.Tools) != len(want) {
		t.Fatalf("progressive list has %d tools, want exactly 3", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if !want[tool.Name] {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: map[string]any{"query": "read_file"}})
	if err != nil || result.IsError {
		t.Fatalf("search failed: result=%+v err=%v", result, err)
	}
	listAfter, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listAfter.Tools) != 3 {
		t.Fatal("search mutated public list")
	}
	full := progressiveSession(t, ctx, srv.URL()+"/mcp")
	fullList, err := full.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(fullList.Tools) != 2 {
		t.Fatalf("full list altered: %d", len(fullList.Tools))
	}
}

func TestProgressiveSharesSessionCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, cleanup := setupTestServer(t, nil, &HTTPServerOptions{MaxSessions: 1})
	defer cleanup()
	progressiveSession(t, ctx, srv.URL()+"/mcp/progressive")
	body := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"capacity","version":"1"}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL()+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("shared capacity status=%d, want 503", resp.StatusCode)
	}
}
