import {
  SECRET_SENTINEL,
  collectKeyValues,
  escapeHTML,
  filterCalls,
  formatTimestamp,
  formatUptime,
  nonEmptyLines,
  normalizeServer,
  statusLabel,
} from "./app-core.mjs";

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

const state = {
  csrf: "",
  etag: "",
  config: null,
  status: null,
  tokens: [],
  selectedTokenIndex: null,
  selectNewestToken: false,
  serverPage: 1,
  serverPageSize: 12,
  callPage: 1,
  callPageSize: 20,
  editorMode: "form",
  editingId: null,
  editorEtag: "",
  refreshing: false,
  refreshTicket: null,
  statusTicket: null,
  authEpoch: 0,
  dataEpoch: 0,
};

async function api(path, options = {}, { withMeta = false } = {}) {
  const epoch = state.authEpoch;
  const headers = new Headers(options.headers || {});
  if (options.body !== undefined) {
    headers.set("Content-Type", "application/json");
    headers.set("X-CSRF-Token", state.csrf);
  }
  const controller = new AbortController();
  const timer = window.setTimeout(() => controller.abort(), options.body === undefined ? 15000 : 60000);
  try {
    const response = await fetch(`/api/admin/v1${path}`, {
      ...options, headers, credentials: "same-origin", signal: controller.signal,
    });
    const text = await response.text();
    if (epoch !== state.authEpoch) {
      const error = new Error("已忽略旧会话的响应");
      error.staleSession = true;
      throw error;
    }
    if (response.status === 401) {
      if (path !== "/auth/login") showLogin();
      throw new Error(path === "/auth/login" ? "管理密钥不正确，请重新复制 Admin Token" : "登录已失效，请重新登录");
    }
    if (!response.ok) {
      const message = response.status === 409
        ? "配置已被其他操作修改。请刷新后重新编辑，当前草稿不会自动覆盖新配置。"
        : response.status === 429 ? "操作过于频繁，请稍后再试" : text.trim() || `请求失败 (${response.status})`;
      throw new Error(message);
    }
    let data = null;
    try { if (text) data = JSON.parse(text); }
    catch { throw new Error("MCP Manager 返回了无法解析的数据"); }
    return withMeta ? { data, etag: response.headers.get("ETag") || "" } : data;
  } catch (error) {
    if (error.name === "AbortError") throw new Error("请求超时，请检查服务状态后重试；写入操作请先刷新确认结果");
    if (error instanceof TypeError) throw new Error("无法连接到 MCP Manager，请检查服务状态");
    throw error;
  } finally {
    window.clearTimeout(timer);
  }
}

function showLogin() {
  // Invalidate pending requests before clearing secrets; late responses may not
  // repopulate a logged-out page, including its hidden DOM.
  Object.assign(state, {
    csrf: "", etag: "", editorEtag: "", config: null, status: null, tokens: [],
    selectedTokenIndex: null, selectNewestToken: false, editingId: null,
    refreshing: false, refreshTicket: null, statusTicket: null,
    authEpoch: state.authEpoch + 1, dataEpoch: state.dataEpoch + 1,
  });
  if ($("#editor")?.open) $("#editor").close();
  $("#serverForm").reset();
  for (const selector of ["#token", "#accessTokenValue", "#newAccessToken", "#serverJSON"]) $(selector).value = "";
  for (const selector of ["#servers", "#calls", "#accessTokenSelect", "#httpAuthorization", "#stdioCommand", "#argRows", "#envRows", "#headerRows"]) $(selector).replaceChildren();
  $("#loginButton").disabled = false;
  $("#loginButton").textContent = "进入管理台";
  $("#token").type = "password";
  $("#accessTokenValue").type = "password";
  $("#togglePassword").textContent = "显示";
  $("#toggleAccessToken").textContent = "显示";
  $("#login").classList.remove("hidden");
  $("#app").classList.add("hidden");
  $("#topActions").classList.add("hidden");
}

function showApp() {
  $("#login").classList.add("hidden");
  $("#app").classList.remove("hidden");
  $("#topActions").classList.remove("hidden");
}

function toast(message, type = "success") {
  const element = document.createElement("div");
  element.className = `toast ${type}`;
  element.textContent = message;
  $("#toastStack").appendChild(element);
  window.setTimeout(() => element.remove(), 3200);
}

