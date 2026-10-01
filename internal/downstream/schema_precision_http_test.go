package downstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPreciseHTTPBodySkipsUnrelatedResponses(t *testing.T) {
	for _, framing := range []string{"json", "sse"} {
		t.Run(framing, func(t *testing.T) {
			id, _ := jsonrpc.MakeID(float64(7))
			capture := &preciseListCapture{id: id}
			owner := &preciseToolTransport{}
			wanted := `{"jsonrpc":"2.0","id":7,"result":{"tools":[{"name":"exact","inputSchema":{"type":"object","const":9007199254740993}}]}}`
			bodyText := wanted
			if framing == "sse" {
				bodyText = "data: {\"jsonrpc\":\"2.0\",\"id\":99,\"result\":{\"tools\":[]}}\n\ndata: " + wanted + "\n\n"
			}
			body := &preciseListBody{ReadCloser: io.NopCloser(strings.NewReader(bodyText)), capture: capture, owner: owner, sse: framing == "sse"}
			if _, err := io.ReadAll(body); err != nil {
				t.Fatal(err)
			}
			if capture.result == nil || len(capture.result.Tools) != 1 {
				t.Fatal("matching response not selected")
			}
		})
	}
}

func TestPreciseHTTPKeepsNotificationsAndRepeatedLists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := mcp.NewServer(&mcp.Implementation{Name: "precise-notify", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "initial", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer ts.Close()
	changes := make(chan struct{}, 1)
	session, err := DialHTTP(ctx, HTTPOptions{Endpoint: ts.URL, OnToolListChanged: func() {
		select {
		case changes <- struct{}{}:
		default:
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.ListAllTools(ctx); err != nil {
		t.Fatal(err)
	}
	server.AddTool(&mcp.Tool{Name: "added", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	select {
	case <-changes:
	case <-ctx.Done():
		t.Fatal("precise HTTP lost SSE notification lifecycle")
	}
	tools, err := session.ListAllTools(ctx)
	if err != nil || len(tools) != 2 {
		t.Fatalf("refresh: %+v %v", tools, err)
	}
}
