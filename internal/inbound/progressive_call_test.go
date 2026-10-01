package inbound

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
	"time"
)

func TestProgressiveCallUnwrapsAndPreservesResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, cleanup := setupTestServer(t, nil, nil)
	defer cleanup()
	received := make(chan *mcp.CallToolRequest, 1)
	resultWant := &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: []byte("test image"), MIMEType: "image/png"}}, StructuredContent: map[string]any{"ok": true}, Meta: mcp.Meta{"example.com/id": "keep"}, IsError: true}
	srv.publisher.SetRouterCallback(func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		received <- req
		return resultWant, nil
	})
	if _, err := srv.publisher.PublishServer("fs", []*mcp.Tool{makeValidTool("read_file")}, nil); err != nil {
		t.Fatal(err)
	}
	session := progressiveSession(t, ctx, srv.URL()+"/mcp/progressive")
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_call_tool", Arguments: json.RawMessage(`{"name":"fs__read_file","arguments":{"param":"hello","id":9007199254740993}}`)})
	if err != nil {
		t.Fatalf("call failed: %v", err)
	}
	select {
	case req := <-received:
		if req.Params.Name != "fs__read_file" || req.Session == nil || req.Extra == nil {
			t.Fatalf("request context lost: %+v", req)
		}
		var args map[string]json.RawMessage
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			t.Fatal(err)
		}
		if string(args["id"]) != "9007199254740993" {
			t.Fatalf("integer precision lost: %s", args["id"])
		}
	case <-ctx.Done():
		t.Fatal("router not invoked")
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("result flags/content lost: %+v", result)
	}
	if _, ok := result.Content[0].(*mcp.ImageContent); !ok {
		t.Fatal("image content flattened")
	}
	if result.Meta["example.com/id"] != "keep" {
		t.Fatalf("result meta lost: %+v", result.Meta)
	}
}