function setConnectionState(connected) {
  const pill = $("#headerHealth");
  if (!connected) {
    pill.className = "status-pill danger";
    pill.innerHTML = '<span class="status-dot"></span>连接中断';
    return;
  }
  const needsRestart = Boolean(state.status?.restartRequired);
  pill.className = `status-pill ${needsRestart ? "warn" : "ok"}`;
  pill.innerHTML = `<span class="status-dot"></span>${needsRestart ? "需要重启" : "运行正常"}`;
}

async function boot() {
  const epoch = state.authEpoch;
  try {
    const session = await api("/auth/me");
    state.csrf = session.csrfToken;
    await refreshAll();
    if (epoch === state.authEpoch) showApp();
  } catch {
    if (epoch === state.authEpoch) showLogin();
  }
}

function selectedToken() {
  if (state.selectedTokenIndex === null) return null;
  return state.tokens.find((entry) => Number(entry.index) === Number(state.selectedTokenIndex)) || null;
}

async function refreshAll({ announce = false } = {}) {
  if (state.refreshing) return;
  state.refreshing = true;
  const ticket = {};
  state.refreshTicket = ticket;
  const epoch = state.authEpoch;
  state.dataEpoch++;
  const previousToken = selectedToken()?.token || "";
  try {
    let snapshot;
    for (let attempt = 0; attempt < 3; attempt++) {
      snapshot = await Promise.all([
        api("/config", {}, { withMeta: true }), api("/status"),
        api("/tokens", {}, { withMeta: true }),
      ]);
      if (snapshot[0].etag && snapshot[0].etag === snapshot[2].etag) break;
      snapshot = null;
    }
    if (!snapshot) throw new Error("配置正在更新，请稍后刷新。未使用版本不一致的服务与 Token 数据。");
    if (epoch !== state.authEpoch) return;
    const [config, status, tokens] = snapshot;
    state.config = config.data;
    state.etag = config.etag;
    state.status = status;
    state.tokens = tokens.data?.tokens || [];
    if (state.selectNewestToken && state.tokens.length) {
      state.selectedTokenIndex = state.tokens[state.tokens.length - 1].index;
    } else if (previousToken) {
      state.selectedTokenIndex = state.tokens.find((entry) => entry.token === previousToken)?.index ?? state.tokens[0]?.index ?? null;
    } else if (!state.tokens.some((entry) => Number(entry.index) === Number(state.selectedTokenIndex))) {
      state.selectedTokenIndex = state.tokens[0]?.index ?? null;
    }
    state.selectNewestToken = false;
    render(status);
    renderTokens();
    setConnectionState(true);
    if (announce) toast("状态已刷新");
  } catch (error) {
    if (epoch === state.authEpoch && !error.staleSession) {
      setConnectionState(false);
      if (announce) toast(error.message, "error");
    }
    throw error;
  } finally {
    if (state.refreshTicket === ticket) { state.refreshing = false; state.refreshTicket = null; }
  }
}

async function refreshStatus() {
  if (state.refreshing || state.statusTicket || $("#app").classList.contains("hidden")) return;
  const ticket = {}, epoch = state.authEpoch, dataEpoch = state.dataEpoch;
  state.statusTicket = ticket;
  try {
    const status = await api("/status");
    if (epoch !== state.authEpoch || dataEpoch !== state.dataEpoch) return;
    state.status = status;
    render(status);
    setConnectionState(true);
  } catch {
    if (epoch === state.authEpoch) setConnectionState(false);
  } finally {
    if (state.statusTicket === ticket) state.statusTicket = null;
  }
}

function render(status) {
  const servers = status.servers || [];
  const calls = status.recentCalls || [];
  const ready = servers.filter((server) => server.state === "ready").length;
  const failed = servers.filter((server) => ["unavailable", "backoff"].includes(server.state)).length;
  const tools = servers.reduce((total, server) => total + (server.publishedToolCount || 0), 0);
  const active = servers.reduce((total, server) => total + (server.activeCalls || 0), 0);
  const successes = calls.filter((call) => call.outcome === "success").length;

  $("#health").textContent = status.restartRequired ? "需重启" : failed ? "部分异常" : "运行中";
  $("#healthDetail").textContent = status.restartRequired
    ? "监听或 Admin / 公网安全设置已变更，需要完整重启"
    : failed ? `${failed} 个服务正在重试` : "MCP Manager 正常接受请求";
  $("#serverCount").textContent = servers.length;
  $("#serverReadyCount").textContent = `${ready} 个可用`;
  $("#toolCount").textContent = tools;
  $("#activeCallCount").textContent = `${active} 个正在调用`;
  $("#callCount").textContent = calls.length;
  $("#successRate").textContent = calls.length ? `成功率 ${Math.round((successes / calls.length) * 100)}%` : "暂无数据";
  $("#hubVersion").textContent = status.version || "—";
  $("#hubUptime").textContent = formatUptime(status.uptimeSeconds);
  $("#lastReload").textContent = status.lastReloadStatus || "配置已加载";

  renderServers(servers);
  renderCalls();
  renderClientAccess();
}

