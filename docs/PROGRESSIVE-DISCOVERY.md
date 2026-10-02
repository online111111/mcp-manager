# Progressive tool discovery

MCP Manager offers two tool surfaces from one process and one downstream catalog:

- `/mcp/progressive` — default for newly generated client configurations. Its `tools/list` is fixed at exactly three tools.
- `/mcp` — unchanged full native-tool interface for existing clients and explicit fallback.

There is no configuration or credential migration. Existing clients are not silently rewritten. Both endpoints share bearer authentication, Host/Origin/HTTPS policy, body limits, protocol negotiation and the combined upstream session limit. Both currently negotiate the stateful MCP baseline through 2025-11-25, not the full 2026-07-28 transport.

## Workflow

1. `hub_search_tools({"query":"column chart"})` returns at most five short matches by default, at most twenty when requested. Results contain names, server, short descriptions, required parameter names and short annotations — never all full schemas. Chinese capability aliases are supported.
2. `hub_describe_tool({"name":"charts__generate_column_chart"})` returns the exact published tool definition, including input/output schemas, annotations and catalog revision.
3. `hub_call_tool({"name":"charts__generate_column_chart","arguments":{...},"revision":12})` validates against the current published input schema and invokes the target through the existing router. The optional revision rejects stale definitions. Arguments remain raw JSON so large integers are not rounded. Disabled/unpublished tools cannot be called through the wrapper.

An empty search lists source names and capability hints. Set `server` to browse a source, with bounded `limit` and opaque `cursor`. Cursors are tied to query, server, page size and catalog revision; repeat the search after a catalog change. An unsuccessful search returns source hints so a lexical miss is not mistaken for absence of all capabilities. Known exact tool names can go directly to describe.

Search and describe are read-only. The generic call is **not read-only or guaranteed idempotent**: it may deploy, delete, or write. Client approval UIs may see the outer wrapper rather than the target, so do not permanently pre-approve `hub_call_tool`. Inspect its target and arguments and apply the same approval policy a direct sensitive operation would require. The Manager's audit records the actual underlying tool name.

## Validation boundary

Progressive calls support JSON Schema draft-07 and 2020-12, using a mature validator. Unspecified dialect defaults to 2020-12. Local references, common composition, object/array constraints and precise numeric constraints are supported. Unsupported dialects/keywords/formats, external references, recursive or excessively expanded schema graphs are rejected rather than treated as successful validation. Schema validation never fetches resources or mutates arguments. Conservative limits bound schema/argument bytes, JSON depth, node count, numeric precision/exponent and validation work. Existing full-interface raw-handler semantics remain unchanged.

Admission binds the validated snapshot to the catalog-owning downstream generation without reversing manager/publisher lock order. Calls retain generation leases, timeout, cancellation, concurrency and no-replay behavior. Hot changes may require re-describe; admitted calls retain their generation.

Results are returned as MCP results, preserving image/audio/resource content blocks, structured content, error flags and metadata. No automatic JSON-text flattening is used for target execution results.

## Client configuration

```bash
# HTTP configuration, progressive by default
mcp-manager export --client cursor --transport http --endpoint https://mcp.example.com

# stdio bridge configuration, progressive by default
mcp-manager export --client claude-desktop --endpoint https://mcp.example.com --token-env

# Explicit old/full fallback
mcp-manager export --client cursor --transport http --endpoint https://mcp.example.com --discovery full
```

Native HTTP clients use `https://mcp.example.com/mcp/progressive`. stdio clients use `mcp-manager stdio --connect https://mcp.example.com/mcp/progressive` with an MCP access token supplied via their secret/environment mechanism. The bridge discovers only the same fixed three definitions. A client supporting MCP calls can use this workflow without list-change notifications, but exact third-party product/version compatibility must still be tested.

Existing client configurations are not changed automatically. To adopt progressive discovery, update the HTTP URL (or the stdio bridge `--connect` URL), retain the MCP access token, and reconnect/reload the MCP connection. No custom client plugin is required: the model invokes the three ordinary MCP tools in sequence. For agents that need guidance, add a rule to search first, describe the selected target, and call with the returned schema/revision instead of guessing arguments.

The Admin console's client access card and connector Agent prompt recommend the progressive endpoint and explain the full fallback. `status`, `doctor`, and Remote Admin accept the progressive endpoint and resolve diagnostics to the Manager root; doctor tests the chosen MCP surface.

## Trade-offs

Cold capabilities require one or two discovery/describe round trips. In exchange, unrelated schemas do not fill startup tool lists or model contexts. This feature does not lazily start downstream processes, change tool permissions, or claim a reduction in their resident memory. Pagination alone is not progressive disclosure: the important boundary is that formal `tools/list` always remains the same three entries.
