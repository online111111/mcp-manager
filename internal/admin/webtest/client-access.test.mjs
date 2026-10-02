import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import vm from "node:vm";
import * as core from "../web/app-core.mjs";

async function loadApp() {
  const source = await readFile(new URL("../web/app.js", import.meta.url), "utf8");
  const elements = new Map();
  function element(selector) {
    if (!elements.has(selector)) {
      const listeners = new Map();
      elements.set(selector, {
        textContent: "", value: "", type: "password", disabled: false,
        classList: { toggle() {}, add() {}, remove() {}, contains() { return false; } },
        addEventListener(event, handler) { listeners.set(event, handler); },
        async emit(event) { return listeners.get(event)?.({ preventDefault() {} }); },
        closest() { return { querySelector: () => element("#endpointHelp") }; },
        focus() {},
      });
    }
    return elements.get(selector);
  }
  const copied = [];
  const context = vm.createContext({
    ...core, console, Headers, AbortSignal,
    document: { querySelector: element, querySelectorAll: () => [], addEventListener() {}, hidden: false },
    window: { setInterval() {}, setTimeout, clearTimeout },
    location: { origin: "https://manager.example", hostname: "manager.example" },
    navigator: { clipboard: { async writeText(text) { copied.push(text); } } },
  });
  vm.runInContext(source.replace(/^import \{[\s\S]*?\} from "\.\/app-core\.mjs";\n/, "").replace(/void boot\(\);\s*$/, ""), context);
  return { context, element, copied, run: (expression) => vm.runInContext(expression, context) };
}

test("client AgentPrompt substitutes progressive and explicit full endpoints", async () => {
  const app = await loadApp();
  setTestToken(app);
  app.context.fetch = async () => ({ ok: true, status: 200, text: async () => "Endpoint={{MCP_ENDPOINT}} Full={{MCP_FULL_ENDPOINT}} Token={{MCP_TOKEN}}" });
  app.run("toast = () => {};");
  await app.element("#copyClientPrompt").emit("click");
  assert.equal(app.copied[0], "Endpoint=https://manager.example/mcp/progressive Full=https://manager.example/mcp Token=test-only-token");
});

test("client access explains the three-tool workflow and explicit full fallback", async () => {
  const app = await loadApp();
  app.run("renderClientAccess();");
  const help = app.element("#endpointHelp").textContent;
  assert.match(help, /固定 3 个工具/);
  assert.match(help, /hub_search_tools → hub_describe_tool → hub_call_tool/);
  assert.match(help, /full.*https:\/\/manager\.example\/mcp/);
  assert.match(help, /不是只读.*审批/);
});

for (const file of ["SKILL.md", "references/client-setup.md", "assets/copy-paste-client-prompt.md"]) {
  test(`connector asset ${file} documents progressive discovery and approval boundaries`, async () => {
    const text = await readFile(new URL(`../client-skill/${file}`, import.meta.url), "utf8");
    for (const required of ["/mcp/progressive", "full", "hub_search_tools", "hub_describe_tool", "hub_call_tool"]) {
      assert.ok(text.includes(required), `${file} missing ${required}`);
    }
    assert.match(text, /固定 3 个工具|exactly three tools/, file);
    assert.match(text, /不是只读|not read-only/, file);
    assert.match(text, /审批|approval/, file);
    if (file.startsWith("assets/")) {
      assert.ok(text.includes("{{MCP_FULL_ENDPOINT}}"));
      assert.ok(text.includes("不要部署"));
    }
  });
}

const setTestToken = (app) => app.run('state.tokens = [{ index: 0, token: "test-only-token" }]; state.selectedTokenIndex = 0;');

test("client HTTP and stdio access default to progressive without rewriting downstream URLs", async () => {
  const app = await loadApp();
  setTestToken(app);
  app.run('state.config = { mcpServers: { remote: { type: "streamable_http", url: "https://downstream.example/custom/mcp" } } }; renderClientAccess();');
  assert.equal(app.element("#mcpEndpoint").textContent, "https://manager.example/mcp/progressive");
  assert.equal(app.element("#stdioCommand").textContent, "mcp-manager stdio --connect https://manager.example/mcp/progressive");
  assert.equal(app.run("state.config.mcpServers.remote.url"), "https://downstream.example/custom/mcp");
  assert.doesNotMatch(app.element("#stdioCommand").textContent, /test-only-token/);
});
