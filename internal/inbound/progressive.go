package inbound

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"sort"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/catalog"
	"github.com/online111111/mcp-manager/internal/router"
)

const ProgressivePath = "/mcp/progressive"
const maxDiscoveryArguments = 1 << 20
const maxDiscoveryResultsBytes = 32 << 10

type discoverySearchArgs struct {
	Query  string `json:"query"`
	Server string `json:"server"`
	Limit  *int   `json:"limit"`
	Cursor string `json:"cursor"`
}

type discoveryMatch struct {
	Name        string               `json:"name"`
	Server      string               `json:"server"`
	Description string               `json:"description"`
	Required    []string             `json:"required,omitempty"`
	Annotations *mcp.ToolAnnotations `json:"annotations,omitempty"`
	score       int
}

type discoverySource struct {
	Server       string `json:"server"`
	ToolCount    int    `json:"toolCount"`
	Capabilities string `json:"capabilities,omitempty"`
}

type discoveryCursor struct {
	Revision    int64  `json:"r"`
	Fingerprint string `json:"f"`
	Offset      int    `json:"o"`
}

func newProgressiveServer(p *Publisher, version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-manager-progressive", Version: version}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: false}}})
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	server.AddTool(&mcp.Tool{Name: "hub_search_tools", Description: "Find tools by intent or server. Empty query lists capability sources; set server to browse its tools. Returns bounded short matches, never full schemas. Then use hub_describe_tool and hub_call_tool. 搜索地图、图表、网页、生图、简历、音乐、数学等能力。", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string", "maxLength": 1024}, "server": map[string]any{"type": "string", "maxLength": 32}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20}, "cursor": map[string]any{"type": "string", "maxLength": 512}}, "additionalProperties": false}, Annotations: readOnly}, p.searchDiscoveryTools)
	server.AddTool(&mcp.Tool{Name: "hub_describe_tool", Description: "Read the complete input/output schemas and annotations for one exact public tool name found by hub_search_tools. Keep the returned revision for a stale-definition check at invocation.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}, "additionalProperties": false}, Annotations: readOnly}, p.describeDiscoveryTool)
	destructive, openWorld := true, true
	server.AddTool(&mcp.Tool{Name: "hub_call_tool", Description: "Invoke a discovered tool with arguments matching its schema and optional catalog revision. May deploy, delete, or change external state: inspect the target and obtain approval when needed. Never treat this entry as read-only or permanently pre-approved.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"}, "revision": map[string]any{"type": "integer", "minimum": 0}}, "required": []string{"name", "arguments"}, "additionalProperties": false}, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, DestructiveHint: &destructive, OpenWorldHint: &openWorld}}, p.callDiscoveryTool)
	return server
}

func decodeDiscoveryArguments(raw json.RawMessage, target any, allowed ...string) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if len(raw) > maxDiscoveryArguments || raw[0] != '{' || !json.Valid(raw) {
		return invalidDiscoveryParams("arguments must be one bounded JSON object")
	}
	// Stream the outer keys rather than float-decoding the nested tool arguments.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if _, err := dec.Token(); err != nil {
		return invalidDiscoveryParams("invalid arguments")
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return invalidDiscoveryParams("invalid arguments")
		}
		key := token.(string)
		if seen[key] {
			return invalidDiscoveryParams("duplicate argument field")
		}
		seen[key] = true
		ok := false
		for _, field := range allowed {
			if field == key {
				ok = true
				break
			}
		}
		if !ok {
			return invalidDiscoveryParams("unknown argument field")
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalidDiscoveryParams("invalid or null argument field")
		}
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return invalidDiscoveryParams("invalid argument types")
	}
	return nil
}

