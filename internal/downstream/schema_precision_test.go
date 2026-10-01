package downstream

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiscoveredSchemaPreservesWireNumberPrecision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := mcp.NewServer(&mcp.Implementation{Name: "exact", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "exact", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","const":9007199254740993}}}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer ts.Close()
	session, err := DialHTTP(ctx, HTTPOptions{Endpoint: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListAllTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(tools[0].InputSchema)
	if !bytes.Contains(data, []byte("9007199254740993")) {
		t.Fatalf("SDK wire decoding rounded constraint: %s", data)
	}
}
