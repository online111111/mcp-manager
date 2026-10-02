package inbound

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
	"time"
)

func TestProgressiveSearchIsBoundedAndDiscoverable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, cleanup := setupTestServer(t, nil, nil)
	defer cleanup()
	tools := []*mcp.Tool{}
	for i := 0; i < 25; i++ {
		tools = append(tools, makeValidTool(fmt.Sprintf("generate_column_chart_%02d", i)))
	}
	if _, err := srv.publisher.PublishServer("charts", tools, nil); err != nil {
		t.Fatal(err)
	}
	session := progressiveSession(t, ctx, srv.URL()+"/mcp/progressive")
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: map[string]any{"query": "柱状图", "limit": 2}})
	if err != nil || result.IsError {
		t.Fatalf("search: %+v %v", result, err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var value struct {
		Matches    []map[string]any `json:"matches"`
		NextCursor string           `json:"nextCursor"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Matches) != 2 {
		t.Fatalf("bounded Chinese search matches=%d, want 2: %s", len(value.Matches), data)
	}
	if value.NextCursor == "" {
		t.Fatal("missing browse cursor")
	}
	if strings.Contains(string(data), "inputSchema") {
		t.Fatal("search leaked full schema")
	}
	empty, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: map[string]any{}})
	if err != nil || empty.IsError {
		t.Fatalf("directory: %+v %v", empty, err)
	}
	directory, _ := json.Marshal(empty.StructuredContent)
	if !strings.Contains(string(directory), `"sources"`) || !strings.Contains(string(directory), `"charts"`) {
		t.Fatalf("missing source directory: %s", directory)
	}
}

func TestProgressiveDescribeExactSchemaAndDisabled(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, cleanup := setupTestServer(t, nil, nil)
	defer cleanup()
	tool := makeValidTool("read_file")
	if _, err := srv.publisher.PublishServer("fs", []*mcp.Tool{tool, makeValidTool("secret")}, []string{"secret"}); err != nil {
		t.Fatal(err)
	}
	session := progressiveSession(t, ctx, srv.URL()+"/mcp/progressive")
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_describe_tool", Arguments: map[string]any{"name": "fs__read_file"}})
	if err != nil || result.IsError {
		t.Fatalf("describe: %+v %v", result, err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(data), `"inputSchema"`) || !strings.Contains(string(data), `"revision"`) {
		t.Fatalf("describe omitted schema/revision: %s", data)
	}
	if _, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_describe_tool", Arguments: map[string]any{"name": "fs__secret"}}); err == nil {
		t.Fatal("disabled tool described")
	}
}