func (p *Publisher) searchDiscoveryTools(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args discoverySearchArgs
	if err := decodeDiscoveryArguments(req.Params.Arguments, &args, "query", "server", "limit", "cursor"); err != nil {
		return nil, err
	}
	args.Query = strings.TrimSpace(args.Query)
	args.Server = strings.TrimSpace(args.Server)
	if len([]rune(args.Query)) > 1024 || len(args.Server) > 32 || len(args.Cursor) > 512 {
		return nil, invalidDiscoveryParams("search input exceeds limit")
	}
	limit := 5
	if args.Limit != nil {
		limit = *args.Limit
	}
	if limit < 1 || limit > 20 {
		return nil, invalidDiscoveryParams("limit must be between 1 and 20")
	}
	snap := p.Snapshot()
	sources := discoverySources(snap)
	if args.Query == "" && args.Server == "" {
		if args.Cursor != "" {
			return nil, invalidDiscoveryParams("cursor requires a query or server")
		}
		return discoveryResult(map[string]any{"revision": snap.Revision, "sources": sources, "hint": "Search an intent, or pass a server name to browse its tools; describe the exact name before calling."})
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", args.Query, args.Server, limit)))
	fingerprint := fmt.Sprintf("%x", hash[:12])
	offset := 0
	if args.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(args.Cursor)
		if err != nil {
			return nil, invalidDiscoveryParams("invalid search cursor")
		}
		var cursor struct {
			Revision    *int64  `json:"r"`
			Fingerprint *string `json:"f"`
			Offset      *int    `json:"o"`
		}
		if len(data) == 0 || decodeDiscoveryArguments(data, &cursor, "r", "f", "o") != nil || cursor.Revision == nil || cursor.Fingerprint == nil || cursor.Offset == nil {
			return nil, invalidDiscoveryParams("invalid search cursor")
		}
		if *cursor.Revision < 0 || len(*cursor.Fingerprint) != 24 || *cursor.Offset <= 0 || *cursor.Offset >= catalog.MaxTotalTools {
			return nil, invalidDiscoveryParams("invalid search cursor")
		}
		if *cursor.Revision != snap.Revision {
			return nil, invalidDiscoveryParams("search cursor is stale; repeat search")
		}
		if *cursor.Fingerprint != fingerprint {
			return nil, invalidDiscoveryParams("search cursor is mismatched; repeat search")
		}
		offset = *cursor.Offset
	}
	clauses := discoveryQueryClauses(args.Query)
	matches := []discoveryMatch{}
	type doc struct {
		tool  *mcp.Tool
		route catalog.RouteEntry
		text  string
		hits  []bool
	}
	docs := []doc{}
	for _, tool := range snap.Tools {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		route, ok := snap.LookupRoute(tool.Name)
		if !ok || (args.Server != "" && route.ServerID != args.Server) {
			continue
		}
		schema, _ := json.Marshal(tool.InputSchema)
		text := strings.ToLower(tool.Name + " " + route.ServerID + " " + tool.Description + " " + string(schema))
		hits := make([]bool, len(clauses))
		for i, alternatives := range clauses {
			for _, term := range alternatives {
				if strings.Contains(text, term) {
					hits[i] = true
					break
				}
			}
		}
		docs = append(docs, doc{tool, route, text, hits})
	}
	for _, d := range docs {
		exact := strings.EqualFold(args.Query, d.tool.Name)
		score := 0
		for i, hit := range d.hits {
			if hit {
				score += 100
				for _, term := range clauses[i] {
					if strings.Contains(strings.ToLower(d.tool.Name), term) {
						score += 3
						break
					}
				}
			}
		}
		if args.Query != "" && !exact && score == 0 {
			continue
		}
		if exact {
			score += 10000
		}
		required := discoveryRequired(d.tool.InputSchema)
		var annotations *mcp.ToolAnnotations
		if d.tool.Annotations != nil {
			annotations = &mcp.ToolAnnotations{
				Title:        shortDiscoveryText(d.tool.Annotations.Title, 64),
				ReadOnlyHint: d.tool.Annotations.ReadOnlyHint, IdempotentHint: d.tool.Annotations.IdempotentHint,
				DestructiveHint: d.tool.Annotations.DestructiveHint, OpenWorldHint: d.tool.Annotations.OpenWorldHint,
			}
		}
		matches = append(matches, discoveryMatch{Name: d.tool.Name, Server: d.route.ServerID, Description: shortDiscoveryText(d.tool.Description, 180), Required: required, Annotations: annotations, score: score})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].Name < matches[j].Name
	})
	if args.Cursor != "" && offset >= len(matches) {
		return nil, invalidDiscoveryParams("search cursor outside result set")
	}
	end := offset + limit
	if end > len(matches) {
		end = len(matches)
	}
	for {
		result := map[string]any{"revision": snap.Revision, "matches": matches[offset:end]}
		if end < len(matches) {
			encoded, _ := json.Marshal(discoveryCursor{snap.Revision, fingerprint, end})
			result["nextCursor"] = base64.RawURLEncoding.EncodeToString(encoded)
		}
		if len(matches) == 0 {
			result["sources"] = sources
			result["hint"] = "No matching tool. Try a shorter intent or pass one of the source names to browse; do not assume an unreachable capability exists."
		}
		response, err := discoveryResult(result)
		if err != nil {
			return nil, err
		}
		// Budget the actual result envelope, including both text and structured
		// copies and JSON escaping, rather than just its matches array.
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= maxDiscoveryResultsBytes {
			return response, nil
		}
		if end <= offset+1 {
			return nil, fmt.Errorf("search result exceeds response budget")
		}
		end--
	}
}

func (p *Publisher) describeDiscoveryTool(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := decodeDiscoveryArguments(req.Params.Arguments, &args, "name"); err != nil {
		return nil, err
	}
	if !catalog.IsValidPublicToolName(args.Name) {
		return nil, invalidDiscoveryParams("invalid public tool name")
	}
	snap := p.Snapshot()
	tool, ok := snap.GetTool(args.Name)
	if !ok {
		return nil, invalidDiscoveryParams("unknown or unavailable tool")
	}
	return discoveryResult(map[string]any{"revision": snap.Revision, "tool": tool})
}

