# MCP Manager configuration

MCP Manager reads one strict JSON configuration file. The file is the persistent source of truth; there is no database or `state.json`.

> Compatibility: the top-level object remains named `hub`. `hub.*` is a configuration schema contract and is intentionally **not** renamed to `manager.*` in v0.4.

## Minimal shape

```json
{
  "version": 1,
  "hub": { "listen": "127.0.0.1:8080" },
  "defaults": {
    "startupTimeout": "20s",
    "callTimeout": "60s",
    "maxConcurrency": 8
  },
  "mcpServers": {
    "my-server": {
      "type": "stdio",
      "command": "node",
      "args": ["server.js"]
    }
  }
}
```

`enabled` defaults to `true`; `type` is `stdio` or `streamable_http`.

## MCP access tokens

`hub.auth.bearerToken` remains the original single-token field and is fully supported. `hub.auth.bearerTokens` adds optional additional tokens with exactly the same MCP access authority:

```json
{
  "hub": {
    "auth": {
      "bearerToken": "${MCP_MANAGER_TOKEN}",
      "bearerTokens": [
        "another-at-least-32-character-token"
      ]
    }
  }
}
```

The effective token set is the legacy `bearerToken` first, followed by `bearerTokens`. Entries may use `${NAME}` expansion, must be at least 32 characters after expansion, and must be unique. The Admin Token must remain distinct from every MCP access token.

The Browser Admin can list the effective MCP access tokens and add or delete them from **Client access**. These changes use the normal CSRF + ETag/CAS + atomic persistence + rollback transaction and are hot-reloaded into inbound MCP authentication without restarting the Manager. The Admin Token itself remains a separate startup-bound credential.

## stdio

stdio servers require `command` and may use `args`, `cwd`, and `env`. Arguments are passed without implicit shell interpolation.

Children do not inherit the Manager process environment wholesale. The implicit baseline is restricted to runtime/OS essentials; ambient credentials, proxy variables, SSH agent sockets, cloud credentials, and arbitrary service variables are excluded. Forward required values explicitly:

```json
{"env":{"GITHUB_TOKEN":"${GITHUB_TOKEN}"}}
```

## Streamable HTTP

Remote services require HTTPS; plaintext HTTP is limited to literal loopback targets. Redirects, URL credentials/fragments, and transport-owned framing headers are rejected.

## Environment expansion

- `${NAME}` resolves from the Manager process environment.
- `$${NAME}` produces literal `${NAME}`.
- Missing required variables fail validation.
- Expansion does not write resolved secrets back to disk and does not invoke a shell.

New public deployment examples use `${MCP_MANAGER_TOKEN}` and `${MCP_MANAGER_ADMIN_TOKEN}`. Existing configurations using `${MCP_HUB_TOKEN}` / `${MCP_HUB_ADMIN_TOKEN}` may remain unchanged during the rename migration because variable expansion is configuration-driven.

## Limits

- config: 1 MiB maximum
- configured servers: 32 maximum
- MCP access tokens: each effective token is at least 32 characters and token values must be unique
- `startupTimeout`: positive, max 24h, default `20s`
- `callTimeout`: positive, max 24h, default `60s`
- `maxConcurrency`: 1-64, default `8`, no waiting queue
- `tools.disabled`: original downstream tool names

## Reload semantics

Connection-level downstream changes use break-before-make generation replacement. Existing admitted calls stay on the generation from which they obtained a lease. A call-timeout-only change affects new calls without replacing the downstream generation.

MCP access-token additions/removals are hot-reloadable. Listener, public-mode/origin/host/proxy policy, and Admin authentication settings remain startup-bound. When `restartRequired` is reported, restart MCP Manager to fully apply those startup-bound settings.

## Import

```bash
mcp-manager import --from source.json --config config.json --dry-run
mcp-manager import --from source.json --config config.json --yes
```

Import does not start servers and does not silently convert legacy SSE into Streamable HTTP.

## Persistence and Admin transactions

Writes use bounded reads, restrictive same-directory temporary files, flush/sync, atomic replacement, parent-directory durability where supported, OS-backed locking, and digest/ETag conflict checks.

Browser Admin and `mcp-manager admin` share one server-side transaction model:

1. authenticate and check mutation preconditions;
2. read the bounded request body before taking the config transaction lock;
3. load current config and verify ETag/CAS;
4. strict-decode and validate;
5. preflight new/connection-changing enabled downstreams and their tool catalog;
6. persist atomically;
7. apply the validated runtime snapshot;
8. roll back persistence/runtime if live apply fails.

Direct file editing remains appropriate for bootstrap, startup-bound settings, or offline recovery, but normal downstream CRUD and MCP token management should prefer Browser/Remote Admin so validation, preflight, CAS, and rollback remain active.

## Validate and serve

```bash
mcp-manager validate --config config.json
mcp-manager serve --config config.json
```

`validate` does not start downstream processes or network sessions.

## Progressive client exposure

No new configuration is required. `/mcp/progressive` exposes fixed search/describe/call entrypoints using the same filtered catalog and security policy; `/mcp` keeps the full interface. Both endpoints share the upstream session capacity. New CLI exports and Admin client prompts recommend progressive discovery; `--discovery full` selects the original interface. See [Progressive discovery](PROGRESSIVE-DISCOVERY.md) for validation limits and approval boundaries.

## Related

- [Remote Admin](REMOTE-ADMIN.md)
- [Security](SECURITY.md)
- [Compatibility](COMPATIBILITY.md)
- [VPS deployment](VPS.md)


## v0.4.5 authentication and resource boundaries

Configured MCP tokens are enforced in local mode as well as public mode. This
includes `/readyz` and `/api/v1/status`; `/healthz` is exempt for liveness monitoring.
Admin token deletion refuses the final MCP token to avoid silently disabling local
authentication. Add a replacement, migrate clients, then remove the old token.
An explicitly token-free local config remains supported for trusted single-user use.

Admin JSON bodies are limited to one object, 1 MiB, exact field names and unique
keys. Reads have deadlines; ETag/CSRF requirements remain unchanged. Downstream
JSON responses, individual stdio lines and individual SSE events are limited to
8 MiB. Tool discovery across pages is bounded to 4096 definitions and 8 MiB before
catalog filtering; published limits remain 512 tools per server / 2048 total,
256 KiB per final tool definition, and 8 MiB catalog accounting.
