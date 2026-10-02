# MCP Manager

[![Release](https://img.shields.io/github/v/release/online111111/mcp-manager?display_name=tag)](https://github.com/online111111/mcp-manager/releases/latest)
[![CI](https://github.com/online111111/mcp-manager/actions/workflows/ci.yml/badge.svg)](https://github.com/online111111/mcp-manager/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go&logoColor=white)](go.mod)

[English](README.md) | **简体中文**

**MCP Manager** 是一个轻量、自托管的 **Model Context Protocol（MCP）网关、聚合器、代理与管理控制台**。本地 stdio 和远程 Streamable HTTP MCP 服务只配置一次，即可通过统一的渐进发现入口提供给 IDE、Coding Agent、AI 助手和仅支持 stdio 的客户端；原 `/mcp` 全量工具入口继续保留兼容。

**最新版本：** [下载 MCP Manager](https://github.com/online111111/mcp-manager/releases/latest) · Windows / Linux / macOS · amd64 / arm64

> 兼容说明：v0.4.0 保留现有 JSON 配置结构以及 `/mcp`、`/admin` 路径，已有部署升级到当前 MCP Manager 二进制时不需要重写配置。

## 核心能力

- `/mcp/progressive` 固定三工具入口，按搜索 → 读取定义 → 调用逐步发现能力；原 `/mcp` 全量工具目录保留回退。
- 管理本地 stdio 子进程和远程 Streamable HTTP 下游。
- 响应式 `/admin/` 管理台：运行状态、调用记录、服务增删改、客户端接入、Agent 部署资源。
- Remote Admin CLI：无需 SSH 手改服务器配置即可 `list/get/add/edit/delete` 下游服务。
- 严格且有大小上限的 JSON 配置，ETag/CAS 并发保护、原子持久化、预检、回滚和热重载。
- 稳定的公开工具名，调用失败后不自动重放可能带副作用的请求。
- 默认回环安全模式，以及显式认证的 HTTPS 公网模式。
- 为只支持 stdio 的客户端提供桥接。
- 内置 `mcp-manager-deployer` Skill 和通用 Agent Prompt，用于部署、升级、验证和恢复。

生产模块当前使用 `github.com/modelcontextprotocol/go-sdk v1.7.0`。这**不代表** MCP Manager 宣称完整原生覆盖 MCP 2026-07-28 的所有能力；具体边界见[兼容性说明](docs/COMPATIBILITY.md)。

## 快速开始

从源码构建需要 Go 1.25 或更新版本：

```bash
go build -trimpath -o mcp-manager ./cmd/mcp-manager
cp config.example.json config.json
./mcp-manager validate --config ./config.json
./mcp-manager serve --config ./config.json
```

新客户端推荐 MCP 地址：

```text
http://127.0.0.1:8080/mcp/progressive
```

原 `http://127.0.0.1:8080/mcp` 仍提供全量原生工具目录。渐进发现已包含在当前源码构建中；旧版 Release 二进制可能仅提供 `/mcp`。

诊断：

```bash
./mcp-manager status --endpoint http://127.0.0.1:8080
./mcp-manager doctor --endpoint http://127.0.0.1:8080
```

需要接手代码、审查或部署项目的 Agent，优先从根目录 [`AGENTS.md`](AGENTS.md) 开始读取。

## 让 Agent 自动部署

如果你的 Agent 已经获得目标服务器的 SSH、终端或服务器管理权限，可以直接把下面这段提示词交给它。提示词同时适用于首次部署和已有实例的安全升级。

```text
请把 https://github.com/online111111/mcp-manager 部署到我已经授权你管理的服务器上。请使用你当前可用的 SSH、终端或服务器管理工具直接完成部署，不要只给我一份命令清单。

开始修改之前：
1. 先阅读仓库中的 AGENTS.md、README.zh-CN.md（或 README.md）、docs/VPS.md、docs/SECURITY.md、docs/CONFIG.md。
2. 检查目标机器的操作系统、CPU 架构、当前权限、已有 MCP Manager 安装、服务管理器、反向代理、监听端口和相关防火墙状态。
3. 如果已经存在 MCP Manager 或旧版本部署，先备份当前二进制、config、环境变量文件、服务定义和反向代理配置，再进行任何修改。除非确实需要迁移或轮换，否则保留现有 JSON schema、/mcp、/admin 路径以及仍然有效的凭据。
4. 优先使用合适的最新 GitHub Release，并校验 SHA256SUMS。如果当前项目还没有正式 Release，则固定到当前 main 的一个精确 commit，记录 commit SHA，从该版本构建 mcp-manager，并在安装前运行目标环境能够执行的仓库验证/测试。

部署要求：
- 在条件允许时使用独立的非 root 服务账号运行 MCP Manager。
- MCP Manager 默认只监听回环地址（通常是 127.0.0.1:8080），不要把 8080 直接暴露到公网。
- 新部署使用彼此不同的高强度 MCP Token 和 Admin Token，分别通过 MCP_MANAGER_TOKEN、MCP_MANAGER_ADMIN_TOKEN 提供。把它们保存在权限受限的环境文件中，不要在日志、聊天、截图或最终汇报中输出具体值。
- 根据仓库示例创建或调整 config.json，并在启动/重启服务前执行 `mcp-manager validate`。
- 使用目标系统原生的服务管理器配置持久运行；普通 Linux VPS 优先使用 systemd。
- 如果需要公网访问，在回环服务前配置 Caddy 或 Nginx 终止 HTTPS，并严格遵循 docs/VPS.md 和 docs/SECURITY.md。不要为了“能访问”而放宽 Host、Origin 或认证安全要求。
- 不要顺手修改与本项目无关的 SSH、防火墙、软件包或代理配置。优先采用可回滚的修改，并在验证完成前保留回滚材料。

部署完成后至少验证：
- 服务已经运行，并且服务管理器状态正常；
- `mcp-manager status` 和 `mcp-manager doctor` 对预期 endpoint 成功；
- `/healthz`、`/readyz` 表现正常；
- 启用 Admin 时，`/admin/` 可以访问并正确认证；
- `/mcp` 能通过预期的本地或 HTTPS 路径访问；
- 如果当前有可用的下游或客户端，至少验证一条代表性的 MCP 客户端/工具调用链路。

如果部署导致原本健康的实例变得不健康，先回滚到之前可用的版本，再继续诊断。

全部完成后，只汇报：安装版本或精确 commit SHA、二进制/config/环境文件/服务/反代配置路径、本地或公网 endpoint、验证结果、备份和回滚位置。不要泄露任何 Secret。

对于环境相关但没有明确指定的细节，优先选择最安全、可逆的方案并自主继续；只有缺少必要访问权限，或者存在无法安全推断的真正阻塞信息时才停止。
```

更详细的部署规则见 [`AGENTS.md`](AGENTS.md) 和 [VPS 部署文档](docs/VPS.md)。兼容的 Agent 还可以直接使用仓库内置的 `internal/admin/agent-skill/` 下的 `mcp-manager-deployer` Skill。

## 环境变量

新安装统一使用：

```bash
MCP_MANAGER_TOKEN=...
MCP_MANAGER_ADMIN_TOKEN=...
```

v0.4 兼容期内，CLI 仍接受历史变量 `MCP_HUB_TOKEN` 和 `MCP_HUB_ADMIN_TOKEN`。新生成的示例和客户端导出只使用 `MCP_MANAGER_*`。已有部署不需要仅为了升级就旋转 Token。

## 客户端接入

原生支持 Streamable HTTP 的客户端连接：

```text
https://mcp.example.com/mcp/progressive
```

仅支持 stdio 的客户端：

```bash
MCP_MANAGER_TOKEN='...' mcp-manager stdio --connect https://mcp.example.com/mcp/progressive
```

支持的客户端优先用 CLI 导出配置：

```bash
mcp-manager export --client cursor --transport stdio --endpoint https://mcp.example.com --token-env

# 显式回退全量原生工具目录
mcp-manager export --client cursor --transport http --endpoint https://mcp.example.com --discovery full
```

已有客户端不会自动迁移：修改连接 URL 后重新连接即可采用渐进发现，MCP Token 不变。不需要专用插件，也不需要把搜索到的目标工具重新注册进客户端列表。Agent 依次使用 `hub_search_tools` 搜索、`hub_describe_tool` 读取参数定义，再通过 `hub_call_tool` 调用目标。具体第三方客户端和模型是否适配仍需实测。

不要把 `hub_call_tool` 永久设成免审批：应检查真实目标名和参数，并保留与直接执行敏感操作相同的审批。完整 Schema、revision 检查、限制与取舍见[渐进发现文档](docs/PROGRESSIVE-DISCOVERY.md)。

## 远程管理下游服务

这里使用 Admin Token，不要和 MCP Bearer Token 混用：

```bash
export MCP_MANAGER_ADMIN_TOKEN='<ADMIN_TOKEN>'

mcp-manager admin list --endpoint https://mcp.example.com
mcp-manager admin get remote-tools --endpoint https://mcp.example.com
mcp-manager admin add filesystem --file ./filesystem.json --endpoint https://mcp.example.com
mcp-manager admin edit filesystem --file ./filesystem.json --endpoint https://mcp.example.com
mcp-manager admin delete filesystem --endpoint https://mcp.example.com --yes
```

远程变更仍然走和浏览器管理台相同的安全事务链：认证会话、CSRF、ETag/CAS、严格校验、下游预检、原子写入、失败回滚和热重载。详见 [Remote Admin](docs/REMOTE-ADMIN.md)。

## 配置

历史 `hub` 配置对象继续保留，这是兼容契约，不应该机械改成 `manager`：

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

完整字段、严格解码、环境继承、热重载和 Admin 写入语义见[配置文档](docs/CONFIG.md)。

## 公网部署

推荐拓扑：

```text
Internet -> HTTPS Caddy/Nginx -> 127.0.0.1:8080 MCP Manager
```

公网模式需要 HTTPS `publicUrl`、明确的 `allowedHosts`、可信代理 CIDR，以及两枚不同且至少 32 字符的 MCP/Admin Token。不要把 8080 直接暴露到公网。详见 [VPS 部署](docs/VPS.md)和[安全边界](docs/SECURITY.md)。

## 旧部署升级

v0.4 的迁移不要求一次性替换所有兼容标识：

1. 备份现有二进制、配置、环境文件、systemd/服务配置和反向代理配置。
2. 安装或暂存新的 `mcp-manager` 二进制。
3. 用 `mcp-manager validate` 验证原有配置；配置 schema 和 `/mcp`、`/admin` 地址不变。
4. 原有 `MCP_HUB_*` 变量在兼容期内可以继续使用；新服务文件优先写 `MCP_MANAGER_*`。
5. 验证通过后再把服务启动命令统一为 `mcp-manager`。
6. 跑 `status`、`doctor`、Admin 登录和至少一条代表性客户端链路后，再删除旧二进制。

旧源码入口已经移除；正式 v0.4 Release 只发布名为 `mcp-manager` 的二进制。

Go module path 已与仓库统一为 `github.com/online111111/mcp-manager`。

## 开发与验证

```bash
go test -count=1 -timeout 180s ./...
go test -race -count=1 -timeout 240s ./...
go vet ./...
node --check internal/admin/web/app.js
node --test internal/admin/webtest/*.test.mjs
go build -trimpath -o dist/mcp-manager ./cmd/mcp-manager
```

CI 覆盖 Linux、Windows、macOS 的兼容/当前 Go 矩阵；当前 Go 还跑 race 与独立 SDK probe，Linux 额外运行 Chromium 回归和真实 MCP Manager 浏览器 smoke，并设有 `govulncheck` 与仓库身份检查。

## Release 包

正式 `v*` tag 会生成：

- Linux amd64/arm64 `.tar.gz`
- Windows amd64/arm64 `.zip`
- macOS amd64/arm64 `.tar.gz`
- `SHA256SUMS`

资产名类似：

```text
mcp-manager-0.4.0-linux-amd64.tar.gz
```

## 社区与认可

- [LINUX DO](https://linux.do/) —— MCP Manager 认可并支持 LINUX DO 社区。
- [烧饼论坛（sb.sb）](https://sb.sb/) —— 欢迎交流 MCP Manager 使用、部署、问题反馈，以及 MCP / Agent 相关实践。

## 文档

- [配置](docs/CONFIG.md)
- [架构](docs/ARCHITECTURE.md)
- [安全边界](docs/SECURITY.md)
- [兼容性](docs/COMPATIBILITY.md)
- [Remote Admin](docs/REMOTE-ADMIN.md)
- [VPS 部署](docs/VPS.md)

## 安全边界

MCP Manager 是可信个人网关，**不是沙箱**。stdio 下游会以 Manager 服务用户的权限运行。建议使用专用非 root 用户，并且只配置你信任的 MCP 服务。

## 开源许可

MCP Manager 使用 [MIT License](LICENSE) 开源。

## Production operation / 长期运行

See [the production runbook](docs/PRODUCTION.md) for upgrade, monitoring, credential, resource-limit and rollback requirements. MCP Manager is a trusted single-operator gateway, not a multi-tenant sandbox. A successful smoke test is not a long-duration soak certification.