function renderPagination(rootSelector, page, totalPages, totalItems, onChange) {
  const root = $(rootSelector);
  if (!root) return;
  if (!totalItems) {
    root.replaceChildren();
    return;
  }

  const start = Math.max(1, page - 2);
  const end = Math.min(totalPages, page + 2);
  const pages = [];
  for (let value = start; value <= end; value++) pages.push(value);
  root.innerHTML = `
    <span class="page-info">共 ${totalItems} 项 · 第 ${page}/${totalPages} 页</span>
    <button class="button tiny secondary" data-page="${page - 1}" ${page <= 1 ? "disabled" : ""} type="button">上一页</button>
    ${pages.map((value) => `<button class="button tiny secondary" data-page="${value}" ${value === page ? 'aria-current="page"' : ""} type="button">${value}</button>`).join("")}
    <button class="button tiny secondary" data-page="${page + 1}" ${page >= totalPages ? "disabled" : ""} type="button">下一页</button>`;
  for (const button of $$('[data-page]', root)) {
    button.addEventListener("click", () => {
      const target = Number(button.dataset.page);
      if (!Number.isInteger(target) || target < 1 || target > totalPages || target === page) return;
      onChange(target);
    });
  }
}

function renderServers(statuses) {
  const statusByID = new Map(statuses.map((server) => [server.id, server]));
  const configured = state.config?.mcpServers || {};
  const allIDs = [...new Set([...Object.keys(configured), ...statusByID.keys()])].sort();
  const query = $("#serverSearch").value.trim().toLowerCase();
  const filteredIDs = allIDs.filter((id) => {
    if (!query) return true;
    const raw = configured[id] || {};
    const searchable = [id, raw.type, raw.command, ...(raw.args || []), raw.url].filter(Boolean).join(" ").toLowerCase();
    return searchable.includes(query);
  });

  state.serverPageSize = Number($("#serverPageSize").value) || 12;
  const totalPages = Math.max(1, Math.ceil(filteredIDs.length / state.serverPageSize));
  state.serverPage = Math.min(Math.max(1, state.serverPage), totalPages);
  const offset = (state.serverPage - 1) * state.serverPageSize;
  const ids = filteredIDs.slice(offset, offset + state.serverPageSize);
  const from = filteredIDs.length ? offset + 1 : 0;
  const to = Math.min(offset + state.serverPageSize, filteredIDs.length);
  $("#serverSummary").textContent = query ? `显示 ${from}–${to} / ${filteredIDs.length} 个匹配 · 共 ${allIDs.length} 个` : `${allIDs.length} 个服务`;

  if (!allIDs.length) {
    $("#servers").innerHTML = '<div class="empty-state"><strong>还没有下游 MCP 服务</strong><p>点击“添加服务”创建第一个连接。</p></div>';
    renderPagination("#serverPagination", 1, 1, 0, () => {});
    return;
  }
  if (!filteredIDs.length) {
    $("#servers").innerHTML = '<div class="empty-state"><strong>没有匹配的服务</strong><p>换一个关键词再试试。</p></div>';
    renderPagination("#serverPagination", 1, 1, 0, () => {});
    return;
  }

  $("#servers").innerHTML = ids.map((id) => {
    const raw = configured[id] || {};
    const status = statusByID.get(id) || { id, state: "starting" };
    const type = raw.type === "streamable_http" ? "Streamable HTTP" : "stdio";
    const description = raw.type === "streamable_http"
      ? raw.url || "未配置 URL"
      : [raw.command || "未配置命令", ...(raw.args || [])].join(" ");
    const error = status.errorCategory ? `<p class="server-error">${escapeHTML(status.errorCategory)}</p>` : "";
    return `
      <article class="server-card">
        <div class="server-main">
          <div>
            <div class="server-title-row"><h3>${escapeHTML(id)}</h3><span class="badge ${escapeHTML(status.state)}">${escapeHTML(statusLabel(status.state))}</span><span class="badge">${escapeHTML(type)}</span></div>
            <p class="server-desc">${escapeHTML(description)}</p>${error}
            <div class="server-meta"><span><b>${status.publishedToolCount || 0}</b> 个工具</span><span><b>${status.activeCalls || 0}</b> 个调用中</span><span>配置版本 <b>${status.activeRevision || "—"}</b></span></div>
          </div>
          <div class="server-actions"><button class="button tiny secondary" data-action="edit" data-server-id="${escapeHTML(id)}" type="button">编辑</button><button class="button tiny secondary" data-action="duplicate" data-server-id="${escapeHTML(id)}" type="button">复制</button><button class="button tiny danger" data-action="delete" data-server-id="${escapeHTML(id)}" type="button">删除</button></div>
        </div>
      </article>`;
  }).join("");

  renderPagination("#serverPagination", state.serverPage, totalPages, filteredIDs.length, (page) => {
    state.serverPage = page;
    renderServers(state.status?.servers || []);
    $("#serversSection").scrollIntoView({ block: "start" });
  });
}

