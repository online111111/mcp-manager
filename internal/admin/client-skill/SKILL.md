---
name: mcp-manager-connector
description: Connect a local AI or MCP client to an already-running MCP Manager instance. Use when the user wants an Agent to configure Codex, Claude Code/Desktop, Cursor, OpenCode, VS Code, or another MCP-capable client with an existing MCP Manager endpoint and access token, verify the connection, or repair a local client configuration. Never deploy, upgrade, or modify the remote MCP Manager server as part of this skill.
---

# MCP Manager Connector

Configure the **current client machine** to use an already-running MCP Manager.

## Required inputs

Obtain both values from the user or the copied Admin-console prompt:

- MCP endpoint, normally `https://<host>/mcp/progressive` (default); full fallback `https://<host>/mcp`
- MCP access token

Do not proceed with a placeholder token.

## Workflow

1. Inspect the current machine and identify installed MCP-capable clients.
2. Prefer the client the user explicitly named. Otherwise report the detected choices and configure the most relevant active client.
3. Read the existing client configuration before changing it.
4. Preserve unrelated MCP servers and settings. Never replace the whole configuration when a targeted merge is possible.
5. Prefer native remote/Streamable HTTP MCP configuration when the installed client supports it.
6. If the client only supports local stdio MCP, use the local `mcp-manager` bridge:

   ```text
   mcp-manager stdio --connect <MCP_ENDPOINT> --token <MCP_TOKEN>
   ```

7. Keep the token out of shell history, logs, screenshots, issue text, and final summaries whenever the client supports an environment-variable or secret-store mechanism. If the client requires an inline token, warn the user which local configuration file will contain it and restrict file permissions where practical.
8. Validate the resulting configuration syntax before restarting/reloading the client.
9. Verify that the client can connect, enumerate tools, and perform a harmless representative MCP operation when possible.
10. Report the client configured, configuration path changed, transport selected, and verification result. Redact the token.

## Progressive discovery (default)

The `/mcp/progressive` endpoint publishes exactly three tools: `hub_search_tools`, `hub_describe_tool`, and `hub_call_tool`. Search returns bounded short matches (an empty query lists sources); describe returns the chosen full schema and catalog revision; call executes the target through the normal router. Verify this workflow with a harmless operation. The generic call entry is **not read-only**: show the target name/arguments and require the same approval that a direct deployment, deletion, or write would need. Do not permanently pre-approve the wrapper. If the user explicitly needs full native schemas or an installed client cannot use this workflow, keep the full fallback `/mcp`; CLI export supports `--discovery full`. Do not migrate unrelated clients without permission.

## Hard boundaries

- Do **not** SSH to, deploy, upgrade, restart, reconfigure, or otherwise mutate the remote MCP Manager server.
- Do **not** rotate or delete MCP Manager tokens.
- Do **not** confuse the MCP access token with the Admin Token.
- Do **not** remove existing client MCP entries unless the user explicitly asks.
- Do **not** claim compatibility was verified if the installed client could not actually be tested.

For client-specific configuration patterns and fallback behavior, read `references/client-setup.md`.
