package inbound

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProgressiveSearchCursorAndInputBoundaries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	srv, cleanup := setupTestServer(t, nil, nil)
	defer cleanup()
	if _, err := srv.publisher.PublishServer("fs", []*mcp.Tool{makeValidTool("read_file"), makeValidTool("write_file")}, nil); err != nil {
		t.Fatal(err)
	}
	session := progressiveSession(t, ctx, srv.URL()+ProgressivePath)
	first, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: map[string]any{"server": "fs", "limit": 1}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(first.StructuredContent)
	var page struct {
		NextCursor string           `json:"nextCursor"`
		Matches    []discoveryMatch `json:"matches"`
	}
	if json.Unmarshal(encoded, &page) != nil || page.NextCursor == "" || len(page.Matches) != 1 {
		t.Fatalf("first page: %s", encoded)
	}
	second, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: map[string]any{"server": "fs", "limit": 1, "cursor": page.NextCursor}})
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, _ := json.Marshal(second.StructuredContent)
	if strings.Contains(string(secondBytes), page.Matches[0].Name) {
		t.Fatalf("cursor repeated tool: %s", secondBytes)
	}
	if _, err := srv.publisher.PublishServer("fs", []*mcp.Tool{makeValidTool("read_file")}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: map[string]any{"server": "fs", "limit": 1, "cursor": page.NextCursor}}); err == nil {
		t.Fatal("stale cursor accepted")
	}
	bad := []string{`[]`, `null`, `{"Query":"read_file"}`, `{"limit":-1}`, `{"limit":21}`, `{"query":null}`, `{"query":"a","query":"b"}`}
	for _, raw := range bad {
		if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatalf("accepted invalid search args %s", raw)
		}
	}
}

func TestProgressiveSearchNaturalEnglishIntent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, cleanup := setupTestServer(t, nil, nil)
	defer cleanup()
	if _, err := srv.publisher.PublishServer("charts", []*mcp.Tool{makeValidTool("generate_column_chart")}, nil); err != nil {
		t.Fatal(err)
	}
	session := progressiveSession(t, ctx, srv.URL()+ProgressivePath)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: map[string]any{"query": "draw a column chart"}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(data), "charts__generate_column_chart") {
		t.Fatalf("natural intent missed: %s", data)
	}
}

func TestProgressiveSearchLargeTitleBoundedPagesAdvance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	srv, cleanup := setupTestServer(t, nil, nil)
	defer cleanup()
	const total = 6
	title := strings.Repeat("标", 60000)
	closed, nondestructive := false, false
	tools := make([]*mcp.Tool, 0, total)
	for i := 0; i < total; i++ {
		tool := makeValidTool(fmt.Sprintf("chart_%02d", i))
		tool.Description = strings.Repeat("图", 180)
		tool.Annotations = &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, DestructiveHint: &nondestructive, OpenWorldHint: &closed}
		properties := map[string]any{}
		required := []string{}
		for j := 0; j < 32; j++ {
			field := fmt.Sprintf("%02d", j) + strings.Repeat("列", 62)
			properties[field] = map[string]any{"type": "string"}
			required = append(required, field)
		}
		tool.InputSchema = map[string]any{"type": "object", "properties": properties, "required": required}
		tools = append(tools, tool)
	}
	snap, err := srv.publisher.PublishServer("charts", tools, nil)
	if err != nil || snap.PublishedCount("charts") != total {
		t.Fatalf("large titles must be legal published tools: snapshot=%+v err=%v", snap, err)
	}
	original, ok := snap.GetTool("charts__chart_00")
	if !ok || original.Annotations.Title != title {
		t.Fatal("large title not retained in catalog")
	}
	session := progressiveSession(t, ctx, srv.URL()+ProgressivePath)
	cursor, offset := "", 0
	seen := map[string]bool{}
	pages := 0
	for {
		args := map[string]any{"server": "charts", "limit": 20}
		if cursor != "" {
			args["cursor"] = cursor
		}
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("search: result=%+v err=%v", result, err)
		}
		data, err := json.Marshal(result)
		if err != nil || len(data) > maxDiscoveryResultsBytes {
			t.Fatalf("complete text + structured result exceeds budget: bytes=%d err=%v", len(data), err)
		}
		var page struct {
			Matches    []discoveryMatch `json:"matches"`
			NextCursor string           `json:"nextCursor"`
		}
		structured, _ := json.Marshal(result.StructuredContent)
		if err := json.Unmarshal(structured, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Matches) == 0 {
			t.Fatalf("large title produced an empty page at offset %d", offset)
		}
		for _, match := range page.Matches {
			if seen[match.Name] {
				t.Fatalf("tool repeated: %s", match.Name)
			}
			seen[match.Name] = true
			a := match.Annotations
			if a == nil || len([]rune(a.Title)) > 65 || !a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
				t.Fatalf("annotations not short or hints altered: %s", match.Name)
			}
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		decoded, err := base64.RawURLEncoding.DecodeString(page.NextCursor)
		var next discoveryCursor
		if err != nil || json.Unmarshal(decoded, &next) != nil || next.Offset <= offset || next.Offset != len(seen) {
			t.Fatalf("nonterminal cursor failed to advance: old=%d cursor=%s", offset, decoded)
		}
		cursor, offset = page.NextCursor, next.Offset
	}
	if len(seen) != total || pages < 2 {
		t.Fatalf("bounded pagination lost tools or ignored doubled budget: tools=%d pages=%d", len(seen), pages)
	}
	if original.Annotations.Title != title {
		t.Fatal("search mutated full catalog annotations")
	}
}

