# AGENTS.md — MCP Manager

This file is the primary entry point for coding, review, deployment, and operations agents working on this repository.

## Project identity

- Product: **MCP Manager**
- Repository: `online111111/mcp-manager`
- Go module: `github.com/online111111/mcp-manager`
- Primary binary / CLI: `mcp-manager`
- Current source version: see `internal/buildinfo/buildinfo.go`; do not infer release availability from source version alone.
- Go: 1.25+
- MCP Go SDK: `github.com/modelcontextprotocol/go-sdk v1.7.0`

Do not reintroduce the old repository slug, old binary name, or old public product name. Historical `MCP_HUB_TOKEN` and `MCP_HUB_ADMIN_TOKEN` remain runtime compatibility aliases only. The JSON `hub.*` object and `/mcp` / `/admin` routes are intentional compatibility contracts and must not be renamed for branding reasons.

## What this project does

MCP Manager is a self-hosted MCP gateway and management plane. It aggregates local stdio and remote Streamable HTTP MCP downstreams behind `/mcp/progressive` (fixed search/describe/call tools for new clients) and the compatible `/mcp` full-tool endpoint, provides a browser Admin console and Remote Admin CLI, supports stdio-only clients through a bridge, and ships deployment assets for agents.

## Read first

Choose only the documents relevant to the task:

- User-facing overview: `README.md`
- Configuration contract: `docs/CONFIG.md`
- Architecture/runtime model: `docs/ARCHITECTURE.md`
- Security boundary: `docs/SECURITY.md`
- Protocol/client compatibility: `docs/COMPATIBILITY.md`
- Remote Admin: `docs/REMOTE-ADMIN.md`
- VPS/public deployment: `docs/VPS.md`
- Maintainer implementation guide: `.github/maintainer/AGENT-IMPLEMENTATION-GUIDE.md`
- Maintainer design notes: `.github/maintainer/DEVELOPMENT-DESIGN.md`
- Current implementation/release evidence: `.github/maintainer/IMPLEMENTATION-STATUS.md`

For deployment-specific automation, also inspect the embedded `mcp-manager-deployer` Skill under `internal/admin/agent-skill/`.

## Repository map

```text
cmd/mcp-manager/             CLI entrypoint
internal/admin/              Admin API, browser UI, embedded Agent assets
internal/bridge/             stdio bridge
internal/cli/                CLI, diagnostics, Remote Admin
internal/config/             strict config, locking, atomic persistence
internal/downstream/         downstream sessions and lifecycle
internal/inbound/            /mcp and diagnostic HTTP surface
internal/process/            cross-platform process-tree handling
internal/runtime/            reload, revision and transaction coordination
docs/                        user/operator-facing documentation
.github/maintainer/          maintainer design/evidence notes
verification/sdkprobe/       independent SDK behavior evidence
```

The repository is public. `.github/maintainer/` is intentionally removed from normal README navigation to reduce user-facing clutter, but it is **not a secrecy boundary**. Never store credentials, private infrastructure details, or other secrets there.

## Build and test

From repository root:

```bash
go test -count=1 -timeout 180s ./...
go test -race -count=1 -timeout 240s ./...
go vet ./...
go build -trimpath -o dist/mcp-manager ./cmd/mcp-manager
node --check internal/admin/web/app.js
node --test internal/admin/webtest/*.test.mjs
```

SDK probe:

```bash
cd verification/sdkprobe
go test -race -count=1 -timeout 120s ./...
```

Browser regression / live smoke when UI or Admin behavior changes:

```bash
cd internal/admin/browsertest
npm ci
npx playwright install --with-deps chromium
npm test
cd ../../..
go build -trimpath -o dist/mcp-manager ./cmd/mcp-manager
node internal/admin/browsertest/live-smoke.mjs
```

Do not weaken assertions, security checks, or test coverage merely to make CI green.

## Deployment baseline

Recommended public topology:

```text
Internet -> HTTPS Caddy/Nginx -> 127.0.0.1:8080 MCP Manager
```

Deployment rules:

1. Inspect OS/arch, existing binary/config/env/service/proxy and current health first.
2. Back up binary, config, environment file, service definition and reverse-proxy config before mutation.
3. Prefer a pinned GitHub Release and verify `SHA256SUMS`; build an exact commit only when necessary.
4. Keep Manager on loopback; never expose port 8080 directly to the public Internet.
5. Use distinct high-entropy MCP/Admin tokens. New deployments use `MCP_MANAGER_TOKEN` and `MCP_MANAGER_ADMIN_TOKEN`.
6. Run persistent deployments as a dedicated non-root service account where practical.
7. Run `mcp-manager validate --config <path>` before restart.
8. After restart run `status`, `doctor`, `/healthz`, `/readyz`, Admin login if enabled, and at least one representative client path when available.
9. Roll back if a previously healthy deployment becomes unhealthy.
10. Never print token values in logs, PRs, screenshots, or completion reports.

For existing pre-v0.4 deployments, preserve `hub.*`, `/mcp`, `/admin`, and existing historical token environment variables unless an explicit migration/rotation is requested.

## Change safety checklist

When touching runtime/downstream code, preserve generation/lease ownership, cancellation, call timeout/concurrency/backoff, catalog revision ordering, no-replay behavior, and graceful drain.

When touching stdio/process handling, preserve direct command/args semantics, safe environment inheritance, explicit secret forwarding, POSIX process groups, and Windows Job Object cleanup.

When touching HTTP/MCP code, preserve HTTPS policy, redirect rejection, Host/Origin checks, body/response bounds, session deadlines, protocol negotiation boundaries, and sanitized errors.

When touching Admin behavior, preserve authentication/session handling, same-origin/CSRF, ETag/CAS, strict bounded JSON, preflight, OS-backed config locking, atomic persistence, hot reload, rollback, and secret placeholders. Slow network bodies must be read before taking the shared config transaction lock.

When touching the browser UI, preserve keyboard focus, ARIA labeling, reduced motion, mobile layouts, editor-open ETag semantics, and atomic config+ETag refresh behavior.

When touching Remote Admin, keep MCP/Admin credentials separate, reject unsafe remote HTTP and redirects, preserve session+CSRF and ETag/CAS semantics, redact secrets, and bound input/output.

## Pull requests and releases

- Work from the latest `main`; do not trust stale summaries over current repository/CI state.
- Prefer small single-purpose branches and regression tests for defects.
- Before merge, verify the final PR head SHA and final-head CI.
- For stacked PRs, merge in dependency order and re-check each remaining base/diff/CI after every merge.
- A green PR is not a formal release.
- Release publication must depend on execution of the exact Windows ZIP through CMD and Windows PowerShell 5.1, not a separately rebuilt test binary.
- Review `docs/PRODUCTION.md` and `.github/maintainer/PRODUCTION-AUDIT.md` for operational scope and evidence boundaries.
- Formal releases must come from the intended exact `v*` tag and publish all expected `mcp-manager-*` archives plus `SHA256SUMS`.

Report conclusions as evidence-bounded findings, e.g. “no known release-blocking issue was found in the audited and tested scope,” not as claims of absolute bug-freedom.