function renderCalls() {
  const filtered = filterCalls(state.status?.recentCalls || [], $("#callSearch").value, $("#callOutcome").value);
  state.callPageSize = Number($("#callPageSize").value) || 20;
  const totalPages = Math.max(1, Math.ceil(filtered.length / state.callPageSize));
  state.callPage = Math.min(Math.max(1, state.callPage), totalPages);
  const offset = (state.callPage - 1) * state.callPageSize;
  const calls = filtered.slice(offset, offset + state.callPageSize);

  $("#calls").innerHTML = calls.map((call) => `
    <tr><td class="call-tool">${escapeHTML(call.tool)}</td><td>${escapeHTML(call.serverId)}</td><td><span class="badge ${call.outcome === "success" ? "success" : "failed"}">${call.outcome === "success" ? "成功" : escapeHTML(call.outcome || "失败")}</span></td><td class="latency">${Number(call.durationMs) || 0} ms</td><td class="call-time">${escapeHTML(formatTimestamp(call.time))}</td></tr>`).join("");
  $("#callsEmpty").classList.toggle("hidden", filtered.length > 0);
  $("#callsSection .table-wrap").classList.toggle("hidden", filtered.length === 0);
  renderPagination("#callPagination", state.callPage, totalPages, filtered.length, (page) => {
    state.callPage = page;
    renderCalls();
  });
}

function renderTokens() {
  const select = $("#accessTokenSelect");
  const current = selectedToken();
  select.innerHTML = state.tokens.map((entry, position) => `<option value="${Number(entry.index)}">Token ${position + 1}${entry.legacy ? " · 主 Token" : ""}</option>`).join("");
  if (current) select.value = String(current.index);
  else if (state.tokens.length) {
    state.selectedTokenIndex = state.tokens[0].index;
    select.value = String(state.tokens[0].index);
  }
  select.disabled = state.tokens.length === 0;
  renderClientAccess();
}

function renderClientAccess() {
  const endpoint = `${location.origin}/mcp/progressive`;
  const token = selectedToken()?.token || "";
  $("#mcpEndpoint").textContent = endpoint;
  const endpointHelp = $("#mcpEndpoint").closest(".connection-card")?.querySelector("p");
  if (endpointHelp) endpointHelp.textContent = `推荐渐进接入，固定 3 个工具：hub_search_tools → hub_describe_tool → hub_call_tool。call 不是只读，不能跳过目标操作所需的审批。需要完整目录时显式回退 full：${location.origin}/mcp（认证相同）。`;
  $("#accessTokenValue").value = token;
  $("#accessTokenValue").placeholder = token ? "" : "还没有可用 MCP Token";
  $("#copyAccessToken").disabled = !token;
  $("#deleteAccessToken").disabled = !token || state.tokens.length <= 1;
  $("#deleteAccessToken").title = state.tokens.length === 1 ? "请先新增替代 Token，再删除最后一枚 Token，避免意外关闭认证" : "撤销当前 Token";
  $("#copyClientPrompt").disabled = !token;
  $("#mcpAuthWarning")?.classList.toggle("hidden", state.tokens.length > 0);
  const revealed = $("#accessTokenValue").type === "text";
  $("#httpAuthorization").textContent = token ? `Authorization: Bearer ${revealed ? token : "[已隐藏]"}` : "请先新增或选择 MCP Token";
  // Pass the credential using MCP_MANAGER_TOKEN, not a visible argv value.
  $("#stdioCommand").textContent = `mcp-manager stdio --connect ${endpoint}`;
}