func TestProgressiveSearchMixedCatalogIntentCoverage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	srv, cleanup := setupTestServer(t, nil, nil)
	defer cleanup()
	for server, names := range map[string][]string{
		"charts": {"generate_column_chart", "generate_line_chart", "generate_pie_chart"},
		"shapes": {"draw_shape"},
		"maps":   {"maps_direction_driving", "maps_weather"},
	} {
		tools := []*mcp.Tool{}
		for _, name := range names {
			tools = append(tools, makeValidTool(name))
		}
		if _, err := srv.publisher.PublishServer(server, tools, nil); err != nil {
			t.Fatal(err)
		}
	}
	session := progressiveSession(t, ctx, srv.URL()+ProgressivePath)
	for _, query := range []string{"draw a column chart", "draw 柱状图", "请画柱状图 chart", "column 图表"} {
		t.Run(query, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: map[string]any{"query": query}})
			if err != nil || result.IsError {
				t.Fatalf("search: result=%+v err=%v", result, err)
			}
			data, _ := json.Marshal(result.StructuredContent)
			var page struct {
				Matches []discoveryMatch `json:"matches"`
			}
			if json.Unmarshal(data, &page) != nil || len(page.Matches) == 0 || page.Matches[0].Name != "charts__generate_column_chart" {
				t.Fatalf("domain high-coverage match lost to unrelated verb: %s", data)
			}
			for _, match := range page.Matches {
				if match.Server != "charts" {
					t.Fatalf("unrelated verb produced non-domain match: %s", data)
				}
			}
		})
	}
	for _, query := range []string{"quantum toaster", "draw quantum toaster"} {
		t.Run(query, func(t *testing.T) {
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: map[string]any{"query": query}})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(result.StructuredContent)
			if !strings.Contains(string(data), `"matches":[]`) || !strings.Contains(string(data), `"sources"`) {
				t.Fatalf("unknown intent invented a tool: %s", data)
			}
		})
	}
}

func TestProgressiveSearchRejectsExplicitZeroLimit(t *testing.T) {
	srv, cleanup := setupTestServer(t, nil, nil)
	defer cleanup()
	_, err := srv.publisher.searchDiscoveryTools(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(`{"limit":0}`)}})
	if err == nil || !strings.Contains(err.Error(), "limit must be between 1 and 20") {
		t.Fatalf("explicit zero was treated as omitted: %v", err)
	}
}