func discoveryRequired(schema any) []string {
	data, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var object struct {
		Required []string `json:"required"`
	}
	if json.Unmarshal(data, &object) != nil {
		return nil
	}
	if len(object.Required) > 32 {
		object.Required = object.Required[:32]
	}
	for i, v := range object.Required {
		object.Required[i] = shortDiscoveryText(v, 64)
	}
	return object.Required
}

func shortDiscoveryText(value string, n int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > n {
		return string(runes[:n]) + "…"
	}
	return value
}

func discoverySources(snap *catalog.Snapshot) []discoverySource {
	result := []discoverySource{}
	labels := map[string]string{"amap-maps": "地图/天气/路线 maps weather routes", "mcp-server-chart": "图表 charts", "reactive-resume": "简历 resume", "image-gen": "生图/改图 images", "markmap": "思维导图 mindmap", "tavily_search": "网页搜索/抓取 web search extract", "render-engine": "数学/公式/三维建模 math formula 3D", "suno-music": "音乐 music", "memory": "知识图谱 memory", "cloudflare_deploy": "网站部署 Cloudflare", "edgeone_deploy": "网站部署 EdgeOne", "public_apis": "公共API", "trends-hub": "热点趋势 trends", "wenyan-mcp": "文章排版 publishing"}
	for server, count := range snap.PublishedCounts {
		if count > 0 {
			result = append(result, discoverySource{server, count, labels[server]})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Server < result[j].Server })
	return result
}

func discoveryQueryClauses(query string) [][]string {
	query = strings.ToLower(strings.TrimSpace(query))
	aliases := []struct {
		zh    string
		terms []string
	}{
		{"柱状图", []string{"column_chart", "bar_chart"}}, {"折线图", []string{"line_chart"}}, {"饼图", []string{"pie_chart"}}, {"散点图", []string{"scatter_chart"}}, {"流程图", []string{"flow_diagram"}}, {"思维导图", []string{"mind_map", "markmap", "mindmap"}},
		{"图表", []string{"chart"}}, {"生图", []string{"generate_image", "image"}}, {"改图", []string{"edit_image", "image"}}, {"地图", []string{"maps", "map"}}, {"天气", []string{"weather"}}, {"路线", []string{"direction", "route"}}, {"简历", []string{"resume"}}, {"音乐", []string{"music"}}, {"公式", []string{"formula", "latex", "typst"}}, {"数学", []string{"sympy", "math", "formula"}}, {"建模", []string{"openscad", "model", "3d"}}, {"搜索", []string{"search"}}, {"网页", []string{"web", "extract", "crawl", "tavily"}}, {"部署", []string{"deploy"}}, {"记忆", []string{"memory", "graph"}},
	}
	clauses := [][]string{}
	for _, alias := range aliases {
		if strings.Contains(query, alias.zh) {
			clauses = append(clauses, alias.terms)
			query = strings.ReplaceAll(query, alias.zh, " ")
		}
	}
	tokens := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	for _, token := range tokens {
		if len(clauses) > 0 {
			nonASCII := false
			for _, r := range token {
				if r > 127 {
					nonASCII = true
				}
			}
			if nonASCII {
				continue
			}
		}
		// Generic request verbs are not capability evidence. Exact public names
		// are matched separately, so omitting these tokens cannot hide them.
		switch token {
		case "a", "the", "please", "an", "to", "for", "draw", "create", "generate", "make", "show", "use", "want", "need", "can", "could", "would", "i", "me", "my", "help", "with", "and", "of":
			continue
		}
		clauses = append(clauses, []string{token})
	}
	return clauses
}

func discoveryResult(value any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}, StructuredContent: value}, nil
}

func invalidDiscoveryParams(message string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: message}
}

func (p *Publisher) callDiscoveryTool(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Revision  *int64          `json:"revision"`
	}
	if err := decodeDiscoveryArguments(req.Params.Arguments, &args, "name", "arguments", "revision"); err != nil {
		return nil, err
	}
	if !catalog.IsValidPublicToolName(args.Name) || len(args.Arguments) == 0 || bytes.TrimSpace(args.Arguments)[0] != '{' || (args.Revision != nil && *args.Revision < 0) {
		return nil, invalidDiscoveryParams("name, object arguments and non-negative optional revision are required")
	}
	snap := p.Snapshot()
	if _, ok := snap.GetTool(args.Name); !ok {
		return nil, invalidDiscoveryParams("unknown or unavailable tool")
	}
	if args.Revision != nil && *args.Revision != snap.Revision {
		return nil, invalidDiscoveryParams("catalog revision is stale; describe the tool again")
	}
	target := *req
	params := *req.Params
	params.Name = args.Name
	params.Arguments = args.Arguments
	target.Params = &params
	return p.dispatchTool(router.WithValidation(ctx, args.Revision), &target)
}
