# MCP Manager compatibility

## Current baseline

The production module and independent SDK probe use `github.com/modelcontextprotocol/go-sdk v1.7.0`. Go 1.25 remains the declared compatibility floor; CI exercises Go 1.25.8 and Go 1.27.3 on Linux, Windows, and macOS.

MCP Manager keeps a stateful/session-oriented compatibility model centered on the 2025-11-25 generation of Streamable HTTP behavior while using SDK v1.7.0. Discovery contains compatibility handling for older peers. The project does **not** claim complete native support for every MCP 2026-07-28 capability exposed by the SDK.

Supported downstream transports:

- managed stdio/IO;
- remote Streamable HTTP over HTTPS, with loopback HTTP for local development.

The product focuses on tool aggregation/routing. Resources, prompts, sampling, roots, elicitation, task extensions, arbitrary extension methods, and a general legacy HTTP+SSE proxy remain outside the supported proxy contract unless explicitly documented.

## Client modes

HTTP-capable clients connect to:

```text
https://mcp.example.com/mcp/progressive
```

stdio-only clients use:

```bash
MCP_MANAGER_TOKEN='...' mcp-manager stdio --connect https://mcp.example.com/mcp/progressive
```

During the v0.4 compatibility window, `MCP_HUB_TOKEN` remains accepted by the CLI. New generated configurations use `MCP_MANAGER_TOKEN`.

Use `mcp-manager export` for supported client formats rather than copying stale examples. New exports and the Admin connector prompt default to `/mcp/progressive`, which publishes exactly `hub_search_tools`, `hub_describe_tool`, and `hub_call_tool`; selected full definitions are returned on demand. Existing `/mcp` clients retain the full native-tool interface. Use `--discovery full` for explicit fallback. The generic call is not read-only and must retain target-specific approval. HTTP and the native stdio bridge are covered by integration tests; that does not certify untested third-party clients. See [Progressive discovery](PROGRESSIVE-DISCOVERY.md).

## Session lifetime and recovery

The compatible `/mcp` and `/mcp/progressive` endpoints are stateful. Their default
inactivity timeout remains 30 minutes; an open GET/SSE stream alone does not refresh
that timer. MCP access tokens and Admin browser cookies are independent of protocol
session IDs. Changing the Admin session timeout does not change MCP session expiry.

The native stdio bridge and Manager's remote HTTP downstream connections send a
protocol `ping` every five minutes by default. Three consecutive keepalive failures
close a broken connection, while the downstream coordinator retains its existing
backoff/reconnection ownership. Local stdio downstream workers do not get HTTP pings.

After a Hub session is lost, the updated native bridge keeps its local stdio client
connection open while a single coordinator creates a fresh Hub session, completes
initialization and tool discovery, and atomically replaces the tool directory. It
never carries a stale `Mcp-Session-Id` into the new initialization. Recovery is bounded
to five attempts with exponential waits starting at 500 ms and capped at five
seconds, with the existing startup deadline applying to each connection/discovery.
Cancellation and local EOF stop outstanding recovery. Authentication failures,
redirects, and certificate verification errors terminate clearly rather than loop.

An interrupted `tools/call` is **not replayed**, even if its response was lost after
the operation executed. That request fails; a later, new explicit call can use the
replacement session. This rule also applies to tools marked read-only because the
bridge cannot safely infer whether a remote operation already happened.

This client recovery requires an updated `mcp-manager` executable on the machine
running the stdio bridge. Updating the center alone does not upgrade downloaded
client executables. Direct HTTP clients must implement their own protocol recovery:
a `404 session not found` requires discarding the old session ID and running a fresh
initialize handshake. Reconnecting TCP while retaining an expired ID is insufficient.
Third-party clients are not claimed to be fixed by the native bridge update.

## Downstream behavior

stdio:

- arguments stay distinct; ordinary commands are not shell-interpolated;
- children inherit only a safe compatibility environment baseline;
- additional secrets/values require explicit server `env` entries;
- explicitly choosing a shell opts into that shell's semantics.

Streamable HTTP:

- non-loopback plaintext HTTP is rejected;
- redirects are rejected;
- configured headers cannot replace MCP/session framing headers;
- request/response handling is bounded;
- normal diagnostics sanitize external transport/protocol errors.

## Automated evidence

Current automated evidence includes SDK v1.7.0 probes, root tests, current-Go race tests across Linux/Windows/macOS, POSIX process-group and Windows Job Object lifecycle tests, publication/pagination/cancellation/routing/hot-reload/drain regressions, stdio bridge authentication/synchronization, Admin CSRF/ETag/preflight/rollback/secret tests, Remote Admin CRUD tests, Chromium UI regressions, a real-MCP-Manager browser smoke test, and `govulncheck`.

This is evidence for tested contracts, not universal certification of every MCP client/server, reverse proxy, OS release, or network provider.

## Manual matrix

| Client/source | Exact version | Connection | Result |
|---|---|---|---|
| Cursor | not recorded | stdio | NOT_RUN |
| Cursor | not recorded | Streamable HTTP | NOT_RUN |
| Claude Desktop | not recorded | stdio | NOT_RUN |
| User stdio MCP | varies | stdio | NOT_RUN |
| User remote MCP | varies | Streamable HTTP | NOT_RUN |

Manual acceptance should record exact version/OS and verify initialize, `tools/list`, a normal call, tool-directory change behavior, and shutdown/reconnect behavior.

## v0.4 compatibility contract

The repository/product identity migration preserves the wire/config contract:

- `/mcp`, `/admin/`, and `/api/...` paths do not change;
- JSON `hub.*` fields do not change;
- `MCP_HUB_TOKEN` and `MCP_HUB_ADMIN_TOKEN` remain runtime fallbacks during the compatibility window;
- the canonical binary/Release name is `mcp-manager`;
- the canonical repository is `online111111/mcp-manager`;
- the canonical Go module is `github.com/online111111/mcp-manager`.

The old source entrypoint is removed; compatibility is intentionally limited to runtime configuration and credential aliases rather than maintaining duplicate source identities.

## Related

- [Configuration](CONFIG.md)
- [Security](SECURITY.md)
- [VPS deployment](VPS.md)
- [Remote Admin](REMOTE-ADMIN.md)
- [SDK probe](../verification/sdkprobe/README.md)