func TestProgressiveSearchCursorStrictPayloadAndBounds(t *testing.T) {
	srv, cleanup := setupTestServer(t, nil, nil)
	defer cleanup()
	if _, err := srv.publisher.PublishServer("fs", []*mcp.Tool{makeValidTool("read_file"), makeValidTool("write_file")}, nil); err != nil {
		t.Fatal(err)
	}
	search := func(args map[string]any) (*mcp.CallToolResult, error) {
		data, _ := json.Marshal(args)
		return srv.publisher.searchDiscoveryTools(context.Background(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Arguments: data}})
	}
	first, err := search(map[string]any{"server": "fs", "limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(first.StructuredContent)
	var page struct {
		NextCursor string `json:"nextCursor"`
	}
	if json.Unmarshal(data, &page) != nil || page.NextCursor == "" {
		t.Fatalf("missing initial cursor: %s", data)
	}
	decoded, _ := base64.RawURLEncoding.DecodeString(page.NextCursor)
	var good discoveryCursor
	if err := json.Unmarshal(decoded, &good); err != nil {
		t.Fatal(err)
	}
	payload := func(offset string) string {
		return fmt.Sprintf(`{"r":%d,"f":%q,"o":%s}`, good.Revision, good.Fingerprint, offset)
	}
	cases := []struct {
		name, raw, want string
	}{
		{"unknown", strings.TrimSuffix(string(decoded), "}") + `,"extra":true}`, "invalid search cursor"},
		{"duplicate", strings.TrimSuffix(string(decoded), "}") + `,"o":1}`, "invalid search cursor"},
		{"wrong-case", fmt.Sprintf(`{"r":%d,"f":%q,"O":1}`, good.Revision, good.Fingerprint), "invalid search cursor"},
		{"missing-revision", fmt.Sprintf(`{"f":%q,"o":1}`, good.Fingerprint), "invalid search cursor"},
		{"missing-offset", fmt.Sprintf(`{"r":%d,"f":%q}`, good.Revision, good.Fingerprint), "invalid search cursor"},
		{"null-offset", payload("null"), "invalid search cursor"},
		{"wrong-type", payload(`"1"`), "invalid search cursor"},
		{"negative-offset", payload("-1"), "invalid search cursor"},
		{"zero-offset", payload("0"), "invalid search cursor"},
		{"catalog-bound", payload("2048"), "invalid search cursor"},
		{"integer-overflow", payload("9223372036854775808"), "invalid search cursor"},
		{"terminal-offset", payload("2"), "outside result set"},
		{"outside-results", payload("3"), "outside result set"},
		{"stale", fmt.Sprintf(`{"r":%d,"f":%q,"o":1}`, good.Revision+1, good.Fingerprint), "stale"},
		{"mismatched", fmt.Sprintf(`{"r":%d,"f":%q,"o":1}`, good.Revision, strings.Repeat("0", 24)), "mismatched"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cursor := base64.RawURLEncoding.EncodeToString([]byte(tc.raw))
			_, err := search(map[string]any{"server": "fs", "limit": 1, "cursor": cursor})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("cursor %s accepted or wrong diagnostic: %v", tc.raw, err)
			}
		})
	}
	for _, args := range []map[string]any{
		{"server": "fs", "limit": 2},
		{"server": "fs", "limit": 1, "query": "file"},
		{"server": "other", "limit": 1},
	} {
		args["cursor"] = page.NextCursor
		if _, err := search(args); err == nil || !strings.Contains(err.Error(), "mismatched") {
			t.Fatalf("cursor query/server/limit mismatch accepted: args=%v err=%v", args, err)
		}
	}
	second, err := search(map[string]any{"server": "fs", "limit": 1, "cursor": page.NextCursor})
	if err != nil || second.IsError {
		t.Fatalf("valid cursor rejected: result=%+v err=%v", second, err)
	}
}

func TestProgressiveUnknownIntentReportsSources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv, cleanup := setupTestServer(t, nil, nil)
	defer cleanup()
	if _, err := srv.publisher.PublishServer("charts", []*mcp.Tool{makeValidTool("generate_column_chart")}, nil); err != nil {
		t.Fatal(err)
	}
	session := progressiveSession(t, ctx, srv.URL()+ProgressivePath)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hub_search_tools", Arguments: map[string]any{"query": "quantum toaster"}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(data), `"matches":[]`) || !strings.Contains(string(data), `"sources"`) {
		t.Fatalf("invented match/missing fallback: %s", data)
	}
}
