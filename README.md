# MCP Manager

[![Release](https://img.shields.io/github/v/release/online111111/mcp-manager?display_name=tag)](https://github.com/online111111/mcp-manager/releases/latest)
[![CI](https://github.com/online111111/mcp-manager/actions/workflows/ci.yml/badge.svg)](https://github.com/online111111/mcp-manager/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](go.mod)

**English** | [简体中文](README.zh-CN.md)

**MCP Manager** is a lightweight, self-hosted **Model Context Protocol (MCP) gateway, aggregator, proxy, and management console**. Configure local stdio and remote Streamable HTTP MCP servers once, then expose a managed progressive-discovery endpoint to IDEs, coding agents, AI assistants, and stdio-only clients. The original `/mcp` full-tool endpoint remains available for compatibility.

**Latest release:** [Download MCP Manager](https://github.com/online111111/mcp-manager/releases/latest) · Windows / Linux / macOS · amd64 / arm64

> Compatibility note: v0.4.0 keeps the existing JSON schema and `/mcp`/`/admin` endpoints so existing deployments can adopt the current MCP Manager binary without rewriting their configuration.

## What it provides

- `/mcp/progressive` with a fixed three-tool search → describe → call workflow, plus the original `/mcp` full-tool catalog for compatible fallback.
- Managed stdio children and remote Streamable HTTP downstreams.
- Responsive `/admin/` console for status, calls, downstream CRUD, client access, and Agent deployment assets.
- Remote Admin CLI: `list`, `get`, `add`, `edit`, and `delete` downstream services without SSH-editing the server config.
- Strict bounded JSON configuration, ETag/CAS conflict protection, atomic persistence, downstream preflight, rollback, and hot reload.
- Stable public tool names and no automatic replay of potentially side-effecting calls.
- Loopback-safe local mode plus an explicit authenticated HTTPS public mode.
- stdio bridge for clients that cannot connect to HTTP MCP endpoints.
- Embedded `mcp-manager-deployer` Skill and copy-paste Agent prompt for deployment, upgrades, verification, and recovery.

The production module uses `github.com/modelcontextprotocol/go-sdk v1.7.0`. That dependency version does **not** mean MCP Manager claims complete/native support for every MCP 2026-07-28 feature; see [compatibility](docs/COMPATIBILITY.md).

## Quick start

Building from source requires Go 1.25 or newer.

```bash
go build -trimpath -o mcp-manager ./cmd/mcp-manager
cp config.example.json config.json
./mcp-manager validate --config ./config.json
./mcp-manager serve --config ./config.json
```

Recommended MCP endpoint for new clients:

```text
http://127.0.0.1:8080/mcp/progressive
```

The original `http://127.0.0.1:8080/mcp` endpoint remains available for full native-tool discovery. Progressive discovery is available in current source builds; older release binaries may expose only `/mcp`.

Diagnostics:

```bash
./mcp-manager status --endpoint http://127.0.0.1:8080
./mcp-manager doctor --endpoint http://127.0.0.1:8080
```

Repository-aware coding, review, and deployment agents should start with [`AGENTS.md`](AGENTS.md).

## Deploy with an AI agent

If your agent can access the target server, copy the following prompt and provide it with the authorized host/connection context. The prompt is designed for both fresh installations and safe in-place upgrades.

```text
Deploy MCP Manager from https://github.com/online111111/mcp-manager to the server I have authorized you to manage. Perform the deployment yourself using the available shell/SSH/server tools; do not merely give me a list of commands.

Before changing anything:
1. Read AGENTS.md, README.md, docs/VPS.md, docs/SECURITY.md, and docs/CONFIG.md from the repository.
2. Inspect the target OS, architecture, privileges, existing MCP Manager installation, service manager, reverse proxy, listening ports, and relevant firewall state.
3. If an existing installation is present, back up its binary, config, environment file, service definition, and reverse-proxy configuration before modifying it. Preserve the existing JSON schema, /mcp and /admin routes, and working credentials unless migration or rotation is actually required.
4. Prefer the latest appropriate GitHub Release and verify its SHA256SUMS. If no formal Release exists, use an exact current main commit, record its commit SHA, build the mcp-manager binary from that revision, and run the repository validation/tests that are practical on the target environment before installing it.

Deployment requirements:
- Run MCP Manager as a dedicated non-root service account where practical.
- Bind MCP Manager to loopback (normally 127.0.0.1:8080). Do not expose port 8080 directly to the public Internet.
- For a new deployment, use distinct high-entropy MCP and Admin credentials via MCP_MANAGER_TOKEN and MCP_MANAGER_ADMIN_TOKEN. Store them in a permission-restricted environment file and never print their values in logs, chat, screenshots, or the final report.
- Create or adapt config.json from the repository examples and validate it with `mcp-manager validate` before starting/restarting the service.
- Configure a persistent service using the host's native service manager (systemd on normal Linux VPS deployments).
- If public access is required, place Caddy or Nginx in front of the loopback service, terminate HTTPS there, and follow docs/VPS.md and docs/SECURITY.md. Do not weaken Host/Origin/authentication requirements just to make the deployment work.
- Avoid unrelated firewall, SSH, package, or proxy changes. Prefer reversible changes and keep rollback material until verification is complete.

After deployment, verify at minimum:
- the service is running and survives a service-manager status check;
- `mcp-manager status` and `mcp-manager doctor` succeed against the intended endpoint;
- `/healthz` and `/readyz` behave as expected;
- `/admin/` is reachable and authenticates correctly when Admin is enabled;
- the `/mcp` endpoint is reachable through the intended local or HTTPS path;
- at least one representative MCP client/tool path works when such a client/downstream is available.

If the deployment makes a previously healthy installation unhealthy, roll back first and then diagnose.

When finished, report only: installed version or exact commit SHA, binary/config/environment/service/proxy paths, public/local endpoints, verification results, and backup/rollback locations. Never reveal secret values.

Use the safest reversible choice for environment-specific details and continue autonomously. Stop only when required access is missing or a genuinely blocking input cannot be inferred safely.
```

For more deployment detail, see [`AGENTS.md`](AGENTS.md) and [VPS deployment](docs/VPS.md). The embedded `mcp-manager-deployer` Skill under `internal/admin/agent-skill/` provides a reusable workflow for compatible agents.

## Authentication environment variables

New installations use:

```bash
MCP_MANAGER_TOKEN=...
MCP_MANAGER_ADMIN_TOKEN=...
```

For the v0.4 compatibility window, the CLI also accepts the historical `MCP_HUB_TOKEN` and `MCP_HUB_ADMIN_TOKEN`. New generated examples and exports use the `MCP_MANAGER_*` names. Existing deployments do not need to rotate credentials merely to upgrade.

## Client access

For native Streamable HTTP clients, connect to:

```text
https://mcp.example.com/mcp/progressive
```

For stdio-only clients:

```bash
MCP_MANAGER_TOKEN='...' mcp-manager stdio --connect https://mcp.example.com/mcp/progressive
```

Generate supported client configuration with:

```bash
mcp-manager export --client cursor --transport stdio --endpoint https://mcp.example.com --token-env

# Explicit full native-tool fallback
mcp-manager export --client cursor --transport http --endpoint https://mcp.example.com --discovery full
```

Existing clients are not migrated automatically: change the connection URL and reconnect to adopt progressive discovery; keep the same MCP access token. No custom client plugin or dynamic tool registration is required. The agent uses `hub_search_tools`, then `hub_describe_tool`, then `hub_call_tool` to invoke the selected target. Exact third-party client/model compatibility still needs verification.

Do not permanently pre-approve `hub_call_tool`: inspect the target name and arguments and apply the same approvals as a direct sensitive operation. See [Progressive discovery](docs/PROGRESSIVE-DISCOVERY.md) for schemas, revision checks, limits and trade-offs.

## Remote Admin

Use the Admin credential, not the MCP bearer token:

```bash
export MCP_MANAGER_ADMIN_TOKEN='<ADMIN_TOKEN>'

mcp-manager admin list --endpoint https://mcp.example.com
mcp-manager admin get remote-tools --endpoint https://mcp.example.com
mcp-manager admin add filesystem --file ./filesystem.json --endpoint https://mcp.example.com
mcp-manager admin edit filesystem --file ./filesystem.json --endpoint https://mcp.example.com
mcp-manager admin delete filesystem --endpoint https://mcp.example.com --yes
```

Remote mutations use the same authenticated Admin management plane as the browser console, including CSRF, ETag/CAS, strict validation, downstream preflight, atomic persistence, rollback, and hot reload. See [Remote Admin CLI](docs/REMOTE-ADMIN.md).

## Configuration

The configuration schema deliberately retains the `hub` object as a compatibility contract:

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
    "memory": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-memory"]
    }
  }
}
```

See [configuration](docs/CONFIG.md) for strict decoding, environment inheritance, reload semantics, limits, and Admin writes.

## Public deployment

Recommended topology:

```text
Internet -> HTTPS Caddy/Nginx -> 127.0.0.1:8080 MCP Manager
```

Public mode requires an HTTPS `publicUrl`, explicit `allowedHosts`, trusted proxy CIDRs, and distinct MCP/Admin credentials of at least 32 characters each. Never expose port 8080 directly to the Internet. See [VPS deployment](docs/VPS.md) and [security](docs/SECURITY.md).

## Legacy deployment migration

The v0.4 migration avoids a flag day:

1. Back up the existing binary, config, environment file, service unit, and proxy config.
2. Install or stage the `mcp-manager` binary.
3. Validate the existing config with `mcp-manager validate`; the schema and endpoints are unchanged.
4. Existing `MCP_HUB_*` variables may remain during the compatibility window; use `MCP_MANAGER_*` for newly written deployments.
5. Change the service command to `mcp-manager` only after validation.
6. Run `status`, `doctor`, Admin login, and a representative client check before deleting the previous binary.

The legacy source entrypoint has been removed. Official v0.4 release archives use only the `mcp-manager` binary name.

The Go module path matches the repository: `github.com/online111111/mcp-manager`.

## Development and verification

```bash
go test -count=1 -timeout 180s ./...
go test -race -count=1 -timeout 240s ./...
go vet ./...
node --check internal/admin/web/app.js
node --test internal/admin/webtest/*.test.mjs
go build -trimpath -o dist/mcp-manager ./cmd/mcp-manager
```

GitHub Actions covers Linux, Windows, and macOS on the compatibility/current Go matrix. Current-Go jobs run race tests and the independent SDK probe; Linux additionally runs Chromium regressions and a real-MCP-Manager browser smoke test. CI also runs `govulncheck` and repository-identity checks.

## Release packaging

A `v*` tag builds:

- Linux amd64/arm64 `.tar.gz`
- Windows amd64/arm64 `.zip`
- macOS amd64/arm64 `.tar.gz`
- `SHA256SUMS`

Assets use names such as `mcp-manager-0.4.0-linux-amd64.tar.gz`.

## Community & recognition

- [LINUX DO](https://linux.do/) — MCP Manager recognizes and supports the LINUX DO community.
- [烧饼论坛 (sb.sb)](https://sb.sb/) — discussion, usage feedback, and sharing MCP / Agent setups.

## Documentation

- [Configuration](docs/CONFIG.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Security](docs/SECURITY.md)
- [Compatibility](docs/COMPATIBILITY.md)
- [Remote Admin](docs/REMOTE-ADMIN.md)
- [VPS deployment](docs/VPS.md)

## Security boundary

MCP Manager is a trusted personal gateway, **not a sandbox**. stdio downstreams execute with the Manager service user's privileges. Run it as a dedicated non-root account and configure only MCP services you trust.

## License

MCP Manager is released under the [MIT License](LICENSE).

## Production operation / 长期运行

See [the production runbook](docs/PRODUCTION.md) for upgrade, monitoring, credential, resource-limit and rollback requirements. MCP Manager is a trusted single-operator gateway, not a multi-tenant sandbox. A successful smoke test is not a long-duration soak certification.