function openEditor(id = null, source = null) {
  state.editingId = id;
  state.editorEtag = state.etag;
  const server = normalizeServer(source || (id && state.config?.mcpServers?.[id]) || {});
  $("#editorTitle").textContent = id ? `编辑 ${id}` : "添加 MCP 服务";
  $("#editorSubtitle").textContent = id ? "修改后保存会立即热重载。" : "填写常用字段即可，高级 JSON 可选。";
  $("#serverId").value = id || "";
  $("#serverId").readOnly = Boolean(id);
  fillForm(server);
  $("#serverJSON").value = JSON.stringify(compactServer(server), null, 2);
  $("#editorError").textContent = "";
  setEditorMode("form");
  $("#editor").showModal();
}

function fillForm(serverInput) {
  const server = normalizeServer(serverInput);
  $("#serverType").value = server.type;
  $("#enabled").checked = server.enabled;
  $("#command").value = server.command;
  $("#argRows").replaceChildren();
  for (const arg of server.args) addArgument(arg);
  $("#cwd").value = server.cwd;
  $("#remoteUrl").value = server.url;
  $("#startupTimeout").value = server.startupTimeout;
  $("#callTimeout").value = server.callTimeout;
  $("#maxConcurrency").value = server.maxConcurrency;
  $("#disabledTools").value = server.tools.disabled.join("\n");
  renderKeyValues("#envRows", server.env);
  renderKeyValues("#headerRows", server.headers);
  syncTransport();
}

function addArgument(value = "") {
  const row = document.createElement("div");
  row.className = "arg-row";
  const input = document.createElement("textarea");
  input.rows = 2;
  input.setAttribute("aria-label", "参数值");
  input.value = value;
  input.argumentValue = value;
  input.addEventListener("input", () => { input.argumentValue = input.value; });
  const remove = document.createElement("button");
  remove.type = "button";
  remove.className = "remove-kv";
  remove.setAttribute("aria-label", "删除参数");
  remove.textContent = "×";
  remove.addEventListener("click", () => row.remove());
  row.append(input, remove);
  $("#argRows").appendChild(row);
}

function renderKeyValues(selector, entries) {
  const root = $(selector);
  root.replaceChildren();
  for (const [key, value] of Object.entries(entries)) addKeyValue(root, key, value);
  ensureKeyValueEmptyState(root);
}

function ensureKeyValueEmptyState(root) {
  if ($$(".kv-row", root).length) return;
  const empty = document.createElement("div");
  empty.className = "kv-empty";
  empty.textContent = "暂无项目";
  root.replaceChildren(empty);
}

function addKeyValue(root, key = "", value = "") {
  $(".kv-empty", root)?.remove();
  const row = document.createElement("div");
  row.className = "kv-row";
  const keyInput = document.createElement("input");
  keyInput.className = "kv-key";
  keyInput.placeholder = "名称";
  keyInput.value = key;
  const secretWrap = document.createElement("div");
  secretWrap.className = "kv-secret-wrap";
  const valueInput = document.createElement("input");
  valueInput.className = "kv-value";
  const secret = value === SECRET_SENTINEL;
  valueInput.placeholder = secret ? (state.editingId ? "值（留空保留现有 Secret）" : "必须重新填写 Secret") : "值";
  valueInput.value = secret ? "" : value;
  valueInput.dataset.secret = secret ? "1" : "0";
  secretWrap.appendChild(valueInput);
  if (secret) {
    const label = document.createElement("span");
    label.className = "kv-secret-label";
    label.textContent = state.editingId ? "SECRET 已设置" : "SECRET 必须重新填写";
    secretWrap.appendChild(label);
  }
  const remove = document.createElement("button");
  remove.className = "remove-kv";
  remove.type = "button";
  remove.title = "删除";
  remove.setAttribute("aria-label", `删除 ${key || "这一项"}`);
  remove.textContent = "×";
  remove.addEventListener("click", () => { row.remove(); ensureKeyValueEmptyState(root); });
  valueInput.addEventListener("input", () => {
    if (!valueInput.value) return;
    valueInput.dataset.secret = "0";
    $(".kv-secret-label", row)?.remove();
  });
  row.append(keyInput, secretWrap, remove);
  root.appendChild(row);
}

function collectKeyValueRows(selector) {
  return collectKeyValues($$(".kv-row", $(selector)).map((row) => ({
    key: $(".kv-key", row).value,
    value: $(".kv-value", row).value,
    preserveSecret: $(".kv-value", row).dataset.secret === "1",
  })));
}

