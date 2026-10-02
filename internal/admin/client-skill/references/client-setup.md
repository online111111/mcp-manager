# Client setup reference

Use this reference only after identifying the installed client and its current version.

## Preferred transport order

1. Native remote MCP / Streamable HTTP, when the installed client documents support for it.
2. Local stdio bridge through the `mcp-manager` binary when remote MCP is unavailable.

The default remote endpoint ends in `/mcp/progressive` and authenticates with an MCP access token. It publishes exactly three tools: `hub_search_tools` → `hub_describe_tool` → `hub_call_tool`. Only requested definitions are returned in tool results; the MCP tool list stays fixed. An empty search lists sources; passing a server name supports bounded browsing. Retain the describe result's revision for stale-schema protection. `hub_call_tool` is not read-only and may change external state: preserve target-specific approval, and never permanently pre-approve the generic wrapper. The unchanged `/mcp` endpoint remains the full native-schema fallback; export with `--discovery full` when explicitly requested. The Admin Token is unrelated and must never be placed into client MCP configuration.

## Safe configuration editing

- Locate the actual configuration path used by the installed client instead of assuming one from memory.
- Back up the existing configuration before editing.
- Parse and merge structured configuration where possible.
- Preserve every unrelated MCP server and client setting.
- If a client offers a CLI for MCP configuration, prefer the supported CLI over direct file edits.
- Do not invent configuration keys for a client version you cannot inspect. Use the stdio bridge when that is the safer compatible path.

## stdio bridge fallback

The generic local bridge is:

```text
mcp-manager stdio --connect <MCP_ENDPOINT> --token <MCP_TOKEN>
```

Before using it:

- confirm `mcp-manager` is installed on the client machine;
- prefer the absolute binary path if the client launches with a restricted PATH;
- avoid printing the token in terminal logs when an environment-variable mechanism is available;
- verify the bridge can reach the remote HTTPS endpoint.

## Verification

A successful setup should demonstrate as many of these as the client exposes:

- configuration parses without errors;
- MCP Manager connects successfully;
- tools can be listed;
- a harmless representative tool can be invoked;
- reconnect/reload survives a client restart.

If any of these cannot be tested, state that limitation explicitly rather than marking the setup fully verified.
