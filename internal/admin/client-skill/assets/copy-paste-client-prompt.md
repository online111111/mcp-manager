请在当前这台客户端电脑上把本机 AI/MCP 客户端接入已经运行的 MCP Manager。

MCP Endpoint（默认渐进式 /mcp/progressive）：
{{MCP_ENDPOINT}}

MCP Access Token：
{{MCP_TOKEN}}

full 全量原生工具回退 Endpoint（仅在用户要求或客户端不适配时选择）：
{{MCP_FULL_ENDPOINT}}

渐进工作流：初始固定 3 个工具，不提前加载全部工具定义。
- hub_search_tools：按需求搜索；空查询列出能力来源，server 参数按服务浏览。
- hub_describe_tool：读取选中工具的完整 Schema 和 revision。
- hub_call_tool：传入真实目标名、符合 Schema 的 arguments，可携带 revision；按正常路由执行。此入口不是只读工具，可能部署、删除或修改外部状态；必须保留针对真实目标操作的审批，不得把整个通用入口设成永久免审批。

要求：
1. 不要部署、升级、重启或修改远端 MCP Manager 服务端。
2. 先检查当前电脑已经安装并实际使用的 MCP 客户端，优先识别 Codex、Claude Code / Claude Desktop、Cursor、OpenCode、VS Code 或其他兼容 MCP 的客户端。
3. 读取现有客户端配置，备份后再修改；不得覆盖或删除其它已有 MCP 服务。
4. 客户端原生支持远程 / Streamable HTTP MCP 时，优先直接使用渐进 Endpoint 和 Token。
5. 如果客户端只支持 stdio，则使用本机 `mcp-manager stdio --connect <endpoint>` 桥接，并通过客户端支持的环境变量/Secret 机制设置 MCP_MANAGER_TOKEN；先确认本机已有可执行文件。CLI export 默认 progressive，显式 `--discovery full` 可导出全量回退配置。
6. 不把 Token 输出到日志、终端历史、截图或最终汇报；必须内联保存时说明配置路径并限制文件权限。
7. 修改后检查配置语法并重新加载客户端，不迁移其他客户端。
8. 实际验证连接后仅列出 3 个入口，并在安全可行时完成 search → describe → 无副作用 call；验证 catalog revision 过期会要求重新 describe。
9. 最后告诉我：配置了哪个客户端、修改了哪个本地配置文件、使用 HTTP 还是 stdio bridge、渐进或 full 模式、验证是否成功。Token 必须脱敏；无法实际测试客户端时明确说明。

请直接执行配置，不要只给我一份操作步骤。