function collectForm() {
  const type = $("#serverType").value;
  const server = { enabled: $("#enabled").checked, type };
  if (type === "stdio") {
    server.command = $("#command").value.trim();
    server.args = $$("#argRows textarea").map((input) => input.argumentValue);
    if ($("#cwd").value.trim()) server.cwd = $("#cwd").value.trim();
    const env = collectKeyValueRows("#envRows");
    if (Object.keys(env).length) server.env = env;
  } else {
    server.url = $("#remoteUrl").value.trim();
    const headers = collectKeyValueRows("#headerRows");
    if (Object.keys(headers).length) server.headers = headers;
  }
  const startupTimeout = $("#startupTimeout").value.trim();
  const callTimeout = $("#callTimeout").value.trim();
  const maxConcurrency = $("#maxConcurrency").value.trim();
  const disabled = nonEmptyLines($("#disabledTools").value);
  if (startupTimeout) server.startupTimeout = startupTimeout;
  if (callTimeout) server.callTimeout = callTimeout;
  if (maxConcurrency) server.maxConcurrency = Number(maxConcurrency);
  if (disabled.length) server.tools = { disabled };
  return server;
}

function compactServer(serverInput) {
  const server = normalizeServer(serverInput);
  const compact = { enabled: server.enabled, type: server.type };
  if (server.type === "stdio") {
    compact.command = server.command;
    if (server.args.length) compact.args = server.args;
    if (server.cwd) compact.cwd = server.cwd;
    if (Object.keys(server.env).length) compact.env = server.env;
  } else {
    compact.url = server.url;
    if (Object.keys(server.headers).length) compact.headers = server.headers;
  }
  if (server.startupTimeout) compact.startupTimeout = server.startupTimeout;
  if (server.callTimeout) compact.callTimeout = server.callTimeout;
  if (server.maxConcurrency) compact.maxConcurrency = Number(server.maxConcurrency);
  if (server.tools.disabled.length) compact.tools = server.tools;
  return compact;
}

function validateServer(id, server) {
  if (!/^[a-z][a-z0-9_-]{0,31}$/.test(id)) throw new Error("服务 ID 格式不正确");
  if (!server || typeof server !== "object" || Array.isArray(server)) throw new Error("服务配置必须是 JSON 对象");
  if (!state.editingId) {
    for (const field of ["env", "headers"]) {
      for (const [key, value] of Object.entries(server[field] || {})) {
        if (value === SECRET_SENTINEL) throw new Error(`${field}.${key} 是脱敏 Secret，请重新填写或删除这一项`);
      }
    }
  }
  if (server.type === "stdio" && !String(server.command || "").trim()) throw new Error("stdio 服务必须填写启动命令");
  if (server.type === "streamable_http" && !String(server.url || "").trim()) throw new Error("Streamable HTTP 服务必须填写 MCP URL");
}

function syncTransport() {
  const isHTTP = $("#serverType").value === "streamable_http";
  $("#stdioFields").classList.toggle("hidden", isHTTP);
  $("#httpFields").classList.toggle("hidden", !isHTTP);
}

function setEditorMode(mode) {
  if (mode === "json" && state.editorMode !== "json") {
    try { $("#serverJSON").value = JSON.stringify(collectForm(), null, 2); }
    catch (error) { $("#editorError").textContent = error.message; return; }
  } else if (mode === "form" && state.editorMode === "json") {
    try { fillForm(JSON.parse($("#serverJSON").value)); }
    catch { $("#editorError").textContent = "请先修正 JSON 格式"; return; }
  }
  state.editorMode = mode;
  $("#formEditor").classList.toggle("hidden", mode !== "form");
  $("#jsonEditor").classList.toggle("hidden", mode !== "json");
  $("#formTab").classList.toggle("active", mode === "form");
  $("#jsonTab").classList.toggle("active", mode === "json");
  $("#editorError").textContent = "";
}

async function deleteServer(id) {
  if (!window.confirm(`确定删除 ${id}？\n该服务会立即从 MCP Manager 中移除。`)) return;
  try {
    await api(`/servers/${encodeURIComponent(id)}`, { method: "DELETE", headers: { "If-Match": state.etag }, body: "{}" });
    toast("服务已删除");
    await refreshAll();
  } catch (error) { toast(error.message, "error"); }
}

$("#loginForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  const token = $("#token").value;
  showLogin();
  const epoch = state.authEpoch;
  $("#loginError").textContent = "";
  const button = $("#loginButton");
  button.disabled = true; button.textContent = "登录中…";
  try {
    const session = await api("/auth/login", { method: "POST", body: JSON.stringify({ token }) });
    state.csrf = session.csrfToken;
    await refreshAll();
    if (epoch === state.authEpoch) showApp();
  } catch (error) {
    if (!error.staleSession) $("#loginError").textContent = error.message;
  } finally {
    if (epoch === state.authEpoch) { button.disabled = false; button.textContent = "进入管理台"; }
  }
});

$("#togglePassword").addEventListener("click", () => {
  const input = $("#token");
  input.type = input.type === "password" ? "text" : "password";
  $("#togglePassword").textContent = input.type === "password" ? "显示" : "隐藏";
});

$("#logout").addEventListener("click", async () => {
  $("#loginError").textContent = "";
  try { await api("/auth/logout", { method: "POST", body: "{}" }); }
  catch { $("#loginError").textContent = "退出请求未确认，服务器会话可能仍然有效。请恢复连接后重新登录并退出。"; }
  finally {
    Object.assign(state, { csrf: "", etag: "", editorEtag: "", config: null, status: null, tokens: [], selectedTokenIndex: null });
    showLogin();
  }
});

$("#servers").addEventListener("click", (event) => {
  const button = event.target.closest("[data-action][data-server-id]");
  if (!button) return;
  const id = button.dataset.serverId;
  if (button.dataset.action === "edit") openEditor(id);
  if (button.dataset.action === "duplicate") {
    const source = structuredClone(state.config.mcpServers[id]);
    openEditor(null, source);
    $("#serverId").value = `${id}-copy`.slice(0, 32);
  }
  if (button.dataset.action === "delete") void deleteServer(id);
});

$("#addServer").addEventListener("click", () => openEditor());
$("#addServerHero").addEventListener("click", () => openEditor());
$("#serverType").addEventListener("change", syncTransport);
$("#addArg").addEventListener("click", () => addArgument());
$("#addEnv").addEventListener("click", () => addKeyValue($("#envRows")));
$("#addHeader").addEventListener("click", () => addKeyValue($("#headerRows")));
$("#formTab").addEventListener("click", () => setEditorMode("form"));
$("#jsonTab").addEventListener("click", () => setEditorMode("json"));
$("#closeEditor").addEventListener("click", () => $("#editor").close());
$("#cancelEditor").addEventListener("click", () => $("#editor").close());

$("#serverForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  const save = $("#saveServer");
  $("#editorError").textContent = "";
  try {
    const id = $("#serverId").value.trim();
    const server = state.editorMode === "json" ? JSON.parse($("#serverJSON").value) : collectForm();
    validateServer(id, server);
    if (!state.editingId && Object.hasOwn(state.config?.mcpServers || {}, id)) throw new Error("这个服务 ID 已存在；请换一个 ID，或从服务列表进入编辑");
    save.disabled = true; save.textContent = "保存中…";
    await api(`/servers/${encodeURIComponent(id)}`, { method: "PUT", headers: { "If-Match": state.editorEtag }, body: JSON.stringify(server) });
    $("#editor").close();
    toast(state.editingId ? "服务已更新" : "服务已添加");
    await refreshAll();
  } catch (error) { $("#editorError").textContent = error.message; }
  finally { save.disabled = false; save.textContent = "保存并热重载"; }
});

$("#refresh").addEventListener("click", () => void refreshAll({ announce: true }).catch(() => {}));
$("#refreshTop").addEventListener("click", () => void refreshAll({ announce: true }).catch(() => {}));

$("#serverSearch").addEventListener("input", () => {
  state.serverPage = 1;
  renderServers(state.status?.servers || []);
});
$("#serverPageSize").addEventListener("change", () => {
  state.serverPage = 1;
  renderServers(state.status?.servers || []);
});
$("#callSearch").addEventListener("input", () => {
  state.callPage = 1;
  renderCalls();
});
$("#callOutcome").addEventListener("change", () => {
  state.callPage = 1;
  renderCalls();
});
$("#callPageSize").addEventListener("change", () => {
  state.callPage = 1;
  renderCalls();
});

$("#accessTokenSelect").addEventListener("change", () => {
  state.selectedTokenIndex = Number($("#accessTokenSelect").value);
  $("#accessTokenValue").type = "password";
  $("#toggleAccessToken").textContent = "显示";
  renderClientAccess();
});

$("#toggleAccessToken").addEventListener("click", () => {
  const input = $("#accessTokenValue");
  input.type = input.type === "password" ? "text" : "password";
  $("#toggleAccessToken").textContent = input.type === "password" ? "显示" : "隐藏";
  renderClientAccess();
});

$("#copyAccessToken").addEventListener("click", async () => {
  const token = selectedToken()?.token;
  if (!token) return toast("请先选择一个 MCP Token", "error");
  try { await navigator.clipboard.writeText(token); toast("MCP Token 已复制"); }
  catch { toast("浏览器未允许复制，请手动复制", "error"); }
});

$("#addTokenForm").addEventListener("submit", async (event) => {
  event.preventDefault();
  const input = $("#newAccessToken");
  const button = event.submitter;
  if (button) { button.disabled = true; button.textContent = "创建中…"; }
  try {
    state.selectNewestToken = true;
    await api("/tokens", { method: "POST", headers: { "If-Match": state.etag }, body: JSON.stringify({ token: input.value.trim() }) });
    input.value = "";
    await refreshAll();
    toast("MCP Token 已新增并热生效");
  } catch (error) {
    state.selectNewestToken = false;
    toast(error.message, "error");
  } finally {
    if (button) { button.disabled = false; button.textContent = "+ 新增 Token"; }
  }
});

$("#deleteAccessToken").addEventListener("click", async () => {
  const entry = selectedToken();
  if (!entry) return;
  if (!window.confirm("确定删除当前 MCP Token？\n正在使用它的客户端会立即失去访问权限。")) return;
  try {
    await api(`/tokens/${entry.index}`, { method: "DELETE", headers: { "If-Match": state.etag }, body: "{}" });
    state.selectedTokenIndex = null;
    await refreshAll();
    toast("MCP Token 已删除并热生效");
  } catch (error) { toast(error.message, "error"); }
});

for (const button of $$(".nav-item")) {
  button.addEventListener("click", () => {
    for (const item of $$(".nav-item")) item.classList.remove("active");
    button.classList.add("active");
    $(`#${button.dataset.section}`).scrollIntoView({ behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth" });
  });
}

for (const button of $$(".copy-button")) {
  button.addEventListener("click", async () => {
    const target = $(`#${button.dataset.copyTarget}`);
    const text = button.dataset.copyTarget === "httpAuthorization"
      ? (selectedToken()?.token ? `Authorization: Bearer ${selectedToken().token}` : "")
      : target && "value" in target ? target.value : target?.textContent || "";
    if (!text) return toast("暂无可复制的内容", "error");
    try { await navigator.clipboard.writeText(text); toast("已复制到剪贴板"); }
    catch { toast("浏览器未允许复制，请手动复制", "error"); }
  });
}

$("#copyClientPrompt").addEventListener("click", async () => {
  const epoch = state.authEpoch;
  const token = selectedToken()?.token;
  if (!token) return toast("请先选择一个 MCP Token", "error");
  const endpoint = `${location.origin}/mcp/progressive`;
  const fullEndpoint = `${location.origin}/mcp`;
  try {
    const response = await fetch("/api/admin/v1/client-prompt", { credentials: "same-origin", signal: AbortSignal.timeout(15000) });
    if (response.status === 401) { showLogin(); throw new Error("登录已失效，请重新登录"); }
    if (!response.ok) throw new Error("客户端 Agent Prompt 暂时不可用");
    const template = await response.text();
    if (epoch !== state.authEpoch) return;
    const prompt = template.split("{{MCP_ENDPOINT}}").join(endpoint)
      .split("{{MCP_FULL_ENDPOINT}}").join(fullEndpoint).split("{{MCP_TOKEN}}").join(token);
    await navigator.clipboard.writeText(prompt);
    toast("客户端 Agent Prompt 已复制");
  } catch (error) { toast(error.message || "浏览器未允许复制，请手动配置客户端", "error"); }
});

window.setInterval(() => { if (!document.hidden && !$("#editor").open) void refreshStatus(); }, 10_000);
document.addEventListener("visibilitychange", () => { if (!document.hidden) void refreshStatus(); });

$("#localTokenHint")?.classList.toggle("hidden", !["127.0.0.1", "localhost", "[::1]"].includes(location.hostname));
$("#pasteAdminToken")?.addEventListener("click", async () => {
  const epoch = state.authEpoch;
  try {
    const value = await navigator.clipboard.readText();
    if (epoch !== state.authEpoch || $("#login").classList.contains("hidden")) return;
    $("#token").value = value;
    $("#token").focus();
    $("#loginError").textContent = "";
  } catch { $("#loginError").textContent = "浏览器未允许读取剪贴板，请点击密钥输入框后按 Ctrl+V 粘贴。"; }
});

void boot();
